package deployment

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bsm/redislock"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/dockerengine"
	"zrt/internal/kube"
	"zrt/internal/model"
	"zrt/internal/secret"
	"zrt/internal/sshdeploy"
)

type hostScriptRunnerStub struct {
	input  sshdeploy.Input
	result sshdeploy.Result
	err    error
}

const (
	dockerTestEnvironmentID = "docker-test-environment"
	dockerTestHostID        = "docker-test-host"
)

type idempotentHostScriptRunnerStub struct {
	result  sshdeploy.Result
	err     error
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (s *idempotentHostScriptRunnerStub) RunHostDeploymentScript(ctx context.Context, _ sshdeploy.Input) (sshdeploy.Result, error) {
	s.calls.Add(1)
	if s.started != nil {
		s.once.Do(func() { close(s.started) })
	}
	if s.release != nil {
		select {
		case <-ctx.Done():
			return sshdeploy.Result{}, ctx.Err()
		case <-s.release:
		}
	}
	return s.result, s.err
}

func (s *hostScriptRunnerStub) RunHostDeploymentScript(_ context.Context, input sshdeploy.Input) (sshdeploy.Result, error) {
	s.input = input
	return s.result, s.err
}

func TestRegistryDeploymentRequiresDigestAndQueuesWithoutImplicitApproval(t *testing.T) {
	service, db, endpointID := newDeploymentTestService(t)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "registry-api", Platform: model.DeploymentDocker,
		EnvironmentID: dockerTestEnvironmentID, HostID: dockerTestHostID,
		RuntimeID: endpointID, WorkloadName: "production-api", RolloutTimeout: 120,
	})
	if err != nil {
		t.Fatalf("创建发布目标失败: %v", err)
	}
	if _, err := service.Request(context.Background(), "requester", RequestInput{
		TargetID: target.ID, Image: "registry.example.com/team/api:latest",
	}); !errors.Is(err, ErrImmutableImageRequired) {
		t.Fatalf("镜像仓库发布未拒绝可变镜像标签: %v", err)
	}

	digestImage := "registry.example.com/team/api@sha256:" + strings.Repeat("a", 64)
	record, err := service.Request(context.Background(), "requester", RequestInput{TargetID: target.ID, Image: digestImage})
	if err != nil {
		t.Fatalf("创建发布任务失败: %v", err)
	}
	if record.Status != model.DeploymentQueued || record.WorkloadName != "production-api" || record.JobID == "" || record.ApprovedBy != nil {
		t.Fatalf("发布目标不应隐式增加审批状态: %+v", record)
	}
	var job model.Job
	if err := db.First(&job, "id = ?", record.JobID).Error; err != nil {
		t.Fatalf("读取发布任务失败: %v", err)
	}
	if job.MaxAttempts != 1 {
		t.Fatalf("有外部副作用的发布任务执行次数应为 1，实际为 %d", job.MaxAttempts)
	}
}

func TestPipelinePreparedLocalImageUsesVerifiedImageID(t *testing.T) {
	target := &model.DeploymentTarget{Platform: model.DeploymentDocker}
	image := "zrt.local/order-api:abcdef123456-12345678"
	imageID := "sha256:" + strings.Repeat("b", 64)
	if normalized, err := validatePipelineImage(image, imageID, target); err != nil || normalized != image {
		t.Fatalf("流水线已经校验的本地镜像应允许用于 Docker 发布: image=%q err=%v", normalized, err)
	}
	if _, err := validatePipelineImage(image, "", target); !errors.Is(err, ErrImmutableImageRequired) {
		t.Fatalf("未携带镜像 ID 的本地标签不能绕过仓库不可变检查: %v", err)
	}
	target.Platform = model.DeploymentKubernetes
	if _, err := validatePipelineImage(image, imageID, target); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("Kubernetes 不应接受只存在于 Docker 主机的本地镜像: %v", err)
	}
	digestImage := "registry.example.com/team/api@sha256:" + strings.Repeat("c", 64)
	if normalized, err := validatePipelineImage(digestImage, "", target); err != nil || normalized != digestImage {
		t.Fatalf("Kubernetes 应接受带摘要的仓库镜像: image=%q err=%v", normalized, err)
	}
}

func TestDeploymentUsesApprovedTargetSnapshot(t *testing.T) {
	service, _, endpointID := newDeploymentTestService(t)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "testing-api", Platform: model.DeploymentDocker,
		EnvironmentID: dockerTestEnvironmentID, HostID: dockerTestHostID,
		RuntimeID: endpointID, WorkloadName: "api-v1", RolloutTimeout: 120,
	})
	if err != nil {
		t.Fatalf("创建发布目标失败: %v", err)
	}
	record, err := service.Request(context.Background(), "operator", RequestInput{
		TargetID: target.ID, Image: "registry.example.com/team/api@sha256:" + strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatalf("创建发布任务失败: %v", err)
	}
	if _, err := service.UpdateTarget(context.Background(), target.ID, TargetInput{
		Name: "testing-api", Platform: model.DeploymentDocker,
		EnvironmentID: dockerTestEnvironmentID, HostID: dockerTestHostID,
		RuntimeID: endpointID, WorkloadName: "api-v2", RolloutTimeout: 240,
	}); err != nil {
		t.Fatalf("更新发布目标失败: %v", err)
	}
	var stored model.DeploymentRecord
	if err := service.db.First(&stored, "id = ?", record.ID).Error; err != nil {
		t.Fatalf("读取发布快照失败: %v", err)
	}
	if stored.WorkloadName != "api-v1" || stored.RolloutTimeout != 120 {
		t.Fatalf("发布目标快照被后续修改污染: %+v", stored)
	}
}

