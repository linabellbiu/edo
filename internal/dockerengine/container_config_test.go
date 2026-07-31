package dockerengine

import (
	"errors"
	"reflect"
	"testing"

	"zrt/internal/model"
)

func TestNormalizeContainerConfigUsesSafeDefaults(t *testing.T) {
	config, err := NormalizeContainerConfig(model.DockerContainerConfig{
		PortMappings: []model.DockerPortMapping{{HostPort: 8080, ContainerPort: 80}},
	})
	if err != nil {
		t.Fatalf("规范化容器配置失败: %v", err)
	}
	if config.Network != "bridge" || config.RestartPolicy != "unless-stopped" ||
		config.PortMappings[0].HostIP != "127.0.0.1" || config.PortMappings[0].Protocol != "tcp" {
		t.Fatalf("默认配置不符合预期: %+v", config)
	}
	if config.EnvironmentVariables == nil {
		t.Fatal("环境变量映射应规范化为空映射")
	}
}

func TestNormalizeContainerConfigNormalizesHealthCheck(t *testing.T) {
	config, err := NormalizeContainerConfig(model.DockerContainerConfig{
		HealthCheck: model.DockerHealthCheck{Enabled: true, Command: []string{"/bin/check", "--ready"}},
	})
	if err != nil {
		t.Fatalf("规范化健康检查失败: %v", err)
	}
	want := model.DockerHealthCheck{
		Enabled: true, Command: []string{"/bin/check", "--ready"}, IntervalSeconds: 30,
		TimeoutSeconds: 5, Retries: 3, StartPeriodSeconds: 10,
	}
	if !reflect.DeepEqual(config.HealthCheck, want) {
		t.Fatalf("健康检查默认值不正确: got=%+v want=%+v", config.HealthCheck, want)
	}
}

func TestNormalizeContainerConfigDeploymentScriptTakesPriority(t *testing.T) {
	config, err := NormalizeContainerConfig(model.DockerContainerConfig{
		DeploymentScript: "  docker run -it ${ZRT_IMAGE}  ",
		Command:          []string{"legacy", "argument"},
	})
	if err != nil {
		t.Fatalf("规范化部署脚本失败: %v", err)
	}
	if config.DeploymentScript != "docker run -it ${ZRT_IMAGE}" || len(config.Command) != 0 {
		t.Fatalf("部署脚本没有覆盖历史启动参数: %+v", config)
	}
}

func TestNormalizeContainerConfigRejectsHostCommandMixedWithStructuredParameters(t *testing.T) {
	_, err := NormalizeContainerConfig(model.DockerContainerConfig{
		DeploymentScript:     "docker run ${ZRT_IMAGE}",
		EnvironmentVariables: map[string]string{"APP_ENV": "production"},
	})
	if !errors.Is(err, ErrInvalidContainerConfig) {
		t.Fatalf("主机 Docker 命令不应与结构化容器参数同时保存: %v", err)
	}
}

func TestNormalizeContainerConfigRejectsUnsafeOrAmbiguousValues(t *testing.T) {
	tests := []struct {
		name   string
		config model.DockerContainerConfig
	}{
		{name: "主机网络", config: model.DockerContainerConfig{Network: "host"}},
		{name: "任意现有网络", config: model.DockerContainerConfig{Network: "other-app"}},
		{name: "重复容器端口", config: model.DockerContainerConfig{PortMappings: []model.DockerPortMapping{
			{HostPort: 8080, ContainerPort: 80}, {HostPort: 8081, ContainerPort: 80},
		}}},
		{name: "环境变量名称", config: model.DockerContainerConfig{EnvironmentVariables: map[string]string{"BAD-NAME": "value"}}},
		{name: "Docker socket", config: model.DockerContainerConfig{VolumeMounts: []model.DockerVolumeMount{
			{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		}}},
		{name: "主机目录挂载", config: model.DockerContainerConfig{VolumeMounts: []model.DockerVolumeMount{
			{Type: "bind", Source: "/srv/app", Target: "/data"},
		}}},
		{name: "包含换行的启动参数", config: model.DockerContainerConfig{Command: []string{"sh\n-c"}}},
		{name: "部署脚本包含空字节", config: model.DockerContainerConfig{DeploymentScript: "docker run ${ZRT_IMAGE}\x00"}},
		{name: "部署脚本不是 Docker run", config: model.DockerContainerConfig{DeploymentScript: "echo ok"}},
		{name: "部署脚本包含 Shell 串联", config: model.DockerContainerConfig{DeploymentScript: "docker run ${ZRT_IMAGE}; touch /tmp/pwned"}},
		{name: "健康检查超时不小于间隔", config: model.DockerContainerConfig{HealthCheck: model.DockerHealthCheck{
			Enabled: true, Command: []string{"check"}, IntervalSeconds: 5, TimeoutSeconds: 5,
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeContainerConfig(test.config); err == nil {
				t.Fatalf("危险配置未被拒绝: %+v", test.config)
			}
		})
	}
}
