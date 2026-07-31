package dockerengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/model"
)

const validComposeYAML = `services:
  api:
    image: ${EDO_IMAGE}
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "test -n \"$${HOSTNAME}\""]
`

func TestValidateComposeYAMLAcceptsManagedUpstreamImage(t *testing.T) {
	if err := ValidateComposeYAML(validComposeYAML, "api"); err != nil {
		t.Fatalf("有效的内联 Compose 配置被拒绝: %v", err)
	}
	withoutImage := "services:\n  api:\n    restart: unless-stopped\n"
	if err := ValidateComposeYAML(withoutImage, "api"); err != nil {
		t.Fatalf("省略目标服务镜像的 Compose 配置被拒绝: %v", err)
	}
	tests := []struct {
		name    string
		yaml    string
		service string
	}{
		{name: "服务不存在", yaml: validComposeYAML, service: "worker"},
		{name: "绕过上游镜像", yaml: "services:\n  api:\n    image: nginx:latest\n", service: "api"},
		{name: "同时构建源码", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    build: .\n", service: "api"},
		{name: "辅助服务构建源码", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n  worker:\n    image: alpine:3.22\n    build: .\n", service: "api"},
		{name: "读取环境文件", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    env_file: .env\n", service: "api"},
		{name: "暴露 Docker API Socket", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    use_api_socket: true\n", service: "api"},
		{name: "未知服务字段", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    future_escape: true\n", service: "api"},
		{name: "通过空环境变量读取宿主机", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    environment:\n      TOKEN:\n", service: "api"},
		{name: "通过列表环境变量读取宿主机", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    environment: [TOKEN]\n", service: "api"},
		{name: "提权容器", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    privileged: true\n", service: "api"},
		{name: "即使关闭提权也不接受该入口", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    privileged: false\n", service: "api"},
		{name: "宿主机网络", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    network_mode: host\n", service: "api"},
		{name: "共享容器网络", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    network_mode: container:daemon\n", service: "api"},
		{name: "禁用网络命名空间", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    network_mode: none\n", service: "api"},
		{name: "共享宿主机 PID", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    pid: host\n", service: "api"},
		{name: "共享宿主机 IPC", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    ipc: host\n", service: "api"},
		{name: "共享宿主机用户命名空间", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    userns_mode: host\n", service: "api"},
		{name: "挂载设备", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    devices: [/dev/kvm:/dev/kvm]\n", service: "api"},
		{name: "修改设备规则", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    device_cgroup_rules: ['c 1:3 mr']\n", service: "api"},
		{name: "增加能力", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    cap_add: [SYS_ADMIN]\n", service: "api"},
		{name: "关闭安全策略", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    security_opt: [seccomp=unconfined]\n", service: "api"},
		{name: "继承其他容器卷", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    volumes_from: [daemon]\n", service: "api"},
		{name: "挂载 Docker Socket", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    volumes: [/var/run/docker.sock:/var/run/docker.sock]\n", service: "api"},
		{name: "挂载绝对宿主机目录", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    volumes: [/etc:/host-etc:ro]\n", service: "api"},
		{name: "挂载相对项目目录", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    volumes: [./data:/data]\n", service: "api"},
		{name: "长格式宿主机挂载", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    volumes:\n      - type: bind\n        source: /etc\n        target: /host-etc\n", service: "api"},
		{name: "命名卷伪装宿主机挂载", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    volumes: [data:/data]\nvolumes:\n  data:\n    driver: local\n    driver_opts:\n      type: none\n      o: bind\n      device: /etc\n", service: "api"},
		{name: "复用主机已有命名卷", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    volumes: [data:/data]\nvolumes:\n  data:\n    external: true\n    name: other-app-data\n", service: "api"},
		{name: "读取其他进程变量", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\n    environment: [TOKEN=${SECRET}]\n", service: "api"},
		{name: "读取外部配置", yaml: "services:\n  api:\n    image: ${EDO_IMAGE}\nconfigs:\n  app:\n    file: ./app.conf\n", service: "api"},
		{name: "通过锚点读取外部密钥", yaml: "x-file: &secret-file\n  file: ./secret.txt\nservices:\n  api:\n    image: ${EDO_IMAGE}\nsecrets:\n  token:\n    <<: *secret-file\n", service: "api"},
		{name: "包含额外文档", yaml: validComposeYAML + "---\nservices: {}\n", service: "api"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateComposeYAML(test.yaml, test.service); err == nil {
				t.Fatal("不安全或不完整的 Compose 配置未被拒绝")
			}
		})
	}
}

func TestComposeYAMLWithManagedImageInjectsUpstreamPlaceholder(t *testing.T) {
	value := "services:\n  api:\n    restart: unless-stopped\n    ports: [8080:8080]\n"
	rendered, err := composeYAMLWithManagedImage(value, "api")
	if err != nil {
		t.Fatalf("注入上游镜像失败: %v", err)
	}
	if !strings.Contains(rendered, "image: ${EDO_IMAGE}") || !strings.Contains(rendered, "restart: unless-stopped") ||
		!strings.Contains(rendered, "8080:8080") {
		t.Fatalf("注入镜像时破坏了 Compose 运行参数: %q", rendered)
	}
	if err := ValidateComposeYAML(rendered, "api"); err != nil {
		t.Fatalf("注入后的 Compose 配置无法再次通过安全校验: %v", err)
	}
}

func TestValidateComposeYAMLAllowsExplicitEnvironmentAndSafeExtensionAnchor(t *testing.T) {
	value := `x-service: &service
  restart: unless-stopped
  environment:
    MODE: production
    EMPTY: ""
services:
  api:
    <<: *service
    image: ${EDO_IMAGE}
    environment: [MODE=production, EMPTY=]
`
	if err := ValidateComposeYAML(value, "api"); err != nil {
		t.Fatalf("显式环境变量和安全扩展锚点被误拒绝: %v", err)
	}
}

func TestValidateComposeYAMLAllowsManagedVolumes(t *testing.T) {
	value := `services:
  api:
    image: ${EDO_IMAGE}
    volumes:
      - data:/var/lib/api
      - /cache
      - type: volume
        source: uploads
        target: /srv/uploads
      - type: tmpfs
        target: /tmp
volumes:
  data: {}
  uploads:
    labels:
      edo.purpose: uploads
`
	if err := ValidateComposeYAML(value, "api"); err != nil {
		t.Fatalf("匿名卷、命名卷或 tmpfs 被误拒绝: %v", err)
	}
}

func TestRunComposeCLIStreamsInlineConfigurationWithFixedArguments(t *testing.T) {
	directory := t.TempDir()
	argumentsFile := filepath.Join(directory, "arguments")
	stdinFile := filepath.Join(directory, "stdin")
	environmentFile := filepath.Join(directory, "environment")
	executable := filepath.Join(directory, "docker")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = compose ] && [ \"$2\" = version ]; then exit 0; fi\nprintf '%%s\\n' \"$@\" > %s\n/bin/cat > %s\n/usr/bin/env > %s\n",
		shellQuote(argumentsFile), shellQuote(stdinFile), shellQuote(environmentFile))
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("DOCKER_HOST", "tcp://must-not-leak.example:2375")
	service := &Service{config: config.Runtime{}}
	input := ComposeDeployInput{
		ServiceName: "api", YAML: validComposeYAML,
		Image: "edo.local/api:fixed", Timeout: 45 * time.Second,
	}
	endpoint := &model.DockerEndpoint{ID: "unix-endpoint", Host: "unix:///var/run/docker.sock", IsActive: true}
	if err := service.runComposeCLI(context.Background(), endpoint, "edo-project", input); err != nil {
		t.Fatalf("执行受控 Compose CLI 失败: %v", err)
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil || string(stdin) != validComposeYAML {
		t.Fatalf("Compose YAML 没有通过标准输入原样传递: value=%q err=%v", stdin, err)
	}
	arguments, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatal(err)
	}
	wantArguments := strings.Join(composeCommandArguments("", "edo-project", "api", 45*time.Second), "\n")
	// 临时工作目录是每次执行独立生成的，只比较它之外的固定参数。
	gotArguments := string(arguments)
	if !strings.HasPrefix(gotArguments, "compose\n--ansi\nnever\n--project-name\nedo-project\n--project-directory\n") ||
		!strings.HasSuffix(gotArguments, "--wait-timeout\n45\napi\n") || strings.Contains(gotArguments, ";") {
		t.Fatalf("Compose CLI 参数不受控: got=%q template=%q", gotArguments, wantArguments)
	}
	environment, err := os.ReadFile(environmentFile)
	if err != nil {
		t.Fatal(err)
	}
	values := string(environment)
	if !strings.Contains(values, "DOCKER_HOST=unix:///var/run/docker.sock\n") ||
		!strings.Contains(values, "EDO_IMAGE=edo.local/api:fixed\n") ||
		strings.Contains(values, "DOCKER_HOST=tcp://must-not-leak.example:2375\n") {
		t.Fatalf("Compose CLI 没有使用目标连接的最小环境: %q", values)
	}
}

func TestComposeCLIUsesConfiguredBuilderMTLSForLocalTarget(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://must-not-leak.example:2375")
	t.Setenv("DOCKER_CERT_PATH", "/must-not-leak")
	service := &Service{config: config.Runtime{
		DockerBuilderHost:        "tcp://docker-builder:2376",
		DockerBuilderTLSCertPath: "/certs/client",
	}}
	environment, cleanup, err := service.composeCLIEnvironment(
		&model.DockerEndpoint{ID: LocalEndpointID, IsActive: true},
		"edo.local/api:fixed",
		RegistryAuth{},
	)
	if err != nil {
		t.Fatalf("生成本地 mTLS Compose 环境失败: %v", err)
	}
	defer cleanup()
	values := environmentValues(environment)
	if values["DOCKER_HOST"] != "tcp://docker-builder:2376" || values["DOCKER_TLS_VERIFY"] != "1" ||
		values["DOCKER_CERT_PATH"] != "/certs/client" {
		t.Fatalf("Compose CLI 没有使用构建运行时 mTLS 配置: %+v", values)
	}
	if strings.Contains(strings.Join(environment, "\n"), "must-not-leak") {
		t.Fatalf("Compose CLI 继承了进程中的其他 Docker 连接: %v", environment)
	}
}

func TestRunComposeWithSSHUsesFixedCommandAndStreamsYAMLAfterSudoPassword(t *testing.T) {
	address, fingerprint, commands, uploads := startDockerSSHImageLoadTestServer(t, "secret")
	bundle := SSHBundle{Password: "secret", UseSudo: true}
	connector, err := newSSHConnector("ssh://deploy@"+address, bundle, fingerprint, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	input := ComposeDeployInput{
		ServiceName: "api", YAML: validComposeYAML,
		Image: "edo.local/api:fixed", Timeout: 45 * time.Second,
		Stdout: &stdout, Stderr: &stderr,
	}
	if err := runComposeWithSSH(context.Background(), connector, bundle, "edo-project", input); err != nil {
		t.Fatalf("通过 SSH 执行受控 Compose 失败: %v", err)
	}
	if command := <-commands; command != "sudo -n -- docker version --format '{{.Server.Version}}'" {
		t.Fatalf("Compose 执行前没有检测免密 sudo: %q", command)
	}
	command := <-commands
	if command != "sudo -S -p '' -- docker compose version --short" {
		t.Fatalf("远程 Compose 没有检查插件可用性: %q", command)
	}
	command = <-commands
	if !strings.HasPrefix(command, "sudo -S -p '' -- env EDO_IMAGE='edo.local/api:fixed' docker 'compose'") ||
		!strings.HasSuffix(command, " '--wait-timeout' '45' 'api'") || strings.Contains(command, "\n") {
		t.Fatalf("远程 Compose 命令不受控: %q", command)
	}
	if uploaded := <-uploads; string(uploaded) != validComposeYAML {
		t.Fatalf("sudo 密码与 Compose YAML 没有正确分离: %q", uploaded)
	}
	if !strings.Contains(stdout.String(), "Compose deployment accepted") {
		t.Fatalf("Compose 输出没有回传流水线日志: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunComposeCLIReportsMissingPlugin(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "docker")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	service := &Service{config: config.Runtime{}}
	err := service.runComposeCLI(context.Background(), &model.DockerEndpoint{
		ID: "unix-endpoint", Host: "unix:///var/run/docker.sock", IsActive: true,
	}, "edo-project", ComposeDeployInput{
		ServiceName: "api", YAML: validComposeYAML, Image: "edo.local/api:fixed", Timeout: 45 * time.Second,
	})
	if !errors.Is(err, ErrComposePluginUnavailable) {
		t.Fatalf("Compose 插件缺失没有返回稳定错误: %v", err)
	}
}

func TestRunComposeWithSSHReportsMissingPlugin(t *testing.T) {
	address, fingerprint, _, _ := startDockerSSHTestServerWithCapabilities(t, "secret", false, false)
	bundle := SSHBundle{Password: "secret"}
	connector, err := newSSHConnector("ssh://deploy@"+address, bundle, fingerprint, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = runComposeWithSSH(context.Background(), connector, bundle, "edo-project", ComposeDeployInput{
		ServiceName: "api", YAML: validComposeYAML, Image: "edo.local/api:fixed", Timeout: 45 * time.Second,
	})
	if !errors.Is(err, ErrComposePluginUnavailable) {
		t.Fatalf("远程 Compose 插件缺失没有返回稳定错误: %v", err)
	}
}

func TestComposeProjectNameDoesNotExposeTargetIdentifier(t *testing.T) {
	first := composeProjectName("target/customer-production")
	second := composeProjectName("target/customer-production")
	if first != second || !strings.HasPrefix(first, "edo-") || strings.Contains(first, "customer") {
		t.Fatalf("Compose 项目名不稳定或泄露内部标识: %q %q", first, second)
	}
}
