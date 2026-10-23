package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gorm.io/gorm"

	"edo/internal/kube"
	"edo/internal/model"
)

type architectureReaderStub struct {
	clusterID string
	projectID string
	result    []model.HostArchitecture
	err       error
}

func (s *architectureReaderStub) KubernetesNodeArchitectures(_ context.Context, _ *gorm.DB, clusterID, projectID string) ([]model.HostArchitecture, error) {
	s.clusterID, s.projectID = clusterID, projectID
	return s.result, s.err
}

func TestKubernetesTargetArchitectureUsesClusterWithoutHost(t *testing.T) {
	reader := &architectureReaderStub{result: []model.HostArchitecture{model.HostArchitectureAMD64, model.HostArchitectureARM64}}
	target := model.DeploymentTarget{Platform: model.DeploymentKubernetes, ProjectID: "project", RuntimeID: "cluster"}
	architectures, err := resolveDeploymentTargetArchitectures(context.Background(), nil, target, reader)
	if err != nil || !reflect.DeepEqual(architectures, reader.result) || reader.clusterID != target.RuntimeID || reader.projectID != target.ProjectID {
		t.Fatalf("Kubernetes 构建平台仍依赖主机: architectures=%v reader=%+v err=%v", architectures, reader, err)
	}
	if singleTargetArchitecture(architectures) != "" {
		t.Fatal("多架构不能被旧字段误写为单一平台")
	}
	target.HostID = "unrelated-amd64-host"
	reader.err = kube.ErrNodeArchitecturesUnavailable
	if result, err := resolveDeploymentTargetArchitectures(context.Background(), nil, target, reader); !errors.Is(err, reader.err) {
		t.Fatalf("集群不可读时不能回退主机架构: result=%v err=%v", result, err)
	}
	if result, err := resolveDeploymentTargetArchitectures(context.Background(), nil, target, nil); !errors.Is(err, kube.ErrNodeArchitecturesUnavailable) || result != nil {
		t.Fatalf("架构读取能力未配置时不能伪造平台: result=%v err=%v", result, err)
	}
}

func TestWorkflowBuildPlatformUsesFrozenClusterArchitectures(t *testing.T) {
	build := model.WorkflowNode{ID: "build", Type: model.WorkflowNodeBuild}
	deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy}
	snapshot := workflowSnapshot{
		Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{build, deploy}}},
		DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{
			deploy.ID: {Platform: model.DeploymentKubernetes, Architectures: []model.HostArchitecture{model.HostArchitectureARM64, model.HostArchitectureAMD64, model.HostArchitectureARM64}},
		},
	}
	plan := workflowBuildPlanSnapshot{Kind: model.BuildPlanDockerfile, ImageRegistryID: "registry"}
	platform, err := workflowBuildPlatform(snapshot, build.ID, plan)
	if err != nil || platform != "linux/amd64,linux/arm64" {
		t.Fatalf("没有复用镜像仓库多架构构建: platform=%q err=%v", platform, err)
	}
	plan.ImageRegistryID = ""
	if _, err := workflowBuildPlatform(snapshot, build.ID, plan); !errors.Is(err, ErrPipelineExecutionConfig) {
		t.Fatalf("没有仓库时不应生成不可推送的多架构镜像: %v", err)
	}
	snapshot.DeploymentTargets[deploy.ID] = workflowDeploymentTargetSnapshot{Architectures: []model.HostArchitecture{"unknown"}}
	if _, err := workflowBuildPlatform(snapshot, build.ID, plan); !errors.Is(err, ErrPipelineExecutionConfig) {
		t.Fatalf("冻结快照中的未知架构没有被拒绝: %v", err)
	}
}