func TestDeploymentEnvironmentSupportsCustomChineseName(t *testing.T) {
	service, _, endpointID := newDeploymentTestService(t)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "华东客户演示", Description: "上海机房的演示环境",
		Platform: model.DeploymentDocker, EnvironmentID: dockerTestEnvironmentID, HostID: dockerTestHostID,
		RuntimeID: endpointID, WorkloadName: "demo-api", RolloutTimeout: 120,
	})
	if err != nil {
		t.Fatalf("创建自定义发布环境失败: %v", err)
	}
	if target.Name != "华东客户演示" || target.Description != "上海机房的演示环境" {
		t.Fatalf("发布环境名称或说明保存错误: %+v", target)
	}
}

func TestDockerTargetDefersContainerNameUntilApplicationExecution(t *testing.T) {
	service, _, endpointID := newDeploymentTestService(t)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "订单服务", Platform: model.DeploymentDocker,
		EnvironmentID: dockerTestEnvironmentID, HostID: dockerTestHostID,
		RuntimeID: endpointID, RolloutTimeout: 120,
	})
	if err != nil {
		t.Fatalf("容器名称留空时创建 Docker 发布目标失败: %v", err)
	}
	if target.WorkloadName != "" {
		t.Fatalf("部署方案不应在尚未绑定应用时生成容器名称: %q", target.WorkloadName)
	}
	first, err := generatedDockerWorkloadName("order_api", "application-1", "plan-1", target.ID)
	if err != nil {
		t.Fatalf("按应用生成容器名称失败: %v", err)
	}
	second, err := generatedDockerWorkloadName("order_api", "application-1", "plan-1", target.ID)
	if err != nil || first != second || !strings.HasPrefix(first, "order_api-") || !workloadNamePattern.MatchString(first) {
		t.Fatalf("容器名称没有保持应用级稳定隔离: first=%q second=%q err=%v", first, second, err)
	}
	otherPlan, err := generatedDockerWorkloadName("order_api", "application-1", "plan-2", target.ID)
	if err != nil || otherPlan == first {
		t.Fatalf("同一应用的不同部署方案没有得到独立容器名称: first=%q other=%q err=%v", first, otherPlan, err)
	}
}

func TestDockerTargetRejectsHostOutsideSelectedEnvironment(t *testing.T) {
	service, db, endpointID := newDeploymentTestService(t)
	now := time.Now().UTC()
	environment := model.Environment{
		ID: "docker-unrelated-environment", Name: "未关联 Docker 环境", IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&environment).Error; err != nil {
		t.Fatalf("创建未关联环境失败: %v", err)
	}
	if _, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "错误环境目标", Platform: model.DeploymentDocker,
		EnvironmentID: environment.ID, HostID: dockerTestHostID,
		RuntimeID: endpointID, RolloutTimeout: 120,
	}); !errors.Is(err, ErrEnvironmentTargetUnavailable) {
		t.Fatalf("未关联环境不应使用 Docker 主机: %v", err)
	}
}

func TestTargetLockSerializesSameEnvironmentOnly(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	service := &Service{locks: redislock.New(redisClient), lockKeyPrefix: "zrt:test:deployment"}
	first := &model.DeploymentRecord{ID: "deployment-1", TargetID: "target-a", RolloutTimeout: 30}
	firstLock, err := service.acquireTargetLock(context.Background(), first)
	if err != nil {
		t.Fatalf("获取首个发布环境锁失败: %v", err)
	}
	defer firstLock.Release(context.Background())

	waitCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := service.acquireTargetLock(waitCtx, &model.DeploymentRecord{
		ID: "deployment-2", TargetID: "target-a", RolloutTimeout: 30,
	}); err == nil {
		t.Fatal("同一发布环境不应同时获得两个锁")
	}

	otherLock, err := service.acquireTargetLock(context.Background(), &model.DeploymentRecord{
		ID: "deployment-3", TargetID: "target-b", RolloutTimeout: 30,
	})
	if err != nil {
		t.Fatalf("不同发布环境应允许并行: %v", err)
	}
	if err := otherLock.Release(context.Background()); err != nil {
		t.Fatalf("释放其他发布环境锁失败: %v", err)
	}
}

