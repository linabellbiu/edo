package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/model"
)

func TestSuccessfulManualWorkflowRunCanExecuteAgain(t *testing.T) {
	for _, reuseArtifacts := range []bool{false, true} {
		name := "rebuild"
		if reuseArtifacts {
			name = "reuse_artifacts"
		}
		t.Run(name, func(t *testing.T) {
			service, db, _, repositoryID := newPipelineTestService(t)
			application := createManualRunTestApplication(t, service, db, repositoryID, "rerun_success")
			if err := db.Model(application.Workflow).Update("is_active", true).Error; err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			original := model.PipelineRun{
				ID: "original-success", ApplicationID: application.ID, WorkflowID: application.Workflow.ID,
				WorkflowRevision: application.Workflow.Revision, Trigger: "poll_push",
				Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), CommitMessage: "已成功的固定版本",
				Status: model.PipelineRunSucceeded, Stage: "completed", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&original).Error; err != nil {
				t.Fatal(err)
			}
			artifactID := ""
			if reuseArtifacts {
				artifactID = createPreviousManualArtifact(t, db, application, original.CommitSHA).ID
			}
			groups := 0
			if reuseArtifacts {
				groups = 1
			}
			options, err := service.ListRetryRunOptions(context.Background(), original.ID)
			if err != nil || options.Ref != original.Ref || options.CommitSHA != original.CommitSHA || len(options.BuildNodes) != groups {
				t.Fatalf("成功运行选项必须保留固定版本和可沿用的制品: options=%+v err=%v", options, err)
			}
			var run *model.PipelineRun
			if reuseArtifacts {
				run, err = service.RetryRunWithArtifacts(context.Background(), original.ID, "operator", nil)
			} else {
				run, err = service.RetryRun(context.Background(), original.ID, "operator")
			}
			if err != nil {
				t.Fatal(err)
			}
			if run.ID == original.ID || run.RetryOfID != original.ID || run.Ref != original.Ref || run.CommitSHA != original.CommitSHA || run.CreatedBy != "operator" {
				t.Fatalf("成功后重新执行必须创建沿用固定版本的新运行: %+v", run)
			}
			if reuseArtifacts {
				if run.ArtifactID != artifactID || run.Status != model.PipelineRunAwaitingApproval || run.ExecutionJobID != "" {
					t.Fatalf("沿用制品应跳过构建并保留审核: %+v", run)
				}
			} else if run.Status != model.PipelineRunRunning || run.ExecutionJobID == "" {
				t.Fatalf("重新构建应提交新的真实任务: %+v", run)
			}
			var preserved model.PipelineRun
			if err := db.First(&preserved, "uuid = ?", original.ID).Error; err != nil {
				t.Fatal(err)
			}
			if preserved.Status != original.Status || preserved.Stage != original.Stage || preserved.CommitSHA != original.CommitSHA || preserved.ExecutionJobID != "" {
				t.Fatalf("原成功记录不得被改写: %+v", preserved)
			}
		})
	}
}

