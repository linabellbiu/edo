package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/model"
)

func TestDepartmentMemberDirectoryPreservesUserScopeAndMinimalFields(t *testing.T) {
	var db *gorm.DB
	var logs bytes.Buffer
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, value *gorm.DB) {
		db = value
		deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	readerRole := createRoleForHTTPTest(t, router, admin, "department-member-reader", []string{access.PermissionUserRead})
	departmentRole := createRoleForHTTPTest(t, router, admin, "department-only-reader", []string{access.PermissionDepartmentRead})
	readerID := createUserForHTTPTest(t, router, admin, "department-member-reader", "", []string{readerRole})
	createUserForHTTPTest(t, router, admin, "department-only-reader", "", []string{departmentRole})
	foreignDepartment := createDepartmentForHTTPTest(t, router, admin, "成员隔离部门")
	foreignID := createUserForHTTPTest(t, router, admin, "foreign-member", foreignDepartment, nil)
	reader := loginForDepartmentTest(t, router, "department-member-reader")
	departmentOnly := loginForDepartmentTest(t, router, "department-only-reader")

	read := func(cookie *http.Cookie, path string) []map[string]any {
		t.Helper()
		response := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("读取部门成员失败: status=%d body=%s", response.Code, response.Body.String())
		}
		var payload struct {
			Users []map[string]any `json:"users"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Users
	}
	users := read(reader, "/api/v1/users?view=members&limit=200")
	foundReader := false
	for _, user := range users {
		if len(user) != 5 || user["id"] == foreignID || user["department_id"] == foreignDepartment {
			t.Fatalf("成员目录超出授权范围或返回多余字段: %#v", user)
		}
		for _, field := range []string{"id", "username", "nickname", "department_id", "is_active"} {
			if _, ok := user[field]; !ok {
				t.Fatalf("缺少成员基本字段 %s", field)
			}
		}
		foundReader = foundReader || user["id"] == readerID
	}
	if !foundReader {
		t.Fatal("成员目录未包含本部门用户")
	}
	denied := performJSONRequest(t, router, http.MethodGet, "/api/v1/users?view=members", nil, departmentOnly)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("部门查看权限意外开放用户目录: status=%d", denied.Code)
	}
	all := read(admin, "/api/v1/users?view=members&limit=200")
	page := read(admin, "/api/v1/users?view=members&limit=1&offset=1")
	if len(all) < 2 || len(page) != 1 || page[0]["id"] != all[1]["id"] {
		t.Fatalf("成员目录分页未保持稳定顺序: all=%v page=%v", all, page)
	}
	var latest model.User
	if err := db.Order("id DESC").First(&latest).Error; err != nil {
		t.Fatal(err)
	}
	if all[0]["id"] != latest.ID {
		t.Fatal("成员目录未按自增 ID 倒序排列")
	}

	const callback = "test:department_member_directory_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" && len(tx.Statement.Selects) == 5 {
			tx.AddError(errors.New("member-directory-private-db-error"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	logs.Reset()
	failed := performJSONRequest(t, router, http.MethodGet, "/api/v1/users?view=members", nil, admin)
	if failed.Code != http.StatusInternalServerError || strings.Contains(failed.Body.String(), "private-db-error") {
		t.Fatalf("成员查询错误未安全返回: status=%d body=%s", failed.Code, failed.Body.String())
	}
	if !strings.Contains(logs.String(), "user_member_list") || !strings.Contains(logs.String(), "member-directory-private-db-error") {
		t.Fatal("成员查询失败未记录操作和原始错误")
	}
}
