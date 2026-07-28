package dockerengine

import (
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestInitialContainerConfigUsesImageDefaultsAndSafeRestartPolicy(t *testing.T) {
	configuration, hostConfiguration := initialContainerConfig("zrt.local/app:commit", "deployment-id")
	if configuration.Image != "zrt.local/app:commit" || configuration.Cmd != nil || configuration.Entrypoint != nil {
		t.Fatalf("首次部署没有保留镜像默认启动配置: %+v", configuration)
	}
	if configuration.Labels["zrt.managed"] != "true" || configuration.Labels["zrt.deployment.id"] != "deployment-id" {
		t.Fatalf("首次部署缺少 ZRT 管理标签: %+v", configuration.Labels)
	}
	if hostConfiguration.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatalf("首次部署没有配置守护进程重启策略: %+v", hostConfiguration.RestartPolicy)
	}
}
