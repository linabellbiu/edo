package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"edo/internal/repository"
)

func TestRepositoryReferenceConflictReturnsStableSafeMessage(t *testing.T) {
	testRepositoryConflictReturnsStableSafeMessage(t, http.MethodPut, "repository_update", repository.ErrRepositoryChanged, "repository_changed")
}

func TestRepositoryDeleteConflictReturnsStableSafeMessage(t *testing.T) {
	testRepositoryConflictReturnsStableSafeMessage(t, http.MethodDelete, "repository_delete", repository.ErrRepositoryInUse, "repository_in_use")
}

func TestRepositoryDeleteRunningConflictReturnsStableSafeMessage(t *testing.T) {
	testRepositoryConflictReturnsStableSafeMessage(t, http.MethodDelete, "repository_delete", repository.ErrRepositoryRunning, "repository_running")
}

func testRepositoryConflictReturnsStableSafeMessage(t *testing.T, method, operation string, conflict error, code string) {
	t.Helper()
	var logs bytes.Buffer
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, "/api/v1/repositories/fixture", nil)
	c.Params = gin.Params{{Key: "id", Value: "fixture"}}
	handler := repositoryHandler{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	const diagnostic = "本例内部诊断不应回显"
	handler.writeServiceError(c, operation, fmt.Errorf("%w: %s", conflict, diagnostic))
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || recorder.Code != http.StatusConflict || payload.Code != code || payload.Message != conflict.Error() {
		t.Fatalf("仓库引用冲突没有返回稳定409边界: status=%d err=%v", recorder.Code, err)
	}
	if strings.Contains(recorder.Body.String(), diagnostic) || !strings.Contains(logs.String(), diagnostic) {
		t.Fatal("仓库冲突的内部诊断和客户端提示没有隔离")
	}
	if !strings.Contains(logs.String(), "operation="+operation) || !strings.Contains(logs.String(), "repository_id=fixture") {
		t.Fatal("仓库冲突日志缺少操作名称或准确仓库目标")
	}
}