func TestSuccessfulRetryRechecksCurrentManualSetting(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createManualRunTestApplication(t, service, db, repositoryID, "manual_rerun_guard")
	workflow := application.Workflow
	workflow.IsActive = true
	if err := db.Save(workflow).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	original := model.PipelineRun{
		ID: "completed-run", ApplicationID: application.ID, WorkflowID: workflow.ID,
		Status: model.PipelineRunSucceeded, Stage: "completed", Trigger: "manual",
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("b", 40), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	view, err := service.FindRun(context.Background(), original.ID)
	if err != nil || !view.RetryAllowed {
		t.Fatalf("手动流程的成功运行应展示重新执行: view=%+v err=%v", view, err)
	}
	fingerprint, err := service.PreviewRetryRun(context.Background(), original.ID, "operator", false)
	if err != nil {
		t.Fatal(err)
	}
	workflow.Source.Config.Events = []string{"push"}
	if err := db.Save(workflow).Error; err != nil {
		t.Fatal(err)
	}
	page, err := service.QueryRuns(context.Background(), RunQueryOptions{})
	if err != nil || len(page.Runs) != 1 || page.Runs[0].RetryAllowed {
		t.Fatalf("关闭手动执行后列表应立即取消成功记录入口: page=%+v err=%v", page, err)
	}
	if _, err := service.ListRetryRunOptions(context.Background(), original.ID); !errors.Is(err, ErrPipelineRunNotRetryable) {
		t.Fatalf("关闭手动执行后不得读取成功运行的重新执行选项: %v", err)
	}
	if _, err := service.PreviewRetryRun(context.Background(), original.ID, "operator", false); !errors.Is(err, ErrPipelineRunNotRetryable) {
		t.Fatalf("关闭手动执行后不得预览成功运行重新执行: %v", err)
	}
	if _, err := service.RetryRunExpected(context.Background(), original.ID, "operator", false, fingerprint); !errors.Is(err, ErrPipelineRunNotRetryable) {
		t.Fatalf("确认后关闭手动执行必须阻止实际重新执行: %v", err)
	}
	var count int64
	if err := db.Model(&model.PipelineRun{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("被拒绝的重新执行不得创建运行: count=%d err=%v", count, err)
	}
	if err := db.Model(&model.Job{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("被拒绝的重新执行不得创建任务: count=%d err=%v", count, err)
	}
}

func TestRetryLetsUserChooseHistoryArtifactsPerBuildNode(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application := createTwoBuildRunTestApplication(t, service, db, repositoryID, "retry_two_builds")
	workflow := application.Workflow
	workflow.IsActive = true
	if err := db.Save(workflow).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	commitSHA := strings.Repeat("6", 40)
	original := model.PipelineRun{
		ID: "two-build-failed-run", ApplicationID: application.ID, WorkflowID: workflow.ID,
		WorkflowRevision: workflow.Revision, Trigger: "manual",
		Ref: "refs/heads/main", CommitSHA: commitSHA, CommitMessage: "固定版本",
		Status: model.PipelineRunFailed, Stage: "deploy_failed", Message: "部署失败",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	apiPlan := buildPlanIDForTestNode(t, workflow, "build-api")
	webPlan := buildPlanIDForTestNode(t, workflow, "build-web")
	// 同一固定版本上先后构建过两批制品：较早的一批仍然可用，用户必须能选到它。
	olderAPI := createFixedVersionArtifact(t, db, application, apiPlan, "build-api", original.Ref, commitSHA, "edo.local/demo-api:v0.0.8-old")
	olderWeb := createFixedVersionArtifact(t, db, application, webPlan, "build-web", original.Ref, commitSHA, "edo.local/demo-web:v0.0.8-old")
	newerAPI := createFixedVersionArtifact(t, db, application, apiPlan, "build-api", original.Ref, commitSHA, "edo.local/demo-api:v0.0.8")
	newerWeb := createFixedVersionArtifact(t, db, application, webPlan, "build-web", original.Ref, commitSHA, "edo.local/demo-web:v0.0.8")

	options, err := service.ListRetryRunOptions(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if options.Ref != original.Ref || options.CommitSHA != original.CommitSHA || len(options.BuildNodes) != 2 ||
		options.BuildNodes[0].NodeID != "build-api" || options.BuildNodes[1].NodeID != "build-web" {
		t.Fatalf("重新执行必须按构建节点给出可选制品: %+v", options)
	}
	if len(options.BuildNodes[0].Artifacts) != 2 ||
		options.BuildNodes[0].Artifacts[0].ID != newerAPI.ID ||
		options.BuildNodes[0].Artifacts[1].ID != olderAPI.ID {
		t.Fatalf("构建节点的制品选项必须最新在前并保留历史构建: %+v", options.BuildNodes[0])
	}
	// 不指定时每个构建节点取最新一份。
	defaultRetry, err := service.RetryRunWithArtifacts(ctx, original.ID, "operator", nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaultRetry.ArtifactID != newerWeb.ID || defaultRetry.Status != model.PipelineRunAwaitingApproval ||
		defaultRetry.Ref != original.Ref || defaultRetry.CommitSHA != commitSHA {
		t.Fatalf("未指定时每个构建节点应沿用最新制品: %+v", defaultRetry)
	}
	assertSkippedNodeArtifact(t, db, defaultRetry.ID, "build-api", newerAPI.ID)
	assertSkippedNodeArtifact(t, db, defaultRetry.ID, "build-web", newerWeb.ID)
	// 用户显式选择较早的构建时，沿用所选的那一份。
	chosen, err := service.RetryRunWithArtifacts(ctx, original.ID, "operator", []string{olderAPI.ID, olderWeb.ID})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.ArtifactID != olderWeb.ID || chosen.RetryOfID != original.ID || chosen.Ref != original.Ref ||
		chosen.CommitSHA != commitSHA || chosen.Status != model.PipelineRunAwaitingApproval || chosen.ExecutionJobID != "" {
		t.Fatalf("显式选择的制品没有按所选沿用: %+v", chosen)
	}
	assertSkippedNodeArtifact(t, db, chosen.ID, "build-api", olderAPI.ID)
	assertSkippedNodeArtifact(t, db, chosen.ID, "build-web", olderWeb.ID)
	var jobs int64
	if err := db.Model(&model.Job{}).Where("payload LIKE ?", "%"+chosen.ID+"%").Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatalf("沿用制品后仍创建了构建任务: count=%d err=%v", jobs, err)
	}
	// 同一构建节点只能选一个；其他版本的制品不属于该固定版本。
	if _, err := service.RetryRunWithArtifacts(ctx, original.ID, "operator", []string{olderAPI.ID, newerAPI.ID}); !errors.Is(err, ErrRetryArtifactInvalid) {
		t.Fatalf("同一构建节点重复选择必须被拒绝: %v", err)
	}
	foreign := createFixedVersionArtifact(t, db, application, apiPlan, "build-api", original.Ref, strings.Repeat("7", 40), "edo.local/demo-api:other")
	if _, err := service.RetryRunWithArtifacts(ctx, original.ID, "operator", []string{foreign.ID}); !errors.Is(err, ErrRetryArtifactInvalid) {
		t.Fatalf("其他版本的制品不得沿用: %v", err)
	}
	// 沿用制品的运行再次重新执行时，仍然可以选到同一批历史制品。
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", chosen.ID).
		Updates(map[string]any{"status": model.PipelineRunFailed, "stage": "deploy_failed"}).Error; err != nil {
		t.Fatal(err)
	}
	chainedOptions, err := service.ListRetryRunOptions(ctx, chosen.ID)
	if err != nil || len(chainedOptions.BuildNodes) != 2 || len(chainedOptions.BuildNodes[1].Artifacts) != 2 {
		t.Fatalf("沿用制品的运行再次重新执行必须保留可选的历史制品: options=%+v err=%v", chainedOptions, err)
	}
	// 已失效的制品不再可选，剩下的构建节点照常执行。
	if err := db.Model(&model.Artifact{}).Where("uuid = ?", olderWeb.ID).Update("status", model.ArtifactStatusExpired).Error; err != nil {
		t.Fatal(err)
	}
	partial, err := service.ListRetryRunOptions(ctx, original.ID)
	if err != nil || len(partial.BuildNodes) != 2 || len(partial.BuildNodes[1].Artifacts) != 1 ||
		partial.BuildNodes[1].Artifacts[0].ID != newerWeb.ID {
		t.Fatalf("只能沿用仍然可用的制品: options=%+v err=%v", partial, err)
	}
}

func createTwoBuildRunTestApplication(t *testing.T, service *Service, db *gorm.DB, repositoryID, name string) *model.Application {
	t.Helper()
	ctx := context.Background()
	authorizeRegistryTestActors(t, service, "admin", "operator", "requester")
	apiPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: name + "api构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ContextPath: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	webPlan, err := service.CreateBuildPlan(ctx, "admin", BuildPlanInput{
		Name: name + "web构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "web/Dockerfile", ContextPath: "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "target-" + strings.ReplaceAll(name, " ", "-"), Name: name + "环境", Platform: model.DeploymentDocker,
		RuntimeID: "docker-1", WorkloadName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	deploymentPlan, err := service.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{
		Name: name + "发布", Kind: model.DeploymentPlanDocker, ServiceName: "api",
		ExecutionConfig:  savedRegistryExecutionForTest(t, service, apiPlan.ID),
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
	source := model.WorkflowNode{ID: "trigger-manual", Type: model.WorkflowNodeTrigger, Name: "代码源",
		Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual", "push"}}}
	tasks := []model.WorkflowNode{
		{ID: "build-api", Type: model.WorkflowNodeBuild, Name: "构建 API 镜像", Config: model.WorkflowNodeConfig{BuildPlanID: apiPlan.ID, ImageName: name + "-api"}},
		{ID: "build-web", Type: model.WorkflowNodeBuild, Name: "构建 Web 镜像", Config: model.WorkflowNodeConfig{BuildPlanID: webPlan.ID, ImageName: name + "-web"}},
		{ID: "approval", Type: model.WorkflowNodeApproval, Name: "发布审核"},
		{ID: "deploy-dev", Type: model.WorkflowNodeDeploy, Name: "部署", Config: model.WorkflowNodeConfig{DeploymentPlanID: deploymentPlan.ID}},
	}
	stages := []model.WorkflowStage{
		{ID: "build", Name: "构建", Tasks: tasks[:2]},
		{ID: "release", Name: "发布", Tasks: tasks[2:]},
	}
	saved, err := service.SaveWorkflow(ctx, application.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflowResult.Workflow.Name,
		Revision: workflowResult.Workflow.Revision, Activate: true, Source: source, Stages: stages,
	})
	if err != nil || !saved.Valid {
		t.Fatalf("保存双构建测试流水线失败: result=%+v err=%v", saved, err)
	}
	application.Workflow = saved.Workflow
	return application
}

func buildPlanIDForTestNode(t *testing.T, workflow *model.ReleaseWorkflow, nodeID string) string {
	t.Helper()
	for _, node := range workflowTasks(workflow.Stages) {
		if node.ID == nodeID {
			return node.Config.BuildPlanID
		}
	}
	t.Fatalf("测试流水线缺少构建节点: %s", nodeID)
	return ""
}

func createFixedVersionArtifact(t *testing.T, db *gorm.DB, application *model.Application, planID, nodeID, ref, commitSHA, image string) *model.Artifact {
	t.Helper()
	now := time.Now().UTC()
	finishedAt := now
	build := model.BuildRun{
		ID: uuid.NewString(), ApplicationID: application.ID, PipelineRunID: uuid.NewString(),
		RepositoryID: application.RepositoryID, WorkflowNodeID: nodeID, BuildPlanID: planID,
		ProducerKind: model.BuildRunProducerDockerfile, Ref: ref, CommitSHA: commitSHA,
		Status: model.BuildRunStatusSucceeded, PlanSnapshot: "{}",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now, FinishedAt: &finishedAt,
	}
	digest := "sha256:" + strings.Repeat("c", 64)
	stored := &model.Artifact{
		ID: uuid.NewString(), ApplicationID: application.ID, BuildRunID: build.ID, PipelineRunID: build.PipelineRunID,
		Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, Name: image, Digest: digest,
		StorageKind: model.ArtifactStorageKindDockerDaemon, ImageRef: image, RuntimeID: "local", LocalImageID: digest,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(stored).Error; err != nil {
		t.Fatal(err)
	}
	return stored
}

func assertSkippedNodeArtifact(t *testing.T, db *gorm.DB, runID, nodeID, artifactID string) {
	t.Helper()
	var record model.PipelineNodeRun
	if err := db.First(&record, "pipeline_run_id = ? AND node_id = ?", runID, nodeID).Error; err != nil {
		t.Fatalf("读取节点记录失败: node=%s err=%v", nodeID, err)
	}
	if record.Status != model.PipelineRunSkipped || record.ArtifactID != artifactID {
		t.Fatalf("沿用制品的构建节点必须跳过并登记制品: %+v", record)
	}
}
