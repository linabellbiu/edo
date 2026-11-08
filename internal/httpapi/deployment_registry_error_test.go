package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"edo/internal/pipeline"
	"github.com/gin-gonic/gin"
)

func TestDeploymentRegistryErrorsHaveSafeClientResponses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{pipeline.ErrDeploymentRegistryRequired, "deployment_registry_required"},
		{pipeline.ErrDeploymentRegistryUnavailable, "deployment_registry_unavailable"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			handler := pipelineHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			handler.writeError(c, "test_saved_registry", errors.Join(tc.err, errors.New("内部诊断不应对外返回")))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), tc.code) || !strings.Contains(recorder.Body.String(), tc.err.Error()) || strings.Contains(recorder.Body.String(), "内部诊断") {
				t.Fatalf("错误响应不安全或状态不正确: %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
