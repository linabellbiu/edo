package k3s

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightCNIPortsUsesActualBackendAndKeepsUDPUnverified(t *testing.T) {
	for _, scenario := range []struct {
		name, backend, listener, action, want string
		cidrs                                 []string
		status                                string
	}{
		{name: "默认 vxlan 无占用", want: "passed"},
		{name: "vxlan 已占用", listener: "UNCONN 0 0 0.0.0.0:8472 0.0.0.0:*", want: "failed"},
		{name: "WireGuard 不误判 vxlan", backend: "wireguard-native", listener: "UNCONN 0 0 0.0.0.0:8472 0.0.0.0:*", want: "passed"},
		{name: "WireGuard IPv4 占用", backend: "wireguard-native", listener: "UNCONN 0 0 0.0.0.0:51820 0.0.0.0:*", want: "failed"},
		{name: "WireGuard 双栈 IPv6 占用", backend: "wireguard-native", cidrs: []string{"10.42.0.0/16", "fd00:42::/56"}, listener: "UNCONN 0 0 [::]:51821 [::]:*", want: "failed"},
		{name: "读取失败", status: "9", want: "failed"},
		{name: "加入沿用未知配置", action: "join", want: "warning"},
		{name: "自定义 CNI", backend: "none", want: "warning"},
		{name: "无 UDP 隧道", backend: "host-gw", want: "warning"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			input := Input{Action: scenario.action, ServerOptions: map[string]any{"flannel-backend": scenario.backend, "cluster-cidr": scenario.cidrs}}
			report := runPreflightShell(t, input, Host{Role: "server"}, "EDO_TEST_UDP_LISTENERS="+scenario.listener, "EDO_TEST_UDP_STATUS="+scenario.status)
			if got := preflightStatus(report, "cni_ports"); got != scenario.want {
				t.Fatalf("CNI 端口检查=%s，期望 %s", got, scenario.want)
			}
		})
	}
}

