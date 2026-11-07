package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/model"
)

func TestConfigurationHistoryHTTPRequiresRevealPermissionAndSafeAudit(t *testing.T) {
	f := newDeliveryVariableProjectFixture(t)
	const canary = "TOKEN=configuration-history-secret-canary"
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/delivery-variables", f.project.ID, map[string]any{
		"name": "orders_runtime", "kind": "config_env", "usage": "deploy", "is_secret": true, "is_active": true, "value": canary,
	})
	var payload struct {
		Variable model.DeliveryVariable `json:"variable"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Variable.ID == "" {
		t.Fatalf("配置创建失败: %d", response.Code)
	}
	path := "/delivery-variables/" + payload.Variable.ID + "/revisions"
	role := deliveryVariableProjectRole(t, f, "configuration-history-reader", []string{"project.read", "delivery_variable.read"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, role); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/1"} {
		response = deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, path+suffix, f.project.ID, nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || bytes.Contains(response.Body.Bytes(), []byte(canary)) {
			t.Fatalf("历史普通读取未保护密钥: %d", response.Code)
		}
	}
	denied := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, path+"/1/reveal", f.project.ID, nil)
	if denied.Code != http.StatusForbidden || bytes.Contains(denied.Body.Bytes(), []byte(canary)) {
		t.Fatal("仅查看权限不应读取正文")
	}
	role = deliveryVariableProjectRole(t, f, "configuration-history-revealer", []string{"project.read", "delivery_variable.read", "delivery_variable.reveal"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, role); err != nil {
		t.Fatal(err)
	}
	allowed := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, path+"/1/reveal", f.project.ID, nil)
	if allowed.Code != http.StatusOK || allowed.Header().Get("Cache-Control") != "no-store" || !bytes.Contains(allowed.Body.Bytes(), []byte(canary)) {
		t.Fatalf("有权限的明确读取失败: %d", allowed.Code)
	}
	var audits []model.AuditLog
	if err := f.db.Where("action = ? AND resource_id = ?", "delivery_variable.reveal", payload.Variable.ID).Order("id ASC").Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 2 || audits[0].Result != model.AuditDenied || audits[1].Result != model.AuditSucceeded {
		t.Fatal("正文读取缺少拒绝或成功审计")
	}
	for _, audit := range audits {
		if bytes.Contains(audit.Metadata, []byte(canary)) {
			t.Fatal("审计中泄漏配置正文")
		}
	}
	for _, suffix := range []string{"?page=0", "?page_size=1000", "/0", "/999"} {
		response = deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, path+suffix, f.project.ID, nil)
		if response.Code < http.StatusBadRequest || bytes.Contains(response.Body.Bytes(), []byte(canary)) {
			t.Fatal("无效历史请求不应成功")
		}
	}
	response = deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, path+"/1/reveal", "", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatal("正文读取必须绑定项目")
	}
}