func TestSSHDeploymentUsesSelectedMembershipAndExactPlanSnapshot(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	environment, host := createSSHDeploymentResources(t, db)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "SSH 命令目标", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID,
		HostID:        host.ID, WorkingDirectory: "/srv/zrt", RolloutTimeout: 90,
	})
	if err != nil {
		t.Fatalf("创建 SSH 发布目标失败: %v", err)
	}
	if target.EnvironmentID != environment.ID ||
		target.RuntimeID != "" || target.WorkloadName != "" || target.WorkingDirectory != "/srv/zrt" {
		t.Fatalf("SSH 发布目标未使用服务端主机环境快照: %+v", target)
	}

	runner := &hostScriptRunnerStub{
		result: sshdeploy.Result{ExitCode: 17, Started: true},
		err:    errors.New("remote command failed"),
	}
	service.ssh = runner
	script := "printf '%s\\n' \"$ZRT_DEPLOYMENT_ID\"  \nexit 17\n\n"
	digest := model.DeploymentPlanExecutionDigest(model.DeploymentPlanScript, script, 120)
	record, err := service.RequestCommandAndRun(context.Background(), "operator", CommandRequestInput{
		TargetID: target.ID, PipelineRunID: "run-1", WorkflowNodeID: "deploy-1",
		DeploymentPlanID: "plan-1", PlanKind: model.DeploymentPlanScript,
		Script: script, ScriptDigest: digest, TimeoutSeconds: 120,
		Environment: map[string]string{"ZRT_PIPELINE_RUN_ID": "run-1"},
	})
	if err == nil {
		t.Fatal("远端命令失败时不应返回成功")
	}
	if runner.input.Script != script || runner.input.WorkingDirectory != target.WorkingDirectory ||
		runner.input.EnvironmentID != environment.ID ||
		runner.input.Environment["ZRT_DEPLOYMENT_ID"] == "" || runner.input.Timeout != 90*time.Second {
		t.Fatalf("SSH 执行输入未保持不可变快照: %+v", runner.input)
	}
	var stored model.DeploymentRecord
	if err := db.First(&stored, "id = ?", record.ID).Error; err != nil {
		t.Fatalf("读取 SSH 发布记录失败: %v", err)
	}
	if stored.Status != model.DeploymentFailed || stored.CommandScript != script || stored.CommandDigest != digest ||
		stored.CommandExitCode == nil || *stored.CommandExitCode != 17 || stored.EnvironmentID != environment.ID ||
		stored.HostID != host.ID || stored.ErrorMessage != "命令脚本部署失败，请查看流水线日志" {
		t.Fatalf("SSH 发布记录快照或退出码错误: %+v", stored)
	}
	if _, err := service.Rollback(context.Background(), stored.ID, "operator"); !errors.Is(err, ErrRollbackUnavailable) {
		t.Fatalf("SSH 命令部署不应自动创建回滚: %v", err)
	}
}

func TestPipelineDeploymentExecutesImmutableTargetSnapshot(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	environment, host := createSSHDeploymentResources(t, db)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "流水线快照目标", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID, HostID: host.ID,
		WorkingDirectory: "/srv/original", RolloutTimeout: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := *target
	if err := db.Model(&model.DeploymentTarget{}).Where("id = ?", target.ID).Updates(map[string]any{
		"host_id": "changed-host", "environment_id": "changed-environment",
		"working_directory": "/srv/changed", "rollout_timeout": 300,
	}).Error; err != nil {
		t.Fatal(err)
	}
	runner := &hostScriptRunnerStub{result: sshdeploy.Result{ExitCode: 0, Started: true}}
	service.ssh = runner
	script := "echo ok\n"
	digest := model.DeploymentPlanExecutionDigest(model.DeploymentPlanScript, script, 120)
	record, err := service.RequestCommandSnapshotAndRun(
		context.Background(), "operator", snapshot,
		CommandRequestInput{
			TargetID: target.ID, PipelineRunID: "snapshot-run", WorkflowNodeID: "deploy",
			DeploymentPlanID: "snapshot-plan", PlanKind: model.DeploymentPlanScript,
			Script: script, ScriptDigest: digest, TimeoutSeconds: 120,
		},
	)
	if err != nil {
		t.Fatalf("按流水线目标快照执行失败: %v", err)
	}
	if runner.input.HostID != host.ID || runner.input.EnvironmentID != environment.ID ||
		runner.input.WorkingDirectory != "/srv/original" {
		t.Fatalf("部署读取了运行创建后的目标改动: %+v", runner.input)
	}
	if record.HostID != host.ID || record.EnvironmentID != environment.ID ||
		record.WorkingDirectory != "/srv/original" || record.RolloutTimeout != 90 {
		t.Fatalf("发布记录没有保存流水线目标快照: %+v", record)
	}
}

