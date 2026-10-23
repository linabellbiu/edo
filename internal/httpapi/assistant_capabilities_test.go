package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/assistant"
)

func TestAssistantCapabilitiesRequireAccessAndWorkWithoutModelKey(t *testing.T) {
	f := newAssistantHTTPFixture(t)
	response := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/assistant/capabilities", nil, f.admin)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("无Key能力目录不可用: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Capabilities []assistant.Capability `json:"capabilities"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Capabilities) < 8 {
		t.Fatalf("能力目录缺失: %v", err)
	}
	for _, cookie := range []*http.Cookie{nil, f.session(t, false)} {
		response = performJSONRequest(t, f.router, http.MethodGet, "/api/v1/assistant/capabilities", nil, cookie)
		if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
			t.Fatalf("未授权能力目录未拒绝: %d", response.Code)
		}
	}
}
