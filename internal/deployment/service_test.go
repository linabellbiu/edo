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
)

func TestProductionDeploymentQueuesWithoutImplicitApproval(t *testing.T) {
	service, db, endpointID := newDeploymentTestService(t)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "production-api", Platform: model.DeploymentDocker, Environment: model.EnvironmentProduction,
		RuntimeID: endpointID, WorkloadName: "production-api", RolloutTimeout: 120,
	})
	if err != nil {
		t.Fatalf("创建生产发布目标失败: %v", err)
	}
	if _, err := service.Request(context.Background(), "requester", RequestInput{
		TargetID: target.ID, Image: "registry.example.com/team/api:latest",
	}); !errors.Is(err, ErrImmutableImageRequired) {
		t.Fatalf("生产发布未拒绝可变镜像标签: %v", err)
	}

	digestImage := "registry.example.com/team/api@sha256:" + strings.Repeat("a", 64)
	record, err := service.Request(context.Background(), "requester", RequestInput{TargetID: target.ID, Image: digestImage})
	if err != nil {
		t.Fatalf("创建生产发布任务失败: %v", err)
	}
	if record.Status != model.DeploymentQueued || record.WorkloadName != "production-api" || record.JobID == "" || record.ApprovedBy != nil {
		t.Fatalf("生产环境不应隐式增加审批状态: %+v", record)
	}
	var job model.Job
	if err := db.First(&job, "id = ?", record.JobID).Error; err != nil {
		t.Fatalf("读取生产发布任务失败: %v", err)
	}
	if job.MaxAttempts != 1 {
		t.Fatalf("有外部副作用的发布任务执行次数应为 1，实际为 %d", job.MaxAttempts)
	}
}

func TestPipelinePreparedLocalImageUsesVerifiedImageID(t *testing.T) {
	target := &model.DeploymentTarget{Platform: model.DeploymentDocker, Environment: model.EnvironmentProduction}
	image := "zrt.local/order-api:abcdef123456-12345678"
	imageID := "sha256:" + strings.Repeat("b", 64)
	if normalized, err := validatePipelineImage(image, imageID, target); err != nil || normalized != image {
		t.Fatalf("流水线已经校验的本地镜像应允许用于生产发布: image=%q err=%v", normalized, err)
	}
	if _, err := validatePipelineImage(image, "", target); !errors.Is(err, ErrImmutableImageRequired) {
		t.Fatalf("未携带镜像 ID 的本地标签不能绕过生产不可变检查: %v", err)
	}
	target.Platform = model.DeploymentKubernetes
	if _, err := validatePipelineImage(image, imageID, target); !errors.Is(err, ErrInvalidImage) {
		t.Fatalf("Kubernetes 不应接受只存在于 Docker 主机的本地镜像: %v", err)
	}
}

func TestDeploymentUsesApprovedTargetSnapshot(t *testing.T) {
	service, _, endpointID := newDeploymentTestService(t)
	target, err := service.CreateTarget(context.Background(), "admin", TargetInput{
		Name: "testing-api", Platform: model.DeploymentDocker, Environment: model.EnvironmentDevelopment,
		RuntimeID: endpointID, WorkloadName: "api-v1", RolloutTimeout: 120,
	})
	if err != nil {
		t.Fatalf("创建发布目标失败: %v", err)
	}
	record, err := service.Request(context.Background(), "operator", RequestInput{
		TargetID: target.ID, Image: "registry.example.com/team/api:2026.07.23",
	})
	if err != nil {
		t.Fatalf("创建发布任务失败: %v", err)
	}
	if _, err := service.UpdateTarget(context.Background(), target.ID, TargetInput{
		Name: "testing-api", Platform: model.DeploymentDocker, Environment: model.EnvironmentDevelopment,
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
		Platform: model.DeploymentDocker, Environment: model.EnvironmentStaging,
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
	return NewService(db, dockerService, kubeService, nil, "", logger), db, endpoint.ID
}
