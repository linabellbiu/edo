package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	hostmanager "edo/internal/host"
	"edo/internal/model"
)

func TestHostResourcesHTTPPermissionsAndProjectIsolation(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, current *gorm.DB) {
		db = current
		deps.Hosts = hostmanager.NewService(current, nil, nil, nil)
	})
	defer cleanup()
	const hostID = "httpapi-deployment-host"
	path := "/api/v1/hosts/" + hostID + "/resources"
	unauthenticated := performJSONRequest(t, router, http.MethodGet, path, nil, nil)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("未登录请求应为401: status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	admin := loginForDepartmentTest(t, router, "admin")
	if err := db.Model(&model.Host{}).Where(&model.Host{ID: hostID}).Updates(map[string]any{
		"ssh_username": "host-resources-private-user", "ssh_credential_ciphertext": "host-resources-private-ciphertext",
		"address": "192.0.2.251", "ssh_host_key_fingerprint": "",
	}).Error; err != nil {
		t.Fatal(err)
	}
	var reader *http.Cookie
	for _, test := range []struct {
		name        string
		permissions []string
		deny        []string
		status      int
	}{
		{"no-resource-permission", []string{"repository.read"}, nil, http.StatusForbidden},
		{"cluster-reader", []string{"cluster.read"}, nil, http.StatusOK},
		{"deployment-reader", []string{"deployment.read"}, nil, http.StatusOK},
		{"cluster-personal-deny", []string{"cluster.read"}, []string{"cluster.read"}, http.StatusForbidden},
		{"deployment-personal-deny", []string{"deployment.read"}, []string{"deployment.read"}, http.StatusForbidden},
		{"other-read-remains-allowed", []string{"cluster.read", "deployment.read"}, []string{"cluster.read"}, http.StatusOK},
		{"both-personally-denied", []string{"cluster.read", "deployment.read"}, []string{"cluster.read", "deployment.read"}, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			role := createRoleForHTTPTest(t, router, admin, "resources-"+test.name, test.permissions)
			username := "res-" + test.name
			userID := createUserForHTTPTest(t, router, admin, username, database.DefaultDepartmentID, []string{role})
			cookie := loginForDepartmentTest(t, router, username)
			if test.deny != nil {
				denied := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/permissions", map[string]any{
					"allow": []string{}, "deny": test.deny,
				}, admin)
				if denied.Code != http.StatusNoContent {
					t.Fatalf("配置个人拒绝失败: status=%d body=%s", denied.Code, denied.Body.String())
				}
			}
			response := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
			if response.Code != test.status {
				t.Fatalf("主机资源权限错误: status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			if test.status == http.StatusOK {
				assertHostResourcesOnlySafeUnavailableFields(t, response.Body.Bytes())
				if test.name == "cluster-reader" {
					reader = cookie
				}
			}
		})
	}
	if reader == nil {
		t.Fatal("缺少已授权的项目读取用户")
	}
	otherProject := model.Project{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, Name: "主机资源隔离项目", IsActive: true}
	if err := db.Create(&otherProject).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Host{}).Where(&model.Host{ID: hostID}).Update("project_id", otherProject.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{hostID, uuid.NewString()} {
		response := performJSONRequest(t, router, http.MethodGet, "/api/v1/hosts/"+id+"/resources", nil, reader)
		var failure errorResponse
		if response.Code != http.StatusNotFound || json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure.Code != "host_not_found" {
			t.Fatalf("范围外和不存在的主机应同为404: status=%d body=%s", response.Code, response.Body.String())
		}
		for _, private := range []string{hostID, "host-resources-private-user", "host-resources-private-ciphertext", "192.0.2.251"} {
			if strings.Contains(response.Body.String(), private) {
				t.Fatal("范围外错误响应泄露了主机信息")
			}
		}
	}
	selected := performJSONRequest(t, router, http.MethodGet, path+"?project_id="+otherProject.ID, nil, admin)
	if selected.Code != http.StatusOK {
		t.Fatalf("管理员选择准确项目后不能读取主机: status=%d body=%s", selected.Code, selected.Body.String())
	}
	assertHostResourcesOnlySafeUnavailableFields(t, selected.Body.Bytes())
}

func assertHostResourcesOnlySafeUnavailableFields(t *testing.T, body []byte) {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope) != 1 || envelope["resources"] == nil {
		t.Fatalf("资源响应混入了主机配置: %s err=%v", body, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope["resources"], &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"state": true, "source": true, "scope": true, "cpu_cores": true, "memory_total_bytes": true,
		"disk_total_bytes": true, "os": true, "disk_mount": true, "collected_at": true,
	}
	if len(fields) != len(allowed) {
		t.Fatalf("资源字段缺失或包含配置字段: %s", body)
	}
	for key := range fields {
		if !allowed[key] {
			t.Fatalf("资源响应包含非资源字段 %q", key)
		}
	}
	for key, want := range map[string]string{"state": `"unavailable"`, "source": `"ssh"`, "scope": `"current_environment"`, "disk_mount": `"/"`, "cpu_cores": "null", "memory_total_bytes": "null", "disk_total_bytes": "null"} {
		if string(fields[key]) != want {
			t.Fatalf("%s 缺测或采集范围错误: got=%s want=%s", key, fields[key], want)
		}
	}
	var collected time.Time
	if err := json.Unmarshal(fields["collected_at"], &collected); err != nil || collected.IsZero() {
		t.Fatalf("缺少真实采集时间: %s", body)
	}
	for _, private := range []string{"ssh_username", "ssh_host_key_fingerprint", "ssh_credential_ciphertext", "address", "host-resources-private-user", "host-resources-private-ciphertext", "192.0.2.251"} {
		if strings.Contains(string(body), private) {
			t.Fatalf("资源响应泄露主机配置或凭据字段: %q", private)
		}
	}
}
