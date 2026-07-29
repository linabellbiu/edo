package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"zrt/internal/logging"
)

func TestListSystemLogsReturnsCurrentProcessEntries(t *testing.T) {
	logger, runtimeLogs := logging.NewRuntime("info")
	logger.Info("系统日志接口测试", "operation", "system_log_test", "component", "httpapi")
	handler := systemLogHandler{service: runtimeLogs, logger: logger}

	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/v1/logs?level=info&query=httpapi", nil)
	handler.list(context)

	if response.Code != http.StatusOK {
		t.Fatalf("读取系统日志响应错误: status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Items []logging.Entry `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析系统日志响应失败: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Operation != "system_log_test" || body.Items[0].Fields["component"] != "httpapi" {
		t.Fatalf("系统日志接口内容错误: %+v", body.Items)
	}
}

func TestListSystemLogsRejectsInvalidFilter(t *testing.T) {
	logger, runtimeLogs := logging.NewRuntime("info")
	handler := systemLogHandler{service: runtimeLogs, logger: logger}

	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/v1/logs?level=verbose", nil)
	handler.list(context)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("无效系统日志查询状态码错误: status=%d body=%s", response.Code, response.Body.String())
	}
}
