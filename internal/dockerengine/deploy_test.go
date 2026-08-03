package dockerengine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"edo/internal/model"
)

func TestInitialContainerConfigUsesImageDefaultsAndSafeRestartPolicy(t *testing.T) {
	configuration, hostConfiguration, err := initialContainerConfig("edo.local/app:commit", "target-id", "deployment-id", model.DockerContainerConfig{})
	if err != nil {
		t.Fatalf("生成默认容器配置失败: %v", err)
	}
	if configuration.Image != "edo.local/app:commit" || configuration.Cmd != nil || configuration.Entrypoint != nil {
		t.Fatalf("首次部署没有保留镜像默认启动配置: %+v", configuration)
	}
	if configuration.Labels["edo.managed"] != "true" || configuration.Labels["edo.deployment.id"] != "deployment-id" ||
		configuration.Labels["edo.deployment.target.id"] != "target-id" {
		t.Fatalf("首次部署缺少 EDO 管理标签: %+v", configuration.Labels)
	}
	if hostConfiguration.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatalf("首次部署没有配置守护进程重启策略: %+v", hostConfiguration.RestartPolicy)
	}
	if hostConfiguration.NetworkMode != container.NetworkMode("bridge") || len(hostConfiguration.PortBindings) != 0 || len(hostConfiguration.Mounts) != 0 {
		t.Fatalf("默认容器配置不够克制: %+v", hostConfiguration)
	}
}

func TestApplyImageDisplayLabelKeepsFriendlyVersionSeparateFromExecutionImage(t *testing.T) {
	configuration, _, err := initialContainerConfig(
		"registry.example.com/team/order_api@sha256:"+strings.Repeat("a", 64),
		"target-id", "deployment-id", model.DockerContainerConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	applyImageDisplayLabel(configuration, "order_api:fea2410d1e47")
	if configuration.Labels[managedImageDisplayLabel] != "order_api:fea2410d1e47" {
		t.Fatalf("容器没有保存简短镜像版本: %+v", configuration.Labels)
	}
	if !strings.Contains(configuration.Image, "@sha256:") {
		t.Fatalf("展示版本不得替换实际执行的不可变镜像: %q", configuration.Image)
	}
}

func TestManagedContainerVolumeNameIsScopedByContainerAndLogicalName(t *testing.T) {
	first := managedContainerVolumeName("target-api", "data")
	if first != managedContainerVolumeName("target-api", "data") || first == managedContainerVolumeName("target-worker", "data") ||
		first == managedContainerVolumeName("target-api", "cache") || !strings.HasPrefix(first, "edo-") {
		t.Fatalf("命名卷必须稳定地隔离到容器和逻辑卷: %q", first)
	}
}

func TestInitialContainerConfigAppliesDeploymentPlan(t *testing.T) {
	input := model.DockerContainerConfig{
		PortMappings:         []model.DockerPortMapping{{HostPort: 18080, ContainerPort: 8080}},
		EnvironmentVariables: map[string]string{"Z_VALUE": "last", "A_VALUE": "first"},
		VolumeMounts:         []model.DockerVolumeMount{{Type: "volume", Source: "app-data", Target: "/data", ReadOnly: true}},
		Network:              "bridge", Command: []string{"server", "--listen", ":8080"},
		HealthCheck: model.DockerHealthCheck{Enabled: true, Command: []string{"server", "health"}},
	}
	configuration, hostConfiguration, err := initialContainerConfig("edo.local/app:commit", "target-id", "deployment-id", input)
	if err != nil {
		t.Fatalf("生成容器配置失败: %v", err)
	}
	if !reflect.DeepEqual(configuration.Cmd, input.Command) || !reflect.DeepEqual(configuration.Env, []string{"A_VALUE=first", "Z_VALUE=last"}) {
		t.Fatalf("启动命令或环境变量未应用: %+v", configuration)
	}
	if configuration.Healthcheck == nil || !reflect.DeepEqual(configuration.Healthcheck.Test, []string{"CMD", "server", "health"}) ||
		configuration.Healthcheck.Interval.String() != "30s" || configuration.Healthcheck.Timeout.String() != "5s" || configuration.Healthcheck.Retries != 3 {
		t.Fatalf("健康检查未应用默认值: %+v", configuration.Healthcheck)
	}
	if hostConfiguration.NetworkMode != container.NetworkMode("bridge") || len(hostConfiguration.PortBindings) != 1 ||
		len(hostConfiguration.Mounts) != 1 || !hostConfiguration.Mounts[0].ReadOnly {
		t.Fatalf("网络、端口或卷挂载未应用: %+v", hostConfiguration)
	}
}

func TestInitialContainerConfigDoesNotRunHostCommandInsideContainer(t *testing.T) {
	input := model.DockerContainerConfig{
		DeploymentScript: "docker run ${EDO_IMAGE}",
		Command:          []string{"legacy", "argument"},
	}
	configuration, _, err := initialContainerConfig("edo.local/app:commit", "target-id", "deployment-id", input)
	if err != nil {
		t.Fatalf("生成部署脚本容器配置失败: %v", err)
	}
	if len(configuration.Entrypoint) != 0 || len(configuration.Cmd) != 0 {
		t.Fatalf("主机侧 Docker 命令不应写入容器 ENTRYPOINT/CMD: %+v", configuration)
	}
}

func TestWaitContainerReadyRequiresStableRunningState(t *testing.T) {
	inspectCount := 0
	err := waitContainerReady(context.Background(), func(context.Context) (client.ContainerInspectResult, error) {
		inspectCount++
		return client.ContainerInspectResult{Container: container.InspectResponse{
			State: &container.State{Status: container.StateRunning, Running: true},
		}}, nil
	}, 5*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("稳定运行的 Docker 容器未通过就绪检查: %v", err)
	}
	if inspectCount < 2 {
		t.Fatalf("Docker 容器只检查了一次，没有验证稳定运行窗口: inspect_count=%d", inspectCount)
	}
}

func TestWaitContainerReadyRejectsStoppedOrRestartedContainer(t *testing.T) {
	tests := []struct {
		name     string
		result   client.ContainerInspectResult
		expected error
	}{
		{
			name: "已经退出",
			result: client.ContainerInspectResult{Container: container.InspectResponse{
				State: &container.State{Status: container.StateExited, ExitCode: 1},
			}},
			expected: ErrContainerNotRunning,
		},
		{
			name: "启动后重启",
			result: client.ContainerInspectResult{Container: container.InspectResponse{
				State: &container.State{Status: container.StateRunning, Running: true}, RestartCount: 1,
			}},
			expected: ErrContainerRestarted,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := waitContainerReady(context.Background(), func(context.Context) (client.ContainerInspectResult, error) {
				return test.result, nil
			}, time.Second, time.Millisecond)
			if !errors.Is(err, test.expected) {
				t.Fatalf("异常 Docker 容器被判定为就绪: err=%v", err)
			}
		})
	}
}