func TestPipelineReleaseIdempotencyBlocksConcurrentExecutionAndReusesSuccess(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	environment, host := createSSHDeploymentResources(t, db)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "幂等发布目标", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID, HostID: host.ID, WorkingDirectory: "/srv/app", RolloutTimeout: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &idempotentHostScriptRunnerStub{
		result:  sshdeploy.Result{ExitCode: 0, Started: true},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	service.ssh = runner
	script := "./deploy.sh\n"
	input := CommandRequestInput{
		TargetID: target.ID, PipelineRunID: "run-idempotent", WorkflowNodeID: "deploy-production",
		DeploymentPlanID: "plan-idempotent", PlanKind: model.DeploymentPlanScript,
		Script: script, ScriptDigest: model.DeploymentPlanExecutionDigest(model.DeploymentPlanScript, script, 120),
		TimeoutSeconds: 120,
	}
	type result struct {
		record *model.DeploymentRecord
		err    error
	}
	firstResult := make(chan result, 1)
	go func() {
		record, runErr := service.RequestCommandSnapshotAndRun(context.Background(), "operator", *target, input)
		firstResult <- result{record: record, err: runErr}
	}()
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("首次发布没有开始执行")
	}

	duplicate, err := service.RequestCommandSnapshotAndRun(context.Background(), "operator", *target, input)
	if !errors.Is(err, ErrPipelineReleaseRunning) || duplicate == nil {
		t.Fatalf("并发重复发布未返回稳定的执行中错误: record=%+v err=%v", duplicate, err)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("并发重复发布产生了第二次外部执行: calls=%d", runner.calls.Load())
	}
	var count int64
	if err := db.Model(&model.DeploymentRecord{}).
		Where("pipeline_run_id = ? AND workflow_node_id = ? AND operation = ?", input.PipelineRunID, input.WorkflowNodeID, model.DeploymentRelease).
		Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("幂等键未限制为一条发布记录: count=%d err=%v", count, err)
	}

	close(runner.release)
	first := <-firstResult
	if first.err != nil || first.record == nil || first.record.ID != duplicate.ID {
		t.Fatalf("首次发布执行结果错误: record=%+v err=%v", first.record, first.err)
	}
	reused, err := service.RequestCommandSnapshotAndRun(context.Background(), "operator", *target, input)
	if err != nil || reused == nil || reused.ID != first.record.ID || reused.Status != model.DeploymentSucceeded {
		t.Fatalf("重投没有复用已成功发布记录: record=%+v err=%v", reused, err)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("复用成功记录时再次执行了部署: calls=%d", runner.calls.Load())
	}
	changed := input
	changed.ArtifactID = "other-artifact"
	conflict, err := service.RequestCommandSnapshotAndRun(context.Background(), "operator", *target, changed)
	if !errors.Is(err, ErrPipelineReleaseConflict) || conflict == nil || conflict.ID != first.record.ID {
		t.Fatalf("相同幂等键下不同制品语义未被拒绝: record=%+v err=%v", conflict, err)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("语义冲突触发了重复外部执行: calls=%d", runner.calls.Load())
	}
}

