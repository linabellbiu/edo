package httpapi

import (
	"bytes"
	"net/http"
	"testing"
)

func TestDeliveryEnvTextHTTP(t *testing.T) {
	f := newDeliveryVariableProjectFixture(t)
	for _, candidate := range []struct{ name, kind string }{{"PROD_ENV", "env_text"}, {".prod.env", "env_file"}} {
		response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/delivery-variables", f.project.ID, map[string]any{
			"name": candidate.name, "kind": candidate.kind, "usage": "deploy", "value": "TOKEN=synthetic-private-env\nEMPTY=", "is_secret": true, "is_active": true,
		})
		if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte("synthetic-private-env")) {
			t.Fatalf("env 文本保存失败或回显: %d", response.Code)
		}
	}
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/delivery-variables", f.project.ID, map[string]any{
		"name": "INVALID_ENV", "kind": "env_text", "usage": "deploy", "value": "TOKEN=synthetic-private-env\nTOKEN=again", "is_secret": true, "is_active": true,
	})
	if response.Code != http.StatusBadRequest || bytes.Contains(response.Body.Bytes(), []byte("synthetic-private-env")) || !bytes.Contains(response.Body.Bytes(), []byte("第 2 行")) {
		t.Fatalf("校验没有返回安全行号: %d", response.Code)
	}
}
