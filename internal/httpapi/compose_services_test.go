package httpapi

import (
	"context"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/pipeline"
	"edo/internal/repository"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

type composeDetectionHTTPCheckout struct {
	releasePlanExecutionRefLister
	calls int
}

func (c *composeDetectionHTTPCheckout) Checkout(_ context.Context, _ model.GitRepository, _, _, _, directory string) error {
	c.calls++
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "docker-compose.yaml"), []byte("services:\n  api:\n    image: private-image\n"), 0600)
}

func TestDetectComposeFileHTTPMetadataAndAuthorization(t *testing.T) {
	checkout := &composeDetectionHTTPCheckout{releasePlanExecutionRefLister: releasePlanExecutionRefLister{commitSHA: strings.Repeat("a", 40)}}
	f := newReleasePlanExecutionHTTPFixture(t, strings.Repeat("a", 40), checkout)
	defer f.close()
	repo, _, err := f.repositories.Create(context.Background(), "admin", repository.Input{Name: "自动检测仓库", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/detect.git", DefaultBranch: "main", AuthType: model.GitAuthNone})
	if err != nil {
		t.Fatal(err)
	}
	app, _, _, _ := createReleasePlanExecutionHTTPApplication(t, f.pipelines, f.db, repo.ID)
	admin := loginForDepartmentTest(t, f.router, "admin")
	path := "/api/v1/applications/" + app.ID + "/compose-file"
	var runsBefore int64
	if err := f.db.Model(&model.PipelineRun{}).Count(&runsBefore).Error; err != nil {
		t.Fatal(err)
	}
	response := performJSONRequest(t, f.router, http.MethodPost, path, map[string]any{"ref": "main"}, admin)
	var result pipeline.ComposeFileDetection
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Path != "docker-compose.yaml" || result.CommitSHA != strings.Repeat("a", 40) {
		t.Fatalf("检测结果错误: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-image") {
		t.Fatal("检测不得返回文件正文")
	}
	var runsAfter int64
	if err := f.db.Model(&model.PipelineRun{}).Count(&runsAfter).Error; err != nil || runsAfter != runsBefore {
		t.Fatal("检测不应创建运行", err)
	}
	for _, tc := range []struct {
		name        string
		permissions []string
		status      int
	}{
		{"compose_workflow_only", []string{"workflow.read"}, http.StatusForbidden},
		{"compose_repo_only", []string{"repository.read"}, http.StatusForbidden},
		{"compose_reader", []string{"workflow.read", "repository.read"}, http.StatusOK},
	} {
		role := createRoleForHTTPTest(t, f.router, admin, tc.name, tc.permissions)
		createUserForHTTPTest(t, f.router, admin, tc.name, database.DefaultDepartmentID, []string{role})
		reader := loginForDepartmentTest(t, f.router, tc.name)
		response = performJSONRequest(t, f.router, http.MethodPost, path, map[string]any{}, reader)
		if response.Code != tc.status {
			t.Fatalf("检测权限错误: %s %d %s", tc.name, response.Code, response.Body.String())
		}
	}
	calls := checkout.calls
	if err := f.db.Model(&model.Application{}).Where("uuid = ?", app.ID).Update("project_id", "foreign-project").Error; err != nil {
		t.Fatal(err)
	}
	response = performJSONRequest(t, f.router, http.MethodPost, path, map[string]any{}, admin)
	if response.Code != http.StatusNotFound || checkout.calls != calls {
		t.Fatalf("不得检测其他项目应用: %d %s", response.Code, response.Body.String())
	}
}
