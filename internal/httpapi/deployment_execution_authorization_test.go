package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"edo/internal/deployment"
)

func TestDeploymentHTTPExecutionAuthorizationFailureIsSafe(t *testing.T) {
	var diagnostics bytes.Buffer
	handler := deploymentHandler{logger: slog.New(slog.NewJSONHandler(&diagnostics, nil))}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Params = gin.Params{{Key: "id", Value: "deployment-a"}}
	handler.writeDeploymentError(ctx, "deployment_rollback", fmt.Errorf("仅日志可见的诊断标记: %w", deployment.ErrExecutionAuthorization))
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || body.Code != "deployment_authorization_revoked" || body.Message != deployment.ErrExecutionAuthorization.Error() {
		t.Fatalf("执行前撤权未返回稳定的权限提示: status=%d body=%s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("诊断标记")) || !bytes.Contains(diagnostics.Bytes(), []byte("诊断标记")) || !bytes.Contains(diagnostics.Bytes(), []byte("deployment-a")) {
		t.Fatal("内部诊断或操作上下文未与用户提示正确分离")
	}
}