func TestRollbackRequiresImmutableImageIdentityAndIsIdempotent(t *testing.T) {
	service, db, endpointID := newDeploymentTestService(t)
	now := time.Now().UTC()
	imageID := "sha256:" + strings.Repeat("e", 64)
	source := model.DeploymentRecord{
		ID: "successful-docker-release", TargetID: "target-rollback", TargetName: "回滚目标",
		Platform: model.DeploymentDocker, RuntimeID: endpointID, WorkloadName: "api", RolloutTimeout: 120,
		Operation: model.DeploymentRelease, Image: "registry.example.com/team/api@sha256:" + strings.Repeat("f", 64),
		PreviousImage: "registry.example.com/team/api:stable", PreviousImageID: imageID,
		DeploymentPlanID: "docker-plan", DeploymentPlanKind: model.DeploymentPlanDocker,
		DockerConfig: model.DockerContainerConfig{Network: "bridge", RestartPolicy: "unless-stopped", EnvironmentVariables: map[string]string{}},
		Status:       model.DeploymentSucceeded, RequestedBy: "operator", CreatedAt: now, UpdatedAt: now,
	}
	source.DockerConfigDigest = model.DockerContainerConfigDigest(source.DockerConfig)
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("创建待回滚发布记录失败: %v", err)
	}
	first, err := service.Rollback(context.Background(), source.ID, "operator")
	if err != nil {
		t.Fatalf("创建固定镜像身份的回滚失败: %v", err)
	}
	var storedRollback model.DeploymentRecord
	if err := db.First(&storedRollback, "id = ?", first.ID).Error; err != nil || storedRollback.IdempotencyKey == nil {
		t.Fatalf("回滚任务未保存幂等标识: record=%+v err=%v", storedRollback, err)
	}
	expectedRollbackKey, _ := rollbackIdempotencyKey(source.ID, 1)
	if *storedRollback.IdempotencyKey != expectedRollbackKey {
		t.Fatalf("回滚幂等标识错误: got=%q want=%q", *storedRollback.IdempotencyKey, expectedRollbackKey)
	}
	if storedRollback.RollbackSourceID != source.ID || storedRollback.RollbackAttempt != 1 {
		t.Fatalf("回滚任务没有记录来源及尝试次数: %+v", storedRollback)
	}
	second, err := service.Rollback(context.Background(), source.ID, "operator")
	if err != nil {
		t.Fatalf("重复回滚请求未复用已有任务: %v", err)
	}
	if first.ID != second.ID || first.ExpectedImageID != imageID || first.Image != imageID {
		t.Fatalf("回滚任务未保持幂等或镜像身份: first=%+v second=%+v", first, second)
	}
	var count int64
	if err := db.Model(&model.DeploymentRecord{}).
		Where("operation = ?", model.DeploymentRollback).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("重复回滚创建了多条执行记录: count=%d err=%v", count, err)
	}

	if err := db.Model(&model.DeploymentRecord{}).Where("id = ?", first.ID).
		Updates(map[string]any{"status": model.DeploymentFailed, "error_code": "rollback_failed"}).Error; err != nil {
		t.Fatalf("标记首次回滚失败失败: %v", err)
	}
	const concurrentRequests = 12
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("读取 SQLite 连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(concurrentRequests)
	sqlDB.SetMaxIdleConns(concurrentRequests)
	type rollbackResult struct {
		record *model.DeploymentRecord
		err    error
	}
	results := make(chan rollbackResult, concurrentRequests)
	var wait sync.WaitGroup
	for range concurrentRequests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record, err := service.Rollback(context.Background(), source.ID, "operator")
			results <- rollbackResult{record: record, err: err}
		}()
	}
	wait.Wait()
	close(results)
	var retryID string
	for result := range results {
		if result.err != nil || result.record == nil {
			t.Fatalf("并发请求失败回滚的重试失败: record=%+v err=%v", result.record, result.err)
		}
		if result.record.ID == first.ID || result.record.RollbackAttempt != 2 || result.record.RollbackSourceID != source.ID {
			t.Fatalf("失败回滚没有创建第 2 次尝试: %+v", result.record)
		}
		if retryID == "" {
			retryID = result.record.ID
		} else if result.record.ID != retryID {
			t.Fatalf("并发重试创建了不同执行记录: first=%s current=%s", retryID, result.record.ID)
		}
	}
	if err := db.Model(&model.DeploymentRecord{}).
		Where("operation = ? AND rollback_source_id = ?", model.DeploymentRollback, source.ID).
		Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("失败后的并发重试未收敛为一次新尝试: count=%d err=%v", count, err)
	}
	var retry model.DeploymentRecord
	if err := db.First(&retry, "id = ?", retryID).Error; err != nil {
		t.Fatalf("读取第 2 次回滚尝试失败: %v", err)
	}
	expectedRetryKey, _ := rollbackIdempotencyKey(source.ID, 2)
	if retry.IdempotencyKey == nil || *retry.IdempotencyKey != expectedRetryKey || retry.JobID == "" {
		t.Fatalf("第 2 次回滚尝试的幂等标识或任务错误: %+v", retry)
	}
	for _, status := range []model.DeploymentStatus{model.DeploymentRunning, model.DeploymentSucceeded} {
		if err := db.Model(&model.DeploymentRecord{}).Where("id = ?", retry.ID).Update("status", status).Error; err != nil {
			t.Fatalf("更新回滚状态为 %s 失败: %v", status, err)
		}
		reused, err := service.Rollback(context.Background(), source.ID, "operator")
		if err != nil || reused == nil || reused.ID != retry.ID || reused.Status != status {
			t.Fatalf("状态为 %s 的回滚尝试未被复用: record=%+v err=%v", status, reused, err)
		}
	}
	if err := db.Model(&model.DeploymentRecord{}).
		Where("operation = ? AND rollback_source_id = ?", model.DeploymentRollback, source.ID).
		Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("执行中或成功的回滚被重复创建: count=%d err=%v", count, err)
	}

	missingIdentity := source
	missingIdentity.ID = "docker-release-without-image-id"
	missingIdentity.PreviousImageID = ""
	if err := db.Create(&missingIdentity).Error; err != nil {
		t.Fatalf("创建缺少镜像身份的历史记录失败: %v", err)
	}
	if _, err := service.Rollback(context.Background(), missingIdentity.ID, "operator"); !errors.Is(err, ErrRollbackUnavailable) {
		t.Fatalf("缺少 Docker Image ID 的回滚未被拒绝: %v", err)
	}

	mutableKubernetes := source
	mutableKubernetes.ID = "kubernetes-release-with-tag"
	mutableKubernetes.Platform = model.DeploymentKubernetes
	mutableKubernetes.RuntimeID = "cluster-1"
	mutableKubernetes.Namespace = "default"
	mutableKubernetes.WorkloadName = "api"
	mutableKubernetes.ContainerName = "api"
	mutableKubernetes.DeploymentPlanKind = model.DeploymentPlanKubernetes
	mutableKubernetes.PreviousImageID = ""
	if err := db.Create(&mutableKubernetes).Error; err != nil {
		t.Fatalf("创建可变 Kubernetes 历史记录失败: %v", err)
	}
	if _, err := service.Rollback(context.Background(), mutableKubernetes.ID, "operator"); !errors.Is(err, ErrRollbackUnavailable) {
		t.Fatalf("Kubernetes 可变标签回滚未被拒绝: %v", err)
	}
}