func TestPreflightKernelOnlyBlocksExplicitlyDisabledFeature(t *testing.T) {
	for _, scenario := range []struct {
		name, configured, filesystem, want string
		loaded, available                  bool
	}{
		{name: "已加载", loaded: true, want: "passed"},
		{name: "已注册文件系统", filesystem: "nodev\toverlay\n", want: "passed"},
		{name: "模块可用", available: true, want: "passed"},
		{name: "配置明确禁用", configured: "# CONFIG_OVERLAY_FS is not set\n", want: "failed"},
		{name: "配置内建但尚未注册", configured: "CONFIG_OVERLAY_FS=y\n", want: "warning"},
		{name: "没有证明", want: "warning"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newProbeShellFixture(t, "")
			if scenario.loaded {
				if err := os.MkdirAll(filepath.Join(fixture.root, "sys/module/overlay"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(fixture.root, "boot"), 0700); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{"boot/config-x86_64": scenario.configured, "proc/filesystems": scenario.filesystem} {
				if err := os.WriteFile(filepath.Join(fixture.root, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.available {
				writePreflightCommand(t, fixture.commands, "modinfo", "exit 0\n")
			}
			script := "edo_check() { printf '%s:%s\\n' \"$1\" \"$2\"; }\n" + modulePreflightScript("overlay_kernel", "overlay", "CONFIG_OVERLAY_FS", true)
			output, err := fixture.run(t, script)
			if err != nil || output != "overlay_kernel:"+scenario.want+"\n" {
				t.Fatalf("内核能力检测错误: %q, %v", output, err)
			}
			if strings.Contains(script, "modprobe") || strings.Contains(script, "mount ") {
				t.Fatal("只读检测不得加载模块或试挂载")
			}
		})
	}
}

func TestPreflightSupervisorRequiresReachableManager(t *testing.T) {
	for _, scenario := range []struct{ name, version, status, want string }{
		{"systemd 可响应", "255", "0", "passed"},
		{"只有 systemctl 二进制", "", "1", "failed"},
		{"空管理器响应", "", "0", "failed"},
		{"管理器超时", "", "124", "failed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newProbeShellFixture(t, "")
			host := Host{Role: "agent"}
			output, err := fixture.run(t, preflightProbeScript(host, Input{}, false), "EDO_TEST_MANAGER_VERSION="+scenario.version, "EDO_TEST_MANAGER_STATUS="+scenario.status)
			if err != nil || preflightStatus(parsePreflightProbe(host, output, nil), "supervisor") != scenario.want {
				t.Fatalf("服务管理器检查错误: %v, %s", err, output)
			}
		})
	}
}

func TestPreflightSupervisorKeepsRunningOpenRCSupported(t *testing.T) {
	fixture := newProbeShellFixture(t, "")
	for _, directory := range []string{"sbin", "run/openrc"} {
		if err := os.MkdirAll(filepath.Join(fixture.root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "sbin/openrc-run"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "run/openrc/softlevel"), []byte("default\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writePreflightCommand(t, fixture.commands, "rc-status", "printf default\nexit \"${EDO_TEST_OPENRC_STATUS:-0}\"\n")
	host := Host{Role: "agent"}
	for _, scenario := range []struct{ status, want string }{{"0", "passed"}, {"1", "failed"}} {
		output, err := fixture.run(t, preflightProbeScript(host, Input{}, false), "EDO_TEST_MANAGER_STATUS=1", "EDO_TEST_OPENRC_STATUS="+scenario.status)
		if err != nil || preflightStatus(parsePreflightProbe(host, output, nil), "supervisor") != scenario.want {
			t.Fatalf("OpenRC 可用性检查错误: %v, %s", err, output)
		}
	}
}

func TestPreflightSelectedEtcdAndEmbeddedRegistryLocalPorts(t *testing.T) {
	for _, port := range []int{2379, 2380, 2381, 2382, 5001, 6443, 6444, 10010, 10248, 10249, 10250, 10256, 10257, 10258, 10259} {
		ports, _ := preflightPorts(Host{Role: "server"}, map[string]any{"cluster-init": true, "embedded-registry": true})
		found := false
		for _, current := range ports {
			found = found || current == port
		}
		if !found {
			t.Fatalf("未检查已选功能所需本地 TCP 端口 %d", port)
		}
	}
	input := Input{Action: "create", ServerOptions: map[string]any{"embedded-registry": true}}
	report := runPreflightShell(t, input, Host{Role: "agent"}, "EDO_TEST_LISTENERS=LISTEN 0 128 0.0.0.0:5001 0.0.0.0:*")
	if preflightStatus(report, "ports") != "failed" {
		t.Fatal("创建集群时未将系统镜像分发端口计入工作节点")
	}
}

func TestK3dInstallationLockDoesNotClaimTaskIsStillRunning(t *testing.T) {
	output := strings.Replace(k3dPreflightFixture(), "EDO_K3S_CHECK\tinstallation_lock\tpassed", "EDO_K3S_CHECK\tinstallation_lock\tfailed", 1)
	report := parseHostPreflight(Host{}, output, nil, k3dPreflightDefinitions)
	if report.Passed {
		t.Fatal("安装锁未阻断")
	}
	for _, item := range report.Checks {
		if item.ID == "installation_lock" && (!strings.Contains(item.Message, "残留") || strings.Contains(item.Message, "正在执行")) {
			t.Fatal("残留锁不得冒充活跃安装任务")
		}
	}
}

func TestNativeGuardDoesNotTreatFailedProcessProbeAsEmpty(t *testing.T) {
	for _, scenario := range []struct {
		status, processes string
		code              int
	}{
		{status: "0", code: 0}, {status: "9", code: 44}, {status: "124", code: 44},
		{status: "0", processes: "k3s-server\n", code: 42},
	} {
		fixture := newProbeShellFixture(t, "")
		output, err := fixture.run(t, preflightScript(nil, nil), "EDO_TEST_PS_STATUS="+scenario.status, "EDO_TEST_PROCESSES="+scenario.processes)
		if scenario.code == 0 {
			if err != nil || !strings.Contains(output, "EDO_K3S_PREFLIGHT_OK") {
				t.Fatalf("成功空进程列表被拒绝: %q, %v", output, err)
			}
			continue
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != scenario.code || strings.Contains(output, "EDO_K3S_PREFLIGHT_OK") {
			t.Fatalf("进程失败或已有进程未阻断: %q, %v", output, err)
		}
	}
}

func TestK3dRuncRequirementDoesNotConfuseUnknownWithSupported(t *testing.T) {
	for _, scenario := range []struct{ version, status, want string }{
		{"1.0.0-rc92", "0", "failed"}, {"v1.0.0-rc93", "0", "passed"}, {"1.0.0-rc100", "0", "passed"},
		{"0.1.1", "0", "failed"}, {"1.0.0", "0", "passed"}, {"v1.3.2", "0", "passed"},
		{"", "0", "warning"}, {"private-daemon-output", "0", "warning"}, {"1.3.2\n0.0.1", "0", "warning"}, {"1.3.2", "9", "warning"},
	} {
		t.Run(scenario.version+scenario.status, func(t *testing.T) {
			fixture := newProbeShellFixture(t, "")
			script := "set +e\nedo_check() { printf '%s:%s\\n' \"$1\" \"$2\"; }\nedo_path=\"$PATH\"\nedo_docker_binary=$(command -v docker)\n" + k3dRuncPreflightScript
			// Docker 调用使用 env -i；夹具以固定文本替代环境读取，不依赖宿主环境。
			writePreflightCommand(t, fixture.commands, "docker", "printf '%s' "+shellQuote(scenario.version)+"\nexit "+scenario.status+"\n")
			output, err := fixture.run(t, script)
			if err != nil || output != "runc:"+scenario.want+"\ncomponent_images:warning\n" {
				t.Fatalf("runc 要求检查错误: %q, %v", output, err)
			}
		})
	}
}

func TestPreflightDownloadChecksPinnedArchitectureBinaryNotOnlyChecksum(t *testing.T) {
	for _, scenario := range []struct{ arch, asset string }{{"x86_64", "k3s"}, {"aarch64", "k3s-arm64"}, {"armv7l", "k3s-armhf"}} {
		t.Run(scenario.arch, func(t *testing.T) {
			input := Input{Installer: map[string]any{"INSTALL_K3S_VERSION": CatalogK3sVersion}}
			host := Host{Role: "agent"}
			report := runPreflightShell(t, input, host, "EDO_TEST_ARCH="+scenario.arch, "EDO_TEST_EXPECTED_ASSET="+scenario.asset)
			if preflightStatus(report, "download") != "passed" {
				t.Fatal("未检查当前架构对应的发布文件")
			}
			report = runPreflightShell(t, input, host, "EDO_TEST_ARCH="+scenario.arch, "EDO_TEST_EXPECTED_ASSET="+scenario.asset, "EDO_TEST_BINARY_STATUS=22")
			if preflightStatus(report, "download") != "failed" {
				t.Fatal("校验文件可达但安装包不可达时未阻断")
			}
		})
	}
}

func TestPreflightDownloadChannelDoesNotClaimExactBinaryWasChecked(t *testing.T) {
	input := Input{Installer: map[string]any{"INSTALL_K3S_CHANNEL": "stable"}}
	report := runPreflightShell(t, input, Host{Role: "agent"}, "EDO_TEST_BINARY_STATUS=22")
	if preflightStatus(report, "download") != "warning" {
		t.Fatal("发布通道端点可达不能冒充已验证精确版本的架构安装包")
	}
	for _, item := range report.Checks {
		if item.ID == "download" && !strings.Contains(item.Message, "尚未验证精确版本") {
			t.Fatal("通道检测必须明确说明尚未验证安装包")
		}
	}
}

func TestNativeDiskRejectsPartialOutputOnProbeFailure(t *testing.T) {
	for _, status := range []string{"9", "124"} {
		report := runPreflightShell(t, Input{}, Host{Role: "server"}, "EDO_TEST_DF_STATUS="+status)
		if preflightStatus(report, "disk") != "failed" {
			t.Fatal("df 返回合法的部分输出但失败时，不得判定磁盘检查通过")
		}
	}
}

func TestNativeCPURejectsPartialValueWithoutNewlineOnFailure(t *testing.T) {
	report := runPreflightShell(t, Input{}, Host{Role: "server"}, "EDO_TEST_CPU=8", "EDO_TEST_CPU_STATUS=124")
	if preflightStatus(report, "cpu") != "failed" {
		t.Fatal("getconf 输出无换行的 8 后超时，不得拼接为 80 或判定已满足 CPU 要求")
	}
}

func TestK3dRejectsPartialOutputOnReadOnlyProbeFailure(t *testing.T) {
	for _, scenario := range []struct {
		name, check                      string
		engine, info, address, disk, cpu int
	}{
		{name: "基线"},
		{name: "Docker 部分版本后失败", check: "docker", engine: 124},
		{name: "Docker 部分目录后失败", check: "disk", info: 124},
		{name: "网卡部分列表后失败", check: "bind_address", address: 124},
		{name: "磁盘部分信息后失败", check: "disk", disk: 124},
		{name: "CPU 无换行部分数值后失败", check: "cpu", cpu: 124},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newProbeShellFixture(t, "")
			// 使用短临时路径创建受控 Unix Socket；不会连接任何 Docker daemon。
			socketDirectory, err := os.MkdirTemp("", "edo-probe-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
			socketPath := filepath.Join(socketDirectory, "docker.sock")
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			if err := os.MkdirAll(filepath.Join(fixture.root, "var/run"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(socketPath, filepath.Join(fixture.root, "var/run/docker.sock")); err != nil {
				t.Fatal(err)
			}
			writePreflightCommand(t, fixture.commands, "docker", fmt.Sprintf(`case "$*" in
  *Components*) printf '1.3.2'; exit 0 ;;
  *version*) printf 'linux 24.0.9'; exit %d ;;
  *info*) printf '%%s' %s; exit %d ;;
esac
exit 9
`, scenario.engine, shellQuote(fixture.root), scenario.info))
			writePreflightCommand(t, fixture.commands, "ip", fmt.Sprintf("printf '2: eth0 inet 192.0.2.10/24 scope global eth0\\n'\nexit %d\n", scenario.address))
			writePreflightCommand(t, fixture.commands, "df", fmt.Sprintf("printf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 99999999 0 99999999 0%%%% /\\n'\nexit %d\n", scenario.disk))
			writePreflightCommand(t, fixture.commands, "getconf", fmt.Sprintf("printf 8\nexit %d\n", scenario.cpu))
			agents := 0
			input := Input{Mode: "k3d", K3d: &K3dOptions{Agents: &agents, APIPort: 16443, BindAddress: "192.0.2.10"}}
			output, err := fixture.run(t, k3dPreflightScript(input))
			if err != nil {
				t.Fatalf("失败探针仍应返回完整报告: %v", err)
			}
			report := parseHostPreflight(Host{Role: "host"}, output, nil, k3dPreflightDefinitions)
			for _, id := range []string{"docker", "bind_address", "disk", "cpu"} {
				want := "passed"
				if id == scenario.check {
					want = "failed"
				}
				if got := preflightStatus(report, id); got != want {
					t.Fatalf("部分输出不应隐藏命令错误 %s=%s，期望 %s；checks=%+v", id, got, want, report.Checks)
				}
			}
		})
	}
}
