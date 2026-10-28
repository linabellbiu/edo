package k3s

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func completePreflightFixture(statuses map[string]string) string {
	var output strings.Builder
	for _, definition := range hostPreflightDefinitions {
		status := "passed"
		if selected := statuses[definition.id]; selected != "" {
			status = selected
		}
		output.WriteString("EDO_K3S_CHECK\t" + definition.id + "\t" + status + "\n")
	}
	output.WriteString("EDO_K3S_HOSTNAME\tworker-a\nEDO_K3S_IDENTITY_BEGIN\naddresses\t192.0.2.10/24\nmachine_id\t0123456789abcdef0123456789abcdef\nsystem_uuid\t\nboot_id\t\nEDO_K3S_IDENTITY_END\nEDO_K3S_REPORT_END\n")
	return output.String()
}

func preflightStatus(report HostPreflight, id string) string {
	for _, check := range report.Checks {
		if check.ID == id {
			return check.Status
		}
	}
	return ""
}

func TestPreflightParserRejectsIncompleteOrAmbiguousReportWithoutLeakingOutput(t *testing.T) {
	host := Host{ID: "host-1", Name: "Host 1", Role: "agent"}
	valid := completePreflightFixture(nil)
	for name, output := range map[string]string{
		"truncated":        strings.ReplaceAll(valid, "EDO_K3S_REPORT_END", ""),
		"missing":          strings.ReplaceAll(valid, "EDO_K3S_CHECK\tmemory\tpassed\n", ""),
		"duplicate":        valid + "EDO_K3S_CHECK\tmemory\tpassed\n",
		"unknown":          valid + "EDO_K3S_CHECK\tunknown\tpassed\n",
		"invalid-status":   strings.Replace(valid, "\tmemory\tpassed", "\tmemory\tsuccess", 1),
		"invalid-hostname": strings.Replace(valid, "EDO_K3S_HOSTNAME\tworker-a", "EDO_K3S_HOSTNAME\tsecret_password.invalid_name", 1),
		"empty-identity":   strings.Replace(valid, "addresses\t192.0.2.10/24\nmachine_id\t0123456789abcdef0123456789abcdef", "addresses\t\nmachine_id\t", 1),
	} {
		t.Run(name, func(t *testing.T) {
			report := parsePreflightProbe(host, output, nil)
			if report.Passed {
				t.Fatal("malformed report passed")
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), "secret_password") {
				t.Fatal("untrusted remote output leaked")
			}
		})
	}
	report := parsePreflightProbe(host, valid, errors.New("private SSH password"))
	encoded, _ := json.Marshal(report)
	if report.Passed || preflightStatus(report, "connection") != "failed" || strings.Contains(string(encoded), "private SSH password") {
		t.Fatal("transport error was not controlled")
	}
}

func TestPreflightParserKeepsWarningsNonBlockingAndIdentityPrivate(t *testing.T) {
	report := parsePreflightProbe(Host{ID: "host-1", Name: "Host 1", Role: "agent"}, completePreflightFixture(map[string]string{"firewall": "warning", "swap": "warning"}), nil)
	if !report.Passed || report.Hostname != "worker-a" || report.Identity.MachineID == "" || len(report.Identity.Addresses) != 1 {
		t.Fatalf("invalid report: %#v", report)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), report.Identity.MachineID) || strings.Contains(string(encoded), "192.0.2.10") {
		t.Fatal("private physical identity leaked into API report")
	}
	failed := parsePreflightProbe(Host{}, completePreflightFixture(map[string]string{"ports": "failed"}), nil)
	if failed.Passed || preflightStatus(failed, "ports") != "failed" {
		t.Fatal("blocking check was ignored")
	}
}

func writePreflightCommand(t *testing.T, directory, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
}