func TestRollbackAdoptsLegacyFailedRecordBeforeCreatingRetry(t *testing.T) {
	service, db, endpointID := newDeploymentTestService(t)
	now := time.Now().UTC()
	imageID := "sha256:" + strings.Repeat("a", 64)
	source := model.DeploymentRecord{
		ID: "legacy-rollback-source", TargetID: "legacy-target", TargetName: "历史回滚目标",
		Platform: model.DeploymentDocker, RuntimeID: endpointID, WorkloadName: "api", RolloutTimeout: 120,
		Operation: model.DeploymentRelease, Image: "registry.example.com/team/api@sha256:" + strings.Repeat("b", 64),
		PreviousImage: "registry.example.com/team/api:stable", PreviousImageID: imageID,
		DeploymentPlanID: "legacy-plan", DeploymentPlanKind: model.DeploymentPlanDocker,
		DockerConfig: model.DockerContainerConfig{Network: "bridge", RestartPolicy: "unless-stopped", EnvironmentVariables: map[string]string{}},
		Status:       model.DeploymentSucceeded, RequestedBy: "operator", CreatedAt: now, UpdatedAt: now,
	}
	source.DockerConfigDigest = model.DockerContainerConfigDigest(source.DockerConfig)
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("创建历史回滚来源失败: %v", err)
	}
	legacyKey, _ := rollbackIdempotencyKey(source.ID, 1)
	legacy := model.DeploymentRecord{
		ID: "legacy-failed-rollback", IdempotencyKey: &legacyKey,
		TargetID: source.TargetID, Operation: model.DeploymentRollback,
		Image: imageID, ExpectedImageID: imageID, Status: model.DeploymentFailed,
		DeploymentPlanID: source.DeploymentPlanID, DeploymentPlanKind: source.DeploymentPlanKind,
		DockerConfig: source.DockerConfig, DockerConfigDigest: source.DockerConfigDigest,
		RequestedBy: "operator", CreatedAt: now, UpdatedAt: now,
	}
	copyTargetSnapshot(&legacy, &source)
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("创建旧结构失败回滚记录失败: %v", err)
	}

	retry, err := service.Rollback(context.Background(), source.ID, "operator")
	if err != nil {
		t.Fatalf("旧结构失败回滚无法重试: %v", err)
	}
	if retry.ID == legacy.ID || retry.RollbackSourceID != source.ID || retry.RollbackAttempt != 2 {
		t.Fatalf("旧结构失败回滚没有创建第 2 次尝试: %+v", retry)
	}
	if err := db.First(&legacy, "id = ?", legacy.ID).Error; err != nil {
		t.Fatalf("重新读取旧结构回滚失败: %v", err)
	}
	if legacy.RollbackSourceID != source.ID || legacy.RollbackAttempt != 1 || legacy.Status != model.DeploymentFailed {
		t.Fatalf("旧结构回滚审计关系没有原位补齐: %+v", legacy)
	}
}

func TestPipelineReleaseIdempotencyDoesNotRetryFailedDeployment(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	environment, host := createSSHDeploymentResources(t, db)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "失败幂等目标", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID, HostID: host.ID, WorkingDirectory: "/srv/app", RolloutTimeout: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &idempotentHostScriptRunnerStub{
		result: sshdeploy.Result{ExitCode: 17, Started: true}, err: errors.New("remote deployment failed"),
	}
	service.ssh = runner
	script := "exit 17\n"
	input := CommandRequestInput{
		TargetID: target.ID, PipelineRunID: "run-failed-idempotent", WorkflowNodeID: "deploy",
		DeploymentPlanID: "plan-failed-idempotent", PlanKind: model.DeploymentPlanScript,
		Script: script, ScriptDigest: model.DeploymentPlanExecutionDigest(model.DeploymentPlanScript, script, 120),
		TimeoutSeconds: 120,
	}
	first, err := service.RequestCommandSnapshotAndRun(context.Background(), "operator", *target, input)
	if err == nil || first == nil {
		t.Fatalf("首次失败发布没有保存发布记录: record=%+v err=%v", first, err)
	}
	duplicate, err := service.RequestCommandSnapshotAndRun(context.Background(), "operator", *target, input)
	if !errors.Is(err, ErrPipelineReleaseFailed) || duplicate == nil || duplicate.ID != first.ID {
		t.Fatalf("失败发布重投未返回稳定错误及原记录: record=%+v err=%v", duplicate, err)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("失败发布被隐式重试: calls=%d", runner.calls.Load())
	}
}

