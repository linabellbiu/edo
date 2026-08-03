package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/credential"
	"edo/internal/model"
	"edo/internal/repository"
)

func TestExecuteBlockedManualRunWithSelectedCommit(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("a", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "manual_version")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil || run.Status != model.PipelineRunBlocked {
		t.Fatalf("开启手动选项的代码触发节点应创建待选择版本的运行: run=%+v err=%v", run, err)
	}

	run, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, "")
	if err != nil {
		t.Fatal(err)
	}
	if run.Ref != "refs/heads/main" || run.CommitSHA != commitSHA || run.Status != model.PipelineRunRunning || run.CurrentNodeID != "build" || run.ExecutionJobID == "" {
		t.Fatalf("流水线运行没有使用所选版本提交真实执行任务: %+v", run)
	}
	assertPipelineBuildJob(t, db, run.ExecutionJobID)
}

func TestExecuteManualRunStartsFromManualCodeTrigger(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("d", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "code_manual_entry")
	workflowResult, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflow := workflowResult.Workflow
	triggerID := workflow.Source.ID
	if triggerID == "" {
		t.Fatal("默认流水线缺少代码触发节点")
	}
	if _, err := service.SaveWorkflow(context.Background(), application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Name,
		Revision: workflow.Revision, Activate: true, Source: workflow.Source, Stages: workflow.Stages,
	}); err != nil {
		t.Fatalf("包含手动选项的代码触发流水线未能启用: %v", err)
	}

	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil || run.Status != model.PipelineRunBlocked {
		t.Fatalf("代码触发节点的手动选项没有进入代码版本选择: run=%+v err=%v", run, err)
	}
	run, err = service.ExecuteRun(
		context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, triggerID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if run.CurrentNodeID != "build" || run.Status != model.PipelineRunRunning ||
		run.Stage != "queued" || run.ExecutionJobID == "" {
		t.Fatalf("手动运行没有从代码触发节点进入构建任务: %+v", run)
	}
	assertPipelineBuildJob(t, db, run.ExecutionJobID)
}

func TestExecuteManualRunCanSelectPreviousArtifact(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("4", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}}}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "manual_existing_artifact")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	stored := createPreviousManualArtifact(t, db, application, commitSHA)
	options, err := service.ListWorkflowRefs(context.Background(), application.ID, application.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Artifacts) != 1 || options.Artifacts[0].ID != stored.ID {
		t.Fatalf("手动执行没有返回匹配当前流水线的历史制品: %+v", options.Artifacts)
	}
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	run, err = service.ExecuteRunSelection(context.Background(), run.ID, "admin", "", "", application.Workflow.Source.ID, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ArtifactID != stored.ID || run.Ref != "refs/heads/main" || run.CommitSHA != commitSHA ||
		run.CurrentNodeID != "approval" || run.Status != model.PipelineRunAwaitingApproval || run.ExecutionJobID != "" {
		t.Fatalf("历史制品没有跳过构建与脚本检查并进入审核: %+v", run)
	}
	var jobs int64
	if err := db.Model(&model.Job{}).Where("payload LIKE ?", "%"+run.ID+"%").Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatalf("选择历史制品后仍创建了构建任务: count=%d err=%v", jobs, err)
	}
}

