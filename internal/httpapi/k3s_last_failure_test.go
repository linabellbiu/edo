package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"edo/internal/model"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

func TestK3sHTTPLastFailureOnlyReturnsLatestFailedLogAndDoesNotInstall(t *testing.T) {
	transport := &k3sHTTPTestTransport{}
	router, cleanup, db := newK3sHTTPTestRouterAndDB(t, transport)
	defer cleanup()
	cookie := loginForDepartmentTest(t, router, "admin")
	var actor model.User
	var host model.Host
	if err := db.First(&actor, "username = ?", "admin").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&host, "uuid = ?", "httpapi-deployment-host").Error; err != nil {
		t.Fatal(err)
	}
	latestID := ""
	for index := 0; index < 14; index++ {
		status := "succeeded"
		if index < 2 {
			status = "failed"
		}
		value := model.K3sInstallation{ID: uuid.NewString(), ProjectID: host.ProjectID, DepartmentID: host.DepartmentID, CreatedBy: actor.ID, Action: "create", Name: "历史安装", Status: status, InputCiphertext: "fixture-only", Preview: datatypes.JSON(`{}`), Hosts: datatypes.JSON(`[]`), Logs: "安全安装日志", CreatedAt: time.Now(), UpdatedAt: time.Now(), ExpiresAt: time.Now()}
		if err := db.Create(&value).Error; err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			latestID = value.ID
		}
	}
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/installations?action=create&status=failed&limit=1", nil, cookie)
	var payload struct {
		Installations []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Logs   string `json:"logs"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(payload.Installations) != 1 || payload.Installations[0].ID != latestID || payload.Installations[0].Status != "failed" || payload.Installations[0].Logs != "安全安装日志" {
		t.Fatalf("失败日志筛选不正确：%d %s", response.Code, response.Body.String())
	}
	invalid := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/installations?action=create&status=unknown&limit=1", nil, cookie)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("无效筛选未被拒绝：%d", invalid.Code)
	}
	if transport.reads.Load() != 0 || transport.runs.Load() != 0 {
		t.Fatal("读取历史日志触发了远程操作")
	}
}
