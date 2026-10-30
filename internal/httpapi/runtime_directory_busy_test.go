package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	artifactmanager "edo/internal/artifact"

	"gorm.io/gorm"
)

func TestLocalArtifactCleanupReturnsConflictWhileDirectoryIsInUse(t *testing.T) {
	var artifacts *artifactmanager.Service
	router, closeTest := newAuthTestRouter(t, func(deps *Dependencies, _ *gorm.DB) { artifacts = deps.Artifacts })
	defer closeTest()
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatal("登录隔离测试管理员失败")
	}
	root := artifacts.Root()
	change, err := artifacts.StageDirectory(context.Background(), root)
	if err != nil {
		t.Fatalf("准备目录维护锁失败: %v", err)
	}
	defer change.Abort()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/settings/runtime-directories/cleanup-artifacts", nil)
	request.AddCookie(login.Result().Cookies()[0])
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		change.Abort()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("目录维护结束后清理请求仍未退出")
		}
		t.Fatal("目录忙碌时清理接口未及时返回")
	}
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"directory_busy"`) ||
		!strings.Contains(response.Body.String(), artifactmanager.ErrArtifactDirectoryBusy.Error()) {
		t.Fatalf("目录忙碌应返回安全的409提示: status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), root) {
		t.Fatal("目录忙碌响应泄露了内部路径")
	}
}
