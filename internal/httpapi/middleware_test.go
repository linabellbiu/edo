package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"zrt/internal/logging"
)

func TestValidRequestID(t *testing.T) {
	for _, value := range []string{"abc", "request-123", "request_123"} {
		if !validRequestID(value) {
			t.Fatalf("合法请求 ID 被拒绝: %s", value)
		}
	}
	for _, value := range []string{"", "token?secret", "含中文"} {
		if validRequestID(value) {
			t.Fatalf("非法请求 ID 被接受: %s", value)
		}
	}
}

func TestAccessLogCanBeDisabledAtRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	_, runtimeLogs := logging.NewRuntime("info")
	if err := runtimeLogs.Apply("info", false); err != nil {
		t.Fatalf("关闭 HTTP 访问日志失败: %v", err)
	}
	router := gin.New()
	router.Use(requestID(), accessLog(logger, runtimeLogs))
	router.GET("/health", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	if output.Len() != 0 {
		t.Fatalf("关闭后仍输出 HTTP 访问日志: %s", output.String())
	}
	if err := runtimeLogs.Apply("info", true); err != nil {
		t.Fatalf("开启 HTTP 访问日志失败: %v", err)
	}
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	if !bytes.Contains(output.Bytes(), []byte(`"operation":"http_request"`)) {
		t.Fatalf("开启后没有输出 HTTP 访问日志: %s", output.String())
	}
}
