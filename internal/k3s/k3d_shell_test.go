package k3s

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestK3dShellUsesPinnedCLIAndOnlyUpdatesReviewedLocalContainers(t *testing.T) {
	for _, scenario := range []string{"success", "minimum-engine", "bad-checksum", "old-engine", "unknown-engine", "wrong-bind", "occupied-port", "low-cpu", "low-memory", "low-disk", "update-failure"} {
		t.Run(scenario, func(t *testing.T) {
			// 使用短路径，兼容 macOS Unix Socket 的路径长度限制。
			directory, err := os.MkdirTemp("/tmp", "edo-k3d-shell-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(directory) })
			commands := filepath.Join(directory, "commands")
			data := filepath.Join(directory, "docker-data")
			for _, path := range []string{commands, data} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			socket := filepath.Join(directory, "docker.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			id := uuid.NewString()
			calls := filepath.Join(directory, "calls")
			defaultConfig := filepath.Join(directory, "original-kubeconfig")
			if err := os.WriteFile(defaultConfig, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			tool := "#!/bin/sh\n[ -z \"${DOCKER_CONTEXT:-}${DOCKER_TLS_VERIFY:-}${KUBECONFIG:-}\" ] || exit 96\n[ \"${DOCKER_HOST:-}\" = " + shellQuote("unix://"+socket) + " ] || exit 95\nprintf 'cli %s\\n' \"$*\" >> " + shellQuote(calls) + "\n"
			tool += "case \"${1:-}\" in\nversion) printf 'k3d version v5.9.0\\n';;\ncluster)\n[ \"${2:-}\" = create ] || exit 94\nshift 2\nname=$1; shift\n[ \"${#name}\" -le 32 ] || exit 98\ncase \"$name\" in ''|*[!a-z0-9-]*|*-) exit 97;; esac\n[ \"$name\" = " + shellQuote(k3dResourceName(id)) + " ] || exit 93\nruntime_label=''; node_label=''\nwhile [ \"$#\" -gt 0 ]; do case \"$1\" in --runtime-label) runtime_label=$2; shift 2;; --k3s-node-label) node_label=$2; shift 2;; *) shift;; esac; done\n[ \"$runtime_label\" = " + shellQuote("edo.dev/installation="+id+"@all") + " ] && [ \"$node_label\" = \"$runtime_label\" ] || exit 92\n;;\nkubeconfig)\n[ \"${2:-}\" = get ] && [ \"${3:-}\" = " + shellQuote(k3dResourceName(id)) + " ] || exit 86\nprintf 'apiVersion: v1\\nkind: Config\\ncurrent-context: chosen\\n';;\n*) exit 91;;\nesac\n"
			asset := filepath.Join(directory, "asset")
			if err := os.WriteFile(asset, []byte(tool), 0600); err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(tool)))
			if scenario == "bad-checksum" {
				hash = strings.Repeat("0", 64)
			}
			engine := "linux 28.0.0"
			if scenario == "minimum-engine" {
				engine = "linux 20.10.5"
			}
			if scenario == "old-engine" {
				engine = "linux 20.10.4"
			}
			if scenario == "unknown-engine" {
				engine = "linux dev"
			}
			docker := "[ -z \"${DOCKER_CONTEXT:-}${DOCKER_TLS_VERIFY:-}${KUBECONFIG:-}\" ] || exit 90\n[ \"$1\" = " + shellQuote("--host=unix://"+socket) + " ] || exit 89\nshift\nprintf 'docker %s\\n' \"$*\" >> " + shellQuote(calls) + "\ncase \"$1\" in\nversion) printf '%s\\n' " + shellQuote(engine) + ";;\ninfo) printf '%s\\n' " + shellQuote(data) + ";;\ninspect)\ncase \"$3\" in *Config.Labels*) printf '%s\\n' " + shellQuote(id) + ";; *) case \"$4\" in *server-0) printf '2000000000 2147483648 2147483648\\n';; *) printf '1000000000 1073741824 1073741824\\n';; esac;; esac\n;;\nupdate) "
			if scenario == "update-failure" {
				docker += "exit 88\n"
			} else {
				docker += "exit 0\n"
			}
			docker += ";;\n*) exit 87;;\nesac\n"
			writePreflightCommand(t, commands, "docker", docker)
			// 测试用上下文统一限制时长；桩只去掉 timeout 参数，不访问真实服务。
			writePreflightCommand(t, commands, "timeout", "shift\nexec \"$@\"\n")
			writePreflightCommand(t, commands, "id", "printf 0\n")
			writePreflightCommand(t, commands, "uname", "case \"${1:-}\" in -s) printf Linux;; *) printf x86_64;; esac\n")
			writePreflightCommand(t, commands, "hostname", "printf k3d-host\n")
			cpu := "8"
			if scenario == "low-cpu" {
				cpu = "2"
			}
			writePreflightCommand(t, commands, "getconf", "printf '%s' "+shellQuote(cpu)+"\n")
			ip := "192.0.2.10"
			if scenario == "wrong-bind" {
				ip = "192.0.2.11"
			}
			writePreflightCommand(t, commands, "ip", "printf '%s\\n' "+shellQuote("2: eth0 inet "+ip+"/24 scope global eth0")+"\n")
			port := ""
			if scenario == "occupied-port" {
				port = "LISTEN 0 128 0.0.0.0:16443 0.0.0.0:*"
			}
			writePreflightCommand(t, commands, "ss", "printf '%s\\n' "+shellQuote(port)+"\n")
			free := "99999999"
			if scenario == "low-disk" {
				free = "1000"
			}
			writePreflightCommand(t, commands, "df", "printf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 99999999 0 "+free+" 0%% /\\n'\n")
			writePreflightCommand(t, commands, "curl", "out=''\nwhile [ \"$#\" -gt 0 ]; do case \"$1\" in -o) out=$2; shift 2;; *) shift;; esac; done\nif [ -n \"$out\" ]; then cp "+shellQuote(asset)+" \"$out\"; fi\n")
			writePreflightCommand(t, commands, "sh", "printf 'addresses\\t192.0.2.10\\nmachine_id\\t0123456789abcdef0123456789abcdef\\nsystem_uuid\\t\\nboot_id\\t\\n'\n")
			memory := "99999999"
			if scenario == "low-memory" {
				memory = "3145728"
			}
			meminfo := filepath.Join(directory, "meminfo")
			if err := os.WriteFile(meminfo, []byte("MemAvailable: "+memory+" kB\n"), 0600); err != nil {
				t.Fatal(err)
			}
			agents := 1
			input := Input{Action: "create", Mode: "k3d", Endpoint: "https://192.0.2.10:16443", K3d: &K3dOptions{Agents: &agents, APIPort: 16443, BindAddress: "192.0.2.10"}}
			prepare, err := k3dPrepareToolScript(input, id)
			if err != nil {
				t.Fatal(err)
			}
			install, err := k3dInstallScript(input, id)
			if err != nil {
				t.Fatal(err)
			}
			originalPath := commands + ":" + os.Getenv("PATH")
			rewrite := strings.NewReplacer("/var/lib/edo/k3d", filepath.Join(directory, "tools"), "/var/lib/edo", directory, "/var/run/docker.sock", socket, "/run/edo-k3s-install.lock", filepath.Join(directory, "install.lock"), "/proc/meminfo", meminfo, "edo_path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "edo_path="+shellQuote(originalPath), "06d8f25bc3a971c4eb29e0ff08429b180402db0f4dec838c9eac427e296800a0", hash)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			run := func(script string) error {
				command := exec.CommandContext(ctx, "/bin/sh", "-s")
				command.Stdin = strings.NewReader(rewrite.Replace(script))
				command.Env = append(os.Environ(), "PATH="+originalPath, "DOCKER_HOST=tcp://unrelated.invalid:2375", "DOCKER_CONTEXT=unrelated", "DOCKER_TLS_VERIFY=1", "KUBECONFIG="+defaultConfig)
				output, err := command.CombinedOutput()
				if err != nil {
					return fmt.Errorf("%w: %s", err, output)
				}
				return nil
			}
			err = run(prepare)
			if err == nil {
				err = run(install)
			}
			if err == nil {
				err = run(k3dReadConfigScript(id))
			}
			wantSuccess := scenario == "success" || scenario == "minimum-engine"
			if (err == nil) != wantSuccess {
				t.Fatalf("shell结果错误: %v", err)
			}
			encoded, _ := os.ReadFile(calls)
			if !wantSuccess && scenario != "update-failure" && strings.Contains(string(encoded), "cli cluster create") {
				t.Fatal("前置检查失败仍创建容器")
			}
			if wantSuccess {
				for _, expected := range []string{"cli cluster create " + k3dResourceName(id), "--runtime-label edo.dev/installation=" + id + "@all", "docker update --cpus 2 --memory 2g --memory-swap 2g k3d-" + k3dResourceName(id) + "-server-0", "docker update --cpus 1 --memory 1g --memory-swap 1g k3d-" + k3dResourceName(id) + "-agent-0", "cli kubeconfig get " + k3dResourceName(id)} {
					if !strings.Contains(string(encoded), expected) {
						t.Fatalf("未执行固定本地CLI/节点限制: %s", expected)
					}
				}
			}
			if strings.Contains(string(encoded), "cluster delete") || strings.Contains(string(encoded), "volume rm") {
				t.Fatal("失败安装删除了资源")
			}
			if original, _ := os.ReadFile(defaultConfig); string(original) != "unchanged" {
				t.Fatal("默认kubeconfig被修改")
			}
			if _, err := os.Stat(filepath.Join(directory, "install.lock")); !os.IsNotExist(err) {
				t.Fatal("安装退出后遗留主机锁")
			}
		})
	}
}
