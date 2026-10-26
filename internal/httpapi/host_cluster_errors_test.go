package httpapi

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"edo/internal/kube"
	"github.com/gin-gonic/gin"
)

func TestHostSaveReportsKubernetesIdentityErrorsWithoutPrivateDetails(t *testing.T) {
	for _, scenario := range []struct {
		err    error
		status int
		code   string
	}{
		{kube.ErrClusterHostMismatch, http.StatusBadRequest, "kubernetes_cluster_host_mismatch"},
		{kube.ErrClusterHostCheckUnavailable, http.StatusBadGateway, "kubernetes_cluster_host_check_unavailable"},
	} {
		t.Run(scenario.code, func(t *testing.T) {
			router := gin.New()
			handler := hostHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			router.POST("/test", func(c *gin.Context) {
				handler.writeError(c, "test", fmt.Errorf("private SSH diagnostic: %w", scenario.err))
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test", nil))
			if response.Code != scenario.status || !strings.Contains(response.Body.String(), scenario.code) || strings.Contains(response.Body.String(), "private SSH diagnostic") {
				t.Fatalf("不应暴露内部诊断: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