func runPreflightShell(t *testing.T, input Input, host Host, extraEnv ...string) HostPreflight {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture runs on Linux")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell unavailable")
	}
	directory := t.TempDir()
	writeLinuxToolFixtures(t, directory)
	// 这里只模拟 root 身份，数据目录使用测试账户实际可写的路径。
	options := map[string]any{"data-dir": directory}
	for key, value := range optionsForHost(host, input) {
		options[key] = value
	}
	if host.Role == "server" {
		input.ServerOptions = options
	} else {
		input.AgentOptions = options
	}
	writePreflightCommand(t, directory, "uname", "if [ \"${1:-}\" = -s ]; then printf Linux; else printf x86_64; fi\n")
	writePreflightCommand(t, directory, "id", "printf 0\n")
	writePreflightCommand(t, directory, "hostname", "printf worker-a\n")
	writePreflightCommand(t, directory, "getconf", "printf '%s' \"${EDO_TEST_CPU:-2}\"\n")
	writePreflightCommand(t, directory, "df", "printf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 99999999 0 %s 0%% /\\n' \"${EDO_TEST_FREE:-99999999}\"\n")
	writePreflightCommand(t, directory, "ss", "printf '%s\\n' \"${EDO_TEST_LISTENERS:-}\"\n")
	writePreflightCommand(t, directory, "curl", "case \"$*\" in *api.example.test*) exit \"${EDO_TEST_ENDPOINT_STATUS:-0}\" ;; esac\nexit 0\n")
	writePreflightCommand(t, directory, "getenforce", "printf '%s' \"${EDO_TEST_SELINUX:-Enforcing}\"\n")
	writePreflightCommand(t, directory, "rpm", "exit \"${EDO_TEST_SELINUX_POLICY_STATUS:-1}\"\n")
	script := preflightEnvironmentScript(host, input, true) + "edo_check existing_installation passed\nprintf 'EDO_K3S_IDENTITY_BEGIN\\naddresses\\t192.0.2.10\\nmachine_id\\t0123456789abcdef0123456789abcdef\\nsystem_uuid\\t\\nboot_id\\t\\nEDO_K3S_IDENTITY_END\\nEDO_K3S_REPORT_END\\n'\n"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-s")
	command.Stdin = strings.NewReader(script)
	command.Env = append(os.Environ(), append([]string{"PATH=" + directory + ":" + os.Getenv("PATH")}, extraEnv...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("shell fixture failed: %v %s", err, output)
	}
	return parsePreflightProbe(host, string(output), nil)
}

func TestPreflightShellUsesRoleCapacityAndConfiguredPorts(t *testing.T) {
	input := Input{Endpoint: "https://api.example.test:6443", Installer: map[string]any{"INSTALL_K3S_VERSION": "v1.35.5+k3s1"}, ServerOptions: map[string]any{"https-listen-port": 7443}}
	worker := runPreflightShell(t, input, Host{Role: "agent"}, "EDO_TEST_CPU=1", "EDO_TEST_FREE=3145728", "EDO_TEST_LISTENERS=LISTEN 0 128 0.0.0.0:6443 0.0.0.0:*")
	if preflightStatus(worker, "cpu") != "passed" || preflightStatus(worker, "disk") != "warning" || preflightStatus(worker, "ports") != "passed" {
		t.Fatalf("worker role checks wrong: %#v", worker)
	}
	server := runPreflightShell(t, input, Host{Role: "server"}, "EDO_TEST_CPU=1", "EDO_TEST_FREE=3145728", "EDO_TEST_LISTENERS=LISTEN 0 128 0.0.0.0:7443 0.0.0.0:*")
	if preflightStatus(server, "cpu") != "failed" || preflightStatus(server, "disk") != "failed" || preflightStatus(server, "ports") != "failed" {
		t.Fatalf("server role checks wrong: %#v", server)
	}
	for _, id := range []string{"firewall", "selinux"} {
		if preflightStatus(server, id) != "warning" {
			t.Fatalf("environment must be warning only: %s", id)
		}
	}
	badCA := runPreflightShell(t, input, Host{Role: "agent"}, "EDO_TEST_ENDPOINT_STATUS=60")
	if preflightStatus(badCA, "endpoint") != "failed" {
		t.Fatal("untrusted endpoint certificate must block installation")
	}
}

func TestMasterPreflightRequiresPreinstalledSELinuxPolicy(t *testing.T) {
	input := Input{Action: "join", JoinRole: "server", Endpoint: "https://api.example.test:6443", Installer: map[string]any{"INSTALL_K3S_VERSION": "v1.35.5+k3s1"}}
	for _, mode := range []string{"Enforcing", "Permissive"} {
		missing := runPreflightShell(t, input, Host{Role: "server"}, "EDO_TEST_SELINUX="+mode)
		if preflightStatus(missing, "selinux") != "failed" {
			t.Fatalf("missing %s policy not blocked", mode)
		}
		ready := runPreflightShell(t, input, Host{Role: "server"}, "EDO_TEST_SELINUX="+mode, "EDO_TEST_SELINUX_POLICY_STATUS=0")
		if preflightStatus(ready, "selinux") != "warning" {
			t.Fatalf("preinstalled %s policy was not accepted", mode)
		}
	}
	disabled := runPreflightShell(t, input, Host{Role: "server"}, "EDO_TEST_SELINUX=Disabled")
	if preflightStatus(disabled, "selinux") != "passed" {
		t.Fatal("disabled SELinux blocked")
	}
}

