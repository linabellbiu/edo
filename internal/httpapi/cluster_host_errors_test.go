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

func TestKubernetesHostValidationErrorsAreSafeAndActionable(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"mismatch", kube.ErrClusterHostMismatch, http.StatusBadRequest, "kubernetes_cluster_host_mismatch"},
		{"unavailable", kube.ErrClusterHostCheckUnavailable, http.StatusBadGateway, "kubernetes_cluster_host_check_unavailable"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			router := gin.New()
			handler := clusterHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			router.GET("/test", func(c *gin.Context) {
				handler.writeKubernetesError(c, "test", fmt.Errorf("private-key and upstream body: %w", scenario.err))
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
			if response.Code != scenario.status || !strings.Contains(response.Body.String(), scenario.code) || strings.Contains(response.Body.String(), "private-key") || strings.Contains(response.Body.String(), "upstream body") {
				t.Fatalf("unsafe error: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
