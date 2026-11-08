package httpapi

import (
	"context"
	"edo/internal/model"
	"edo/internal/repository"
	"encoding/json"
	"net/http"
	"testing"
)

func TestWorkflowInlineDeploymentHTTPPersistsWithoutCreatingLibraryResources(t *testing.T) {
	f := newReleasePlanExecutionHTTPFixture(t, "0123456789abcdef0123456789abcdef01234567")
	defer f.close()
	repo, _, err := f.repositories.Create(context.Background(), "admin", repository.Input{Name: "自定义部署接口仓库", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/inline.git", DefaultBranch: "main", AuthType: model.GitAuthNone})
	if err != nil {
		t.Fatal(err)
	}
	app, workflowID, _, _ := createReleasePlanExecutionHTTPApplication(t, f.pipelines, f.db, repo.ID)
	workflow, err := f.pipelines.GetApplicationWorkflow(context.Background(), app.ID, workflowID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range workflow.Workflow.Stages {
		for j := range workflow.Workflow.Stages[i].Tasks {
			node := &workflow.Workflow.Stages[i].Tasks[j]
			if node.Type != model.WorkflowNodeDeploy {
				continue
			}
			var plan model.DeploymentPlan
			if err = f.db.Preload("DeploymentTarget").Where("uuid = ?", node.Config.DeploymentPlanID).First(&plan).Error; err != nil {
				t.Fatal(err)
			}
			node.Config.DeploymentPlanID = ""
			node.Config.DeploymentSource = "inline"
			node.Config.InlineDeploymentPlan = &model.WorkflowDeploymentPlan{Name: "流水线内的部署方案", Kind: plan.Kind, DockerConfig: plan.DockerConfig, TimeoutSeconds: plan.TimeoutSeconds, ExecutionConfig: plan.ExecutionConfig, DeploymentTarget: plan.DeploymentTarget}
		}
	}
	var plansBefore, targetsBefore int64
	f.db.Model(&model.DeploymentPlan{}).Count(&plansBefore)
	f.db.Model(&model.DeploymentTarget{}).Count(&targetsBefore)
	cookie := loginForDepartmentTest(t, f.router, "admin")
	response := performJSONRequest(t, f.router, http.MethodPut, "/api/v1/applications/"+app.ID+"/workflows/"+workflowID, map[string]any{"schema_version": model.WorkflowSchemaVersion, "name": workflow.Workflow.Name, "revision": workflow.Workflow.Revision, "source": workflow.Workflow.Source, "stages": workflow.Workflow.Stages, "activate": false}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("正常接口无法保存自定义方案: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Workflow model.ReleaseWorkflow `json:"workflow"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, stage := range body.Workflow.Stages {
		for _, node := range stage.Tasks {
			if node.Type == model.WorkflowNodeDeploy {
				found = node.Config.DeploymentPlanID == "" && node.Config.InlineDeploymentPlan != nil && node.Config.InlineDeploymentPlan.Name == "流水线内的部署方案"
			}
		}
	}
	if !found {
		t.Fatal("保存接口未回显流水线内方案")
	}
	var plansAfter, targetsAfter int64
	f.db.Model(&model.DeploymentPlan{}).Count(&plansAfter)
	f.db.Model(&model.DeploymentTarget{}).Count(&targetsAfter)
	if plansBefore != plansAfter || targetsBefore != targetsAfter {
		t.Fatal("保存自定义方案不应新增库记录")
	}
}
