package dockerengine

import (
	"slices"
	"strings"
	"testing"
)

func TestDockerRunCommandArgumentsInjectsManagedRuntimeValues(t *testing.T) {
	arguments, err := dockerRunCommandArguments(
		"docker run -it --name ${EDO_CONTAINER_NAME} -e APP_ENV=production ${EDO_IMAGE}",
		"sha256:"+strings.Repeat("a", 64), "registry.example.com/team/app:run-1",
		"order_api-a1b2c3d4", "target-1", "deployment-1",
	)
	if err != nil {
		t.Fatalf("生成 Docker 主机命令失败: %v", err)
	}
	checks := [][]string{
		{"--init"},
		{"--name", "order_api-a1b2c3d4"},
		{"--network", "bridge"},
		{"--label", "edo.managed=true"},
		{"--label", "edo.deployment.target.id=target-1"},
		{"sha256:" + strings.Repeat("a", 64)},
	}
	for _, expected := range checks {
		if !containsArgumentSequence(arguments, expected) {
			t.Fatalf("Docker 主机命令缺少受控参数 %v: %v", expected, arguments)
		}
	}
	if !slices.Contains(arguments, "--detach") {
		t.Fatalf("Docker 主机命令没有强制使用后台运行: %v", arguments)
	}
}

func TestDockerRunCommandPreservesExplicitInit(t *testing.T) {
	for _, setting := range []string{"--init", "--init=true", "--init=false"} {
		args, err := dockerRunCommandArguments("docker run "+setting+" ${EDO_IMAGE}", "image", "", "app", "target", "deployment")
		if err != nil {
			t.Fatal(err)
		}
		var count int
		for _, arg := range args {
			if arg == "--init" || strings.HasPrefix(arg, "--init=") {
				count++
				if arg != setting {
					t.Fatal("显式 init 参数被覆盖")
				}
			}
		}
		if count != 1 {
			t.Fatal("init 参数缺失或重复")
		}
	}
}

func TestParseDockerRunTemplateRejectsShellAndUnsafeHostOptions(t *testing.T) {
	invalid := []string{
		"docker run ${EDO_IMAGE} && touch /tmp/pwned",
		"docker run --privileged ${EDO_IMAGE}",
		"docker run -v /:/host ${EDO_IMAGE}",
		"docker run --name fixed ${EDO_IMAGE}",
		"docker run example.com/team/app:latest",
		"sh -c 'docker run ${EDO_IMAGE}'",
	}
	for _, command := range invalid {
		if _, err := parseDockerRunTemplate(command); err == nil {
			t.Fatalf("不安全的 Docker 主机命令未被拒绝: %s", command)
		}
	}
}

func containsArgumentSequence(arguments, expected []string) bool {
	if len(expected) == 0 || len(expected) > len(arguments) {
		return false
	}
	for index := 0; index <= len(arguments)-len(expected); index++ {
		if slices.Equal(arguments[index:index+len(expected)], expected) {
			return true
		}
	}
	return false
}
