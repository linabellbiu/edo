package k3s

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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

func (t *sshTransport) Run(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string, output io.Writer) error {
	endpoint := (&url.URL{Scheme: "ssh", User: url.User(host.SSHUsername), Host: net.JoinHostPort(host.Address, strconv.Itoa(host.SSHPort))}).String()
	connector, err := sshclient.NewConnector(endpoint, bundle, host.SSHHostKeyFingerprint, t.timeout)
	if err != nil {
		return ErrRemote
	}
	client, err := connector.ConnectPinned(ctx)
	if err != nil {
		return ErrRemote
	}
	defer client.Close()
	command := "sh -se"
	input := io.Reader(strings.NewReader(script))
	if host.SSHUsername != "root" {
		if !bundle.UseSudo {
			return ErrRemote
		}
		probe, err := client.NewSession()
		if err != nil {
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
			if password == "" {
				return ErrRemote
			}
			command = "sudo -S -p '' -- sh -se"
			input = io.MultiReader(strings.NewReader(password+"\n"), input)
		}
	}
	session, err := client.NewSession()
	if err != nil {
		return ErrRemote
	}
	defer session.Close()
	if output == nil {
		output = io.Discard
	}
	session.Stdout, session.Stderr, session.Stdin = output, output, input
	if err := session.Start(command); err != nil {
		return ErrRemote
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			var remoteExit *ssh.ExitError
			if errors.As(err, &remoteExit) {
				switch remoteExit.ExitStatus() {
				case 42:
					return ErrExistingInstallation
				case 43:
					return ErrHostBusy
				}
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
		return ctx.Err()
	}
}

func (t *sshTransport) Read(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
	output := &limitedOutput{limit: 1024 * 1024}
	err := t.Run(ctx, host, bundle, script, output)
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.exceeded {
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

func previewCommand(host Host, installer map[string]any) string {
	return "# " + host.Name + " (" + host.Address + ")\n" + "curl --fail --location https://get.k3s.io -o /tmp/edo-k3s-install.sh\nsudo " + installerCommand(host.Role, RedactOptions(installer), "/tmp/edo-k3s-install.sh")
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
	paths := []string{"/etc/rancher/k3s", "/etc/rancher/rke2", "/etc/kubernetes/kubelet.conf", "/var/lib/kubelet", "/var/snap/microk8s", dataDir}
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
		script.WriteString("[ -x " + binary + " ]\n[ \"$(" + binary + " --version | head -n 1 | awk '{print $3}')\" = " + shellQuote(version) + " ]\n")
		script.WriteString("if command -v rke2 >/dev/null 2>&1; then printf 'EDO_K3S_EXISTS\\n'; exit 42; fi\n")
	} else {
		script.WriteString("if command -v k3s >/dev/null 2>&1 || command -v rke2 >/dev/null 2>&1; then printf 'EDO_K3S_EXISTS\\n'; exit 42; fi\n")
	}
	for _, directory := range []string{unitDir, "/etc/systemd/system", "/usr/lib/systemd/system", "/etc/init.d"} {
		script.WriteString("for unit in " + shellQuote(directory) + "/k3s* " + shellQuote(directory) + "/rke2* " + shellQuote(directory) + "/kubelet.service; do if [ -e \"$unit\" ]; then printf 'EDO_K3S_EXISTS\\n'; exit 42; fi; done\n")
	}
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
	options := input.AgentOptions
	if host.Role == "server" {
		options = input.ServerOptions
	}
	dataDir, _ := options["data-dir"].(string)
	if dataDir != "" && (!path.IsAbs(dataDir) || path.Clean(dataDir) != dataDir || dataDir == "/") {
		return "", ErrInvalidInput
	}
	config, err := yaml.Marshal(runtimeOptions(options, host, installationID, input.Endpoint, token))
	if err != nil {
		return "", ErrInvalidInput
	}
	encoded := base64.StdEncoding.EncodeToString(config)
	// Repeat the read-only guard in the same privileged shell immediately before
	// writing configuration; a successful old preview is not install permission.
	script := "set -eu\n[ \"$(id -u)\" = 0 ]\nif ! mkdir -m 700 /run/edo-k3s-install.lock; then printf 'EDO_K3S_BUSY\\n'; exit 43; fi\nwork=''\ncleanup() { if [ -n \"$work\" ]; then rm -f \"$work/install.sh\"; rmdir \"$work\"; fi; rmdir /run/edo-k3s-install.lock; }\ntrap cleanup EXIT\ntrap 'exit 130' HUP INT TERM\n"
	script += preflightScript(input.Installer, options, true) + "\nwork=$(mktemp -d /tmp/edo-k3s-install.XXXXXX)\n"
	script += "curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --connect-timeout 15 --max-time 180 --retry 3 https://get.k3s.io -o \"$work/install.sh\"\n"
	script += "install -d -m 700 /etc/rancher/k3s\nprintf '%s' " + shellQuote(encoded) + " | base64 -d > /etc/rancher/k3s/config.yaml\nchmod 600 /etc/rancher/k3s/config.yaml\n"
	script += installerCommand(host.Role, input.Installer, "\"$work/install.sh\"") + "\n"
	if host.Role == "server" {
		script += "ready=false\nfor attempt in $(seq 1 90); do if " + shellQuote(binaryPath(input.Installer)) + " kubectl --kubeconfig /etc/rancher/k3s/k3s.yaml --request-timeout=3s get --raw=/readyz >/dev/null 2>&1; then ready=true; break; fi; sleep 2; done\n\"$ready\"\n"
	}
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
