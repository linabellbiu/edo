package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"edo/internal/credential"
	"edo/internal/model"
	"edo/internal/repository"
)

func TestAssistantDraftPreviewRollsBackAndCreationStaysInactive(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	ctx := deliveryTestContext()
	input := AssistantWorkflowInput{ApplicationName: "assistant_draft", RepositoryID: repositoryID, Name: "测试流水线", Preset: "blank", Branch: "main", Events: []string{"manual"}}
	preview, err := s.PreviewAssistantWorkflow(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, entity := range []any{&model.Application{}, &model.ReleaseWorkflow{}, &model.PipelineRun{}, &model.BuildPlan{}, &model.DeploymentPlan{}} {
		var count int64
		if err := db.Model(entity).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("预览产生持久数据 %T: %d %v", entity, count, err)
		}
	}
	again, err := s.PreviewAssistantWorkflow(ctx, "admin", input)
	if err != nil || again.Fingerprint != preview.Fingerprint {
		t.Fatalf("预览指纹不稳定: %v", err)
	}
	created, err := s.CreateAssistantWorkflowExpected(ctx, "admin", input, preview.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.GetApplicationWorkflow(ctx, created.ApplicationID, created.WorkflowID)
	if err != nil || w.Workflow.IsActive {
		t.Fatalf("创建草稿自动启用: %+v %v", w, err)
	}
	if _, err := s.CreateAssistantWorkflowExpected(ctx, "admin", input, preview.Fingerprint); err == nil {
		t.Fatal("重复创建未拒绝")
	}
}

func TestAssistantDraftUsesNativePlansAndAtomicRollback(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	ctx := deliveryTestContext()
	target := deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "现有环境", Platform: model.DeploymentDocker, RuntimeID: "local", WorkloadName: "app"})
	input := AssistantWorkflowInput{ApplicationName: "assistant_full", RepositoryID: repositoryID, Name: "镜像发布", Preset: "docker-container", Branch: "main", Events: []string{"manual"}, Approval: true,
		NewBuild:      &AssistantBuildInput{Name: "助手构建", Dockerfile: "Dockerfile", Context: "."},
		NewDeployment: &AssistantDeploymentInput{Name: "助手部署", Kind: model.DeploymentPlanDocker, EnvironmentID: target.EnvironmentID, Workload: "api"}}
	preview, err := s.PreviewAssistantWorkflow(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, entity := range []any{&model.Application{}, &model.ReleaseWorkflow{}, &model.BuildPlan{}, &model.DeploymentPlan{}, &model.DeploymentTarget{}} {
		var count int64
		db.Model(entity).Count(&count)
		if count != 0 {
			t.Fatalf("预览试写未回滚: %T=%d", entity, count)
		}
	}
	if err := db.Model(&model.Environment{}).Where("uuid = ?", target.EnvironmentID).Update("name", "环境位置变化").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAssistantWorkflowExpected(ctx, "admin", input, preview.Fingerprint); !errors.Is(err, ErrRetrySnapshotChanged) {
		t.Fatalf("环境变化未阻止旧确认: %v", err)
	}
	preview, err = s.PreviewAssistantWorkflow(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateAssistantWorkflowExpected(ctx, "admin", input, preview.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := s.GetApplicationWorkflow(ctx, created.ApplicationID, created.WorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Workflow.IsActive || len(workflow.Workflow.Stages) != 3 {
		t.Fatalf("缺少构建/审核/部署或已启用: %+v", workflow.Workflow)
	}
	activation, err := s.PreviewAssistantActivation(ctx, created.ApplicationID, created.WorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BuildPlan{}).Where("name = ?", "助手构建").Update("dockerfile_path", "changed/Dockerfile").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateAssistantWorkflowExpected(ctx, created.ApplicationID, created.WorkflowID, "admin", activation); !errors.Is(err, ErrRetrySnapshotChanged) {
		t.Fatalf("构建配置变化仍启用: %v", err)
	}
	activation, err = s.PreviewAssistantActivation(ctx, created.ApplicationID, created.WorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateAssistantWorkflowExpected(ctx, created.ApplicationID, created.WorkflowID, "admin", activation); err != nil {
		t.Fatal(err)
	}
	var runs int64
	db.Model(&model.PipelineRun{}).Count(&runs)
	if runs != 0 {
		t.Fatal("启用时自动部署")
	}
}

func TestAssistantDeployFixedVersionAndChangedConfiguration(t *testing.T) {
	s, db, secrets, repositoryID := newPipelineTestService(t)
	commit := strings.Repeat("a", 40)
	s.repositories = repository.NewService(db, secrets, credential.NewService(db, secrets), pipelineRefLister{refs: repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: commit}}}}, 4)
	app := createManualRunTestApplication(t, s, db, repositoryID, "assistant_run")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", app.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	input := AssistantRunInput{ApplicationID: app.ID, WorkflowID: app.Workflow.ID, Ref: "refs/heads/main", Commit: commit}
	ctx := context.Background()
	stamp, err := s.PreviewAssistantRun(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.PreviewAssistantRun(ctx, "admin", input)
	if err != nil || again != stamp {
		t.Fatalf("部署预览不稳定: %v", err)
	}
	var count int64
	db.Model(&model.PipelineRun{}).Count(&count)
	if count != 0 {
		t.Fatal("部署预览创建了运行")
	}
	if err := db.Model(&model.DockerEndpoint{}).Where("uuid = ?", "docker-1").Update("host", "unix:///changed.sock").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteAssistantRunExpected(ctx, "admin", input, stamp); !errors.Is(err, ErrRetrySnapshotChanged) {
		t.Fatalf("运行位置变化仍部署: %v", err)
	}
	stamp, err = s.PreviewAssistantRun(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.ExecuteAssistantRunExpected(ctx, "admin", input, stamp)
	if err != nil || run == nil || run.CommitSHA != commit || run.ExecutionJobID == "" {
		t.Fatalf("未走原流水线任务: %+v %v", run, err)
	}
	assertPipelineBuildJob(t, db, run.ExecutionJobID)
	input.Commit = ""
	if _, err := s.PreviewAssistantRun(ctx, "admin", input); err == nil {
		t.Fatal("缺少固定Commit仍允许部署")
	}
}

func TestAssistantExistingArtifactSkipsBuildButRetainsApproval(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	app := createManualRunTestApplication(t, s, db, repositoryID, "assistant_artifact")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", app.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	artifact := createPreviousManualArtifact(t, db, app, strings.Repeat("d", 40))
	input := AssistantRunInput{ApplicationID: app.ID, WorkflowID: app.Workflow.ID, ArtifactID: artifact.ID}
	ctx := context.Background()
	stamp, err := s.PreviewAssistantRun(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.ExecuteAssistantRunExpected(ctx, "admin", input, stamp)
	if err != nil || run == nil || run.Status != model.PipelineRunAwaitingApproval || run.ArtifactID != artifact.ID || run.CurrentNodeID != "approval" {
		t.Fatalf("制品没有复用或绕过审核: %+v %v", run, err)
	}
	var builds int64
	db.Model(&model.BuildRun{}).Count(&builds)
	if builds != 1 {
		t.Fatal("已有制品触发了新构建")
	}
	input.Ref = "refs/heads/main"
	if _, err := s.PreviewAssistantRun(ctx, "admin", input); err == nil {
		t.Fatal("允许同时选择代码和已有制品")
	}
}
