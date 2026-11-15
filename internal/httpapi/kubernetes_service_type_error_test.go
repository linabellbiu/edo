package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"edo/internal/kube"
	"github.com/gin-gonic/gin"
)

func TestKubernetesServiceTypeErrorsKeepSafeActionableResponses(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{kube.ErrDeploymentTemplateHeadlessType, http.StatusConflict, "kubernetes_service_type_change_blocked"},
		{kube.ErrDeploymentTemplateExternalNamePorts, http.StatusBadRequest, "kubernetes_service_ports_required"},
		{kube.ErrDeploymentTemplateHPA, http.StatusBadRequest, "kubernetes_hpa_configuration_failed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			handler := pipelineHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			handler.writeError(c, "test_service_type", errors.Join(tc.err, errors.New("private raw server detail")))
			body := recorder.Body.String()
			if recorder.Code != tc.status || !strings.Contains(body, tc.code) || !strings.Contains(body, tc.err.Error()) || strings.Contains(body, "private raw") {
				t.Fatal("服务类型错误未安全显示正确引导", recorder.Code, body)
			}
		})
	}
}
