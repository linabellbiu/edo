package k3s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"

	"edo/internal/model"
	"edo/internal/sshclient"
)

type sshTransport struct{ timeout time.Duration }

// 先消费密码帧，再认证和执行脚本，避免 sudo 缓存变化时把密码当作脚本。
const sudoFramedCommand = `sh -c 'IFS= read -r edo_sudo_password || exit 70; printf "%s\n" "$edo_sudo_password" | sudo -S -p "" -v || exit 71; unset edo_sudo_password; sudo -n -- sh -se; edo_status=$?; exit "$edo_status"'`

func (t *sshTransport) Run(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string, output io.Writer) error {
	endpoint := (&url.URL{Scheme: "ssh", User: url.User(host.SSHUsername), Host: net.JoinHostPort(host.Address, strconv.Itoa(host.SSHPort))}).String()
	connector, err := sshclient.NewConnector(endpoint, bundle, host.SSHHostKeyFingerprint, t.timeout)
	if err != nil {
		logSSHTransportFailure(ctx, host.ID, "connector", err)
		return ErrRemote
	}
	client, err := connector.ConnectPinned(ctx)
	if err != nil {
		logSSHTransportFailure(ctx, host.ID, "connect", err)
		return ErrRemote
	}
	defer client.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopCancellation()
	command := "sh -se"
	input := io.Reader(strings.NewReader(script))
	needsSudo := host.SSHUsername != "root"
	if needsSudo && !bundle.UseSudo {
		uid, err := privilegeCommand(client, "id -u", "")
		if err != nil || strings.TrimSpace(uid) != "0" {
			logSSHTransportFailure(ctx, host.ID, "sudo_required", ErrInvalidInput)
			return ErrRemote
		}
		needsSudo = false
	}
	if needsSudo {
		probe, err := client.NewSession()
		if err != nil {
			logSSHTransportFailure(ctx, host.ID, "sudo_probe_session", err)
			return ErrRemote
		}
		probeDone := make(chan error, 1)
		go func() { probeDone <- probe.Run("sudo -n -- true") }()
		var probeErr error
		select {
		case probeErr = <-probeDone:
		case <-ctx.Done():
			_ = probe.Close()
			_ = client.Close()
			logSSHTransportFailure(ctx, host.ID, "sudo_probe_timeout", ctx.Err())
			return ctx.Err()
		}
		_ = probe.Close()
		if probeErr == nil {
			command = "sudo -n -- sh -se"
		} else {
			password := bundle.SudoPassword
			if password == "" {
				password = bundle.Password
			}
			if password == "" || strings.ContainsAny(password, "\r\n\x00") {
				logSSHTransportFailure(ctx, host.ID, "sudo_authentication", probeErr)
				return ErrRemote
			}
			command = sudoFramedCommand
			input = io.MultiReader(strings.NewReader(password+"\n"), input)
		}
	}
	session, err := client.NewSession()
	if err != nil {
		logSSHTransportFailure(ctx, host.ID, "session", err)
		return ErrRemote
	}
	defer session.Close()
	if output == nil {
		output = io.Discard
	}
	session.Stdout, session.Stderr, session.Stdin = output, output, input
	if err := session.Start(command); err != nil {
		logSSHTransportFailure(ctx, host.ID, "start", err)
		return ErrRemote
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			logSSHTransportFailure(ctx, host.ID, "wait", err)
			var remoteExit *ssh.ExitError
			if errors.As(err, &remoteExit) {
				cause := ErrRemote
				switch remoteExit.ExitStatus() {
				case 42:
					cause = ErrExistingInstallation
				case 43:
					cause = ErrHostBusy
				case 44:
					cause = ErrPreflightFailed
				case 68:
					cause = ErrDownloaderMissing
				}
				return &remoteExitError{code: remoteExit.ExitStatus(), cause: cause}
			}
			return ErrRemote
		}
		return nil
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		logSSHTransportFailure(ctx, host.ID, "cancellation", ctx.Err())
		return ctx.Err()
	}
}

func logSSHTransportFailure(ctx context.Context, hostID, stage string, err error) {
	// 仅记录阶段、主机标识和 SSH 错误；不读取输出、脚本或认证数据。
	slog.ErrorContext(ctx, "K3s 主机 SSH 操作失败", "operation", "k3s_ssh", "host_id", hostID, "stage", stage, "err", err)
}

func (t *sshTransport) Read(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
	output := &limitedOutput{limit: 1024 * 1024}
	err := t.Run(ctx, host, bundle, script, output)
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.exceeded {
		logSSHTransportFailure(ctx, host.ID, "read_limit", errors.New("只读 SSH 结果超过容量限制"))
		return "", ErrRemote
	}
	return output.buffer.String(), err
}

type limitedOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (o *limitedOutput) Write(value []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	size := len(value)
	left := o.limit - o.buffer.Len()
	if left < len(value) {
		o.exceeded = true
		if left > 0 {
			_, _ = o.buffer.Write(value[:left])
		}
		return size, nil
	}
	_, _ = o.buffer.Write(value)
	return size, nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func installerValue(value any) string {
	switch typed := value.(type) {
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(value)
	}
}

func installerCommand(role string, installer map[string]any, scriptPath string) string {
	keys := make([]string, 0, len(installer))
	for key := range installer {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	command := "env"
	for _, key := range keys {
		command += " " + shellQuote(key+"="+installerValue(installer[key]))
	}
	return command + " sh " + scriptPath + " " + role + " --config /etc/rancher/k3s/config.yaml"
}

func previewCommand(host Host, input Input) (string, error) {
	sources, err := sourcesForInput(input)
	if err != nil {
		return "", err
	}
	installer, err := installerForSource(input)
	if err != nil {
		return "", err
	}
	return "# " + host.Name + " (" + host.Address + ")\n" + "curl --fail --location --proto '=https' --proto-redir '=https' " + shellQuote(sources.InstallerURL) + " -o /tmp/edo-k3s-install.sh\nsudo " + installerCommand(host.Role, RedactOptions(installer), "/tmp/edo-k3s-install.sh"), nil
}

func binaryPath(installer map[string]any) string {
	directory, _ := installer["INSTALL_K3S_BIN_DIR"].(string)
	if directory == "" {
		directory = "/usr/local/bin"
	}
	return path.Join(directory, "k3s")
}

func preloadedBinary(installer map[string]any) bool {
	return installer["INSTALL_K3S_SKIP_DOWNLOAD"] == true || installer["INSTALL_K3S_BIN_DIR_READ_ONLY"] == true
}

func preflightScript(installer map[string]any, options map[string]any, ownedLock ...bool) string {
	dataDir, _ := options["data-dir"].(string)
	if dataDir == "" {
		dataDir = "/var/lib/rancher/k3s"
	}
	unitDir, _ := installer["INSTALL_K3S_SYSTEMD_DIR"].(string)
	if unitDir == "" {
		unitDir = "/etc/systemd/system"
	}
	paths := []string{"/etc/rancher/k3s", "/etc/rancher/rke2", "/var/lib/rancher/rke2", "/etc/kubernetes/kubelet.conf", "/etc/kubernetes/admin.conf", "/etc/kubernetes/manifests", "/var/lib/kubelet", "/var/snap/microk8s", dataDir}
	if !preloadedBinary(installer) {
		paths = append(paths, binaryPath(installer))
	}
	var script strings.Builder
	script.WriteString("set -eu\n[ \"$(id -u)\" = 0 ]\n[ \"$(uname -s)\" = Linux ]\ncommand -v curl >/dev/null\ncommand -v base64 >/dev/null\n")
	if len(ownedLock) == 0 || !ownedLock[0] {
		script.WriteString("if [ -d /run/edo-k3s-install.lock ]; then printf 'EDO_K3S_BUSY\\n'; exit 43; fi\n")
	}
	for _, target := range paths {
		script.WriteString("if [ -e " + shellQuote(target) + " ] || [ -L " + shellQuote(target) + " ]; then printf 'EDO_K3S_EXISTS\\n'; exit 42; fi\n")
	}
	if preloadedBinary(installer) {
		binary := shellQuote(binaryPath(installer))
		version, _ := installer["INSTALL_K3S_VERSION"].(string)
		script.WriteString("[ -x " + binary + " ]\n[ \"$(timeout 3 " + binary + " --version | head -n 1 | awk '{print $3}')\" = " + shellQuote(version) + " ]\n")
		script.WriteString("if command -v rke2 >/dev/null 2>&1; then printf 'EDO_K3S_EXISTS\\n'; exit 42; fi\n")
	} else {
		script.WriteString("if command -v k3s >/dev/null 2>&1 || command -v rke2 >/dev/null 2>&1; then printf 'EDO_K3S_EXISTS\\n'; exit 42; fi\n")
	}
	for _, directory := range []string{unitDir, "/etc/systemd/system", "/usr/lib/systemd/system", "/etc/init.d"} {
		script.WriteString("for unit in " + shellQuote(directory) + "/k3s* " + shellQuote(directory) + "/rke2* " + shellQuote(directory) + "/kubelet.service; do if [ -e \"$unit\" ]; then printf 'EDO_K3S_EXISTS\\n'; exit 42; fi; done\n")
	}
	script.WriteString("if edo_processes=$(timeout 2 ps -eo comm= 2>/dev/null); then :; else printf 'EDO_K3S_PROCESS_CHECK_FAILED\\n'; exit 44; fi\n")
	script.WriteString("if printf '%s\\n' \"$edo_processes\" | awk '$1 ~ /^(k3s|rke2|kubelet|kube-apiserver|kube-controller|kube-scheduler|microk8s)(-|$)/ {found=1} END{exit !found}'; then printf 'EDO_K3S_EXISTS\\n'; exit 42; else edo_process_status=$?; if [ \"$edo_process_status\" != 1 ]; then printf 'EDO_K3S_PROCESS_CHECK_FAILED\\n'; exit 44; fi; fi\nunset edo_processes edo_process_status\n")
	script.WriteString("printf 'EDO_K3S_PREFLIGHT_OK\\n'\n")
	return script.String()
}

func runtimeOptions(base map[string]any, host Host, installationID, endpoint, token string) map[string]any {
	result := map[string]any{}
	for key, value := range base {
		result[key] = value
	}
	labels := stringList(result["node-label"])
	labels = append(labels, "edo.dev/installation="+installationID, "edo.dev/host="+host.ID)
	result["node-label"] = labels
	result["token"] = token
	if host.Role == "server" {
		result["write-kubeconfig"] = "/etc/rancher/k3s/k3s.yaml"
		result["write-kubeconfig-mode"] = "0600"
		parsed, _ := url.Parse(endpoint)
		sans := stringList(result["tls-san"])
		if !contains(sans, parsed.Hostname()) {
			sans = append(sans, parsed.Hostname())
		}
		result["tls-san"] = sans
	} else {
		result["server"] = endpoint
	}
	return result
}

func stringList(value any) []string {
	result := []string{}
	switch values := value.(type) {
	case []string:
		return append(result, values...)
	case []any:
		for _, item := range values {
			result = append(result, fmt.Sprint(item))
		}
	case string:
		if values != "" {
			result = append(result, values)
		}
	}
	return result
}
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func installScript(host Host, input Input, installationID, token string) (string, error) {
	sources, err := sourcesForInput(input)
	if err != nil {
		return "", err
	}
	version, _ := input.Installer["INSTALL_K3S_VERSION"].(string)
	if _, valid := parseK3sVersion(version); !valid {
		return "", &ValidationError{Message: "一键安装需要固定精确 K3s 版本，不能仅指定发布通道"}
	}
	options := input.AgentOptions
	if host.Role == "server" {
		options = input.ServerOptions
	}
	dataDir, _ := options["data-dir"].(string)
	if dataDir != "" && (!path.IsAbs(dataDir) || path.Clean(dataDir) != dataDir || dataDir == "/") {
		return "", ErrInvalidInput
	}
	final := runtimeOptions(options, host, installationID, input.Endpoint, token)
	if isServerJoin(input) {
		if host.Role != "server" || input.Endpoint == "" || token == "" || input.ServerToken == "" {
			return "", ErrPreflightRequired
		}
		final["server"] = input.Endpoint
	}
	config, err := yaml.Marshal(final)
	if err != nil {
		return "", ErrInvalidInput
	}
	installer, err := installerForSource(input)
	if err != nil {
		return "", err
	}
	// 只负责传递配置和调用原版官方安装器，不额外探测或安装宿主机依赖。
	script := "set -eu\numask 077\nif ! mkdir -m 700 /run/edo-k3s-install.lock; then printf 'EDO_K3S_BUSY\\n'; exit 43; fi\nwork=''\ncleanup() { if [ -n \"$work\" ]; then rm -f \"$work/install.sh\"; rmdir \"$work\"; fi; rmdir /run/edo-k3s-install.lock; }\ntrap cleanup EXIT\ntrap 'exit 130' HUP INT TERM\nwork=$(mktemp -d /tmp/edo-k3s-install.XXXXXX)\nprintf 'EDO_K3S_STAGE\\tdownload\\n'\n"
	script += "if command -v curl >/dev/null 2>&1; then\ncurl --fail --silent --show-error --location " + shellQuote(sources.InstallerURL) + " -o \"$work/install.sh\"\nelif command -v wget >/dev/null 2>&1; then\nwget -qO \"$work/install.sh\" " + shellQuote(sources.InstallerURL) + "\nelse\nprintf 'EDO_K3S_DOWNLOADER_MISSING\\n'; exit 68\nfi\n"
	script += "printf 'EDO_K3S_STAGE\\tconfiguration\\n'\nmkdir -p /etc/rancher/k3s\nif [ -e /etc/rancher/k3s/config.yaml ] || [ -L /etc/rancher/k3s/config.yaml ]; then printf 'EDO_K3S_EXISTS\\n'; exit 42; fi\n(set -C; printf '%s' " + shellQuote(string(config)) + " > /etc/rancher/k3s/config.yaml)\n"
	script += "printf 'EDO_K3S_STAGE\\tinstaller\\n'\n" + installerCommand(host.Role, installer, "\"$work/install.sh\"") + "\n"
	return script, nil
}

func classifyRemoteRead(output string, err error) error {
	if strings.Contains(output, "EDO_K3S_BUSY") || errors.Is(err, ErrHostBusy) {
		return ErrHostBusy
	}
	if strings.Contains(output, "EDO_K3S_EXISTS") {
		return ErrExistingInstallation
	}
	if err != nil {
		if errors.Is(err, ErrExistingInstallation) {
			return ErrExistingInstallation
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrRemote
	}
	if !strings.Contains(output, "EDO_K3S_PREFLIGHT_OK") {
		return ErrRemote
	}
	return nil
}