func TestPreflightCAThroughStdinVerifiesRealTLSWithoutWritingRemoteFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX curl fixture runs on Linux")
	}
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl unavailable")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cacerts" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected or authenticated endpoint probe")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	directory := t.TempDir()
	writeLinuxToolFixtures(t, directory)
	writePreflightCommand(t, directory, "curl", "case \"$*\" in *"+server.URL+"*) exec "+shellQuote(curl)+" \"$@\" ;; esac\nexit 0\n")
	input := Input{Endpoint: server.URL, targetCAData: ca, Installer: map[string]any{"INSTALL_K3S_VERSION": "v1.35.5+k3s1"}}
	run := func(input Input) string {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "sh", "-s")
		command.Stdin = strings.NewReader(preflightEnvironmentScript(Host{Role: "agent"}, input, true))
		command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"))
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("TLS probe shell: %v %s", err, output)
		}
		return string(output)
	}
	if output := run(input); !strings.Contains(output, "EDO_K3S_CHECK\tendpoint\tpassed") {
		t.Fatal("provided CA did not validate endpoint")
	}
	input.targetCAData = nil
	if output := run(input); !strings.Contains(output, "EDO_K3S_CHECK\tendpoint\tfailed") {
		t.Fatal("untrusted CA accepted")
	}
}

func TestPreflightDownloadFollowsOnlyBoundedHTTPSRedirects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX curl fixture runs on Linux")
	}
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl unavailable")
	}
	var redirect atomic.Value
	var plaintextRequests, finalRequests atomic.Int32
	plaintext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plaintextRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer plaintext.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.Header.Get("Authorization") != "" {
			t.Error("download probe must send only unauthenticated HEAD requests")
		}
		switch r.URL.Path {
		case "/ok":
			finalRequests.Add(1)
			w.WriteHeader(http.StatusOK)
		case "/blocked":
			w.WriteHeader(http.StatusForbidden)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/plaintext":
			http.Redirect(w, r, plaintext.URL, http.StatusFound)
		default:
			http.Redirect(w, r, redirect.Load().(string), http.StatusFound)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	writeLinuxToolFixtures(t, directory)
	caPath := filepath.Join(directory, "source-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	writePreflightCommand(t, directory, "curl", "case \"$*\" in *"+server.URL+"*) exec "+shellQuote(curl)+" \"$@\" ;; esac\nexit 0\n")
	input := Input{Installer: map[string]any{"INSTALL_K3S_VERSION": "v1.35.5+k3s1", "INSTALL_K3S_ARTIFACT_URL": server.URL}}
	for _, scenario := range []struct {
		path, status string
	}{
		{"/ok", "passed"},
		{"/blocked", "failed"},
		{"/loop", "failed"},
		{"/plaintext", "failed"},
	} {
		t.Run(scenario.path, func(t *testing.T) {
			redirect.Store(server.URL + scenario.path)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-s")
			command.Stdin = strings.NewReader(preflightEnvironmentScript(Host{Role: "agent"}, input, false))
			command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "CURL_CA_BUNDLE="+caPath, "NO_PROXY=127.0.0.1,localhost")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("download probe shell: %v %s", err, output)
			}
			if !strings.Contains(string(output), "EDO_K3S_CHECK\tdownload\t"+scenario.status) {
				t.Fatalf("unexpected redirect result: %s", output)
			}
		})
	}
	if finalRequests.Load() != 1 || plaintextRequests.Load() != 0 {
		t.Fatalf("redirect targets: HTTPS=%d plaintext=%d", finalRequests.Load(), plaintextRequests.Load())
	}
	apiScript := preflightEnvironmentScript(Host{Role: "agent"}, Input{Endpoint: "https://api.example.test:6443"}, true)
	for _, line := range strings.Split(apiScript, "\n") {
		if strings.Contains(line, "/cacerts") && strings.Contains(line, "--location") {
			t.Fatal("cluster API probe must retain its no-redirect policy")
		}
	}
}

