package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestConfigurationFilesHTTP(t *testing.T) {
	f := newDeliveryVariableProjectFixture(t)
	for _, format := range []string{"yaml", "toml", "env", "ini", "json", "text"} {
		response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/delivery-variables", f.project.ID, map[string]any{"name": "app." + format, "kind": "config_" + format, "usage": "deploy", "value": "private-configuration-original", "is_secret": true, "is_active": true, "auto_deploy": false})
		if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte("private-configuration-original")) {
			t.Fatalf("配置保存或隐藏失败: %s %d", format, response.Code)
		}
		var body struct {
			Variable struct {
				Kind string `json:"kind"`
				Auto bool   `json:"auto_deploy"`
			} `json:"variable"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Variable.Kind != "config_"+format || body.Variable.Auto {
			t.Fatal("配置类型或解耦语义错误", err)
		}
	}
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/delivery-variables", f.project.ID, map[string]any{"name": "bad.yaml", "kind": "config_yaml", "usage": "deploy", "value": "safe", "is_active": true, "auto_deploy": true})
	if response.Code != http.StatusBadRequest {
		t.Fatal("配置允许自动注入", response.Code)
	}
}
