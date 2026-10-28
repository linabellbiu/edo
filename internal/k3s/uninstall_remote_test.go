//go:build linux || darwin

package k3s

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type uninstallShellFixture struct {
	root, commands string
	configDigest   string
	fakeSocket     bool
	summary        UninstallSummary
	rewrite        *strings.Replacer
}

func newUninstallShellFixture(t *testing.T) uninstallShellFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	commands := filepath.Join(root, "commands")
	for _, directory := range []string{commands, root + "/usr/local/bin", root + "/etc/systemd/system", root + "/etc/rancher/k3s", root + "/var/lib/rancher/k3s", root + "/run/systemd/system", root + "/etc/init.d"} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	summary := UninstallSummary{SourceInstallationID: "source-fixture", HostID: "host-fixture", Role: "server", ServiceName: "k3s", ScriptPath: "/usr/local/bin/k3s-uninstall.sh", DataDir: "/var/lib/rancher/k3s"}
	rewrite := strings.NewReplacer("/usr/local/bin", root+"/usr/local/bin", "/etc", root+"/etc", "/var/lib", root+"/var/lib", "/var/run", root+"/var/run", "/var/snap", root+"/var/snap", "/run", root+"/run")
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(root+path, []byte(rewrite.Replace(value)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("/usr/local/bin/k3s-uninstall.sh", "#!/bin/sh\n# /usr/local/bin/k3s-killall.sh K3S_DATA_DIR /etc/systemd/system/k3s.service /etc/rancher/k3s remove_uninstall\nprintf invoked > "+shellQuote(root+"/official-invoked")+"\n")
	write("/usr/local/bin/k3s-killall.sh", "#!/bin/sh\n# KUBE- containerd-shim\n")
	write("/etc/rancher/k3s/config.yaml", "node-label:\n- edo.dev/installation=source-fixture\n- edo.dev/host=host-fixture\n")
	write("/etc/systemd/system/k3s.service", "ExecStart=/usr/local/bin/k3s server --config /etc/rancher/k3s/config.yaml\n")
	// 官方默认环境文件可以合法为空，仍必须审阅其身份与空内容摘要。
	write("/etc/systemd/system/k3s.service.env", "")
	writePreflightCommand(t, commands, "id", "printf '0\\n'\n")
	writePreflightCommand(t, commands, "stat", `for item do last=$item; done
case "$last" in *.service.env) size=0;; *) size=100;; esac
case "$2" in
'%u') printf '0\n';;
'%a') printf '700\n';;
'%h') printf '1\n';;
'%s') printf '%s\n' "$size";;
'%d:%i:%u:%g:%a:%h:%s:%Y:%Z') printf '1:1:0:0:700:1:%s:1:1\n' "$size";;
*) exit 2;;
esac
`)
	writePreflightCommand(t, commands, "readlink", `for item do result=$item; done; printf '%s\n' "$result"`)
	writePreflightCommand(t, commands, "findmnt", "printf '/\\n'\n")
	writePreflightCommand(t, commands, "ps", "printf 'sh\\n'\n")
	writePreflightCommand(t, commands, "systemctl", "case \"$*\" in\n*FragmentPath*) printf '%s\\n' "+shellQuote(root+"/etc/systemd/system/k3s.service")+";;\n*LoadState*) printf 'not-found\\n';;\n*DropInPaths*) :;;\n'list-unit-files'*) printf 'k3s.service enabled\\n';;\n*) exit 2;;\nesac\n")
	for _, tool := range []string{"ip", "iptables-save", "ip6tables-save"} {
		writePreflightCommand(t, commands, tool, ":\n")
	}
	writeLinuxToolFixtures(t, commands)
	config, err := os.ReadFile(root + "/etc/rancher/k3s/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(config)
	script, _ := os.ReadFile(root + "/usr/local/bin/k3s-uninstall.sh")
	killall, _ := os.ReadFile(root + "/usr/local/bin/k3s-killall.sh")
	unit, _ := os.ReadFile(root + "/etc/systemd/system/k3s.service")
	scriptSHA, killallSHA, unitSHA := sha256.Sum256(script), sha256.Sum256(killall), sha256.Sum256(unit)
	// 测试只执行无害夹具；生产白名单常量在这个临时脚本副本中替换为夹具摘要。
	combinedRewrite := strings.NewReplacer(officialServerUninstallSHA256, fmt.Sprintf("%x", scriptSHA), officialKillallSHA256, fmt.Sprintf("%x", killallSHA), officialServerUnitSHA256, fmt.Sprintf("%x", unitSHA), "/usr/local/bin", root+"/usr/local/bin", "/usr/lib", root+"/usr/lib", "/etc", root+"/etc", "/var/lib", root+"/var/lib", "/var/run", root+"/var/run", "/var/snap", root+"/var/snap", "/run", root+"/run")
	return uninstallShellFixture{root: root, commands: commands, configDigest: fmt.Sprintf("%x", digest), summary: summary, rewrite: combinedRewrite}
}

func (f uninstallShellFixture) run(t *testing.T, script string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-se")
	if f.fakeSocket {
		script = strings.Replace(script, "[ -S /var/run/docker.sock ]", "[ -f "+shellQuote(f.root+"/docker-marker")+" ]", 1)
	}
	command.Stdin = strings.NewReader(f.rewrite.Replace(script))
	command.Env = []string{"PATH=" + f.commands + ":/usr/bin:/bin:/usr/sbin:/sbin", "GORACE=atexit_sleep_ms=0"}
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestUninstallShellReadOnlyGuardAndExactFrozenOfficialExecution(t *testing.T) {
	f := newUninstallShellFixture(t)
	output, err := f.run(t, uninstallProbeScript(f.summary, f.configDigest))
	if err != nil {
		t.Fatalf("probe: %v %s", err, output)
	}
	proof, err := parseUninstallProof(output)
	if err != nil {
		t.Fatalf("proof: %v %s", err, output)
	}
	if _, err := os.Stat(f.root + "/official-invoked"); !os.IsNotExist(err) {
		t.Fatal("probe executed uninstaller")
	}
	output, err = f.run(t, uninstallExecutionScript(savedUninstall{Summary: f.summary, Proof: proof, ConfigDigest: f.configDigest}))
	if err != nil {
		t.Fatalf("official invocation: %v %s", err, output)
	}
	if value, err := os.ReadFile(f.root + "/official-invoked"); err != nil || string(value) != "invoked" {
		t.Fatal("frozen script not invoked")
	}
}

func TestUninstallShellBlocksFailedReadsSharedMountsOtherServicesAndReplacedFiles(t *testing.T) {
	for _, scenario := range []string{"ps-error", "mount-error", "unit-error", "other-service", "other-init-service", "dangling-init-service", "shared-mount", "prefix-mount", "docker-prefix-bind", "docker-raw-link", "cni-netns-mount", "cni-rules", "dropin", "config-dropin-empty", "config-dropin-override", "config-dropin-symlink", "foreign-cli-link", "environment", "live-installer", "installer-lock", "symlink", "wrong-origin", "replaced-script", "replaced-unit", "read-only-proof"} {
		t.Run(scenario, func(t *testing.T) {
			f := newUninstallShellFixture(t)
			output, err := f.run(t, uninstallProbeScript(f.summary, f.configDigest))
			if err != nil {
				t.Fatalf("initial probe: %v %s", err, output)
			}
			proof, err := parseUninstallProof(output)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "ps-error":
				writePreflightCommand(t, f.commands, "ps", "exit 2\n")
			case "mount-error":
				writePreflightCommand(t, f.commands, "findmnt", "exit 2\n")
			case "unit-error":
				writePreflightCommand(t, f.commands, "systemctl", "exit 2\n")
			case "other-service":
				if err := os.WriteFile(f.root+"/etc/systemd/system/k3s-other.service", []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "other-init-service":
				if err := os.WriteFile(f.root+"/etc/init.d/k3s-other", []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			case "dangling-init-service":
				if err := os.Symlink(f.root+"/absent", f.root+"/etc/init.d/k3s-other"); err != nil {
					t.Fatal(err)
				}
			case "shared-mount":
				writePreflightCommand(t, f.commands, "findmnt", "printf '%s\\n' "+shellQuote(f.root+"/var/lib/rancher/k3s/shared")+"\n")
			case "prefix-mount":
				writePreflightCommand(t, f.commands, "findmnt", "printf '%s\\n' "+shellQuote(f.root+"/run/k3s-foreign")+"\n")
			case "cni-netns-mount":
				writePreflightCommand(t, f.commands, "findmnt", "printf '%s\\n' "+shellQuote(f.root+"/run/netns/cni-foreign")+"\n")
			case "docker-prefix-bind", "docker-raw-link":
				f.fakeSocket = true
				if err := os.WriteFile(f.root+"/docker-marker", nil, 0600); err != nil {
					t.Fatal(err)
				}
				bind := f.root + "/run/k3s-other"
				if scenario == "docker-raw-link" {
					bind = f.root + "/var/lib/rancher/k3s/link"
					writePreflightCommand(t, f.commands, "readlink", "for item do last=$item; done\ncase \"$last\" in "+shellQuote(bind)+") printf '/outside/safe-data\\n';; *) printf '%s\\n' \"$last\";; esac\n")
				}
				writePreflightCommand(t, f.commands, "docker", "case \"$*\" in *' info '*) printf '/safe/docker-data\\n';; *' ps '*) printf '%s\\n' "+shellQuote(strings.Repeat("a", 64))+";; *' inspect '*) printf '%s\\n' "+shellQuote(bind)+";; *) exit 2;; esac\n")
			case "cni-rules":
				writePreflightCommand(t, f.commands, "iptables-save", "printf '%s\\n' '-A CNI-EXTERNAL -j ACCEPT'\n")
			case "dropin":
				if err := os.Mkdir(f.root+"/etc/systemd/system/k3s.service.d", 0700); err != nil {
					t.Fatal(err)
				}
			case "config-dropin-empty", "config-dropin-override":
				dir := f.root + "/etc/rancher/k3s/config.yaml.d"
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if scenario == "config-dropin-override" {
					if err := os.WriteFile(dir+"/99-storage.yaml", []byte("data-dir: /unrelated/data\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "config-dropin-symlink":
				if err := os.Symlink(f.root, f.root+"/etc/rancher/k3s/config.yaml.d"); err != nil {
					t.Fatal(err)
				}
			case "foreign-cli-link":
				if err := os.Symlink("/usr/bin/foreign-kubectl", f.root+"/usr/local/bin/kubectl"); err != nil {
					t.Fatal(err)
				}
			case "environment":
				if err := os.WriteFile(f.root+"/etc/systemd/system/k3s.service.env", []byte("K3S_DATA_DIR=/unrelated/data\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "live-installer":
				writePreflightCommand(t, f.commands, "ps", "printf '%s\\n' 'sh /tmp/edo-k3s-install.fixture/install.sh'\n")
			case "installer-lock":
				if err := os.Mkdir(f.root+"/run/edo-k3s-install.lock", 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(f.root+"/etc/rancher/k3s/config.yaml", f.root+"/config-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.root+"/config-real", f.root+"/etc/rancher/k3s/config.yaml"); err != nil {
					t.Fatal(err)
				}
			case "wrong-origin":
				if err := os.WriteFile(f.root+"/etc/rancher/k3s/config.yaml", []byte("node-label: []\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced-script":
				file, err := os.OpenFile(f.root+"/usr/local/bin/k3s-uninstall.sh", os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = file.WriteString("\n# replaced\n")
				_ = file.Close()
			case "replaced-unit":
				file, err := os.OpenFile(f.root+"/etc/systemd/system/k3s.service", os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = file.WriteString("ExecStop=/usr/bin/false\n")
				_ = file.Close()
			case "read-only-proof":
				proof.Killall = strings.Replace(proof.Killall, ":100:", ":101:", 1)
			}
			if output, err := f.run(t, uninstallExecutionScript(savedUninstall{Summary: f.summary, Proof: proof, ConfigDigest: f.configDigest})); err == nil {
				t.Fatalf("unsafe scene executed: %s", output)
			}
			if _, err := os.Stat(f.root + "/official-invoked"); !os.IsNotExist(err) {
				t.Fatal("blocked guard invoked official script")
			}
		})
	}
}

func TestUninstallShellVerificationFailsForResidualLockWithoutDeletingIt(t *testing.T) {
	f := newUninstallShellFixture(t)
	// 后验夹具只保留一个未知锁；所有删除仅针对测试本身创建的临时目录。
	for _, file := range []string{"/usr/local/bin/k3s-uninstall.sh", "/usr/local/bin/k3s-killall.sh", "/etc/systemd/system/k3s.service", "/etc/systemd/system/k3s.service.env"} {
		if err := os.Remove(f.root + file); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{"/etc/rancher/k3s", "/var/lib/rancher/k3s"} {
		if err := os.RemoveAll(f.root + directory); err != nil {
			t.Fatal(err)
		}
	}
	writePreflightCommand(t, f.commands, "systemctl", "case \"$*\" in *LoadState*) printf 'not-found\\n';; 'list-unit-files'*) :;; *) exit 2;; esac\n")
	lock := f.root + "/run/edo-k3s-install.lock"
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := f.run(t, uninstallVerificationScript(f.summary)); err == nil {
		t.Fatalf("residual lock passed: %s", output)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("unknown lock was removed")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	output, err := f.run(t, uninstallVerificationScript(f.summary))
	if err != nil || output != "EDO_K3S_UNINSTALL_VERIFIED\n" {
		t.Fatalf("clean fixture: %v %s", err, output)
	}
}
