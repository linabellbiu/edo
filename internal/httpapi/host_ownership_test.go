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

func TestHostOwnershipK3sErrorIsConflictAndDoesNotLeakDiagnostics(t *testing.T) {
	router := gin.New()
	handler := k3sHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	router.POST("/test", func(c *gin.Context) {
		handler.writeError(c, "test", fmt.Errorf("private-source-detail: %w", kube.ErrHostClusterOccupied))
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/test", nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "host_cluster_occupied") || !strings.Contains(response.Body.String(), "一台主机只能属于一个集群") || strings.Contains(response.Body.String(), "private-source-detail") {
		t.Fatalf("unsafe conflict: %d %s", response.Code, response.Body.String())
	}
}
