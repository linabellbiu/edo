package deployment

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"

	"edo/internal/dockerengine"
)

func TestComposeRecreatePreservesRepositoryAndConfigurationEnvPaths(t *testing.T) {
	plan := &dockerengine.ComposeContainerControlPlan{Action: "recreate", Service: "api", Containers: []dockerengine.ComposeContainerState{{ID: strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64)}}}
	scope := dockerengine.ComposeProjectScope{ProjectName: "demo", WorkingDirectory: "/srv/app", ConfigurationDirectory: "~/edo/frozen-app/config", ConfigurationParameters: `--env-file .env --env-file "${EDO_APP_CONFIG_PATH}/other.env"`}
	script, err := composeRecreateScript(uuid.NewString(), scope, plan, "services:\n  api:\n    image: alpine:3.22\n")
	if err != nil || !strings.Contains(script, "--env-file '.env'") || !strings.Contains(script, `--env-file "${EDO_APP_CONFIG_PATH:?缺少应用配置目录}/"'other.env'`) || !strings.Contains(script, "edo/frozen-app/config") || !strings.Contains(script, "--force-recreate") {
		t.Fatal("重建改变了已保存的 ENV 路径或没有复用参数", err)
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(script)
	if err := command.Run(); err != nil {
		t.Fatal("重建脚本语法无效", err)
	}
}