func TestExecuteManualRunCanSelectUploadedFileArtifact(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "手工上传文件制品构建", Kind: model.BuildPlanScript,
		Script: "mkdir -p dist && printf ready > dist/app", ArtifactPath: "dist",
	})
	if err != nil {
		t.Fatal(err)
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "手工上传文件制品部署", Kind: model.DeploymentPlanScript, Script: "echo deploy", TimeoutSeconds: 120,
		DeploymentTarget: deploymentPlanTargetInput(t, service, model.DeploymentTarget{
			ID: "uploaded-artifact-target", Name: "手工上传制品目标", Platform: model.DeploymentSSH,
			EnvironmentID: "uploaded-artifact-environment", HostID: "uploaded-artifact-host", RolloutTimeout: 120,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "manual_uploaded_artifact", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflowResult, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := stageWorkflowGraph(buildPlan.ID, deploymentPlan.ID, []string{"manual"}, "main")
	saved, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflowResult.Workflow.Name,
		Revision: workflowResult.Workflow.Revision, Activate: true, Source: source, Stages: stages,
	})
	if err != nil || !saved.Valid {
		t.Fatalf("启用手工上传制品测试流水线失败: result=%+v err=%v", saved, err)
	}
	application.Workflow = saved.Workflow
	now := time.Now().UTC()
	finishedAt := now
	build := model.BuildRun{
		ID: "uploaded-build", ApplicationID: application.ID, BuildPlanID: buildPlan.ID,
		ProducerKind: model.BuildRunProducerUpload, Status: model.BuildRunStatusSucceeded,
		PlanSnapshot: "{}", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now, FinishedAt: &finishedAt,
	}
	stored := &model.Artifact{
		ID: "uploaded-artifact", ApplicationID: application.ID, BuildRunID: build.ID,
		Kind: model.ArtifactKindFileBundle, Status: model.ArtifactStatusAvailable,
		Name: "release.tar.gz", Digest: "sha256:" + strings.Repeat("7", 64),
		StorageKind: model.ArtifactStorageKindLocalFile, StorageKey: "manual/release.tar.gz",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(stored).Error; err != nil {
		t.Fatal(err)
	}
	options, err := service.ListWorkflowRefs(ctx, application.ID, saved.Workflow.ID)
	if err != nil || len(options.Artifacts) != 1 || options.Artifacts[0].ID != stored.ID {
		t.Fatalf("手工上传文件制品没有进入手动执行选项: artifacts=%+v err=%v", options.Artifacts, err)
	}
	run, err := service.PrepareWorkflowRun(ctx, application.ID, saved.Workflow.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	run, err = service.ExecuteRunSelection(ctx, run.ID, "admin", "", "", source.ID, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ArtifactID != stored.ID || run.Ref != "" || run.CommitSHA != "" ||
		run.CurrentNodeID != "approval" || run.Status != model.PipelineRunAwaitingApproval {
		t.Fatalf("手工上传文件制品没有跳过构建和 Shell 后进入审核: %+v", run)
	}
}

func TestExecuteManualRunRejectsCodeAndArtifactTogether(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("5", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}}}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "manual_mixed_artifact")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	stored := createPreviousManualArtifact(t, db, application, commitSHA)
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ExecuteRunSelection(context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, application.Workflow.Source.ID, stored.ID)
	if !errors.Is(err, ErrManualSelectionInvalid) {
		t.Fatalf("同时选择代码版本和历史制品未被拒绝: %v", err)
	}
}

func TestExecuteManualRunRejectsAutomaticOnlyCodeTrigger(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("8", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "reject_auto_entry")
	result, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	triggerID := result.Workflow.Source.ID
	result.Workflow.Source.Config.Events = []string{"push"}
	if triggerID == "" {
		t.Fatal("阶段式流水线缺少代码源")
	}
	saved, err := service.SaveWorkflow(context.Background(), application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: result.Workflow.Name,
		Revision: result.Workflow.Revision, Activate: true,
		Source: result.Workflow.Source, Stages: result.Workflow.Stages,
	})
	if err != nil {
		t.Fatalf("准备自动和手动代码触发入口失败: %v", err)
	}
	if !saved.Workflow.IsActive {
		t.Fatal("测试流水线未启用")
	}
	if _, err := service.PrepareRun(context.Background(), application.ID, "admin"); !errors.Is(err, ErrManualReleaseDisabled) {
		t.Fatalf("未开启 manual 的代码源仍可创建手动运行: %v", err)
	}
	var count int64
	if err := db.Model(&model.PipelineRun{}).Where("application_id = ?", application.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("拒绝自动入口后产生了执行副作用: count=%d err=%v", count, err)
	}
}

func TestExecuteManualRunRejectsStaleCommit(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	remoteCommit := strings.Repeat("b", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: remoteCommit}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "reject_stale_version")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	run, _ := service.PrepareRun(context.Background(), application.ID, "admin")

	_, err := service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", strings.Repeat("c", 40), "")
	if !errors.Is(err, ErrManualCommitNotFound) {
		t.Fatalf("远端已经变化时应拒绝执行: %v", err)
	}
	if err := db.First(run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunBlocked || run.CommitSHA != "" {
		t.Fatalf("校验失败不应修改流水线运行: %+v", run)
	}
}