func TestSSHDeploymentTargetAcceptsAnyConfiguredHostEnvironment(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	first, host := createSSHDeploymentResources(t, db)
	now := time.Now().UTC()
	second := model.Environment{
		ID: "environment-ssh-second", Name: "SSH 第二环境", IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	unrelated := model.Environment{
		ID: "environment-ssh-unrelated", Name: "SSH 未关联环境", IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&[]model.Environment{second, unrelated}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.EnvironmentHost{
		EnvironmentID: second.ID, HostID: host.ID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for index, environmentID := range []string{first.ID, second.ID} {
		if _, err := service.CreateTarget(context.Background(), "admin", TargetInput{
			Name: "共享主机目标 " + string(rune('A'+index)), Platform: model.DeploymentSSH,
			EnvironmentID: environmentID, HostID: host.ID, RolloutTimeout: 60,
		}); err != nil {
			t.Fatalf("已关联环境 %s 应允许使用共享主机: %v", environmentID, err)
		}
	}
	if _, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "未关联环境目标", Platform: model.DeploymentSSH,
		EnvironmentID: unrelated.ID, HostID: host.ID, RolloutTimeout: 60,
	}); !errors.Is(err, ErrEnvironmentTargetUnavailable) {
		t.Fatalf("未关联环境不应使用该主机: %v", err)
	}
}

func TestSSHDeploymentLockUsesLowerPlanAndTargetTimeout(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	service := &Service{locks: redislock.New(redisClient), lockKeyPrefix: "zrt:test:ssh-lock"}
	record := &model.DeploymentRecord{
		ID: "deployment-ssh", TargetID: "target-ssh", Platform: model.DeploymentSSH,
		CommandTimeout: 120, RolloutTimeout: 45,
	}
	lock, err := service.acquireTargetLock(context.Background(), record)
	if err != nil {
		t.Fatalf("获取 SSH 发布锁失败: %v", err)
	}
	defer lock.Release(context.Background())
	if ttl := redisServer.TTL("zrt:test:ssh-lock:target-ssh"); ttl != 165*time.Second {
		t.Fatalf("SSH 发布锁应使用较小超时加保护窗口: got=%s want=%s", ttl, 165*time.Second)
	}
	if _, valid := effectiveSSHTimeout(&model.DeploymentRecord{CommandTimeout: 29, RolloutTimeout: 60}); valid {
		t.Fatal("低于 30 秒的部署方案超时不应进入执行")
	}
	if _, valid := effectiveSSHTimeout(&model.DeploymentRecord{CommandTimeout: 60, RolloutTimeout: 3601}); valid {
		t.Fatal("超过 3600 秒的发布目标超时不应进入执行")
	}
}

func TestComposeDeploymentUsesLowerPlanAndTargetTimeout(t *testing.T) {
	record := &model.DeploymentRecord{
		Platform: model.DeploymentDocker, DeploymentPlanKind: model.DeploymentPlanCompose,
		ComposeTimeout: 600, RolloutTimeout: 120,
	}
	if timeout, valid := effectiveComposeTimeout(record); !valid || timeout != 120*time.Second {
		t.Fatalf("Compose 部署没有使用方案与目标中较小的超时: timeout=%s valid=%v", timeout, valid)
	}
	record.ComposeTimeout = 29
	if _, valid := effectiveComposeTimeout(record); valid {
		t.Fatal("过短的 Compose 方案超时未被拒绝")
	}
	record.ComposeTimeout, record.RolloutTimeout = 120, 3601
	if _, valid := effectiveComposeTimeout(record); valid {
		t.Fatal("过长的 Compose 目标超时未被拒绝")
	}
}

func TestSSHDeploymentRejectsTamperedDigestAndRelativeWorkingDirectory(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	environment, host := createSSHDeploymentResources(t, db)
	if _, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "无效 SSH 目录", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID, HostID: host.ID,
		WorkingDirectory: "relative/path", RolloutTimeout: 60,
	}); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("SSH 工作目录必须是规范绝对路径: %v", err)
	}
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "有效 SSH 目标", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID, HostID: host.ID, RolloutTimeout: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.ssh = &hostScriptRunnerStub{}
	if _, err := service.RequestCommandAndRun(context.Background(), "operator", CommandRequestInput{
		TargetID: target.ID, DeploymentPlanID: "plan-1", PlanKind: model.DeploymentPlanScript,
		Script: "echo ok\n", ScriptDigest: strings.Repeat("0", 64), TimeoutSeconds: 60,
	}); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("被篡改的部署脚本摘要未被拒绝: %v", err)
	}
	var count int64
	if err := db.Model(&model.DeploymentRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("无效摘要不应创建发布记录: count=%d err=%v", count, err)
	}
}

func TestLocalCommandTargetRequiresBuiltinReadyCapability(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	now := time.Now().UTC()
	environment := model.Environment{
		ID: "environment-local-command", Name: "本地命令环境", Level: model.EnvironmentDevelopment,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&environment).Error; err != nil {
		t.Fatalf("创建本地命令环境失败: %v", err)
	}
	if err := db.Model(&model.Host{}).Where("id = ?", model.BuiltinLocalHostID).
		Update("is_active", true).Error; err != nil {
		t.Fatalf("绑定本地主机环境失败: %v", err)
	}
	if err := db.Create(&model.EnvironmentHost{
		EnvironmentID: environment.ID, HostID: model.BuiltinLocalHostID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("绑定本地主机环境失败: %v", err)
	}
	capability := model.HostCapability{
		HostID: model.BuiltinLocalHostID, Kind: model.HostCapabilityLocalExec,
		Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Where("host_id = ? AND kind = ?", capability.HostID, capability.Kind).
		Assign(map[string]any{"status": model.HostCapabilityReady, "updated_at": now}).
		FirstOrCreate(&capability).Error; err != nil {
		t.Fatalf("创建本地执行能力失败: %v", err)
	}
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "本地命令目标", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID, HostID: model.BuiltinLocalHostID,
		WorkingDirectory: "/srv/zrt", RolloutTimeout: 90,
	})
	if err != nil {
		t.Fatalf("已启用本地执行能力时应允许创建命令目标: %v", err)
	}
	if target.HostID != model.BuiltinLocalHostID || target.EnvironmentID != environment.ID ||
		target.Platform != model.DeploymentSSH {
		t.Fatalf("本地命令目标没有使用主机和环境快照: %+v", target)
	}
	if err := db.Model(&model.HostCapability{}).
		Where("host_id = ? AND kind = ?", model.BuiltinLocalHostID, model.HostCapabilityLocalExec).
		Update("status", model.HostCapabilityUnreachable).Error; err != nil {
		t.Fatalf("修改本地执行能力状态失败: %v", err)
	}
	if _, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "不可用的本地命令目标", Platform: model.DeploymentSSH,
		EnvironmentID: environment.ID, HostID: model.BuiltinLocalHostID, RolloutTimeout: 90,
	}); !errors.Is(err, ErrEnvironmentTargetUnavailable) {
		t.Fatalf("本地执行能力不可用时不应创建发布目标: %v", err)
	}
}