func TestInstallScriptRechecksSharedEnvironmentBeforeWritingConfiguration(t *testing.T) {
	input := Input{Action: "join", Endpoint: "https://api.example.test:6443", Installer: map[string]any{"INSTALL_K3S_VERSION": "v1.35.5+k3s1"}}
	script, err := installScript(Host{Role: "agent"}, input, "fixture-installation", "private-bootstrap-token")
	if err != nil {
		t.Fatal(err)
	}
	guard := strings.Index(script, "if [ \"$edo_failed\" != 0 ]; then exit 44; fi")
	write := strings.Index(script, "base64 -d > /etc/rancher/k3s/config.yaml")
	if guard < 0 || write < guard || !strings.Contains(script, "edo_check endpoint failed") {
		t.Fatal("shared preflight must complete before configuration writes")
	}
	download := strings.Index(script, "--retry 3 'https://get.k3s.io' -o")
	finalGuard := strings.LastIndex(script, "if [ -e '/etc/rancher/k3s'")
	if download < 0 || finalGuard < download || finalGuard > write {
		t.Fatal("existing-installation guard must run again after script download and before configuration writes")
	}
	for _, unsafe := range []string{" --insecure", " -k ", "systemctl stop", "systemctl disable", "ufw disable", "setenforce ", "apt install", "yum install"} {
		if strings.Contains(preflightProbeScript(Host{Role: "agent"}, input, true), unsafe) {
			t.Fatalf("read-only probe contains %q", unsafe)
		}
	}
	serverPorts, _ := preflightPorts(Host{Role: "server"}, map[string]any{"https-listen-port": 7443, "cluster-init": true})
	if !slices.Contains(serverPorts, 7443) || slices.Contains(serverPorts, 6443) || !slices.Contains(serverPorts, 2379) {
		t.Fatal("configured server ports ignored")
	}
}

func TestComponentPreflightPortsUseFinalArgumentsAndRetainUnchangedDefaults(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		arguments []string
		proxy     bool
		ports     []int
		warning   bool
	}{
		{"kubelet-defaults", nil, false, []int{10248, 10250}, false},
		{"kubelet-unrelated", []string{"v=4"}, false, []int{10248, 10250}, false},
		{"kubelet-last-value", []string{"port=invalid", "--port=11250", "healthz-port=0", "read-only-port=11255"}, false, []int{11250, 11255}, false},
		{"kubelet-unknown-port", []string{"port=bad"}, false, []int{10248}, true},
		{"kubelet-config-file", []string{"config=/etc/custom-kubelet.yaml"}, false, nil, true},
		{"proxy-defaults", nil, true, []int{10249, 10256}, false},
		{"proxy-ipv6-last", []string{"metrics-bind-address=invalid", "metrics-bind-address=[::1]:11349", "healthz-bind-address=:11356"}, true, []int{11349, 11356}, false},
		{"proxy-disabled", []string{"metrics-bind-address=", "healthz-bind-address=0.0.0.0:0"}, true, []int{}, false},
		{"proxy-invalid", []string{"metrics-bind-address=::1:123"}, true, []int{10256}, true},
		{"proxy-invalid-range", []string{"healthz-bind-address=127.0.0.1:99999"}, true, []int{10249}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ports, warning := componentPreflightPorts(scenario.arguments, scenario.proxy)
			if !slices.Equal(ports, scenario.ports) || warning != scenario.warning {
				t.Fatalf("ports=%v warning=%v", ports, warning)
			}
		})
	}
}