func TestExecuteDockerfileBuildWithoutImageRegistryQueuesLocalBuild(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("f", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "version_before_config")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	run, err := service.PrepareRun(context.Background(), application.ID, "admin")
	if err != nil {
		t.Fatalf("尚未选择代码版本时应该生成待手动选择的运行: %v", err)
	}

	run, err = service.ExecuteRun(context.Background(), run.ID, "admin", "refs/heads/main", commitSHA, "")
	if err != nil {
		t.Fatalf("本地 Dockerfile 构建不应强制绑定镜像仓库: %v", err)
	}
	if run.Status != model.PipelineRunRunning || run.Ref != "refs/heads/main" || run.CommitSHA != commitSHA ||
		run.CurrentNodeID != "build" || run.ExecutionJobID == "" {
		t.Fatalf("未绑定镜像仓库时没有提交 Docker 构建任务: %+v", run)
	}
	assertPipelineBuildJob(t, db, run.ExecutionJobID)
	var component model.PipelineRunRepository
	if err := db.First(&component, "pipeline_run_id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if component.ImageRegistryID != "" {
		t.Fatalf("阶段式运行不应从应用级字段写入镜像仓库快照: %+v", component)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BuildPlans["build"].ImageRegistryID != "" {
		t.Fatalf("默认 Dockerfile 构建方案应使用本地构建运行时: %+v", snapshot.BuildPlans["build"])
	}
}

func TestLocalExecutionImageUsesCommitShortHash(t *testing.T) {
	prepared := &executionContext{
		application: model.Application{ID: "application-id", Name: "order_api"},
		run: model.PipelineRun{
			ID: "12345678-abcd-efab-cdef-1234567890ab", CommitSHA: "abcdef1234567890abcdef1234567890abcdef12",
		},
	}
	image, err := localExecutionImage(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if image != "edo.local/order_api:abcdef123456" {
		t.Fatalf("本地镜像标签没有使用 Commit 短哈希: %q", image)
	}
}

func TestExecutionImageTagIsStableForSameCommit(t *testing.T) {
	first, err := executionImageTag(model.PipelineRun{ID: "11111111-abcd-efab-cdef-1234567890ab", CommitSHA: "abcdef1234567890"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := executionImageTag(model.PipelineRun{ID: "22222222-abcd-efab-cdef-1234567890ab", CommitSHA: "abcdef1234567890"})
	if err != nil {
		t.Fatal(err)
	}
	if first != "abcdef123456" || second != first {
		t.Fatalf("同一 Commit 的镜像展示标签应保持稳定: first=%q second=%q", first, second)
	}
}

func TestPipelineRunKeepsExactSSHDeploymentPlanSnapshot(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	script := "printf 'release'  \n\n"
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "script-plan-target", Name: "SSH 发布位置", Platform: model.DeploymentSSH,
		EnvironmentID: "environment-1", HostID: "host-1", WorkingDirectory: "/srv/app",
		RolloutTimeout: 180, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	plan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: "精确脚本快照", Kind: model.DeploymentPlanScript, DeploymentTarget: deploymentPlanTargetInput(t, service, target),
		Script: script, TimeoutSeconds: 180,
	})
	if err != nil {
		t.Fatal(err)
	}
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: "脚本发布前构建", Kind: model.BuildPlanScript,
		Script: "mkdir -p dist && printf ready > dist/app", ArtifactPath: "dist",
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: "script_snapshot", RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, stages := stageWorkflowGraph(buildPlan.ID, plan.ID, []string{"manual"}, "main")
	workflow := &model.ReleaseWorkflow{
		ID: "script-snapshot-workflow", ApplicationID: application.ID,
		SchemaVersion: model.WorkflowSchemaVersion, Name: "脚本快照流水线",
		Revision: 1, IsActive: true, Source: source, Stages: stages,
	}
	run, err := service.newResolvedWorkflowRun(
		ctx, application, workflow, source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	stored := snapshot.DeploymentPlans["deploy-dev"]
	wantDigest := model.DeploymentPlanExecutionDigest(plan.Kind, script, plan.TimeoutSeconds)
	if stored.Script != script || stored.Kind != plan.Kind || stored.TimeoutSeconds != plan.TimeoutSeconds ||
		model.DeploymentPlanExecutionDigest(stored.Kind, stored.Script, stored.TimeoutSeconds) != wantDigest {
		t.Fatalf("SSH 部署方案没有按原始字节创建不可变快照: %+v", stored)
	}
	if err := db.Model(&model.DeploymentPlan{}).Where("id = ?", plan.ID).
		Updates(map[string]any{"script": "echo modified\n", "timeout_seconds": 30}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err = parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DeploymentPlans["deploy-dev"].Script != script {
		t.Fatal("部署方案后续修改污染了已经创建的流水线运行")
	}
}

func TestRetryFailedRunCreatesNewAuditedExecution(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "retry_run")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	failed := model.PipelineRun{
		ID: "failed-run", ApplicationID: application.ID, Trigger: "manual",
		WorkflowID: application.Workflow.ID, WorkflowRevision: application.Workflow.Revision,
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("d", 40),
		Status: model.PipelineRunFailed, Stage: "failed", Message: "构建失败",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&failed).Error; err != nil {
		t.Fatal(err)
	}

	retried, err := service.RetryRun(context.Background(), failed.ID, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID == failed.ID || retried.RetryOfID != failed.ID || retried.Ref != failed.Ref || retried.CommitSHA != failed.CommitSHA {
		t.Fatalf("重新执行没有创建相同代码版本的新运行: %+v", retried)
	}
	if retried.Status != model.PipelineRunRunning || retried.ExecutionJobID == "" {
		t.Fatalf("重新执行没有提交新的真实任务: %+v", retried)
	}
	var original model.PipelineRun
	if err := db.First(&original, "id = ?", failed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if original.Status != model.PipelineRunFailed || original.ExecutionJobID != "" {
		t.Fatalf("重新执行不应覆盖原失败记录: %+v", original)
	}
	var job model.Job
	if err := db.First(&job, "id = ?", retried.ExecutionJobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Kind != "pipeline.build" || job.MaxAttempts != 4 || !job.IsIdempotent {
		t.Fatalf("重新执行任务的副作用保护不正确: %+v", job)
	}
}

func TestRetryFailedRunCanUseArtifactBuiltByOriginalRun(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "retry_built_artifact")
	if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ?", application.ID).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	commitSHA := strings.Repeat("9", 40)
	failed := model.PipelineRun{
		ID: "failed-artifact-retry", ApplicationID: application.ID, Trigger: "manual",
		WorkflowID: application.Workflow.ID, WorkflowRevision: application.Workflow.Revision,
		Ref: "refs/heads/main", CommitSHA: commitSHA, CommitMessage: "固定版本重试",
		Status: model.PipelineRunFailed, Stage: "deploy_failed", Message: "部署失败",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&failed).Error; err != nil {
		t.Fatal(err)
	}
	stored := createPreviousManualArtifact(t, db, application, commitSHA)
	if _, err := service.RetryRunSelection(context.Background(), failed.ID, "operator", stored.ID); !errors.Is(err, ErrRetryArtifactInvalid) {
		t.Fatalf("重试使用其他运行的制品未被拒绝: %v", err)
	}
	if err := db.Model(&model.BuildRun{}).Where("id = ?", stored.BuildRunID).Update("pipeline_run_id", failed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Artifact{}).Where("id = ?", stored.ID).Update("pipeline_run_id", failed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.PipelineRun{}).Where("id = ?", failed.ID).Update("artifact_id", stored.ID).Error; err != nil {
		t.Fatal(err)
	}
	options, err := service.ListRetryRunOptions(context.Background(), failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if options.Ref != failed.Ref || options.CommitSHA != failed.CommitSHA || len(options.Artifacts) != 1 || options.Artifacts[0].ID != stored.ID {
		t.Fatalf("重试选项没有固定原代码版本和原运行制品: %+v", options)
	}
	retried, err := service.RetryRunSelection(context.Background(), failed.ID, "operator", stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.RetryOfID != failed.ID || retried.Ref != failed.Ref || retried.CommitSHA != failed.CommitSHA ||
		retried.ArtifactID != stored.ID || retried.CurrentNodeID != "approval" || retried.Status != model.PipelineRunAwaitingApproval ||
		retried.ExecutionJobID != "" {
		t.Fatalf("重试没有使用原运行制品跳过构建并保留固定代码版本: %+v", retried)
	}
	var jobs int64
	if err := db.Model(&model.Job{}).Where("payload LIKE ?", "%"+retried.ID+"%").Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatalf("使用原运行制品重试后仍创建了构建任务: count=%d err=%v", jobs, err)
	}
}

func TestRetryRunRejectsNonFailedRun(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "reject_duplicate_run")
	now := time.Now().UTC()
	run := model.PipelineRun{
		ID: "succeeded-run", ApplicationID: application.ID, Trigger: "manual",
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("e", 40),
		Status: model.PipelineRunSucceeded, Stage: "completed", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RetryRun(context.Background(), run.ID, "operator"); !errors.Is(err, ErrPipelineRunNotRetryable) {
		t.Fatalf("成功运行不应允许重新执行: %v", err)
	}
}

func TestRetryWorkflowSourceKeepsMatchingCodeTrigger(t *testing.T) {
	workflow := &model.ReleaseWorkflow{Source: model.WorkflowNode{
		ID: "trigger-main", Type: model.WorkflowNodeTrigger, Name: "主分支", Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"push", "manual"}},
	}}
	source := retryWorkflowSource(workflow, &model.PipelineRun{Ref: "refs/heads/main"})
	if source == nil || source.ID != "trigger-main" {
		t.Fatalf("重新执行应保留匹配的代码触发入口: %+v", source)
	}
}

func TestRetryWorkflowSourceDoesNotRematchFixedTag(t *testing.T) {
	workflow := &model.ReleaseWorkflow{Source: model.WorkflowNode{
		ID: "trigger-manual", Type: model.WorkflowNodeTrigger, Name: "手动入口",
		Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual"}},
	}}
	source := retryWorkflowSource(workflow, &model.PipelineRun{
		Trigger: "manual", Ref: "refs/tags/v1.2.3", CommitSHA: strings.Repeat("a", 40),
	})
	if source == nil || source.ID != "trigger-manual" {
		t.Fatalf("重试不应按当前 Tag 规则重新匹配已固定版本: %+v", source)
	}
}

func TestRetryMergedPullRequestKeepsEventSnapshot(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "retry_merged_pr")
	var workflow model.ReleaseWorkflow
	if err := db.First(&workflow, "application_id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	workflow.Source.Config.Events = []string{"pr"}
	workflow.Source.Config.PRSourcePattern = "feature/*"
	workflow.Source.Config.PRTargetPattern = "main"
	workflow.Source.Config.PRActions = []string{"merged"}
	workflow.IsActive = true
	if err := db.Save(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	failed := model.PipelineRun{
		ID: "failed-merged-pr", ApplicationID: application.ID, Trigger: "poll_pr", TriggerAction: "merged",
		WorkflowID: workflow.ID, WorkflowRevision: workflow.Revision,
		SourceBranch: "feature/payment", TargetBranch: "main", Ref: "refs/pull/12/head", CommitSHA: strings.Repeat("f", 40),
		Status: model.PipelineRunFailed, Stage: "failed", CreatedBy: "system", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&failed).Error; err != nil {
		t.Fatal(err)
	}
	retried, err := service.RetryRun(context.Background(), failed.ID, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if retried.RetryOfID != failed.ID || retried.TriggerAction != "merged" ||
		retried.SourceBranch != "feature/payment" || retried.TargetBranch != "main" ||
		pipelineRunCheckoutRef(*retried) != "refs/heads/main" {
		t.Fatalf("重新执行没有保留合并 PR 的不可变事件快照: %+v", retried)
	}
}

func createManualRunTestApplication(t *testing.T, service *Service, db *gorm.DB, repositoryID, name string) *model.Application {
	t.Helper()
	ctx := context.Background()
	buildPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: name + "构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ContextPath: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := model.DeploymentTarget{
		ID: "target-" + strings.ReplaceAll(name, " ", "-"), Name: name + "环境", Platform: model.DeploymentDocker,
		RuntimeID: "docker-1", WorkloadName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: name + "发布", Kind: model.DeploymentPlanDocker, ServiceName: "api",
		DeploymentTarget: deploymentPlanTargetInput(t, service, target),
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApplication(ctx, "admin", ApplicationInput{
		Name: name, RepositoryID: repositoryID,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflowResult, err := service.GetWorkflow(ctx, application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := stageWorkflowGraph(buildPlan.ID, deploymentPlan.ID, []string{"manual", "push"}, "main")
	saved, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflowResult.Workflow.Name,
		Revision: workflowResult.Workflow.Revision, Source: source, Stages: stages,
	})
	if err != nil || !saved.Valid {
		t.Fatalf("保存阶段式测试流水线失败: result=%+v err=%v", saved, err)
	}
	application.Workflow = saved.Workflow
	return application
}

func createPreviousManualArtifact(t *testing.T, db *gorm.DB, application *model.Application, commitSHA string) *model.Artifact {
	t.Helper()
	if application == nil || application.Workflow == nil {
		t.Fatal("测试应用缺少流水线")
	}
	buildPlanID := ""
	for _, node := range workflowTasks(application.Workflow.Stages) {
		if node.Type == model.WorkflowNodeBuild {
			buildPlanID = node.Config.BuildPlanID
			break
		}
	}
	if buildPlanID == "" {
		t.Fatal("测试流水线缺少构建方案")
	}
	now := time.Now().UTC()
	finishedAt := now
	build := model.BuildRun{
		ID: "build-" + application.ID, ApplicationID: application.ID, PipelineRunID: "previous-" + application.ID,
		RepositoryID: application.RepositoryID, WorkflowNodeID: "build", BuildPlanID: buildPlanID,
		ProducerKind: model.BuildRunProducerDockerfile, Ref: "refs/heads/main", CommitSHA: commitSHA,
		Status: model.BuildRunStatusSucceeded, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now, FinishedAt: &finishedAt,
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	stored := &model.Artifact{
		ID: "artifact-" + application.ID, ApplicationID: application.ID, BuildRunID: build.ID,
		PipelineRunID: build.PipelineRunID, Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable,
		Name: "edo.local/" + application.Name + ":previous", Digest: digest,
		StorageKind: model.ArtifactStorageKindDockerDaemon, ImageRef: "edo.local/" + application.Name + ":previous",
		RuntimeID: "local", LocalImageID: digest, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(stored).Error; err != nil {
		t.Fatal(err)
	}
	return stored
}

func stageWorkflowGraph(buildPlanID, deploymentPlanID string, events []string, branch string) (model.WorkflowNode, []model.WorkflowStage) {
	source := model.WorkflowNode{ID: "trigger-dev", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Branch: branch, Events: events}}
	tasks := []model.WorkflowNode{
		{ID: "build", Type: model.WorkflowNodeBuild, Name: "构建制品", Config: model.WorkflowNodeConfig{
			BuildPlanID: buildPlanID,
		}},
		{ID: "shell", Type: model.WorkflowNodeShell, Name: "验证制品", Config: model.WorkflowNodeConfig{
			Script: "echo verify", WorkingDirectory: ".", TimeoutSeconds: 30,
		}},
		{ID: "approval", Type: model.WorkflowNodeApproval, Name: "发布审核"},
		{ID: "manual", Type: model.WorkflowNodeManual, Name: "人工放行"},
		{ID: "deploy-dev", Type: model.WorkflowNodeDeploy, Name: "部署", Config: model.WorkflowNodeConfig{
			DeploymentPlanID: deploymentPlanID,
		}},
	}
	stages := []model.WorkflowStage{
		{ID: "build", Name: "构建", Tasks: tasks[:1]},
		{ID: "verify", Name: "验证", Tasks: tasks[1:2]},
		{ID: "release", Name: "发布", Tasks: tasks[2:]},
	}
	return source, stages
}

func assertPipelineBuildJob(t *testing.T, db *gorm.DB, jobID string) {
	t.Helper()
	var job model.Job
	if err := db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Kind != "pipeline.build" || job.MaxAttempts != 4 || !job.IsIdempotent {
		t.Fatalf("首个阶段任务不是可幂等重试的构建任务: %+v", job)
	}
}

func TestManualWorkflowSourceUsesOnlyManualCodeTriggers(t *testing.T) {
	workflow := &model.ReleaseWorkflow{Source: model.WorkflowNode{
		ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源",
		Config: model.WorkflowNodeConfig{Branch: "release/*", Events: []string{"push", "manual"}},
	}}
	selected := manualWorkflowSource(workflow, "refs/heads/release/v1", "source")
	if selected == nil || selected.ID != "source" {
		t.Fatalf("没有使用用户选择的发布路径: %+v", selected)
	}
	if manualWorkflowSource(workflow, "refs/heads/main", "unknown") != nil {
		t.Fatal("未知代码源标识不能作为手动来源")
	}
	if fallback := manualWorkflowSource(workflow, "refs/heads/main", ""); fallback == nil || fallback.ID != "source" {
		t.Fatalf("唯一手动代码源应允许默认选择: %+v", fallback)
	}
	workflow.Source.Config.Events = []string{"push"}
	if manualWorkflowSource(workflow, "refs/heads/main", "") != nil {
		t.Fatal("未开启 manual 的代码源不能作为手动来源")
	}
}

func TestListApplicationRefsReturnsOnlyManualCodeTriggers(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	commitSHA := strings.Repeat("7", 40)
	service.repositories = repository.NewService(
		db, secrets, credential.NewService(db, secrets),
		pipelineRefLister{refs: repository.RefResult{
			Branches: []repository.GitRef{{Name: "main", SHA: commitSHA}},
		}}, 4,
	)
	application := createManualRunTestApplication(t, service, db, repositoryID, "manual_entry_list")
	result, err := service.GetWorkflow(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}

	options, err := service.ListApplicationRefs(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.ManualSources) != 1 || options.ManualSources[0].ID != "trigger-dev" {
		t.Fatalf("手动入口列表没有返回阶段式流水线的代码源: %+v", options.ManualSources)
	}
	result.Workflow.Source.Config.Events = []string{"push"}
	if err := db.Save(result.Workflow).Error; err != nil {
		t.Fatal(err)
	}
	options, err = service.ListApplicationRefs(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.ManualSources) != 0 {
		t.Fatalf("仅自动触发的代码源出现在手动入口列表: %+v", options.ManualSources)
	}
}

func TestWorkflowAllowsManualOnlyCodeTriggerAsSource(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "manual_source_only")
	source, stages := application.Workflow.Source, cloneWorkflowStages(application.Workflow.Stages)
	source.Config.Events = []string{"manual"}
	if issues := service.validateWorkflow(context.Background(), application, model.WorkflowSchemaVersion, source, stages); len(issues) != 0 {
		t.Fatalf("只开启 manual 的代码触发入口应该有效: %+v", issues)
	}
}

func TestWorkflowRequiresNamedNonEmptyUniqueStages(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "stage_constraint")
	stages := cloneWorkflowStages(application.Workflow.Stages)
	stages[0].Tasks = nil
	if issues := service.validateWorkflow(context.Background(), application, model.WorkflowSchemaVersion, application.Workflow.Source, stages); !hasWorkflowIssue(issues, "empty_stage") {
		t.Fatalf("空阶段未被拒绝: %+v", issues)
	}
	stages = cloneWorkflowStages(application.Workflow.Stages)
	stages[1].ID = stages[0].ID
	if issues := service.validateWorkflow(context.Background(), application, model.WorkflowSchemaVersion, application.Workflow.Source, stages); !hasWorkflowIssue(issues, "duplicate_stage") {
		t.Fatalf("重复阶段标识未被拒绝: %+v", issues)
	}
}

func TestWorkflowValidatesCodeTriggerAsOnlyEntryType(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "entry_validation")
	application, err := service.FindApplication(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, stages := application.Workflow.Source, cloneWorkflowStages(application.Workflow.Stages)
	validIssues := service.validateWorkflow(context.Background(), application, model.WorkflowSchemaVersion, source, stages)
	if len(validIssues) != 0 {
		t.Fatalf("包含 manual 选项的代码触发入口应该有效: %+v", validIssues)
	}
	source.Type = model.WorkflowNodeManual
	issues := service.validateWorkflow(context.Background(), application, model.WorkflowSchemaVersion, source, stages)
	if !hasWorkflowIssue(issues, "invalid_source_type") {
		t.Fatalf("非代码源类型作为唯一入口时未被拒绝: %+v", issues)
	}
}

func TestWorkflowRejectsDuplicateTaskIDs(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "task_identity_validation")
	application, err := service.FindApplication(context.Background(), application.ID)
	if err != nil {
		t.Fatal(err)
	}
	stages := cloneWorkflowStages(application.Workflow.Stages)
	stages[1].Tasks[0].ID = stages[0].Tasks[0].ID
	issues := service.validateWorkflow(context.Background(), application, model.WorkflowSchemaVersion, application.Workflow.Source, stages)
	if !hasWorkflowIssue(issues, "duplicate_node") {
		t.Fatalf("重复任务标识未被拒绝: %+v", issues)
	}
}

func TestProductionWorkflowDoesNotRequireImplicitApprovalNode(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "no_implicit_approval")
	stages := cloneWorkflowStages(application.Workflow.Stages)
	for i := range stages {
		filtered := stages[i].Tasks[:0]
		for j := range stages[i].Tasks {
			if stages[i].Tasks[j].Type != model.WorkflowNodeApproval {
				filtered = append(filtered, stages[i].Tasks[j])
			}
		}
		stages[i].Tasks = filtered
	}
	issues := service.validateWorkflow(context.Background(), application, model.WorkflowSchemaVersion, application.Workflow.Source, stages)
	if len(issues) != 0 {
		t.Fatalf("没有审核任务的发布路径应由阶段配置决定: %+v", issues)
	}
}

func TestExplicitApprovalNodeControlsWorkflowRun(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "explicit_approval")
	now := time.Now().UTC()
	run, err := service.newResolvedWorkflowRun(
		context.Background(), application, application.Workflow, application.Workflow.Source,
		"manual", "refs/heads/main", strings.Repeat("a", 40), "requester", "手动执行", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	run.CurrentNodeID = "approval"
	run.Status = model.PipelineRunAwaitingApproval
	run.Stage = string(model.WorkflowNodeApproval)
	if err := db.Create(run).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveRun(context.Background(), run.ID, "requester"); !errors.Is(err, ErrWorkflowSelfApproval) {
		t.Fatalf("审核节点没有阻止申请人自审: %v", err)
	}
	run, err = service.ApproveRun(context.Background(), run.ID, "reviewer")
	if err != nil || run.Status != model.PipelineRunRunning || run.CurrentNodeID != "manual" || run.Stage != string(model.WorkflowNodeManual) || run.ExecutionJobID != "" {
		t.Fatalf("审核通过后没有自动进入人工放行任务: run=%+v err=%v", run, err)
	}
	if run.ApprovedBy == nil || *run.ApprovedBy != "reviewer" {
		t.Fatalf("审核节点没有记录审核人: %+v", run)
	}
	run, err = service.AdvanceRun(context.Background(), run.ID, "operator", "")
	if err != nil || run.Status != model.PipelineRunRunning || run.CurrentNodeID != "deploy-dev" || run.Stage != "queued" || run.ExecutionJobID == "" {
		t.Fatalf("人工放行后没有提交部署任务: run=%+v err=%v", run, err)
	}
}

func TestApplicationRepositoryLinksRejectMissingOrDuplicateLink(t *testing.T) {
	application := &model.Application{
		ID:           "application-1",
		RepositoryID: "primary",
		Repositories: []model.ApplicationRepository{
			{ApplicationID: "application-1", RepositoryID: "ignored-extra"},
			{ApplicationID: "application-1", RepositoryID: "primary"},
		},
	}
	if _, err := applicationRepositoryLink(application); !errors.Is(err, ErrApplicationRepositoryInvariant) {
		t.Fatalf("重复仓库关联必须被视为数据不变量错误: %v", err)
	}
	application.Repositories = []model.ApplicationRepository{{
		ApplicationID: application.ID,
		RepositoryID:  application.RepositoryID,
	}}
	link, err := applicationRepositoryLink(application)
	if err != nil || link.RepositoryID != "primary" {
		t.Fatalf("唯一且匹配的仓库关联应通过校验: link=%+v err=%v", link, err)
	}
	application.Repositories = nil
	if _, err := applicationRepositoryLink(application); !errors.Is(err, ErrApplicationRepositoryInvariant) {
		t.Fatalf("缺失仓库关联必须被视为数据不变量错误: %v", err)
	}
}