func TestWaitContainerReadyUsesDockerHealthResult(t *testing.T) {
	statuses := []container.HealthStatus{container.Starting, container.Healthy}
	index := 0
	err := waitContainerReady(context.Background(), func(context.Context) (client.ContainerInspectResult, error) {
		status := statuses[index]
		if index < len(statuses)-1 {
			index++
		}
		return client.ContainerInspectResult{Container: container.InspectResponse{
			State: &container.State{
				Status: container.StateRunning, Running: true,
				Health: &container.Health{Status: status},
			},
		}}, nil
	}, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("Docker 健康检查通过后仍未判定容器就绪: %v", err)
	}

	inspectErr := errors.New("inspect failed")
	err = waitContainerReady(context.Background(), func(context.Context) (client.ContainerInspectResult, error) {
		return client.ContainerInspectResult{}, inspectErr
	}, time.Second, time.Millisecond)
	if !errors.Is(err, inspectErr) {
		t.Fatalf("Docker inspect 原始错误未保留给内部诊断: %v", err)
	}
}

func TestDeploymentErrorWithRollbackPreservesPrimaryAndRollbackState(t *testing.T) {
	primaryErr := fmt.Errorf("%w: exit_code=1", ErrContainerNotRunning)
	if err := deploymentErrorWithRollback(primaryErr, nil); !errors.Is(err, ErrContainerNotRunning) || errors.Is(err, ErrContainerRollbackFailed) {
		t.Fatalf("回滚成功后没有保留原始发布分类: %v", err)
	}

	rollbackErr := errors.New("rename failed")
	err := deploymentErrorWithRollback(primaryErr, rollbackErr)
	if !errors.Is(err, ErrContainerRollbackFailed) {
		t.Fatalf("回滚失败没有提升为需要人工处理的分类: %v", err)
	}
	if !strings.Contains(err.Error(), "exit_code=1") || !strings.Contains(err.Error(), "rename failed") {
		t.Fatalf("系统日志需要的发布和回滚诊断信息不完整: %v", err)
	}
}
