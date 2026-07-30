package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/artifact"
	"zrt/internal/deployment"
	"zrt/internal/dockerengine"
	"zrt/internal/model"
)

func TestTransientBuildErrorUsesTypedCauses(t *testing.T) {
	if !transientBuildError(context.DeadlineExceeded) {
		t.Fatal("构建超时应允许 Dockerfile 构建重试")
	}
	if transientBuildError(context.Canceled) {
		t.Fatal("主动取消构建不应自动重试")
	}
	if !transientBuildError(&net.DNSError{IsTimeout: true}) {
		t.Fatal("网络超时应允许 Dockerfile 构建重试")
	}
	if transientBuildError(errors.New("Dockerfile 语法错误")) {
		t.Fatal("普通构建错误不得按字符串猜测为可重试错误")
	}
	if !transientBuildError(&completedBuildAdvanceError{cause: errors.New("数据库暂时不可用")}) {
		t.Fatal("任务结果已经落库后，推进失败必须允许安全重试")
	}
	retryable := &buildTaskExecutionError{cause: context.DeadlineExceeded, retryable: true}
	if !IsRetryableBuildTaskError(retryable) || IsRetryableBuildTaskError(errors.New("失败")) {
		t.Fatal("构建任务重试标记识别错误")
	}
}

func TestPipelineKeepsRunningWhenDuplicateDeliveryFindsActiveRelease(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "幂等发布应用", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := model.PipelineRun{
		ID: "pipeline-idempotent-running", ApplicationID: application.ID,
		Trigger: "push", Ref: "refs/heads/main", CommitSHA: "0123456789012345678901234567890123456789",
		Status: model.PipelineRunRunning, Stage: "deploy", CurrentNodeID: "deploy", ExecutionJobID: "job-1",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	cause := deployment.ErrPipelineReleaseRunning
	if err := service.handleDeploymentExecutionError(context.Background(), failureStateForRun(run), "发布执行失败", cause); !errors.Is(err, cause) {
		t.Fatalf("重复投递没有返回类型化幂等错误: %v", err)
	}
	if err := db.First(&run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunRunning || run.Stage != "deploy" {
		t.Fatalf("并发重复投递错误地终止了仍在执行的流水线: %+v", run)
	}
}

func TestPipelineMarksRunFailedWhenExistingReleaseAlreadyFailed(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "失败发布应用", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := model.PipelineRun{
		ID: "pipeline-idempotent-failed", ApplicationID: application.ID,
		Trigger: "push", Ref: "refs/heads/main", CommitSHA: "0123456789012345678901234567890123456789",
		Status: model.PipelineRunRunning, Stage: "deploy", CurrentNodeID: "deploy", ExecutionJobID: "job-2",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	cause := deployment.ErrPipelineReleaseFailed
	if err := service.handleDeploymentExecutionError(context.Background(), failureStateForRun(run), "发布已经失败", cause); !errors.Is(err, cause) {
		t.Fatalf("失败记录没有返回类型化幂等错误: %v", err)
	}
	if err := db.First(&run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunFailed || run.Stage != "failed" {
		t.Fatalf("已失败的发布记录没有同步流水线失败状态: %+v", run)
	}
}

func TestFailExecutionDoesNotOverwriteNewTaskState(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "失败状态并发校验", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := model.PipelineRun{
		ID: "run-failure-cas", ApplicationID: application.ID, Trigger: "manual", Ref: "refs/heads/main",
		CommitSHA: strings.Repeat("2", 40), Status: model.PipelineRunRunning, Stage: "queued",
		CurrentNodeID: "new-node", ExecutionJobID: "new-job", WorkflowSnapshot: "{}",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	component := model.PipelineRunRepository{
		ID: "component-failure-cas", PipelineRunID: run.ID, RepositoryID: repositoryID,
		Ref: run.Ref, CommitSHA: run.CommitSHA, Status: model.PipelineRunRepositoryReady,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		return tx.Create(&component).Error
	}); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("旧任务失败")
	if err := service.failExecution(context.Background(),
		taskFailureState(run.ID, "old-job", "old-node", model.PipelineRunRunning), "旧任务执行失败", cause,
	); !errors.Is(err, cause) {
		t.Fatalf("旧任务失败应保留原始错误: %v", err)
	}
	if err := db.First(&run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunRunning || run.CurrentNodeID != "new-node" || run.ExecutionJobID != "new-job" {
		t.Fatalf("过期失败结果覆盖了当前任务: %+v", run)
	}
	if err := db.First(&component, "id = ?", component.ID).Error; err != nil {
		t.Fatal(err)
	}
	if component.Status != model.PipelineRunRepositoryReady {
		t.Fatalf("CAS 未命中时仍错误更新仓库状态: %+v", component)
	}
}

func TestCompletedDeploymentMustMatchSucceededRunSnapshot(t *testing.T) {
	digestImage := "registry.example.com/team/api@sha256:" + strings.Repeat("a", 64)
	prepared := &executionContext{
		run:  model.PipelineRun{ID: "run-1"},
		node: model.WorkflowNode{ID: "deploy-1", Type: model.WorkflowNodeDeploy},
		artifact: model.Artifact{
			ID: "artifact-1", Kind: model.ArtifactKindOCIImage,
			StorageKind: model.ArtifactStorageKindRegistry, ImageRef: digestImage,
		},
		deploymentPlan: model.DeploymentPlan{ID: "plan-1", Kind: model.DeploymentPlanKubernetes},
		target: model.DeploymentTarget{
			ID: "target-1", Name: "生产集群", Platform: model.DeploymentKubernetes,
			RuntimeID: "cluster-1", Namespace: "default", WorkloadName: "api",
			ContainerName: "api", RolloutTimeout: 300,
		},
	}
	record := model.DeploymentRecord{
		ID: "deployment-1", PipelineRunID: prepared.run.ID, WorkflowNodeID: prepared.node.ID,
		ArtifactID: prepared.artifact.ID, TargetID: prepared.target.ID, TargetName: prepared.target.Name,
		Platform: prepared.target.Platform, RuntimeID: prepared.target.RuntimeID,
		Namespace: prepared.target.Namespace, WorkloadName: prepared.target.WorkloadName,
		ContainerName: prepared.target.ContainerName, RolloutTimeout: prepared.target.RolloutTimeout,
		Operation: model.DeploymentRelease, Image: digestImage, Status: model.DeploymentSucceeded,
		DeploymentPlanID: prepared.deploymentPlan.ID, DeploymentPlanKind: prepared.deploymentPlan.Kind,
	}
	if !completedDeploymentMatches(prepared, &record) {
		t.Fatal("完整且成功的发布记录未通过流水线完成校验")
	}
	for _, mutate := range []func(*model.DeploymentRecord){
		func(item *model.DeploymentRecord) { item.Status = model.DeploymentFailed },
		func(item *model.DeploymentRecord) { item.ArtifactID = "other-artifact" },
		func(item *model.DeploymentRecord) { item.TargetID = "other-target" },
		func(item *model.DeploymentRecord) {
			item.Image = "registry.example.com/team/other@sha256:" + strings.Repeat("b", 64)
		},
		func(item *model.DeploymentRecord) { item.DeploymentPlanID = "other-plan" },
	} {
		changed := record
		mutate(&changed)
		if completedDeploymentMatches(prepared, &changed) {
			t.Fatalf("不一致的发布记录被用于完成流水线: %+v", changed)
		}
	}
}

func TestCompletedDeploymentAutomaticallyAdvancesOrdinaryPipeline(t *testing.T) {
	tests := []struct {
		name       string
		next       model.WorkflowNode
		wantStatus model.PipelineRunStatus
		wantStage  string
		wantJob    bool
	}{
		{
			name: "Shell", next: model.WorkflowNode{
				ID: "shell-next", Type: model.WorkflowNodeShell, Name: "部署后检查",
				Config: model.WorkflowNodeConfig{Script: "true", RuntimeImage: model.DefaultRuntimeImage, WorkingDirectory: ".", TimeoutSeconds: 30},
			},
			wantStatus: model.PipelineRunRunning, wantStage: "queued", wantJob: true,
		},
		{
			name:       "审核",
			next:       model.WorkflowNode{ID: "approval-next", Type: model.WorkflowNodeApproval, Name: "上线审核"},
			wantStatus: model.PipelineRunAwaitingApproval, wantStage: string(model.WorkflowNodeApproval),
		},
		{
			name: "再次部署",
			next: model.WorkflowNode{
				ID: "deploy-next", Type: model.WorkflowNodeDeploy, Name: "部署第二环境",
				Config: model.WorkflowNodeConfig{DeploymentPlanID: "plan-next"},
			},
			wantStatus: model.PipelineRunRunning, wantStage: "queued", wantJob: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, db, _, repositoryID := newPipelineTestService(t)
			application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
				Name: "部署推进" + test.name, RepositoryID: repositoryID, PollIntervalSeconds: 60,
			})
			if err != nil {
				t.Fatal(err)
			}
			current := model.WorkflowNode{
				ID: "deploy-current", Type: model.WorkflowNodeDeploy, Name: "部署当前环境",
				Config: model.WorkflowNodeConfig{DeploymentPlanID: "plan-current"},
			}
			snapshot := workflowSnapshot{
				SchemaVersion:     model.WorkflowSchemaVersion,
				Source:            model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"},
				Stages:            []model.WorkflowStage{{ID: "delivery", Name: "交付", Tasks: []model.WorkflowNode{current, test.next}}},
				ApprovalEnabled:   true,
				DeploymentPlans:   map[string]workflowDeploymentPlanSnapshot{},
				DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{},
			}
			if test.next.Type == model.WorkflowNodeDeploy {
				snapshot.DeploymentPlans[test.next.ID] = workflowDeploymentPlanSnapshot{
					ID: "plan-next", Kind: model.DeploymentPlanKubernetes, TimeoutSeconds: 120,
				}
				snapshot.DeploymentTargets[test.next.ID] = workflowDeploymentTargetSnapshot{
					ID: "target-next", Name: "第二环境", Platform: model.DeploymentKubernetes,
					RuntimeID: "cluster-next", Namespace: "default", WorkloadName: "api", ContainerName: "api", RolloutTimeout: 120,
				}
			}
			snapshotJSON, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			run := model.PipelineRun{
				ID: "run-deploy-advance", ApplicationID: application.ID, Trigger: "manual",
				Ref: "refs/heads/main", CommitSHA: strings.Repeat("1", 40), Status: model.PipelineRunRunning,
				Stage: "deploy", CurrentNodeID: current.ID, WorkflowSnapshot: string(snapshotJSON),
				ExecutionJobID: "job-deploy-current", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
			}
			component := model.PipelineRunRepository{
				ID: "component-deploy-advance", PipelineRunID: run.ID, RepositoryID: repositoryID,
				Ref: run.Ref, CommitSHA: run.CommitSHA, Status: model.PipelineRunRepositoryReady,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&run).Error; err != nil {
					return err
				}
				return tx.Create(&component).Error
			}); err != nil {
				t.Fatal(err)
			}
			image := "registry.example.com/team/api@sha256:" + strings.Repeat("a", 64)
			prepared := &executionContext{
				run: run, node: current, snapshot: snapshot,
				artifact: model.Artifact{
					ID: "artifact", Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable,
					StorageKind: model.ArtifactStorageKindRegistry, ImageRef: image,
				},
				deploymentPlan: model.DeploymentPlan{ID: "plan-current", Kind: model.DeploymentPlanKubernetes},
				target: model.DeploymentTarget{
					ID: "target-current", Name: "当前环境", Platform: model.DeploymentKubernetes,
					RuntimeID: "cluster", Namespace: "default", WorkloadName: "api", ContainerName: "api", RolloutTimeout: 120,
				},
			}
			record := model.DeploymentRecord{
				ID: "deployment-current", PipelineRunID: run.ID, WorkflowNodeID: current.ID,
				ArtifactID: prepared.artifact.ID, TargetID: prepared.target.ID, TargetName: prepared.target.Name,
				Platform: prepared.target.Platform, RuntimeID: prepared.target.RuntimeID,
				Namespace: prepared.target.Namespace, WorkloadName: prepared.target.WorkloadName,
				ContainerName: prepared.target.ContainerName, RolloutTimeout: prepared.target.RolloutTimeout,
				Operation: model.DeploymentRelease, Image: image, Status: model.DeploymentSucceeded,
				DeploymentPlanID: prepared.deploymentPlan.ID, DeploymentPlanKind: prepared.deploymentPlan.Kind,
			}
			if err := service.completeExecution(context.Background(), prepared, &record); err != nil {
				t.Fatalf("部署完成后推进失败: %v", err)
			}
			var updated model.PipelineRun
			if err := db.First(&updated, "id = ?", run.ID).Error; err != nil {
				t.Fatal(err)
			}
			if updated.CurrentNodeID != test.next.ID || updated.Status != test.wantStatus || updated.Stage != test.wantStage {
				t.Fatalf("部署完成后未进入后续任务: %+v", updated)
			}
			if test.wantJob {
				if updated.ExecutionJobID == "" || updated.ExecutionJobID == run.ExecutionJobID {
					t.Fatalf("后续可执行任务未创建新 Job: %+v", updated)
				}
				var job model.Job
				if err := db.First(&job, "id = ?", updated.ExecutionJobID).Error; err != nil {
					t.Fatal(err)
				}
				if test.next.Type == model.WorkflowNodeShell && job.MaxAttempts != 1 {
					t.Fatalf("部署后的 Shell 任务不得重复执行: %+v", job)
				}
			}
		})
	}
}