func TestDeploymentTargetResolvesUniqueEnvironmentHost(t *testing.T) {
	service, _, endpointID := newDeploymentTestService(t)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "环境自动解析目标", Platform: model.DeploymentDocker,
		EnvironmentID: dockerTestEnvironmentID,
		HostID:        "客户端伪造主机", RuntimeID: "客户端伪造运行时",
		WorkloadName: "demo", RolloutTimeout: 120,
	})
	if err != nil {
		t.Fatalf("环境内唯一 Docker 主机应自动解析: %v", err)
	}
	if target.HostID != dockerTestHostID || target.RuntimeID != endpointID {
		t.Fatalf("服务端没有忽略客户端主机并按环境解析: %+v", target)
	}
}

func TestDeploymentTargetRejectsAmbiguousEnvironmentHosts(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	now := time.Now().UTC()
	host := model.Host{
		ID: "docker-test-host-second", Name: "Docker 测试主机二", Mode: model.HostModeSSH,
		SSHPort: 22, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	endpoint := model.DockerEndpoint{
		ID: "docker-endpoint-second", Name: "local-docker-second", Host: "unix:///var/run/docker-second.sock",
		HostID: host.ID, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	resources := []any{
		&host,
		&endpoint,
		&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityDocker, RuntimeID: endpoint.ID, Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now},
		&model.EnvironmentHost{EnvironmentID: dockerTestEnvironmentID, HostID: host.ID, CreatedAt: now},
	}
	for _, resource := range resources {
		if err := db.Create(resource).Error; err != nil {
			t.Fatalf("创建第二台环境主机失败: %v", err)
		}
	}
	if _, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "环境歧义目标", Platform: model.DeploymentDocker,
		EnvironmentID: dockerTestEnvironmentID, WorkloadName: "demo", RolloutTimeout: 120,
	}); !errors.Is(err, ErrEnvironmentTargetAmbiguous) {
		t.Fatalf("环境存在多台 Docker 主机时必须拒绝静默选择: %v", err)
	}
}

func createSSHDeploymentResources(t *testing.T, db *gorm.DB) (model.Environment, model.Host) {
	t.Helper()
	now := time.Now().UTC()
	environment := model.Environment{
		ID: "environment-ssh", Name: "SSH 开发环境", Level: model.EnvironmentDevelopment,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&environment).Error; err != nil {
		t.Fatalf("创建 SSH 测试环境失败: %v", err)
	}
	host := model.Host{
		ID: "host-ssh", Name: "SSH 主机", Mode: model.HostModeSSH, Address: "192.0.2.10",
		SSHPort: 22, SSHUsername: "deploy", SSHAuthType: model.SSHAuthPassword,
		SSHCredentialCiphertext: "encrypted", SSHHostKeyFingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatalf("创建 SSH 测试主机失败: %v", err)
	}
	if err := db.Create(&model.EnvironmentHost{
		EnvironmentID: environment.ID, HostID: host.ID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("创建 SSH 主机环境关联失败: %v", err)
	}
	capability := model.HostCapability{
		HostID: host.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatalf("创建 SSH 测试能力失败: %v", err)
	}
	return environment, host
}

func newDeploymentTestService(t *testing.T) (*Service, *gorm.DB, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开发布测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移发布测试数据库失败: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化发布测试密钥失败: %v", err)
	}
	runtimeConfig := config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second}
	dockerService := dockerengine.NewService(db, secretManager, runtimeConfig)
	kubeService := kube.NewService(db, secretManager, runtimeConfig)
	endpoint := model.DockerEndpoint{
		ID: "docker-endpoint", Name: "local-docker", Host: "unix:///var/run/docker.sock",
		HostID:   dockerTestHostID,
		IsActive: true, CreatedBy: "admin", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("创建测试 Docker 连接失败: %v", err)
	}
	now := time.Now().UTC()
	resources := []any{
		&model.Environment{ID: dockerTestEnvironmentID, Name: "Docker 测试环境", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		&model.Host{ID: dockerTestHostID, Name: "Docker 测试主机", Mode: model.HostModeSSH, SSHPort: 22, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		&model.HostCapability{HostID: dockerTestHostID, Kind: model.HostCapabilityDocker, RuntimeID: endpoint.ID, Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now},
		&model.EnvironmentHost{EnvironmentID: dockerTestEnvironmentID, HostID: dockerTestHostID, CreatedAt: now},
	}
	for _, resource := range resources {
		if err := db.Create(resource).Error; err != nil {
			t.Fatalf("创建 Docker 环境测试资源失败: %v", err)
		}
	}
	return NewService(db, dockerService, kubeService, nil, nil, "", logger), db, endpoint.ID
}
