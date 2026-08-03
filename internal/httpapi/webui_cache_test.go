package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWebUICacheHeadersKeepHTMLFreshAndHashedAssetsImmutable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(webUICacheHeaders())
	router.GET("/", func(c *gin.Context) { c.Data(http.StatusOK, "text/html", []byte("index")) })
	router.GET("/pipeline-runs/:id", func(c *gin.Context) { c.Data(http.StatusOK, "text/html", []byte("index")) })
	router.GET("/assets/app-deadbeef.js", func(c *gin.Context) { c.Data(http.StatusOK, "text/javascript", []byte("export {}")) })
	router.GET("/api/v1/health/live", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	tests := []struct {
		path string
		want string
	}{
		{path: "/", want: "no-store"},
		{path: "/pipeline-runs/example", want: "no-store"},
		{path: "/assets/app-deadbeef.js", want: "public, max-age=31536000, immutable"},
		{path: "/api/v1/health/live", want: ""},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			router.ServeHTTP(response, request)
			if got := response.Header().Get("Cache-Control"); got != test.want {
				t.Fatalf("缓存策略不正确: got=%q want=%q", got, test.want)
			}
		})
	}
}
