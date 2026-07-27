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

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/dockerengine"
	"zrt/internal/kube"
	"zrt/internal/model"
	"zrt/internal/secret"
)

func TestProductionDeploymentRequiresDigestAndSeparateApproval(t *testing.T) {
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
		t.Fatalf("创建生产发布申请失败: %v", err)
	}
	if record.Status != model.DeploymentAwaitingApproval || record.WorkloadName != "production-api" || record.JobID != "" {
		t.Fatalf("生产发布申请状态或快照错误: %+v", record)
	}
	if _, err := service.Approve(context.Background(), record.ID, "requester"); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("生产发布自审批未被拒绝: %v", err)
	}
	approved, err := service.Approve(context.Background(), record.ID, "reviewer")
	if err != nil {
		t.Fatalf("审批生产发布失败: %v", err)
	}
	if approved.Status != model.DeploymentQueued || approved.JobID == "" || approved.ApprovedBy == nil || *approved.ApprovedBy != "reviewer" {
		t.Fatalf("生产发布审批结果错误: %+v", approved)
	}
	var job model.Job
	if err := db.First(&job, "id = ?", approved.JobID).Error; err != nil {
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
	return NewService(db, dockerService, kubeService, logger), db, endpoint.ID
}
