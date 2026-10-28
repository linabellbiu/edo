package k3s

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type probeShellFixture struct {
	root, commands string
}

func newProbeShellFixture(t *testing.T, missing string) probeShellFixture {
	t.Helper()
	fixture := probeShellFixture{root: t.TempDir()}
	fixture.commands = filepath.Join(fixture.root, "commands")
	if err := os.Mkdir(fixture.commands, 0700); err != nil {
		t.Fatal(err)
	}
	writeLinuxToolFixtures(t, fixture.commands)
	for _, tool := range []string{"awk", "base64", "tr", "head", "timeout", "sha256sum", "ln", "install", "mktemp", "env"} {
		target := filepath.Join(fixture.commands, tool)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		binary, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("测试所需工具不可用 %s: %v", tool, err)
		}
		if err := os.Symlink(binary, target); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"uname":     "if [ \"${1:-}\" = -s ]; then printf Linux; else printf x86_64; fi\n",
		"id":        "printf 0\n",
		"hostname":  "printf worker-a\n",
		"getconf":   "printf 8\n",
		"df":        "printf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 99999999 0 99999999 0%% /\\n'\n",
		"ss":        "exit \"${EDO_TEST_SS_STATUS:-0}\"\n",
		"curl":      "case \"$*\" in *api.example.test*) exit \"${EDO_TEST_ENDPOINT_STATUS:-0}\" ;; esac\nexit 0\n",
		"ps":        "exit 0\n",
		"docker":    "exit 9\n",
		"ip":        "printf '2: eth0 inet 192.0.2.10/24 scope global eth0\\n'\n",
		"systemctl": "exit 0\n",
		"sh":        "printf 'addresses\\t192.0.2.10\\nmachine_id\\t0123456789abcdef0123456789abcdef\\nsystem_uuid\\t\\nboot_id\\t\\n'\n",
	} {
		if name != missing {
			writePreflightCommand(t, fixture.commands, name, body)
		}
	}
	for _, directory := range []string{"run/systemd/system", "sys/fs/cgroup", "proc"} {
		if err := os.MkdirAll(filepath.Join(fixture.root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"sys/fs/cgroup/cgroup.controllers": "cpu memory\n", "proc/meminfo": "MemTotal: 8388608 kB\n"} {
		if err := os.WriteFile(filepath.Join(fixture.root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (fixture probeShellFixture) run(t *testing.T, script string, extraEnv ...string) (string, error) {
	t.Helper()
	// 所有固定主机路径重定向到本轮夹具，绝不读取本机 Kubernetes 或身份文件。
	rewrite := strings.NewReplacer(
		"edo_path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "edo_path="+shellQuote(fixture.commands),
		"/etc/", fixture.root+"/etc/", "/var/", fixture.root+"/var/", "/run/", fixture.root+"/run/",
		"/sys/", fixture.root+"/sys/", "/proc/", fixture.root+"/proc/", "/usr/local/bin/", fixture.root+"/bin/",
		"/sbin/openrc-run", fixture.root+"/sbin/openrc-run",
	)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-se")
	command.Stdin = strings.NewReader(rewrite.Replace(script))
	command.Env = append(os.Environ(), append([]string{"PATH=" + fixture.commands}, extraEnv...)...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestNativePreflightReportSurvivesTransportErrexit(t *testing.T) {
	for _, scenario := range []struct {
		name, missing, check string
		environment          []string
		existing             bool
	}{
		{name: "已有安装", check: "existing_installation", existing: true},
		{name: "缺少基础命令", missing: "ss", check: "tools"},
		{name: "监听端口读取失败", check: "ports", environment: []string{"EDO_TEST_SS_STATUS=9"}},
		{name: "集群证书检查失败", check: "endpoint", environment: []string{"EDO_TEST_ENDPOINT_STATUS=60"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newProbeShellFixture(t, scenario.missing)
			if scenario.existing {
				if err := os.MkdirAll(filepath.Join(fixture.root, "etc/rancher/k3s"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			input := Input{Endpoint: "https://api.example.test:6443", Installer: map[string]any{"INSTALL_K3S_VERSION": CatalogK3sVersion}}
			host := Host{ID: "fixture-host", Role: "agent"}
			output, err := fixture.run(t, preflightProbeScript(host, input, true), scenario.environment...)
			if err != nil {
				t.Fatalf("检查项失败不得被 sh -se 误判为 SSH 失败: %v\n%s", err, output)
			}
			report := parsePreflightProbe(host, output, nil)
			if report.Passed || preflightStatus(report, scenario.check) != "failed" || len(report.Checks) != len(hostPreflightDefinitions)+1 || preflightStatus(report, "report") != "" || preflightStatus(report, "connection") != "" {
				t.Fatalf("未保留完整且失败闭合的主机报告: %+v", report.Checks)
			}
			if !errors.Is(requirePreflight(&PreflightReport{Hosts: []HostPreflight{report}}), ErrPreflightFailed) {
				t.Fatal("有阻断项的报告不得允许安装")
			}
		})
	}
}

func TestK3dPreflightReportSurvivesTransportErrexit(t *testing.T) {
	fixture := newProbeShellFixture(t, "docker")
	agents := 0
	input := Input{Mode: "k3d", K3d: &K3dOptions{Agents: &agents, APIPort: 16443, BindAddress: "192.0.2.10"}}
	output, err := fixture.run(t, k3dPreflightScript(input))
	if err != nil {
		t.Fatalf("Docker 检查失败不得被 sh -se 误判为 SSH 失败: %v\n%s", err, output)
	}
	report := parseHostPreflight(Host{ID: "fixture-host", Role: "host"}, output, nil, k3dPreflightDefinitions)
	if report.Passed || preflightStatus(report, "tools") != "failed" || preflightStatus(report, "docker") != "failed" || len(report.Checks) != len(k3dPreflightDefinitions)+1 || preflightStatus(report, "report") != "" || preflightStatus(report, "connection") != "" {
		t.Fatalf("k3d 缺少完整且失败闭合的预检报告: %+v", report.Checks)
	}
}

func TestNativeInstallationGuardStillFailsClosedWithExistingInstallation(t *testing.T) {
	fixture := newProbeShellFixture(t, "")
	if err := os.MkdirAll(filepath.Join(fixture.root, "etc/rancher/k3s"), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := fixture.run(t, preflightScript(nil, nil)+"printf 'must-not-continue\\n'\n")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 || strings.Contains(output, "must-not-continue") {
		t.Fatalf("实际安装保护必须保留退出 42 且不继续: err=%v output=%q", err, output)
	}
}
