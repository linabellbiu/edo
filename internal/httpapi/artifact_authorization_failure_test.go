package httpapi

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	artifactmanager "edo/internal/artifact"
	"edo/internal/database"
)

func TestArtifactUploadAuthorizationFailureIsSafeAndNotAlwaysForbidden(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		failure error
	}{
		{"storage_failure", http.StatusInternalServerError, errors.New("测试内部存储诊断")},
		{"filesystem_permission_failure", http.StatusInternalServerError, fs.ErrPermission},
		{"permission_denied", http.StatusForbidden, errRequestAuthorizationDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			// 授权拒绝发生在服务调用前；无需数据库或存储目录。
			handler := artifactHandler{service: &artifactmanager.Service{}, logger: logger,
				uploadAuthorization: func(*gin.Context) func(context.Context) error {
					return func(context.Context) error { return test.failure }
				}}
			router := gin.New()
			router.POST("/upload", handler.upload)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("public body")))
			if response.Code != test.status || strings.Contains(response.Body.String(), "测试内部存储诊断") {
				t.Fatalf("授权失败分类或脱敏错误: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestArtifactRequestAuthorizationPreservesDatabaseFailure(t *testing.T) {
	var deps Dependencies
	router, closeTest := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) { deps = *value })
	defer closeTest()
	cookie := loginForDepartmentTest(t, router, "admin")
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), AllDepartments: true,
	})
	user, err := deps.Accounts.FindByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/upload", nil).WithContext(ctx)
	c.Request.AddCookie(cookie)
	c.Set(currentUserKey, user)
	check := requestAuthorization(deps, c, "artifact.create")
	if err := check(ctx); err != nil {
		t.Fatalf("健康存储的正对照失败: %v", err)
	}
	if err := deps.Database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := check(ctx); err == nil || errors.Is(err, errRequestAuthorizationDenied) {
		t.Fatalf("数据库故障被当成撤权或成功: %v", err)
	}
}
