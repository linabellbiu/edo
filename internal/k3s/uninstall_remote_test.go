//go:build linux || darwin

package k3s

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type uninstallShellFixture struct {
	root, commands string
	summary        UninstallSummary
	rewrite        *strings.Replacer
}

const uninstallStatFixture = `for item do last=$item; done
case "$2" in
'%u') printf '0\n';;
'%a') printf '700\n';;
'%h') printf '1\n';;
'%s') printf '100\n';;
'%d:%i:%u:%g:%a:%h:%s:%Y:%Z') printf '1:1:0:0:700:1:100:1:1\n';;
*) exit 2;;
esac
`

func newUninstallShellFixture(t *testing.T) uninstallShellFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	commands := filepath.Join(root, "commands")
	for _, directory := range []string{commands, root + "/usr/local/bin", root + "/etc/systemd/system", root + "/etc/rancher/k3s", root + "/var/lib/rancher/k3s", root + "/run", root + "/etc/init.d"} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fixture := uninstallShellFixture{
		root: root, commands: commands,
		summary: UninstallSummary{SourceInstallationID: "source-fixture", HostID: "host-fixture", Role: "server", ServiceName: "k3s", ScriptPath: "/usr/local/bin/k3s-uninstall.sh", DataDir: "/var/lib/rancher/k3s"},
		rewrite: strings.NewReplacer("/usr/local/bin", root+"/usr/local/bin", "/usr/lib", root+"/usr/lib", "/etc", root+"/etc", "/var/lib", root+"/var/lib", "/var/run", root+"/var/run", "/var/snap", root+"/var/snap", "/run", root+"/run"),
	}
	// 无害脚本只记录调用；测试不运行真正的官方卸载，也不删除主机资源。
	fixture.write(t, "/usr/local/bin/k3s-uninstall.sh", "#!/bin/sh\nprintf invoked > "+shellQuote(root+"/official-invoked")+"\nprintf '%s' \"${K3S_DATA_DIR-unset}\" > "+shellQuote(root+"/received-data-dir")+"\nprintf '%s\\n' \"$@\" > "+shellQuote(root+"/received-arguments")+"\n")
	fixture.write(t, "/usr/local/bin/k3s-killall.sh", "#!/bin/sh\n# 卸载脚本引用的同机官方辅助脚本夹具\n")
	writePreflightCommand(t, commands, "id", "printf '0\\n'\n")
	writePreflightCommand(t, commands, "stat", uninstallStatFixture)
	writePreflightCommand(t, commands, "readlink", `for item do result=$item; done; printf '%s\n' "$result"`)
	writeLinuxToolFixtures(t, commands)
	return fixture
}

func (f uninstallShellFixture) write(t *testing.T, name, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.root+name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.root+name, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f uninstallShellFixture) run(t *testing.T, script string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-se")
	command.Stdin = strings.NewReader(f.rewrite.Replace(script))
	command.Env = []string{"PATH=" + f.commands + ":/usr/bin:/bin:/usr/sbin:/sbin", "GORACE=atexit_sleep_ms=0"}
	output, err := command.CombinedOutput()
	return string(output), err
}

func (f uninstallShellFixture) probe(t *testing.T) uninstallProof {
	t.Helper()
	output, err := f.run(t, uninstallProbeScript(f.summary))
	if err != nil {
		t.Fatalf("读取脚本身份失败: %v %s", err, output)
	}
	proof, err := parseUninstallProof(output)
	if err != nil {
		t.Fatalf("脚本身份协议无效: %v %s", err, output)
	}
	return proof
}

func TestUninstallShellReadOnlyGuardAndExactFrozenOfficialExecution(t *testing.T) {
	f := newUninstallShellFixture(t)
	proof := f.probe(t)
	if _, err := os.Stat(f.root + "/official-invoked"); !os.IsNotExist(err) {
		t.Fatal("只读预览执行了卸载脚本")
	}
	output, err := f.run(t, uninstallExecutionScript(savedUninstall{Summary: f.summary, Proof: proof}))
	if err != nil {
		t.Fatalf("调用已冻结的目标脚本失败: %v %s", err, output)
	}
	if value, err := os.ReadFile(f.root + "/official-invoked"); err != nil || string(value) != "invoked" {
		t.Fatal("没有执行准确目标脚本")
	}
	if value, err := os.ReadFile(f.root + "/received-data-dir"); err != nil || string(value) != "unset" {
		t.Fatal("EDO 向官方脚本注入了旧安装计划的数据目录")
	}
	if value, err := os.ReadFile(f.root + "/received-arguments"); err != nil || strings.TrimSpace(string(value)) != "" {
		t.Fatal("EDO 向官方脚本注入了额外参数")
	}
}

