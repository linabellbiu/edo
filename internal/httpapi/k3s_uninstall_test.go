package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/hostcredential"
	"edo/internal/k3s"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sshclient"
)

type k3sUninstallHTTPTransport struct {
	k3sHTTPTestTransport
}

func TestK3sUninstallUnknownOutcomeIsConflictAndKeepsDiagnosticsPrivate(t *testing.T) {
	var logs bytes.Buffer
	handler := k3sHandler{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/kubernetes/k3s/uninstall/plans", nil)
	handler.uninstallError(ctx, "uninstall_unknown_outcome", errors.Join(k3s.ErrUninstallIncomplete, errors.New("internal-diagnostic-marker")))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "禁止再次安装或卸载") || strings.Contains(response.Body.String(), "internal-diagnostic-marker") {
		t.Fatalf("不明结果未阻断或泄露内部诊断: status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), "uninstall_unknown_outcome") || !strings.Contains(logs.String(), "internal-diagnostic-marker") {
		t.Fatal("未记录操作上下文及原始诊断错误")
	}
}

func (transport *k3sUninstallHTTPTransport) Read(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
	if strings.Contains(script, "EDO_K3S_UNINSTALL_PROOF_END") {
		transport.reads.Add(1)
		var result strings.Builder
		for index, key := range []string{"script", "killall", "config", "unit", "environment"} {
			fmt.Fprintf(&result, "EDO_K3S_UNINSTALL_PROOF\t%s\t1:%d:0:0:700:1:100:1:1:%s\n", key, index+1, strings.Repeat("a", 64))
		}
		return result.String() + "EDO_K3S_UNINSTALL_PROOF_END\n", ctx.Err()
	}
	return transport.k3sHTTPTestTransport.Read(ctx, host, bundle, script)
}

func TestK3sUninstallHTTPRequiresIndependentPermission(t *testing.T) {
	transport := &k3sHTTPTestTransport{}
	router, cleanup := newK3sHTTPTestRouter(t, transport)
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	requests := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/kubernetes/k3s/uninstall-targets?host_id=httpapi-deployment-host", nil},
		{http.MethodPost, "/api/v1/kubernetes/k3s/uninstall/plans", map[string]string{"installation_id": "missing-installation", "host_id": "httpapi-deployment-host"}},
		{http.MethodPost, "/api/v1/kubernetes/k3s/uninstallations", map[string]string{"plan_id": "missing-plan", "confirmation": "确认"}},
	}
	for _, item := range requests {
		response := performJSONRequest(t, router, item.method, item.path, item.body, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("匿名请求未被拦截: method=%s path=%s status=%d", item.method, item.path, response.Code)
		}
	}
	for _, scenario := range []struct {
		name        string
		permissions []string
	}{
		{"installer", []string{access.PermissionClusterRead, access.PermissionClusterCreate, access.PermissionClusterUpdate, access.PermissionClusterExecute, "kubernetes.node.create"}},
		{"reader", []string{access.PermissionClusterRead}},
		{"missing-read", []string{access.PermissionK3sUninstall}},
	} {
		role := createRoleForHTTPTest(t, router, admin, "uninstall-"+scenario.name, scenario.permissions)
		username := "uninstall-" + scenario.name
		createUserForHTTPTest(t, router, admin, username, "", []string{role})
		cookie := loginForDepartmentTest(t, router, username)
		for _, item := range requests {
			response := performJSONRequest(t, router, item.method, item.path, item.body, cookie)
			if response.Code != http.StatusForbidden {
				t.Fatalf("缺少独立权限或读取权限仍可请求: actor=%s method=%s path=%s status=%d", scenario.name, item.method, item.path, response.Code)
			}
		}
	}
	if transport.reads.Load() != 0 || transport.runs.Load() != 0 {
		t.Fatal("卸载权限检查前执行了 SSH 读取或脚本")
	}
}

func TestK3sUninstallHTTPRejectsMissingOrInexactConfirmation(t *testing.T) {
	transport := &k3sHTTPTestTransport{}
	router, cleanup, db := newK3sHTTPTestRouterAndDB(t, transport)
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	before := k3sHTTPPreflightState(t, db)
	for _, confirmation := range []string{"", "确认 ", " 确认", "CONFIRM", "确认确认"} {
		response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/uninstallations", map[string]string{
			"plan_id": "missing-plan", "confirmation": confirmation,
		}, admin)
		var payload errorResponse
		if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Code != "k3s_uninstall_confirmation" {
			t.Fatalf("未要求用户精确输入确认: status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if transport.reads.Load() != 0 || transport.runs.Load() != 0 {
		t.Fatal("确认输入未满足时执行了远程命令")
	}
	for name, count := range k3sHTTPPreflightState(t, db) {
		if count != before[name] {
			t.Fatalf("确认输入未满足却创建了记录: %s", name)
		}
	}
}

