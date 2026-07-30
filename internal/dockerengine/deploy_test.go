package dockerengine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"

	"zrt/internal/model"
)

func TestInitialContainerConfigUsesImageDefaultsAndSafeRestartPolicy(t *testing.T) {
	configuration, hostConfiguration, err := initialContainerConfig("zrt.local/app:commit", "target-id", "deployment-id", model.DockerContainerConfig{})
	if err != nil {
		t.Fatalf("生成默认容器配置失败: %v", err)
	}
	if configuration.Image != "zrt.local/app:commit" || configuration.Cmd != nil || configuration.Entrypoint != nil {
		t.Fatalf("首次部署没有保留镜像默认启动配置: %+v", configuration)
	}
	if configuration.Labels["zrt.managed"] != "true" || configuration.Labels["zrt.deployment.id"] != "deployment-id" ||
		configuration.Labels["zrt.deployment.target.id"] != "target-id" {
		t.Fatalf("首次部署缺少 ZRT 管理标签: %+v", configuration.Labels)
	}
	if hostConfiguration.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatalf("首次部署没有配置守护进程重启策略: %+v", hostConfiguration.RestartPolicy)
	}
	if hostConfiguration.NetworkMode != container.NetworkMode("bridge") || len(hostConfiguration.PortBindings) != 0 || len(hostConfiguration.Mounts) != 0 {
		t.Fatalf("默认容器配置不够克制: %+v", hostConfiguration)
	}
}

func TestManagedContainerVolumeNameIsScopedByContainerAndLogicalName(t *testing.T) {
	first := managedContainerVolumeName("target-api", "data")
	if first != managedContainerVolumeName("target-api", "data") || first == managedContainerVolumeName("target-worker", "data") ||
		first == managedContainerVolumeName("target-api", "cache") || !strings.HasPrefix(first, "zrt-") {
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
	configuration, hostConfiguration, err := initialContainerConfig("zrt.local/app:commit", "target-id", "deployment-id", input)
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