func TestUninstallShellLeavesMountsNetworkServicesAndConfigurationToOfficialScript(t *testing.T) {
	f := newUninstallShellFixture(t)
	// 模拟用户安装后维护的配置与其他服务。EDO 不读取其状态，不执行辅助清理。
	for name, value := range map[string]string{
		"/etc/rancher/k3s/config.yaml":                                          "data-dir: /user-managed/data\nnode-label: []\n",
		"/etc/rancher/k3s/config.yaml.d/99-storage.yaml":                        "snapshotter: native\n",
		"/etc/systemd/system/k3s.service":                                       "用户维护的系统服务\n",
		"/etc/systemd/system/k3s.service.env":                                   "HTTPS_PROXY=http://user-managed-proxy\n",
		"/etc/systemd/system/k3s.service.d/override.conf":                       "用户维护的服务参数\n",
		"/etc/default/k3s":                                                      "用户维护的环境\n",
		"/etc/init.d/k3s":                                                       "OpenRC 官方安装脚本生成的服务\n",
		"/var/lib/cni/state":                                                    "已有容器网络状态\n",
		"/var/lib/kubelet/pods/pod/volumes/kubernetes.io~empty-dir/cache/state": "Pod 临时卷\n",
	} {
		f.write(t, name, value)
	}
	for _, tool := range []string{"ps", "findmnt", "systemctl", "rc-service", "rc-update", "docker", "tailscale", "ip", "iptables", "iptables-save", "ip6tables-save", "mount", "umount", "rm", "kill"} {
		writePreflightCommand(t, f.commands, tool, "printf '%s\\n' "+shellQuote(tool)+" >> "+shellQuote(f.root+"/unexpected-tool")+"\nexit 97\n")
	}
	proof := f.probe(t)
	if output, err := f.run(t, uninstallExecutionScript(savedUninstall{Summary: f.summary, Proof: proof})); err != nil {
		t.Fatalf("正常运行状态仍被 EDO 自制检查阻断: %v %s", err, output)
	}
	if _, err := os.Stat(f.root + "/unexpected-tool"); !os.IsNotExist(err) {
		t.Fatal("EDO 额外调用了挂载、网络、服务或清理工具")
	}
	if value, err := os.ReadFile(f.root + "/etc/rancher/k3s/config.yaml"); err != nil || string(value) != "data-dir: /user-managed/data\nnode-label: []\n" {
		t.Fatal("EDO 改写了用户维护的 K3s 配置")
	}
	if _, err := os.Stat(f.root + "/official-invoked"); err != nil {
		t.Fatal("没有将操作交给目标主机的卸载脚本")
	}
}

