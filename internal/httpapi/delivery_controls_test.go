package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeliveryControlHTTPRequiresProjectAndHidesSecrets(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	call := func(method, path, project string, payload any) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(body))
		req.AddCookie(f.cookie)
		req.Header.Set("Content-Type", "application/json")
		if project != "" {
			req.Header.Set("X-EDO-Project-ID", project)
		}
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, req)
		return response
	}
	for _, path := range []string{"/delivery-variables", "/application-databases", "/environment-policies"} {
		if response := call(http.MethodGet, path, "", nil); response.Code != 400 {
			t.Fatalf("接口未要求项目: %s %d", path, response.Code)
		}
	}
	response := call(http.MethodPost, "/delivery-variables", f.project.ID, map[string]any{"name": "PRIVATE_TOKEN", "kind": "env", "usage": "build", "is_secret": true, "is_active": true, "value": "secret-for-http-boundary"})
	if response.Code != 200 || bytes.Contains(response.Body.Bytes(), []byte("secret-for-http-boundary")) {
		t.Fatalf("密钥创建失败或回显明文: %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodGet, "/delivery-variables", f.project.ID, nil)
	if response.Code != 200 || bytes.Contains(response.Body.Bytes(), []byte("secret-for-http-boundary")) {
		t.Fatal("密钥列表回显明文")
	}
	response = call(http.MethodGet, "/delivery-variables", "inaccessible-project", nil)
	if response.Code != 403 {
		t.Fatal("跨项目请求未被拒绝")
	}
}
