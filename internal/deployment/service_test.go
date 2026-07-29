package deployment

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strings"
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

func (s *hostScriptRunnerStub) RunHostDeploymentScript(_ context.Context, input sshdeploy.Input) (sshdeploy.Result, error) {
	s.input = input
	return s.result, s.err
}

func TestRegistryDeploymentRequiresDigestAndQueuesWithoutImplicitApproval(t *testing.T) {
	service, db, endpointID := newDeploymentTestService(t)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "registry-api", Platform: model.DeploymentDocker,
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
		Platform:  model.DeploymentDocker,
		RuntimeID: endpointID, WorkloadName: "demo-api", RolloutTimeout: 120,
	})
	if err != nil {
		t.Fatalf("创建自定义发布环境失败: %v", err)
	}
	if target.Name != "华东客户演示" || target.Description != "上海机房的演示环境" {
		t.Fatalf("发布环境名称或说明保存错误: %+v", target)
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

func TestSSHDeploymentUsesServerDerivedEnvironmentAndExactPlanSnapshot(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	environment, host := createSSHDeploymentResources(t, db)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "SSH 命令目标", Platform: model.DeploymentSSH,
		EnvironmentID: "client-value-is-ignored",
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

func TestSSHDeploymentRejectsTamperedDigestAndRelativeWorkingDirectory(t *testing.T) {
	service, db, _ := newDeploymentTestService(t)
	_, host := createSSHDeploymentResources(t, db)
	if _, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "无效 SSH 目录", Platform: model.DeploymentSSH, HostID: host.ID,
		WorkingDirectory: "relative/path", RolloutTimeout: 60,
	}); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("SSH 工作目录必须是规范绝对路径: %v", err)
	}
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "有效 SSH 目标", Platform: model.DeploymentSSH, HostID: host.ID, RolloutTimeout: 60,
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
		Updates(map[string]any{"environment_id": environment.ID, "is_active": true}).Error; err != nil {
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
		HostID: model.BuiltinLocalHostID, WorkingDirectory: "/srv/zrt", RolloutTimeout: 90,
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
		HostID: model.BuiltinLocalHostID, RolloutTimeout: 90,
	}); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("本地执行能力不可用时不应创建发布目标: %v", err)
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
		EnvironmentID: environment.ID, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatalf("创建 SSH 测试主机失败: %v", err)
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
		IsActive: true, CreatedBy: "admin", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("创建测试 Docker 连接失败: %v", err)
	}
	return NewService(db, dockerService, kubeService, nil, nil, "", logger), db, endpoint.ID
}
