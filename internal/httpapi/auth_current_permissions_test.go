package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"gorm.io/gorm"

	"edo/internal/access"
)

func TestCurrentProfileDoesNotRestoreRevokedPermissionBeforePolicyReload(t *testing.T) {
	var deps Dependencies
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, database *gorm.DB) { deps = *value; db = database })
	defer cleanup()
	user, err := deps.Accounts.CreateUser(context.Background(), "profile-reader", "审计读者", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	grantProjectMembershipForTest(t, deps, user)
	if err := deps.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Allow: []string{access.PermissionAuditRead}}); err != nil {
		t.Fatal(err)
	}
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "profile-reader", "password": "correct horse battery staple"}, nil)
	if login.Code != http.StatusOK {
		t.Fatal("测试读者无法登录")
	}
	cookie := login.Result().Cookies()[0]
	permissions := func() []string {
		response := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("当前身份读取失败: status=%d", response.Code)
		}
		var value struct {
			User struct {
				Permissions []string `json:"permissions"`
			} `json:"user"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value.User.Permissions
	}
	hasAudit := func(values []string) bool {
		for _, value := range values {
			if value == access.PermissionAuditRead {
				return true
			}
		}
		return false
	}
	if !hasAudit(permissions()) {
		t.Fatal("原授权未生效，不能验收撤权")
	}
	// 另一服务实例正常提交撤权，但当前实例尚未接到策略重载通知。
	other, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Deny: []string{access.PermissionAuditRead}}); err != nil {
		t.Fatal(err)
	}
	if hasAudit(permissions()) {
		t.Fatal("撤权已提交，当前身份响应仍返回旧权限，可能重新显示敏感页面")
	}
}

func TestCurrentProfileKeepsStorageDiagnosticsOutOfResponse(t *testing.T) {
	var deps Dependencies
	var db *gorm.DB
	var logs bytes.Buffer
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, database *gorm.DB) {
		value.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		deps = *value
		db = database
	})
	defer cleanup()
	user, err := deps.Accounts.CreateUser(context.Background(), "profile-failure", "故障读者", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	grantProjectMembershipForTest(t, deps, user)
	if err := deps.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Allow: []string{access.PermissionAuditRead}}); err != nil {
		t.Fatal(err)
	}
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "profile-failure", "password": "correct horse battery staple"}, nil)
	if login.Code != http.StatusOK {
		t.Fatal("测试用户登录失败")
	}
	const marker = "sql-current-private-diagnostic"
	const callback = "profile-snapshot-read-failure"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "role_permissions" {
			tx.AddError(errors.New(marker))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(callback)
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, login.Result().Cookies()[0])
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), marker) || strings.Contains(response.Body.String(), `"permissions"`) {
		t.Fatal("身份快照读取失败被误报为空权限或泄漏了诊断信息")
	}
	if !strings.Contains(logs.String(), marker) || !strings.Contains(logs.String(), "auth_me_permissions") {
		t.Fatal("内部日志缺少身份快照故障的诊断上下文")
	}
}
