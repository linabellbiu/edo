package deployment

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"

	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/testutil"
)

func newDeploymentRequestMockService(t *testing.T) (*Service, sqlmock.Sqlmock, func() *model.DeploymentRecord) {
	t.Helper()
	db, mock := testutil.NewSQLMock(t)
	var snapshot *model.DeploymentRecord
	if err := db.Callback().Create().Before("gorm:create").Register("test:deployment_request_snapshot", func(tx *gorm.DB) {
		if record, ok := tx.Statement.Dest.(*model.DeploymentRecord); ok {
			copy := *record
			snapshot = &copy
		}
	}); err != nil {
		t.Fatalf("登记发布快照检查失败: %v", err)
	}
	service := NewService(db, &dockerengine.Service{}, nil, nil, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	return service, mock, func() *model.DeploymentRecord { return snapshot }
}

func composeRequestMockInput() (model.DeploymentTarget, RequestInput) {
	target := model.DeploymentTarget{
		ID: "compose-target", ProjectID: "compose-project", DepartmentID: "compose-department",
		Name: "Compose 测试目标", Platform: model.DeploymentDocker,
		EnvironmentID: "compose-environment", HostID: "compose-host", RuntimeID: "compose-runtime",
		RolloutTimeout: 120,
	}
	input := RequestInput{
		TargetID: target.ID, ApplicationID: "compose-application", ApplicationName: "compose_app",
		ArtifactID: "compose-artifact", Image: "registry.example.com/team/app@sha256:" + strings.Repeat("a", 64),
		PipelineRunID: "compose-run", WorkflowNodeID: "compose-node", DeploymentPlanID: "compose-plan",
		PlanKind: model.DeploymentPlanCompose, ComposeYAML: "services:\n  app:\n    restart: no\n",
		ComposeService: "app", TimeoutSeconds: 90,
	}
	input.ComposeDigest = model.DeploymentPlanComposeExecutionDigest(input.ComposeYAML, input.ComposeService, input.TimeoutSeconds)
	return target, input
}

func TestPipelineDockerSnapshotNameValidationBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name         string
		kind         model.DeploymentPlanKind
		workloadName string
	}{
		{name: "Compose 不需要单容器名称", kind: model.DeploymentPlanCompose},
		{name: "Compose 保留已有目标快照", kind: model.DeploymentPlanCompose, workloadName: "existing-container"},
		{name: "Docker 自动生成容器名称", kind: model.DeploymentPlanDocker},
		{name: "Docker 保留显式容器名称", kind: model.DeploymentPlanDocker, workloadName: "existing-container"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, mock, snapshot := newDeploymentRequestMockService(t)
			target, input := composeRequestMockInput()
			target.WorkloadName = tc.workloadName
			input.PlanKind = tc.kind
			if tc.kind == model.DeploymentPlanDocker {
				config, err := dockerengine.NormalizeContainerConfig(model.DockerContainerConfig{})
				if err != nil {
					t.Fatal(err)
				}
				input.DockerConfig, input.DockerConfigDigest = config, model.DockerContainerConfigDigest(config)
			}
			// 存储哨兵保证测试停在快照边界，不进入任何 Docker 或 Kubernetes 执行器。
			storageErr := errors.New("模拟发布快照写入失败")
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO "deployment_records"`).WillReturnError(storageErr)
			mock.ExpectRollback()
			record, err := service.RequestSnapshotAndRun(context.Background(), "operator", target, input)
			if record != nil || !errors.Is(err, storageErr) {
				t.Fatalf("有效请求未到达发布快照边界: record=%v err=%v", record, err)
			}
			stored := snapshot()
			if stored == nil || stored.TargetID != target.ID || stored.RuntimeID != target.RuntimeID ||
				stored.ProjectID != target.ProjectID || stored.DepartmentID != target.DepartmentID ||
				stored.DeploymentPlanID != input.DeploymentPlanID || stored.DeploymentPlanKind != tc.kind ||
				stored.PipelineRunID != input.PipelineRunID || stored.WorkflowNodeID != input.WorkflowNodeID ||
				stored.ArtifactID != input.ArtifactID || stored.Status != model.DeploymentQueued {
				t.Fatalf("发布请求未保留准确身份及运行快照: %+v", stored)
			}
			wantName := tc.workloadName
			if tc.kind == model.DeploymentPlanDocker {
				wantName, err = ResolveDockerWorkloadName(tc.workloadName, input.ApplicationName, input.ApplicationID, input.DeploymentPlanID, target.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.ComposeYAML != "" || stored.ComposeService != "" || stored.ComposeDigest != "" || stored.ComposeTimeout != 0 ||
					stored.DockerConfigDigest != input.DockerConfigDigest {
					t.Fatalf("Docker 单容器快照混入 Compose 配置: %+v", stored)
				}
			} else if stored.ComposeYAML != input.ComposeYAML || stored.ComposeService != input.ComposeService ||
				stored.ComposeDigest != input.ComposeDigest || stored.ComposeTimeout != input.TimeoutSeconds || stored.DockerConfigDigest != "" {
				t.Fatalf("Compose 服务快照不完整或混入单容器配置: %+v", stored)
			}
			if stored.WorkloadName != wantName || target.WorkloadName != tc.workloadName {
				t.Fatalf("容器名称规则错误或污染原始目标: snapshot=%q target=%q want=%q", stored.WorkloadName, target.WorkloadName, wantName)
			}
		})
	}
}

func TestPipelineComposeSnapshotRejectsInvalidBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*model.DeploymentTarget, *RequestInput, *Service)
	}{
		{name: "旧 Docker 请求仍必须指定容器名称", mutate: func(_ *model.DeploymentTarget, input *RequestInput, _ *Service) { input.PlanKind = "" }},
		{name: "篡改 Compose 摘要", mutate: func(_ *model.DeploymentTarget, input *RequestInput, _ *Service) {
			input.ComposeDigest = strings.Repeat("b", 64)
		}},
		{name: "无效 Compose 配置", mutate: func(_ *model.DeploymentTarget, input *RequestInput, _ *Service) {
			input.ComposeYAML = "services:\n  app:\n    privileged: true\n"
			input.ComposeDigest = model.DeploymentPlanComposeExecutionDigest(input.ComposeYAML, input.ComposeService, input.TimeoutSeconds)
		}},
		{name: "Compose 不允许 Kubernetes 目标", mutate: func(target *model.DeploymentTarget, _ *RequestInput, _ *Service) {
			target.Platform, target.Namespace, target.WorkloadName, target.ContainerName = model.DeploymentKubernetes, "default", "app", "app"
		}},
		{name: "缺少方案身份", mutate: func(_ *model.DeploymentTarget, input *RequestInput, _ *Service) { input.DeploymentPlanID = "" }},
		{name: "服务不在 Compose 中", mutate: func(_ *model.DeploymentTarget, input *RequestInput, _ *Service) {
			input.ComposeService = "missing"
			input.ComposeDigest = model.DeploymentPlanComposeExecutionDigest(input.ComposeYAML, input.ComposeService, input.TimeoutSeconds)
		}},
		{name: "超出 Compose 超时边界", mutate: func(_ *model.DeploymentTarget, input *RequestInput, _ *Service) {
			input.TimeoutSeconds = 29
			input.ComposeDigest = model.DeploymentPlanComposeExecutionDigest(input.ComposeYAML, input.ComposeService, input.TimeoutSeconds)
		}},
		{name: "缺少 Docker 执行器", mutate: func(_ *model.DeploymentTarget, _ *RequestInput, service *Service) { service.docker = nil }},
		{name: "已有非法目标名称仍被拒绝", mutate: func(target *model.DeploymentTarget, _ *RequestInput, _ *Service) {
			target.WorkloadName = "invalid/name"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, snapshot := newDeploymentRequestMockService(t)
			target, input := composeRequestMockInput()
			tc.mutate(&target, &input, service)
			record, err := service.RequestSnapshotAndRun(context.Background(), "operator", target, input)
			if record != nil || !errors.Is(err, ErrInvalidTarget) || snapshot() != nil {
				t.Fatalf("无效发布请求未在写入前阻断: record=%v err=%v snapshot=%v", record, err, snapshot())
			}
		})
	}
}
