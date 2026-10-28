package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIOnlyRouterDoesNotServeFrontend(t *testing.T) {
	router := NewRouter(Dependencies{Environment: "test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, route := range router.Routes() {
		if !strings.HasPrefix(route.Path, "/api/v1/") {
			t.Errorf("Go 服务不得注册前端页面或静态资源路由: method=%s path=%s", route.Method, route.Path)
		}
	}
	for _, path := range []string{"/", "/login", "/index.html", "/pipeline-runs/example", "/assets", "/assets/app.js", "/assets/_plugin-vue_export-helper.js?version=1", "/api", "/api/", "/api/v1", "/api/v1/unknown"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			t.Run(method+" "+path, func(t *testing.T) {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(method, path, nil))
				if response.Code != http.StatusNotFound || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("未知路径必须稳定返回不缓存的 JSON 404: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
				}
				var body errorResponse
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != "not_found" || body.Message != "请求的资源不存在" || body.RequestID == "" {
					t.Fatalf("未知路径响应格式不正确: body=%s err=%v", response.Body.String(), err)
				}
				if response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("X-Frame-Options") != "DENY" {
					t.Fatal("API-only 模式不得移除现有安全响应头")
				}
			})
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil))
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("API 健康检查必须保留: status=%d body=%s", response.Code, response.Body.String())
	}
}
