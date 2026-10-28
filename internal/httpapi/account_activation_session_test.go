package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"gorm.io/gorm"

	"edo/internal/account"
)

func TestAccountDisableInvalidatesDormantSessionAfterReactivationHTTP(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	userID := createUserForHTTPTest(t, router, admin, "activation-worker", "", nil)
	active := loginForDepartmentTest(t, router, "activation-worker")
	dormant := loginForDepartmentTest(t, router, "activation-worker")
	for _, cookie := range []*http.Cookie{active, dormant} {
		if response := performJSONRequest(t, router, http.MethodGet, "/api/v1/projects", nil, cookie); response.Code != http.StatusOK {
			t.Fatalf("旧会话的真实接口前置未满足: status=%d", response.Code)
		}
	}
	setActive := func(value bool) {
		t.Helper()
		response := performJSONRequest(t, router, http.MethodPatch, "/api/v1/users/"+userID+"/status", map[string]bool{"active": value}, admin)
		if response.Code != http.StatusNoContent {
			t.Fatalf("修改专项账户启停状态失败: active=%v status=%d", value, response.Code)
		}
	}
	setActive(false)
	// 仅活跃会话在停用期触发校验；休眠会话不请求、不删除，也不读取会话存储。
	if response := performJSONRequest(t, router, http.MethodGet, "/api/v1/projects", nil, active); response.Code != http.StatusUnauthorized {
		t.Fatalf("停用账号的活跃会话未失效: status=%d", response.Code)
	}
	setActive(true)
	for name, cookie := range map[string]*http.Cookie{"活跃旧会话": active, "休眠旧会话": dormant} {
		if response := performJSONRequest(t, router, http.MethodGet, "/api/v1/projects", nil, cookie); response.Code != http.StatusUnauthorized {
			t.Errorf("重新启用账号后%s复活: status=%d", name, response.Code)
		}
	}
	fresh := loginForDepartmentTest(t, router, "activation-worker")
	if response := performJSONRequest(t, router, http.MethodGet, "/api/v1/projects", nil, fresh); response.Code != http.StatusOK {
		t.Fatalf("重新启用后正常新登录不可用: status=%d", response.Code)
	}
}

func TestAccountStatusTransitionsAreIdempotentAndProtectSuperuserHTTP(t *testing.T) {
	var accounts *account.Service
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, _ *gorm.DB) { accounts = deps.Accounts })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	userID := createUserForHTTPTest(t, router, admin, "activation-idempotent", "", nil)
	initial, err := accounts.FindByID(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		active bool
		bumps  uint64
	}{{true, 0}, {false, 1}, {false, 1}, {true, 1}, {true, 1}, {false, 2}} {
		response := performJSONRequest(t, router, http.MethodPatch, "/api/v1/users/"+userID+"/status", map[string]bool{"active": step.active}, admin)
		if response.Code != http.StatusNoContent {
			t.Fatalf("幂等启停请求失败: active=%v status=%d", step.active, response.Code)
		}
		stored, err := accounts.FindByID(context.Background(), userID)
		if err != nil {
			t.Fatalf("读取启停后的账户状态失败: err=%v", err)
		}
		if stored.IsActive != step.active || stored.AuthVersion != initial.AuthVersion+step.bumps {
			t.Fatalf("状态与认证版本没有按实际停用转换原子更新: active=%v want_bumps=%d actual_active=%v actual_version=%d", step.active, step.bumps, stored.IsActive, stored.AuthVersion)
		}
	}
	adminInfo := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, admin)
	adminID := currentUserIDFromResponse(t, adminInfo.Body.Bytes())
	before, err := accounts.FindByID(context.Background(), adminID)
	if err != nil {
		t.Fatal(err)
	}
	denied := performJSONRequest(t, router, http.MethodPatch, "/api/v1/users/"+adminID+"/status", map[string]bool{"active": false}, admin)
	if denied.Code != http.StatusConflict {
		t.Fatalf("当前超级管理员停用保护失效: status=%d", denied.Code)
	}
	after, err := accounts.FindByID(context.Background(), adminID)
	if err != nil || !after.IsActive || after.AuthVersion != before.AuthVersion {
		t.Fatalf("被拒绝的超级管理员操作改变了认证状态: err=%v", err)
	}
}

func TestAccountStatusWriteFailureDoesNotPartiallyRevokeHTTP(t *testing.T) {
	var accounts *account.Service
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, database *gorm.DB) { accounts, db = deps.Accounts, database })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	userID := createUserForHTTPTest(t, router, admin, "activation-write-failure", "", nil)
	before, err := accounts.FindByID(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("test:activation-status-write-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("测试账户状态写入失败"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove("test:activation-status-write-failure") })
	response := performJSONRequest(t, router, http.MethodPatch, "/api/v1/users/"+userID+"/status", map[string]bool{"active": false}, admin)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("账户状态写入失败被忽略: status=%d", response.Code)
	}
	after, err := accounts.FindByID(context.Background(), userID)
	if err != nil || !after.IsActive || after.AuthVersion != before.AuthVersion {
		t.Fatalf("账户状态写入失败后留下部分撤权状态: err=%v", err)
	}
}
