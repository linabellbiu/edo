package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestComposeProjectNameIsNotUserConfiguration(t *testing.T) {
	var config model.WorkflowNodeConfig
	if err := json.Unmarshal([]byte(`{"compose":{"project_name":"legacy-project","tag_variable":"${EDO_IMAGE_TAG}"}}`), &config); err != nil {
		t.Fatal(err)
	}
	if config.Compose == nil || config.Compose.ProjectName != "" {
		t.Fatal("旧手填值不得作为运行身份读取")
	}
	config.Compose.ProjectName = "client-injected-result"
	node := model.WorkflowNode{ID: "deploy", Name: "部署", Type: model.WorkflowNodeDeploy, Config: config}
	if err := sanitizeWorkflowNode(&node); err != nil {
		t.Fatal(err)
	}
	if node.Config.Compose.ProjectName != "" {
		t.Fatal("客户端不能预置自动解析结果")
	}
	encoded, err := json.Marshal(node.Config)
	if err != nil || strings.Contains(string(encoded), "project_name") {
		t.Fatal("新方案仍保存了用户项目名")
	}
}

func TestComposeProjectResolutionIsFrozenAndCannotDrift(t *testing.T) {
	service, db, _, repoID := newPipelineTestService(t)
	ctx := deliveryTestContext()
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	configurePipelineExecutionTestAuthority(t, service, projectID, database.DefaultDepartmentID, "compose-executor")
	application, err := service.CreateApplication(ctx, "compose-executor", ApplicationInput{Name: "compose_identity", RepositoryID: repoID})
	if err != nil {
		t.Fatal(err)
	}
	node := model.WorkflowNode{ID: "deploy", Name: "部署", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: "plan", Compose: &model.ComposeDeploymentSettings{TagVariable: "${EDO_IMAGE_TAG}"}}}
	yaml := "name: ${STACK}\nservices:\n  app:\n    image: busybox:stable\n"
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, AuthorityID: "compose-executor", Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}, DeploymentPlans: map[string]workflowDeploymentPlanSnapshot{"deploy": {ID: "plan", Kind: model.DeploymentPlanComposeStack, ComposeYAML: yaml}}, DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{"deploy": {ID: "target"}}}
	encoded, _ := json.Marshal(snapshot)
	run := model.PipelineRun{ID: "compose-run", ApplicationID: application.ID, ProjectID: projectID, DepartmentID: database.DefaultDepartmentID, Status: model.PipelineRunRunning, WorkflowSnapshot: string(encoded)}
	if err := db.WithContext(ctx).Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	prepared := &executionContext{run: run, node: node, snapshot: snapshot, application: *application, target: model.DeploymentTarget{ID: "target"}, deploymentPlan: model.DeploymentPlan{ID: "plan", Kind: model.DeploymentPlanComposeStack, ComposeYAML: yaml}, variables: map[string]string{"STACK": "team-prod"}}
	if err := service.freezeComposeProject(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	var stored model.PipelineRun
	if err := db.WithContext(ctx).First(&stored, "uuid = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	frozen, err := parseWorkflowSnapshot(&stored)
	if err != nil || frozen.Stages[0].Tasks[0].Config.Compose.ProjectName != "team-prod" || !strings.Contains(stored.WorkflowSnapshot, `"resolved_project_name":"team-prod"`) {
		t.Fatal("实际项目名未写入运行快照")
	}
	before := stored.WorkflowSnapshot
	if err := service.freezeComposeProject(ctx, prepared); err != nil {
		t.Fatalf("同一结果的重复冻结应幂等: %v", err)
	}
	prepared.variables["STACK"] = "different-project"
	if err := service.freezeComposeProject(ctx, prepared); err == nil {
		t.Fatal("变量变化导致已有运行切换项目")
	}
	prepared.variables["STACK"] = "team-prod"
	prepared.variableRedactions = []string{"team-prod"}
	if err := service.freezeComposeProject(ctx, prepared); err == nil {
		t.Fatal("密钥变量进入了公开项目身份")
	}
	prepared.variableRedactions = nil
	if err := service.freezeComposeProject(context.Background(), prepared); err == nil {
		t.Fatal("缺少权限范围仍能写入运行身份")
	}
	if err := db.WithContext(ctx).First(&stored, "uuid = ?", run.ID).Error; err != nil || stored.WorkflowSnapshot != before {
		t.Fatal("失败解析修改了已有运行快照")
	}
}
