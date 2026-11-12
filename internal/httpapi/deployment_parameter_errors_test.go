package httpapi

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"edo/internal/model"
	"edo/internal/repository"
)

func TestWorkflowSaveAcceptsRepositoryEnvFileAndRejectsUnsafeParameters(t *testing.T) {
	f := newReleasePlanExecutionHTTPFixture(t, strings.Repeat("a", 40))
	defer f.close()
	repo, _, err := f.repositories.Create(context.Background(), "admin", repository.Input{Name: "参数诊断仓库", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/parameters.git", DefaultBranch: "main", AuthType: model.GitAuthNone})
	if err != nil {
		t.Fatal(err)
	}
	app, _, _, _ := createReleasePlanExecutionHTTPApplication(t, f.pipelines, f.db, repo.ID)
	flow, err := f.pipelines.GetWorkflow(context.Background(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	var target model.DeploymentTarget
	if err := f.db.Where("platform = ?", model.DeploymentDocker).First(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.HostCapability{HostID: target.HostID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	var registry model.ImageRegistry
	if err := f.db.First(&registry).Error; err != nil {
		t.Fatal(err)
	}
	execution := &model.DeploymentExecutionConfig{DeploymentInputVersion: 1, DeploymentRegistryID: registry.ID, DeploymentParameters: "--env-file ../secret", Compose: &model.ComposeDeploymentSettings{TagVariable: "${EDO_IMAGE_TAG}"}}
	deploy := model.WorkflowNode{ID: "repository-compose", Name: "Compose 部署", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentSource: "inline", ComposeSource: &model.WorkflowComposeSource{Kind: "repository", Path: "docker-compose.yaml"}, InlineDeploymentPlan: &model.WorkflowDeploymentPlan{Name: "Compose 部署", Kind: model.DeploymentPlanComposeStack, TimeoutSeconds: 600, ExecutionConfig: execution, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentSSH, EnvironmentID: target.EnvironmentID, HostIDs: []string{target.HostID}}}}}
	input := workflowRequest{Name: flow.Workflow.Name, SchemaVersion: model.WorkflowSchemaVersion, Revision: flow.Workflow.Revision, Activate: true, Source: flow.Workflow.Source, Stages: []model.WorkflowStage{{ID: "deploy", Name: "部署", Tasks: []model.WorkflowNode{deploy}}}}
	admin := loginForDepartmentTest(t, f.router, "admin")
	path := "/api/v1/applications/" + app.ID + "/workflows/" + flow.Workflow.ID
	bad := performJSONRequest(t, f.router, http.MethodPut, path, input, admin)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "部署参数无效") || strings.Contains(bad.Body.String(), "部署方案配置无效") {
		t.Fatalf("无效路径错误未说明原因: %d %s", bad.Code, bad.Body.String())
	}
	stored, err := f.pipelines.FindApplicationWorkflow(context.Background(), app.ID, flow.Workflow.ID)
	if err != nil || stored.Revision != flow.Workflow.Revision || !stored.IsActive || !reflect.DeepEqual(stored.Source, flow.Workflow.Source) || !reflect.DeepEqual(stored.Stages, flow.Workflow.Stages) {
		t.Fatalf("失败保存改写原已启用流水线: %+v %v", stored, err)
	}
	execution.DeploymentParameters = "--env-file .env"
	good := performJSONRequest(t, f.router, http.MethodPut, path, input, admin)
	if good.Code != http.StatusOK || !strings.Contains(good.Body.String(), `"valid":true`) {
		t.Fatalf("修正参数后仍无法保存并启用: %d %s", good.Code, good.Body.String())
	}
	reopened, err := f.pipelines.GetApplicationWorkflow(context.Background(), app.ID, flow.Workflow.ID)
	if err != nil || !reopened.Valid || !reopened.Workflow.IsActive || reopened.Workflow.Revision != flow.Workflow.Revision+1 {
		t.Fatalf("重新读取未返回已启用的有效配置: %+v %v", reopened, err)
	}
	savedConfig := reopened.Workflow.Stages[0].Tasks[0].Config
	if savedConfig.ComposeSource == nil || savedConfig.ComposeSource.Path != "docker-compose.yaml" || savedConfig.InlineDeploymentPlan == nil || savedConfig.InlineDeploymentPlan.ExecutionConfig == nil || savedConfig.InlineDeploymentPlan.ExecutionConfig.DeploymentRegistryID != registry.ID || savedConfig.InlineDeploymentPlan.ExecutionConfig.DeploymentParameters != "--env-file .env" {
		t.Fatalf("保存后丢失或改写部署草稿: %+v", savedConfig)
	}
	var count int64
	if err := f.db.Model(&model.PipelineRun{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("保存配置不得执行流水线", err)
	}
}
