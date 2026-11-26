package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

func TestCanceledAuthorizationBoundariesDoNotMutateOrAuditDenial(t *testing.T) {
	for _, test := range []struct {
		name, table, method, path string
		cancelBefore              bool
	}{
		{name: "请求进入前取消", method: http.MethodDelete, path: "/repositories/", cancelBefore: true},
		{name: "登录身份读取取消", table: "users", method: http.MethodDelete, path: "/repositories/"},
		{name: "项目读取取消", table: "projects", method: http.MethodDelete, path: "/repositories/"},
		{name: "单权限读取取消", table: "role_permissions", method: http.MethodDelete, path: "/repositories/"},
		{name: "任一权限读取取消", table: "role_permissions", method: http.MethodGet, path: "/repositories"},
		{name: "身份权限快照取消", table: "role_permissions", method: http.MethodGet, path: "/auth/me"},
		{name: "项目目录读取取消", table: "projects", method: http.MethodGet, path: "/projects"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStreamBoundaryFixture(t)
			admin := loginForDepartmentTest(t, f.router, "admin")
			projectID := database.DefaultProjectID(f.admin.DepartmentID)
			repositoryID := createRepositoryForDepartmentTest(t, f.router, admin, "cancel-boundary-repository")
			role := createRoleForHTTPTest(t, f.router, admin, "cancel-boundary-role", []string{"repository.read", "repository.delete"})
			if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, projectID, f.user.ID, role); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			triggered := false
			if test.cancelBefore {
				cancel()
			} else {
				const callback = "test:authorization-cancel"
				if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
					if !triggered && tx.Statement.Table == test.table {
						triggered = true
						cancel()
						tx.AddError(context.Canceled)
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = f.db.Callback().Query().Remove(callback) }()
			}
			path := test.path
			if test.method == http.MethodDelete {
				path += repositoryID
			}
			requestID := uuid.NewString()
			request := httptest.NewRequest(test.method, "/api/v1"+path, nil).WithContext(ctx)
			request.Header.Set("X-EDO-Project-ID", projectID)
			request.Header.Set("X-Request-ID", requestID)
			request.AddCookie(f.cookie)
			response := httptest.NewRecorder()
			f.router.ServeHTTP(response, request)
			if !test.cancelBefore && !triggered {
				t.Fatal("测试未触发指定鉴权查询的取消")
			}
			if response.Code != statusClientClosedRequest || response.Body.Len() != 0 || response.Header().Get("Set-Cookie") != "" {
				t.Fatalf("取消请求不应返回鉴权错误或修改会话：status=%d body=%s", response.Code, response.Body.String())
			}
			var repository model.GitRepository
			if err := f.db.First(&repository, "uuid = ?", repositoryID).Error; err != nil || repository.ProjectID != projectID {
				t.Fatalf("未完成鉴权的请求修改了资源：%v", err)
			}
			var count int64
			if err := f.db.Model(&model.AuditLog{}).Where("request_id = ?", requestID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("取消请求不能伪造成功或拒绝审计：count=%d err=%v", count, err)
			}
		})
	}
}

func TestProjectScopeDatabaseFailuresRemainUnavailableNotDenied(t *testing.T) {
	const diagnostic = "project-scope-private-db-error"
	for _, test := range []struct {
		name, method, path string
		err                error
	}{
		{"业务读取失败", http.MethodGet, "/api/v1/applications", errors.New(diagnostic)},
		{"平台范围失败", http.MethodGet, "/api/v1/roles", errors.New(diagnostic)},
		{"业务写入前失败", http.MethodPost, "/api/v1/repositories", errors.New(diagnostic)},
		{"内部取消不能冒充客户端取消", http.MethodGet, "/api/v1/applications", context.Canceled},
		{"内部超时不能冒充撤权", http.MethodGet, "/api/v1/applications", context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			var db *gorm.DB
			var logs bytes.Buffer
			router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, database *gorm.DB) {
				db = database
				deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			})
			defer cleanup()
			cookie := loginForDepartmentTest(t, router, "admin")
			const callback = "test:project-scope-db-failure"
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "projects" {
					tx.AddError(test.err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Callback().Query().Remove(callback) }()
			logs.Reset()
			requestID := uuid.NewString()
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("X-Request-ID", requestID)
			request.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), diagnostic) || strings.Contains(response.Body.String(), "project_denied") {
				t.Fatalf("项目存储错误应安全返回 5xx 而非撤权：%d %s", response.Code, response.Body.String())
			}
			if !strings.Contains(logs.String(), test.err.Error()) || !strings.Contains(logs.String(), "project_scope_lookup") {
				t.Fatal("项目读取错误缺少内部诊断上下文")
			}
			var count int64
			if err := db.Model(&model.AuditLog{}).Where("request_id = ?", requestID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("项目存储故障不能伪造权限拒绝审计：count=%d err=%v", count, err)
			}
		})
	}
}

func TestBusinessAuditRemainsAfterClientCancelsExecutedAction(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	scope := database.DepartmentScope{UserID: f.user.ID, DepartmentID: f.project.DepartmentID, ProjectID: f.project.ID}
	ctx = database.WithDepartmentScope(ctx, scope)
	requestID := uuid.NewString()
	router := gin.New()
	router.Use(requestIDMiddlewareForAuditTest(requestID, f.user))
	router.POST("/performed-action", auditAction(f.deps.Audits, f.deps.Logger, "test.executed", "project"), func(c *gin.Context) {
		setAuditResourceID(c, f.project.ID)
		cancel()
		c.Status(http.StatusNoContent)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/performed-action", nil).WithContext(ctx))
	var entry model.AuditLog
	if err := f.db.First(&entry, "request_id = ?", requestID).Error; err != nil || entry.Result != model.AuditSucceeded || entry.ProjectID != f.project.ID {
		t.Fatalf("动作执行后的断线不能丢失真实审计：%v", err)
	}
}

func requestIDMiddlewareForAuditTest(id string, user *model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(requestIDKey, id)
		c.Set(currentUserKey, user)
		c.Next()
	}
}