func TestLoadExecutionRejectsRegistryArtifactOutsideBoundRegistry(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	artifactService, err := artifact.NewService(db, t.TempDir(), 1024*1024, service.logger)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureArtifacts(artifactService)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "仓库绑定校验应用", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	registry := model.ImageRegistry{
		ID: "registry-bound", Name: "绑定仓库", Provider: model.RegistryGeneric,
		Endpoint: "https://registry.example.com", Namespace: "team", IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	dockerConfig, err := dockerengine.NormalizeContainerConfig(model.DockerContainerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	node := model.WorkflowNode{
		ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "部署",
		Config: model.WorkflowNodeConfig{DeploymentPlanID: "plan-1"},
	}
	snapshot, err := json.Marshal(workflowSnapshot{
		SchemaVersion: model.WorkflowSchemaVersion,
		Source:        model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"},
		Stages:        []model.WorkflowStage{{ID: "deploy-stage", Name: "部署", Tasks: []model.WorkflowNode{node}}},
		DeploymentPlans: map[string]workflowDeploymentPlanSnapshot{
			node.ID: {ID: "plan-1", Kind: model.DeploymentPlanDocker, DockerConfig: dockerConfig, TimeoutSeconds: 120},
		},
		DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{
			node.ID: {
				ID: "target-1", Name: "目标", Platform: model.DeploymentDocker,
				RuntimeID: "runtime-1", WorkloadName: "api", RolloutTimeout: 120,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("c", 64)
	run := model.PipelineRun{
		ID: "registry-binding-run", ApplicationID: application.ID, Trigger: "push",
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("d", 40), Status: model.PipelineRunRunning,
		Stage: "deploy", CurrentNodeID: node.ID, ExecutionJobID: "registry-binding-job",
		ArtifactID: "registry-binding-artifact", WorkflowSnapshot: string(snapshot),
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	component := model.PipelineRunRepository{
		ID: "registry-binding-component", PipelineRunID: run.ID, RepositoryID: repositoryID,
		Ref: run.Ref, CommitSHA: run.CommitSHA, Status: model.PipelineRunRepositoryReady,
		CreatedAt: now, UpdatedAt: now,
	}
	storedArtifact := model.Artifact{
		ID: run.ArtifactID, ApplicationID: application.ID, BuildRunID: "build-1", PipelineRunID: run.ID,
		Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, Name: "api",
		Digest: digest, StorageKind: model.ArtifactStorageKindRegistry,
		ImageRef: "other.example.com/team/api@" + digest, ImageRegistryID: registry.ID,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&component).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&storedArtifact).Error; err != nil {
		t.Fatal(err)
	}
	payload := DeployTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}
	if _, err := service.loadExecution(context.Background(), payload, run.ExecutionJobID); !errors.Is(err, ErrPipelineExecutionConfig) {
		t.Fatalf("部署前未拒绝与登记仓库主机不匹配的制品: %v", err)
	}
	validImage := "registry.example.com/team/api@" + digest
	if err := db.Model(&model.Artifact{}).Where("id = ?", storedArtifact.ID).Update("image_ref", validImage).Error; err != nil {
		t.Fatal(err)
	}
	prepared, err := service.loadExecution(context.Background(), payload, run.ExecutionJobID)
	if err != nil || prepared.artifact.ImageRef != validImage {
		t.Fatalf("匹配仓库边界的不可变制品无法进入部署: prepared=%+v err=%v", prepared, err)
	}
}

func TestSSHDeploymentPlanSnapshotValidation(t *testing.T) {
	plan := model.DeploymentPlan{
		ID: "plan-1", Kind: model.DeploymentPlanScript,
		Script: "printf 'deploy'\n", TimeoutSeconds: 120,
	}
	target := model.DeploymentTarget{
		Platform: model.DeploymentSSH, HostID: "host-1", EnvironmentID: "environment-1",
	}
	if !validSSHDeploymentPlanSnapshot(&plan, &target) {
		t.Fatal("完整 SSH 部署方案快照应当有效")
	}

	tests := []struct {
		name   string
		mutate func(*model.DeploymentPlan, *model.DeploymentTarget)
	}{
		{name: "缺少方案标识", mutate: func(plan *model.DeploymentPlan, _ *model.DeploymentTarget) { plan.ID = "" }},
		{name: "执行方式不是命令脚本", mutate: func(plan *model.DeploymentPlan, _ *model.DeploymentTarget) { plan.Kind = model.DeploymentPlanDocker }},
		{name: "脚本为空", mutate: func(plan *model.DeploymentPlan, _ *model.DeploymentTarget) { plan.Script = "   " }},
		{name: "超时过短", mutate: func(plan *model.DeploymentPlan, _ *model.DeploymentTarget) { plan.TimeoutSeconds = 29 }},
		{name: "超时过长", mutate: func(plan *model.DeploymentPlan, _ *model.DeploymentTarget) { plan.TimeoutSeconds = 3601 }},
		{name: "部署目标不是 SSH", mutate: func(_ *model.DeploymentPlan, target *model.DeploymentTarget) {
			target.Platform = model.DeploymentDocker
		}},
		{name: "缺少主机", mutate: func(_ *model.DeploymentPlan, target *model.DeploymentTarget) { target.HostID = "" }},
		{name: "缺少环境归属", mutate: func(_ *model.DeploymentPlan, target *model.DeploymentTarget) { target.EnvironmentID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutatedPlan, mutatedTarget := plan, target
			test.mutate(&mutatedPlan, &mutatedTarget)
			if validSSHDeploymentPlanSnapshot(&mutatedPlan, &mutatedTarget) {
				t.Fatalf("不完整的 SSH 部署方案快照被接受: plan=%+v target=%+v", mutatedPlan, mutatedTarget)
			}
		})
	}
}

func TestComposeDeploymentPlanSnapshotValidation(t *testing.T) {
	plan := model.DeploymentPlan{
		ID: "compose-plan", Kind: model.DeploymentPlanCompose,
		ComposeYAML: "services:\n  api:\n    image: ${ZRT_IMAGE}\n", ServiceName: "api", TimeoutSeconds: 120,
	}
	target := model.DeploymentTarget{Platform: model.DeploymentDocker}
	if !validComposeDeploymentPlanSnapshot(&plan, &target) {
		t.Fatal("完整的 Docker Compose 方案快照应当有效")
	}
	tests := []struct {
		name   string
		mutate func(*model.DeploymentPlan, *model.DeploymentTarget)
	}{
		{name: "缺少方案标识", mutate: func(plan *model.DeploymentPlan, _ *model.DeploymentTarget) { plan.ID = "" }},
		{name: "服务不存在", mutate: func(plan *model.DeploymentPlan, _ *model.DeploymentTarget) { plan.ServiceName = "worker" }},
		{name: "镜像不是上游制品", mutate: func(plan *model.DeploymentPlan, _ *model.DeploymentTarget) {
			plan.ComposeYAML = "services:\n  api:\n    image: nginx:latest\n"
		}},
		{name: "错误目标类型", mutate: func(_ *model.DeploymentPlan, target *model.DeploymentTarget) {
			target.Platform = model.DeploymentKubernetes
		}},
		{name: "超时越界", mutate: func(plan *model.DeploymentPlan, _ *model.DeploymentTarget) { plan.TimeoutSeconds = 3601 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutatedPlan, mutatedTarget := plan, target
			test.mutate(&mutatedPlan, &mutatedTarget)
			if validComposeDeploymentPlanSnapshot(&mutatedPlan, &mutatedTarget) {
				t.Fatalf("无效的 Docker Compose 快照被接受: plan=%+v target=%+v", mutatedPlan, mutatedTarget)
			}
		})
	}
}

func TestNormalizeComposeDeploymentPlanKeepsInlineYAMLAndService(t *testing.T) {
	input, err := normalizeDeploymentPlanInput(DeploymentPlanInput{
		Name: "Compose 发布", Kind: model.DeploymentPlanCompose,
		ComposeYAML: "\nservices:\n  api:\n    image: ${ZRT_IMAGE}\n", ServiceName: " api ",
		DeploymentTarget: &deployment.TargetInput{Platform: model.DeploymentDocker},
	})
	if err != nil {
		t.Fatalf("有效的 Docker Compose 部署方案被拒绝: %v", err)
	}
	if input.ComposeYAML != "services:\n  api:\n    image: ${ZRT_IMAGE}\n" || input.ServiceName != "api" || input.TimeoutSeconds != 600 {
		t.Fatalf("Docker Compose 方案没有规范化为开箱即用默认值: %+v", input)
	}
	if _, err := normalizeDeploymentPlanInput(DeploymentPlanInput{
		Name: "Compose 发布", Kind: model.DeploymentPlanCompose,
		ComposeYAML: "services:\n  api:\n    image: nginx:latest\n", ServiceName: "api",
		DeploymentTarget: &deployment.TargetInput{Platform: model.DeploymentDocker},
	}); err == nil {
		t.Fatal("没有消费上游制品的 Docker Compose 方案未被拒绝")
	}
}
