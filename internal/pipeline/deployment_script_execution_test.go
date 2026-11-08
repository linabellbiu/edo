package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/sshdeploy"
	"github.com/google/uuid"
)

// 真实执行只写隔离临时目录，配置路径在落库测试快照前替换，不读取用户 HOME。
func TestDeploymentScriptWithoutBuildSavesFreezesAndExecutesLocalHost(t *testing.T) {
	s, db, secrets, repositoryID := newPipelineTestService(t)
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app, err := s.CreateApplication(context.Background(), "admin", ApplicationInput{Name: "script_inputs", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	actor := model.User{ID: "script-authority", Username: "script_authority", DepartmentID: app.DepartmentID, IsActive: true, IsSuperuser: true}
	if err = db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureDeliveryAccess(permissions)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, UserID: actor.ID})
	input := deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "脚本执行主机", Platform: model.DeploymentDocker, EnvironmentID: "script-inputs-env", RuntimeID: dockerengine.LocalEndpointID, WorkingDirectory: directory, WorkloadName: "sample", RolloutTimeout: 120})
	if err := db.Model(&model.Host{}).Where("uuid = ?", model.BuiltinLocalHostID).Update("architecture", model.HostArchitectureAMD64).Error; err != nil {
		t.Fatal(err)
	}
	capability := model.HostCapability{HostID: model.BuiltinLocalHostID, Kind: model.HostCapabilityLocalExec, Status: model.HostCapabilityReady}
	if err := db.Where("host_id = ? AND kind = ?", capability.HostID, capability.Kind).Assign(map[string]any{"status": model.HostCapabilityReady}).FirstOrCreate(&capability).Error; err != nil {
		t.Fatal(err)
	}
	workflow, err := s.GetWorkflow(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := model.WorkflowNode{ID: "source", Name: "代码源", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Events: []string{"manual"}, Branch: "main"}}
	script := "printf '%s|%s|%s' \"${EDO_GIT_TAG}\" \"${APP_ENV}\" \"${EDO_APP_CONFIG_PATH}\" > result.txt\n"
	fileBody := "MODE=one\n"
	configurationInput := cicd.VariableInput{Name: "runtime_settings", Kind: "config_env", Usage: "deploy", Value: &fileBody, IsSecret: true, IsActive: true, ApplicationID: app.ID, EnvironmentID: input.EnvironmentID}
	configuration, err := s.Delivery().SaveVariable(ctx, actor.ID, "", configurationInput)
	if err != nil {
		t.Fatal(err)
	}
	node := model.WorkflowNode{ID: "deploy", Name: "部署脚本", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentSource: "inline", DeploymentInputVersion: 1, DeploymentScript: script, EnvironmentVariables: map[string]string{"APP_ENV": "production"}, InlineDeploymentPlan: &model.WorkflowDeploymentPlan{Name: "流水线脚本部署", Kind: model.DeploymentPlanDocker, TimeoutSeconds: 120, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentDocker, EnvironmentID: input.EnvironmentID, WorkingDirectory: directory, WorkloadName: "sample"}}}}
	node.Config.DeploymentInputs = []model.DeploymentInput{{Kind: "configuration", ConfigurationID: configuration.ID, Path: "settings/runtime.env"}}
	node.Config.DeploymentTagReference = "EDO_IMAGE_TAG"
	invalid, err := s.SaveWorkflow(ctx, app.ID, actor.ID, WorkflowInput{SchemaVersion: 1, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Source: source, Stages: []model.WorkflowStage{{ID: "deploy-stage", Name: "部署", Tasks: []model.WorkflowNode{node}}}})
	if err != nil || invalid.Valid {
		t.Fatalf("裸镜像 Tag 变量名未被校验拒绝: %+v %v", invalid, err)
	}
	workflow = invalid
	node.Config.DeploymentTagReference = "${EDO_IMAGE_TAG}"
	saved, err := s.SaveWorkflow(ctx, app.ID, actor.ID, WorkflowInput{SchemaVersion: 1, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: []model.WorkflowStage{{ID: "deploy-stage", Name: "部署", Tasks: []model.WorkflowNode{node}}}})
	if err != nil || !saved.Valid {
		t.Fatalf("无构建脚本部署无法保存: %+v %v", saved, err)
	}
	run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "refs/tags/v1", strings.Repeat("a", 40), actor.ID, "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.ApplicationConfigDirectory != "~/edo/"+app.Name+"/config" {
		t.Fatal("默认配置路径未冻结")
	}
	frozen.ApplicationConfigDirectory = filepath.Join(directory, "config")
	data, _ := json.Marshal(frozen)
	run.WorkflowSnapshot = string(data)
	run.Status = model.PipelineRunRunning
	run.CurrentNodeID = "deploy"
	run.ExecutionJobID = uuid.NewString()
	if err = db.Create(run).Error; err != nil {
		t.Fatal(err)
	}
	component := model.PipelineRunRepository{ID: uuid.NewString(), PipelineRunID: run.ID, RepositoryID: repositoryID, Ref: run.Ref, CommitSHA: run.CommitSHA}
	if err = db.Create(&component).Error; err != nil {
		t.Fatal(err)
	}
	deployments := deployment.NewService(db, s.docker, nil, sshdeploy.NewService(db, secrets, config.Runtime{}), nil, "", s.logger)
	deployments.ConfigureReleaseVariables(secrets, permissions)
	s.ConfigureExecution(s.docker, deployments, s.logger)
	if err = s.executeDeployTask(ctx, DeployTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: "deploy"}, run.ExecutionJobID); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(filepath.Join(frozen.ApplicationConfigDirectory, "result.txt"))
	if err != nil || string(result) != "v1|production|"+frozen.ApplicationConfigDirectory {
		t.Fatalf("脚本未获取真实变量: %q %v", result, err)
	}
	initial, err := os.ReadFile(filepath.Join(frozen.ApplicationConfigDirectory, "settings/runtime.env"))
	if err != nil || string(initial) != fileBody {
		t.Fatal("部署未生成所选配置", err)
	}
	var final model.PipelineRun
	if err = db.First(&final, "uuid = ?", run.ID).Error; err != nil || final.Status != model.PipelineRunSucceeded {
		t.Fatalf("真实任务状态不正确: %+v %v", final, err)
	}
	fileBody = "MODE=two\n"
	configurationInput.ExpectedVersion = configuration.Version
	if _, err := s.Delivery().SaveVariable(ctx, actor.ID, configuration.ID, configurationInput); err != nil {
		t.Fatal(err)
	}
	if _, err := deployments.SaveRuntimeConfiguration(ctx, final.DeploymentID, actor.ID, deployment.InstanceControlConfigurationInput{RestartScript: "cat \"${EDO_APP_CONFIG_PATH}/settings/runtime.env\" > restart.txt; cat \"${EDO_APP_CONFIG_PATH}/settings/runtime.env\"", TimeoutSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	state, err := deployments.ControlRuntime(ctx, final.DeploymentID, actor.ID, deployment.RuntimeControlInput{Action: "restart"})
	if err != nil || state.Kind != "script" || state.Output != "" {
		t.Fatalf("生命周期运行或脱敏失败: %+v %v", state, err)
	}
	changed, err := os.ReadFile(filepath.Join(frozen.ApplicationConfigDirectory, "restart.txt"))
	if err != nil || string(changed) != fileBody {
		t.Fatal("重启未使用最新配置正文", err)
	}
	if err := deployments.RemoveRuntime(ctx, final.DeploymentID, actor.ID); !errors.Is(err, deployment.ErrRuntimeControlUnsupported) {
		t.Fatalf("脚本实例误用了内置容器删除: %v", err)
	}

}
