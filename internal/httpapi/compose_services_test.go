package httpapi

import (
	"context"
	"edo/internal/model"
	"edo/internal/repository"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestComposeServicesHTTPReturnsOnlyParsedServiceNames(t *testing.T) {
	f := newReleasePlanExecutionHTTPFixture(t, "0123456789abcdef0123456789abcdef01234567")
	defer f.close()
	repo, _, err := f.repositories.Create(context.Background(), "admin", repository.Input{Name: "Compose来源接口仓库", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/compose.git", DefaultBranch: "main", AuthType: model.GitAuthNone})
	if err != nil {
		t.Fatal(err)
	}
	app, _, _, _ := createReleasePlanExecutionHTTPApplication(t, f.pipelines, f.db, repo.ID)
	cookie := loginForDepartmentTest(t, f.router, "admin")
	url := "/api/v1/applications/" + app.ID + "/compose-services"
	valid := "services:\n  worker:\n    image: alpine:3.22\n    environment:\n      PRIVATE_VALUE: never-return-this-body\n  api:\n    image: alpine:3.22\n"
	response := performJSONRequest(t, f.router, http.MethodPost, url, map[string]any{"content": valid}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("服务解析失败: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Services []string `json:"services"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Join(body.Services, ",") != "api,worker" || strings.Contains(response.Body.String(), "never-return-this-body") {
		t.Fatal("接口应只返回服务名", response.Body.String())
	}
	generic := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/workflows/compose-services", map[string]any{"content": valid}, cookie)
	if generic.Code != http.StatusOK || strings.Contains(generic.Body.String(), "never-return-this-body") {
		t.Fatal("未关联应用的正文也应复用服务解析接口", generic.Code, generic.Body.String())
	}
	for _, input := range []map[string]any{{"content": "services: [broken"}, {"source": map[string]any{"kind": "repository", "path": "../private"}}, {"source": map[string]any{"kind": "configuration", "configuration_id": "unavailable"}}} {
		response = performJSONRequest(t, f.router, http.MethodPost, url, input, cookie)
		if (response.Code != http.StatusBadRequest && response.Code != http.StatusForbidden) || strings.Contains(response.Body.String(), "services: [broken") {
			t.Fatalf("无效来源必须返回安全错误: %d %s", response.Code, response.Body.String())
		}
	}
}
