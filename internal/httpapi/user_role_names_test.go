package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/model"
)

func TestUserListIncludesOnlyBoundRoleNamesWithoutRoleRead(t *testing.T) {
	var db *gorm.DB
	var logs bytes.Buffer
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, value *gorm.DB) {
		db = value
		deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	readerRole := createRoleForHTTPTest(t, router, admin, "role-name-reader", []string{access.PermissionUserRead})
	memberRole := createRoleForHTTPTest(t, router, admin, "role-name-member", []string{access.PermissionAuditRead})
	foreignRole := createRoleForHTTPTest(t, router, admin, "role-name-foreign", []string{access.PermissionAuditRead})
	unboundRole := createRoleForHTTPTest(t, router, admin, "role-name-unbound", []string{access.PermissionAuditRead})
	updated := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+memberRole, map[string]any{
		"name": "role-name-member", "display_name": "本部门审计员", "description": "role-name-private-description",
	}, admin)
	if updated.Code != http.StatusOK {
		t.Fatalf("更新测试角色显示名称失败: status=%d body=%s", updated.Code, updated.Body.String())
	}
	readerID := createUserForHTTPTest(t, router, admin, "role-name-reader", "", []string{readerRole})
	memberID := createUserForHTTPTest(t, router, admin, "role-name-member", "", []string{readerRole, memberRole})
	foreignDepartment := createDepartmentForHTTPTest(t, router, admin, "角色名称隔离部门")
	foreignID := createUserForHTTPTest(t, router, admin, "role-name-foreign", foreignDepartment, []string{foreignRole})
	emptyID := createUserForHTTPTest(t, router, admin, "role-name-empty", "", nil)
	// 遗留的无效绑定仍可呈现原 UUID，但不能伪造角色显示名称。
	missingRole := uuid.NewString()
	if err := db.Create(&model.UserRole{UserID: memberID, RoleID: missingRole}).Error; err != nil {
		t.Fatal(err)
	}
	reader := loginForDepartmentTest(t, router, "role-name-reader")
	denied := performJSONRequest(t, router, http.MethodGet, "/api/v1/roles", nil, reader)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("用户查看权限意外开放角色目录: status=%d", denied.Code)
	}
	var roleQueries atomic.Int64
	const callback = "test:user_list_role_names"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "roles" {
			roleQueries.Add(1)
			if !slices.Equal(tx.Statement.Selects, []string{"uuid", "display_name"}) {
				t.Errorf("用户列表读取了角色名称之外的字段: %v", tx.Statement.Selects)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	type userRoles struct {
		ID        string            `json:"id"`
		RoleIDs   []string          `json:"role_ids"`
		RoleNames map[string]string `json:"role_names"`
	}
	readUsers := func(path string) []userRoles {
		t.Helper()
		response := performJSONRequest(t, router, http.MethodGet, path, nil, reader)
		if response.Code != http.StatusOK {
			t.Fatalf("读取用户列表失败: status=%d body=%s", response.Code, response.Body.String())
		}
		for _, hidden := range []string{foreignID, foreignRole, unboundRole, "role-name-foreign", "role-name-unbound", "role-name-private-description"} {
			if strings.Contains(response.Body.String(), hidden) {
				t.Fatalf("用户列表泄漏不应返回的角色或用户信息: %s", hidden)
			}
		}
		var payload struct {
			Users []userRoles `json:"users"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Users
	}
	users := readUsers("/api/v1/users")
	wanted := map[string]map[string]string{
		readerID: {readerRole: "role-name-reader"},
		memberID: {readerRole: "role-name-reader", memberRole: "本部门审计员"},
		emptyID:  {},
	}
	for _, user := range users {
		if user.RoleNames == nil {
			t.Fatalf("用户 %s 缺少角色名称映射", user.ID)
		}
		if expected, ok := wanted[user.ID]; ok {
			if !maps.Equal(user.RoleNames, expected) {
				t.Fatalf("角色名称未按本用户绑定投影: user=%s got=%v want=%v", user.ID, user.RoleNames, expected)
			}
			delete(wanted, user.ID)
		}
		for roleID := range user.RoleNames {
			if !slices.Contains(user.RoleIDs, roleID) {
				t.Fatalf("用户 %s 获得未绑定角色的名称 %s", user.ID, roleID)
			}
		}
		if user.ID == memberID && !slices.Contains(user.RoleIDs, missingRole) {
			t.Fatal("无效角色的原绑定 UUID 被意外改写")
		}
	}
	if len(wanted) != 0 || roleQueries.Load() != 1 {
		t.Fatalf("用户缺失或角色名称没有一次批量查询: missing=%v queries=%d", wanted, roleQueries.Load())
	}
	roleQueries.Store(0)
	page := readUsers("/api/v1/users?limit=1&offset=1")
	if len(page) != 1 || page[0].ID != emptyID || len(page[0].RoleNames) != 0 || roleQueries.Load() != 0 {
		t.Fatalf("空角色分页额外查询或泄漏其它页名称: users=%+v queries=%d", page, roleQueries.Load())
	}
	const failure = "synthetic-role-name-storage-error"
	const failureCallback = "test:user_list_role_names_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(failureCallback, func(tx *gorm.DB) {
		if tx.Statement.Table == "roles" {
			tx.AddError(errors.New(failure))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(failureCallback) })
	logs.Reset()
	failed := performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, reader)
	if failed.Code != http.StatusInternalServerError || strings.Contains(failed.Body.String(), failure) ||
		!strings.Contains(failed.Body.String(), "服务暂时不可用，请稍后重试") {
		t.Fatalf("角色名称查询故障未返回安全错误: status=%d body=%s", failed.Code, failed.Body.String())
	}
	if !strings.Contains(logs.String(), failure) || !strings.Contains(logs.String(), "user_list_role_names") ||
		!strings.Contains(logs.String(), "request_id") || !strings.Contains(logs.String(), "role_count") {
		t.Fatalf("角色名称查询故障缺少操作上下文或原始错误: %s", logs.String())
	}
}
