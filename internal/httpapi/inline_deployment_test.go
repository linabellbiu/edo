package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/model"
	"edo/internal/repository"
)

func TestWorkflowInlineDeploymentHTTPPersistsWithoutCreatingLibraryResources(t *testing.T) {
	f := newReleasePlanExecutionHTTPFixture(t, "0123456789abcdef0123456789abcdef01234567")
	defer f.close()
	repo, err := f.repositories.Create(context.Background(), "admin", repository.Input{Name: "自定义部署接口仓库", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/inline.git", DefaultBranch: "main", AuthType: model.GitAuthNone})
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

func TestWorkflowDeploymentShortcutUsesSameSaveTransaction(t *testing.T) {
	f := newReleasePlanExecutionHTTPFixture(t, "0123456789abcdef0123456789abcdef01234567")
	defer f.close()
	ctx := context.Background()
	admin := loginForDepartmentTest(t, f.router, "admin")
	role := createRoleForHTTPTest(t, f.router, admin, "workflow-only-editor", []string{"workflow.read", "workflow.update"})
	createUserForHTTPTest(t, f.router, admin, "workflow-only-editor", "", []string{role})
	editor := loginForDepartmentTest(t, f.router, "workflow-only-editor")
	repo, err := f.repositories.Create(ctx, "admin", repository.Input{
		Name: "快捷部署方案仓库", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/shortcut.git", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	app, flowID, _, _ := createReleasePlanExecutionHTTPApplication(t, f.pipelines, f.db, repo.ID)
	flow, err := f.pipelines.GetApplicationWorkflow(ctx, app.ID, flowID)
	if err != nil {
		t.Fatal(err)
	}
	var target model.DeploymentTarget
	if err := f.db.Where("platform = ?", model.DeploymentDocker).First(&target).Error; err != nil {
		t.Fatal(err)
	}
	task := model.WorkflowNode{ID: "inline-shortcut", Name: "当前任务", Type: model.WorkflowNodeDeploy, Disabled: true, Config: model.WorkflowNodeConfig{
		DeploymentSource: "inline",
		InlineDeploymentPlan: &model.WorkflowDeploymentPlan{
			Name: "当前任务", Kind: model.DeploymentPlanDocker, TimeoutSeconds: 300, DeploymentTarget: &target,
		},
	}}
	path := "/api/v1/applications/" + app.ID + "/workflows/" + flowID
	initialRevision := flow.Workflow.Revision
	request := workflowRequest{
		SaveOnly: true, TaskID: task.ID, SchemaVersion: 1, Revision: initialRevision, Source: flow.Workflow.Source,
		Stages:                     []model.WorkflowStage{{ID: "shortcut-stage", Name: "部署任务", Tasks: []model.WorkflowNode{task}}},
		ReusableDeploymentPlanName: "通用部署快捷方案",
	}
	denied := performJSONRequest(t, f.router, http.MethodPut, path, request, editor)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("无创建方案权限的保存应拒绝: %d %s", denied.Code, denied.Body.String())
	}
	saved := performJSONRequest(t, f.router, http.MethodPut, path, request, admin)
	var result struct {
		PlanID   string                `json:"reusable_deployment_plan_id"`
		Workflow model.ReleaseWorkflow `json:"workflow"`
	}
	if saved.Code != http.StatusOK || json.Unmarshal(saved.Body.Bytes(), &result) != nil || result.PlanID == "" {
		t.Fatalf("快捷方案与任务未一起保存: %d %s", saved.Code, saved.Body.String())
	}
	var plan model.DeploymentPlan
	if err := f.db.First(&plan, "uuid = ?", result.PlanID).Error; err != nil {
		t.Fatal(err)
	}
	if plan.DeploymentTargetID != "" || plan.Name != request.ReusableDeploymentPlanName || plan.ProjectID != app.ProjectID ||
		result.Workflow.Revision != initialRevision+1 || result.Workflow.IsActive != flow.Workflow.IsActive {
		t.Fatalf("方案不应复制目标或改变任务之外的数据: plan=%+v revision=%d", plan, result.Workflow.Revision)
	}
	storedTask := result.Workflow.Stages[len(result.Workflow.Stages)-1].Tasks[0]
	if storedTask.ID != task.ID || !storedTask.Disabled || storedTask.Config.InlineDeploymentPlan == nil || storedTask.Config.InlineDeploymentPlan.DeploymentTarget == nil {
		t.Fatal("当前任务的自定义配置、执行目标或开关发生变化")
	}

	request.Revision = result.Workflow.Revision
	request.Stages[0].Tasks[0].Name = "重名时不能写入此名称"
	conflict := performJSONRequest(t, f.router, http.MethodPut, path, request, admin)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("同名快捷方案应返回冲突: %d %s", conflict.Code, conflict.Body.String())
	}
	loaded, err := f.pipelines.GetApplicationWorkflow(ctx, app.ID, flowID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Workflow.Revision != result.Workflow.Revision || loaded.Workflow.Stages[len(loaded.Workflow.Stages)-1].Tasks[0].Name != task.Name {
		t.Fatal("快捷方案创建失败时不得提交当前任务的修改")
	}

	request.ReusableDeploymentPlanName = ""
	direct := performJSONRequest(t, f.router, http.MethodPut, path, request, editor)
	if direct.Code != http.StatusOK {
		t.Fatalf("无创建方案权限者仍能直接保存任务: %d %s", direct.Code, direct.Body.String())
	}

	request.Revision++
	request.ReusableDeploymentPlanName = "仓库 Compose 方案"
	config := &request.Stages[0].Tasks[0].Config
	config.InlineDeploymentPlan.Kind = model.DeploymentPlanComposeStack
	config.InlineDeploymentPlan.DeploymentTarget = &model.DeploymentTarget{Platform: model.DeploymentSSH}
	config.ComposeSource = &model.WorkflowComposeSource{Kind: "repository", Path: "deploy/compose.yaml"}
	savedCompose := performJSONRequest(t, f.router, http.MethodPut, path, request, admin)
	if savedCompose.Code != http.StatusOK || json.Unmarshal(savedCompose.Body.Bytes(), &result) != nil || result.PlanID == "" {
		t.Fatalf("未保存仓库 Compose 来源: %d %s", savedCompose.Code, savedCompose.Body.String())
	}
	plan = model.DeploymentPlan{}
	if err := f.db.First(&plan, "uuid = ?", result.PlanID).Error; err != nil {
		t.Fatal(err)
	}
	if plan.ComposeYAML != "" || plan.ExecutionConfig == nil || plan.ExecutionConfig.ComposeSource == nil ||
		*plan.ExecutionConfig.ComposeSource != *config.ComposeSource {
		t.Fatal("快捷 Compose 方案应保存真实来源，等执行时读取正文")
	}
}
