package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"edo/internal/deployment"
)

func TestRuntimeControlAuthorizationFailureReturnsSafeForbidden(t *testing.T) {
	for _, action := range []string{"stop", "restart", "scale", "remove"} {
		t.Run(action, func(t *testing.T) {
			var logs bytes.Buffer
			handler := deploymentHandler{logger: slog.New(slog.NewTextHandler(&logs, nil))}
			router := gin.New()
			router.POST("/deployments/:id/runtime", func(c *gin.Context) {
				handler.writeRuntimeError(c, "deployment_runtime_"+action, errors.Join(deployment.ErrExecutionAuthorization, errors.New("fixture internal diagnostic")))
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/deployments/test-deployment/runtime", nil))
			var payload errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusForbidden || payload.Code != "deployment_authorization_revoked" || payload.Message != deployment.ErrExecutionAuthorization.Error() {
				t.Fatalf("运行授权失效未返回稳定403: status=%d code=%s message=%s", response.Code, payload.Code, payload.Message)
			}
			if strings.Contains(response.Body.String(), "fixture internal diagnostic") {
				t.Fatal("运行授权错误向客户端泄露内部诊断")
			}
			if !strings.Contains(logs.String(), "deployment_runtime_"+action) || !strings.Contains(logs.String(), "test-deployment") || !strings.Contains(logs.String(), "fixture internal diagnostic") {
				t.Fatal("运行授权拒绝日志缺少操作、目标或诊断上下文")
			}
		})
	}
}