func TestK3sUninstallHTTPDoesNotRequireInstallationPermission(t *testing.T) {
	transport := &k3sHTTPTestTransport{}
	router, cleanup := newK3sHTTPTestRouter(t, transport)
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	role := createRoleForHTTPTest(t, router, admin, "uninstall-explicit-only", []string{access.PermissionClusterRead, access.PermissionK3sUninstall})
	createUserForHTTPTest(t, router, admin, "uninstall-explicit-user", "", []string{role})
	cookie := loginForDepartmentTest(t, router, "uninstall-explicit-user")
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/uninstall-targets?host_id=httpapi-deployment-host", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("独立卸载权限被错误要求同时拥有安装权限: status=%d body=%s", response.Code, response.Body.String())
	}
	response = performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/installations?action=uninstall&host_id=httpapi-deployment-host", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("独立卸载权限不能查看自己的本主机卸载历史: status=%d body=%s", response.Code, response.Body.String())
	}
	if transport.runs.Load() != 0 {
		t.Fatal("读取候选或历史不能执行卸载")
	}
}

func TestK3sUninstallHTTPQueuesOnlyExplicitlyConfirmedOwnedPlan(t *testing.T) {
	transport := &k3sUninstallHTTPTransport{}
	router, cleanup, db := newK3sHTTPTestRouterAndDB(t, transport)
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	var host model.Host
	if err := db.First(&host, "uuid = ?", "httpapi-deployment-host").Error; err != nil {
		t.Fatal(err)
	}
	host.ID, host.RowID, host.Name = uuid.NewString(), 0, "官方卸载接口测试主机"
	keys, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := json.Marshal(sshclient.Bundle{Password: "test-only-ssh-password", UseSudo: true})
	if err != nil {
		t.Fatal(err)
	}
	host.SSHCredentialCiphertext, err = keys.Encrypt(string(bundle), hostcredential.AAD(host.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	sourceResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/plans", map[string]any{
		"action": "create", "profile": "default", "name": "官方卸载接口来源", "server_host_id": host.ID,
	}, admin)
	var source k3s.Plan
	if sourceResponse.Code != http.StatusCreated || json.Unmarshal(sourceResponse.Body.Bytes(), &source) != nil {
		t.Fatalf("受控来源预览失败: status=%d body=%s", sourceResponse.Code, sourceResponse.Body.String())
	}
	if err := db.Model(&model.K3sInstallation{}).Where("uuid = ?", source.ID).Updates(map[string]any{"status": "failed", "finished_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	role := createRoleForHTTPTest(t, router, admin, "uninstall-queue-only", []string{access.PermissionClusterRead, access.PermissionK3sUninstall})
	userID := createUserForHTTPTest(t, router, admin, "uninstall-queue-user", "", []string{role})
	cookie := loginForDepartmentTest(t, router, "uninstall-queue-user")
	targets := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/uninstall-targets?host_id="+host.ID, nil, cookie)
	if targets.Code != http.StatusOK || !strings.Contains(targets.Body.String(), source.ID) {
		t.Fatalf("同项目同事的托管来源不可见: status=%d body=%s", targets.Code, targets.Body.String())
	}
	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/uninstall/plans", map[string]string{"installation_id": source.ID, "host_id": host.ID}, cookie)
	var plan k3s.Plan
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &plan) != nil || plan.Action != "uninstall" || plan.Uninstall == nil {
		t.Fatalf("卸载预览失败: status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "test-only-ssh-password") || strings.Contains(response.Body.String(), "700:1:100:1:1:") {
		t.Fatal("卸载预览被缓存或泄露私有现场摘要")
	}
	notOwner := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/uninstallations", map[string]string{"plan_id": plan.ID, "confirmation": "确认"}, admin)
	if notOwner.Code != http.StatusNotFound {
		t.Fatalf("其他操作者可以提交卸载计划: status=%d body=%s", notOwner.Code, notOwner.Body.String())
	}
	queued := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/uninstallations", map[string]string{"plan_id": plan.ID, "confirmation": "确认"}, cookie)
	if queued.Code != http.StatusAccepted || !strings.Contains(queued.Body.String(), "\"status\":\"pending\"") {
		t.Fatalf("确认后的卸载未异步排队: status=%d body=%s", queued.Code, queued.Body.String())
	}
	repeated := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/k3s/uninstallations", map[string]string{"plan_id": plan.ID, "confirmation": "确认"}, cookie)
	if repeated.Code != http.StatusConflict {
		t.Fatalf("同一卸载被重复提交: status=%d body=%s", repeated.Code, repeated.Body.String())
	}
	detail := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/installations/"+plan.ID, nil, cookie)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "\"action\":\"uninstall\"") {
		t.Fatalf("独立卸载用户无法读取自己任务: status=%d body=%s", detail.Code, detail.Body.String())
	}
	history := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/installations?action=uninstall&host_id="+host.ID, nil, cookie)
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), plan.ID) {
		t.Fatalf("本主机卸载历史未返回任务: status=%d body=%s", history.Code, history.Body.String())
	}
	var member model.ProjectMember
	if err := db.Where("project_id = ? AND user_id = ?", database.DefaultProjectID(host.DepartmentID), userID).First(&member).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("role_id = ? AND permission = ?", member.RoleID, access.PermissionK3sUninstall).Delete(&model.RolePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	revoked := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/k3s/installations/"+plan.ID, nil, cookie)
	if revoked.Code != http.StatusForbidden {
		t.Fatalf("卸载撤权后仍可读取任务: status=%d body=%s", revoked.Code, revoked.Body.String())
	}
	if transport.runs.Load() != 0 {
		t.Fatal("HTTP 请求内执行了官方卸载，应该仅排队给后台")
	}
}