func TestPreflightShellChecksConfiguredComponentPortsWithoutMaskingDefaults(t *testing.T) {
	input := Input{AgentOptions: map[string]any{"kubelet-arg": []string{"port=11250", "healthz-port=0"}, "kube-proxy-arg": []string{"metrics-bind-address=[::1]:11349"}}}
	unusedDefaults := runPreflightShell(t, input, Host{Role: "agent"}, "EDO_TEST_LISTENERS=LISTEN 0 128 0.0.0.0:10250 0.0.0.0:*")
	if preflightStatus(unusedDefaults, "ports") != "passed" {
		t.Fatal("overridden default port falsely blocked installation")
	}
	configuredConflict := runPreflightShell(t, input, Host{Role: "agent"}, "EDO_TEST_LISTENERS=LISTEN 0 128 0.0.0.0:11250 0.0.0.0:*")
	if preflightStatus(configuredConflict, "ports") != "failed" {
		t.Fatal("configured kubelet port conflict ignored")
	}
	input.AgentOptions = map[string]any{"kubelet-arg": []string{"v=4"}}
	defaultConflict := runPreflightShell(t, input, Host{Role: "agent"}, "EDO_TEST_LISTENERS=LISTEN 0 128 0.0.0.0:10250 0.0.0.0:*")
	if preflightStatus(defaultConflict, "ports") != "failed" {
		t.Fatal("unrelated component argument masked a real default port conflict")
	}
	input.AgentOptions = map[string]any{"kubelet-arg": []string{"port=invalid"}}
	unknown := runPreflightShell(t, input, Host{Role: "agent"})
	if preflightStatus(unknown, "ports") != "warning" {
		t.Fatal("unknown configured port cannot be reported as verified")
	}
}

func TestPreflightMissingToolsStillReturnsCompleteControlledReport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture runs on Linux")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	directory := t.TempDir()
	writePreflightCommand(t, directory, "id", "printf 0\n")
	writePreflightCommand(t, directory, "uname", "if [ \"${1:-}\" = -s ]; then printf Linux; else printf x86_64; fi\n")
	suffix := "edo_check existing_installation passed\nprintf 'EDO_K3S_IDENTITY_BEGIN\\naddresses\\t192.0.2.10\\nmachine_id\\t0123456789abcdef0123456789abcdef\\nsystem_uuid\\t\\nboot_id\\t\\nEDO_K3S_IDENTITY_END\\nEDO_K3S_REPORT_END\\n'\n"
	command := exec.Command(shell, "-s")
	command.Env = append(os.Environ(), "PATH="+directory)
	command.Stdin = strings.NewReader("set -u\n" + preflightEnvironmentScript(Host{Role: "agent"}, Input{Endpoint: "https://api.example.test:6443"}, true) + suffix)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("missing tools aborted report: %v", err)
	}
	report := parsePreflightProbe(Host{Role: "agent"}, string(output), nil)
	if report.Passed || preflightStatus(report, "tools") != "failed" || preflightStatus(report, "report") == "failed" {
		t.Fatalf("missing commands must block with a complete report: %#v", report)
	}
}

func TestPreflightReadonlyFilesystemBlocksWithoutWritingProbeFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture runs on Linux")
	}
	if err := exec.Command("sh", "-c", "[ -w /sys ]").Run(); err == nil {
		t.Skip("/sys is writable on this platform")
	}
	input := Input{AgentOptions: map[string]any{"data-dir": "/sys/edo-preflight-unused/data"}}
	report := runPreflightShell(t, input, Host{Role: "agent"})
	if preflightStatus(report, "disk") != "failed" {
		t.Fatal("readonly mount accepted")
	}
	if _, err := os.Stat("/sys/edo-preflight-unused"); !os.IsNotExist(err) {
		t.Fatal("preflight created a disk probe directory")
	}
}

func TestPreflightDownloadProbeUsesSelectedChannelAndArchitecture(t *testing.T) {
	script := preflightProbeScript(Host{Role: "agent"}, Input{Installer: map[string]any{"INSTALL_K3S_CHANNEL": "v1.35", "INSTALL_K3S_CHANNEL_URL": "https://mirror.example.test/channels"}}, false)
	if !strings.Contains(script, "https://mirror.example.test/channels/v1.35") {
		t.Fatal("selected release channel ignored")
	}
	script = preflightProbeScript(Host{Role: "agent"}, Input{Installer: map[string]any{"INSTALL_K3S_VERSION": "v1.35.5+k3s1"}}, false)
	if !strings.Contains(script, "aarch64|arm64) edo_arch=arm64") || !strings.Contains(script, "armv7*) edo_arch=arm") || !strings.Contains(script, "/v1.35.5%2Bk3s1/sha256sum-") {
		t.Fatal("release architecture mapping ignored")
	}
	ports, _ := preflightPorts(Host{Role: "server"}, map[string]any{"disable-agent": true, "disable-controller-manager": true, "disable-cloud-controller": true, "disable-scheduler": true})
	for _, port := range []int{10250, 10248, 10010, 10249, 10256, 10257, 10258, 10259, 6444} {
		if slices.Contains(ports, port) {
			t.Fatalf("disabled component port still checked: %d", port)
		}
	}
}
