package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDeploymentScriptCheckUsesNormalPermissionAndSafeDiagnostics(t *testing.T) {
	f := newReleasePlanExecutionHTTPFixture(t, "0123456789abcdef0123456789abcdef01234567")
	defer f.close()
	path := "/api/v1/workflows/deployment-script/check"
	denied := performJSONRequest(t, f.router, http.MethodPost, path, map[string]any{"script": "echo ok"}, nil)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("未登录仍可检查脚本: %d", denied.Code)
	}
	cookie := loginForDepartmentTest(t, f.router, "admin")
	for _, script := range []string{"printf '%s' \"${IMAGE_TAG}\"", "if PRIVATE_SECRET=hidden; then\n"} {
		response := performJSONRequest(t, f.router, http.MethodPost, path, map[string]any{"script": script}, cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("语法接口失败: %d %s", response.Code, response.Body.String())
		}
		var body struct {
			Valid  bool `json:"valid"`
			Issues []struct {
				Line    uint   `json:"line"`
				Column  uint   `json:"column"`
				Message string `json:"message"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Valid != !strings.Contains(script, "PRIVATE_SECRET") || strings.Contains(response.Body.String(), "PRIVATE_SECRET") || strings.Contains(response.Body.String(), "hidden") {
			t.Fatalf("校验结果或脱敏错误: %s", response.Body.String())
		}
	}
}