func TestUninstallShellBlocksInvalidIdentityLocksAndReplacedScripts(t *testing.T) {
	for _, scenario := range []string{"non-root", "stat-error", "hash-error", "installer-lock", "dangling-lock", "script-symlink", "killall-symlink", "missing-script", "missing-killall", "non-root-file", "writable-file", "multiple-links", "empty-script", "oversized-script", "replaced-script", "replaced-killall", "changed-proof"} {
		t.Run(scenario, func(t *testing.T) {
			f := newUninstallShellFixture(t)
			proof := f.probe(t)
			switch scenario {
			case "non-root":
				writePreflightCommand(t, f.commands, "id", "printf '1000\\n'\n")
			case "stat-error":
				writePreflightCommand(t, f.commands, "stat", "exit 2\n")
			case "hash-error":
				writePreflightCommand(t, f.commands, "sha256sum", "exit 2\n")
			case "installer-lock":
				if err := os.Mkdir(f.root+"/run/edo-k3s-install.lock", 0700); err != nil {
					t.Fatal(err)
				}
			case "dangling-lock":
				if err := os.Symlink(f.root+"/absent", f.root+"/run/edo-k3s-install.lock"); err != nil {
					t.Fatal(err)
				}
			case "script-symlink", "killall-symlink":
				name := "/usr/local/bin/k3s-uninstall.sh"
				if scenario == "killall-symlink" {
					name = "/usr/local/bin/k3s-killall.sh"
				}
				if err := os.Rename(f.root+name, f.root+"/real-script"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.root+"/real-script", f.root+name); err != nil {
					t.Fatal(err)
				}
			case "missing-script", "missing-killall":
				name := "/usr/local/bin/k3s-uninstall.sh"
				if scenario == "missing-killall" {
					name = "/usr/local/bin/k3s-killall.sh"
				}
				if err := os.Remove(f.root + name); err != nil {
					t.Fatal(err)
				}
			case "non-root-file":
				writePreflightCommand(t, f.commands, "stat", strings.ReplaceAll(uninstallStatFixture, "printf '0\\n'", "printf '1000\\n'"))
			case "writable-file":
				writePreflightCommand(t, f.commands, "stat", strings.ReplaceAll(uninstallStatFixture, "printf '700\\n'", "printf '777\\n'"))
			case "multiple-links":
				writePreflightCommand(t, f.commands, "stat", strings.ReplaceAll(uninstallStatFixture, "'%h') printf '1\\n'", "'%h') printf '2\\n'"))
			case "empty-script":
				writePreflightCommand(t, f.commands, "stat", strings.ReplaceAll(uninstallStatFixture, "'%s') printf '100\\n'", "'%s') printf '0\\n'"))
			case "oversized-script":
				writePreflightCommand(t, f.commands, "stat", strings.ReplaceAll(uninstallStatFixture, "'%s') printf '100\\n'", "'%s') printf '1048577\\n'"))
			case "replaced-script", "replaced-killall":
				name := "/usr/local/bin/k3s-uninstall.sh"
				if scenario == "replaced-killall" {
					name = "/usr/local/bin/k3s-killall.sh"
				}
				if err := os.WriteFile(f.root+name, []byte("#!/bin/sh\nprintf changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-proof":
				proof.Killall = strings.Replace(proof.Killall, ":100:", ":101:", 1)
			}
			if output, err := f.run(t, uninstallExecutionScript(savedUninstall{Summary: f.summary, Proof: proof})); err == nil {
				t.Fatalf("文件身份或锁已变化仍执行: %s", output)
			}
			if _, err := os.Stat(f.root + "/official-invoked"); !os.IsNotExist(err) {
				t.Fatal("已阻断的目标仍执行了卸载脚本")
			}
		})
	}
}

func TestUninstallShellVerificationOnlyObservesExactTargetWithoutCleanup(t *testing.T) {
	f := newUninstallShellFixture(t)
	if output, err := f.run(t, uninstallVerificationScript(f.summary)); err == nil {
		t.Fatalf("目标卸载脚本仍存在却通过验证: %s", output)
	}
	if _, err := os.Stat(f.root + "/usr/local/bin/k3s-uninstall.sh"); err != nil {
		t.Fatal("结果验证删除了目标脚本")
	}
	// 仅测试夹具模拟官方卸载结束；EDO 的结果验证不负责删除其他文件或服务。
	if err := os.Remove(f.root + "/usr/local/bin/k3s-uninstall.sh"); err != nil {
		t.Fatal(err)
	}
	f.write(t, "/run/edo-k3s-install.lock/marker", "用户后续操作的锁\n")
	f.write(t, "/var/lib/rancher/k3s/user-state", "用户维护的数据\n")
	for _, tool := range []string{"ps", "systemctl", "rm", "umount", "ip", "iptables"} {
		writePreflightCommand(t, f.commands, tool, "printf unexpected >> "+shellQuote(f.root+"/unexpected-tool")+"\nexit 97\n")
	}
	output, err := f.run(t, uninstallVerificationScript(f.summary))
	if err != nil || output != "EDO_K3S_UNINSTALL_VERIFIED\n" {
		t.Fatalf("准确目标验证失败: %v %s", err, output)
	}
	for _, name := range []string{"/usr/local/bin/k3s-killall.sh", "/var/lib/rancher/k3s/user-state", "/run/edo-k3s-install.lock/marker"} {
		if _, err := os.Stat(f.root + name); err != nil {
			t.Fatalf("结果验证触碰了其他资源: %s", name)
		}
	}
	if _, err := os.Stat(f.root + "/unexpected-tool"); !os.IsNotExist(err) {
		t.Fatal("结果验证执行了自制清理或服务检查")
	}
	if err := os.Symlink(f.root+"/absent", f.root+"/usr/local/bin/k3s-uninstall.sh"); err != nil {
		t.Fatal(err)
	}
	if output, err := f.run(t, uninstallVerificationScript(f.summary)); err == nil {
		t.Fatalf("目标存在悬空符号链接却通过: %s", output)
	}
}
