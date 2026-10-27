package k3s

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSystemRegistryProbeUsesReadOnlyTrustedHTTPSAndAcceptsOnlyReachableStatuses(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("本机未提供 curl")
	}
	var status atomic.Int32
	var requests, redirected atomic.Int32
	plaintext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer plaintext.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodHead || r.URL.Path != "/v2/" || r.Header.Get("Authorization") != "" {
			t.Error("仓库预检必须是无凭据的只读基础端点请求")
		}
		if status.Load() == http.StatusFound {
			w.Header().Set("Location", plaintext.URL)
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="PRIVATE_AUTH_ENDPOINT"`)
		w.WriteHeader(int(status.Load()))
		_, _ = io.WriteString(w, "PRIVATE_REGISTRY_BODY")
	}))
	defer server.Close()
	directory := t.TempDir()
	writePreflightCommand(t, directory, "timeout", "shift\nexec \"$@\"\n")
	writePreflightCommand(t, directory, "curl", "exec "+shellQuote(curl)+" \"$@\"\n")
	caPath := filepath.Join(directory, "registry-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	registry := strings.TrimPrefix(server.URL, "https://") + "/team"
	input := Input{Action: "create", SystemImageRegistry: &registry}
	for _, code := range []int{200, 401, 302, 403, 404, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			status.Store(int32(code))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-s")
			command.Stdin = strings.NewReader("set -eu\nedo_check() { printf '%s:%s\\n' \"$1\" \"$2\"; }\n" + systemRegistryProbeScript(input))
			command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "CURL_CA_BUNDLE="+caPath, "NO_PROXY=127.0.0.1,localhost")
			output, err := command.CombinedOutput()
			want := "system_registry:failed\n"
			if code == 200 || code == 401 {
				want = "system_registry:passed\n"
			}
			if err != nil || string(output) != want {
				t.Fatalf("HTTPS 基础端点状态判断或输出保护错误: code=%d output=%q err=%v", code, output, err)
			}
		})
	}
	if requests.Load() != 6 || redirected.Load() != 0 {
		t.Fatalf("探测重定向或请求次数异常: selected=%d plaintext=%d", requests.Load(), redirected.Load())
	}
	for _, code := range []int{200, 401} {
		status.Store(int32(code))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		command := exec.CommandContext(ctx, "sh", "-s")
		command.Stdin = strings.NewReader("edo_check() { printf '%s:%s\\n' \"$1\" \"$2\"; }\n" + systemRegistryProbeScript(input))
		command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "CURL_CA_BUNDLE="+filepath.Join(directory, "missing-ca.pem"), "NO_PROXY=127.0.0.1,localhost")
		output, err := command.CombinedOutput()
		cancel()
		if err != nil || string(output) != "system_registry:failed\n" {
			t.Fatal("不可信 HTTPS 证书仍通过仓库预检", string(output), err)
		}
	}
}

func TestSystemRegistryProbeDoesNotProbeUnselectedOrInheritedDefault(t *testing.T) {
	for _, input := range []Input{{Action: "create"}, {Action: "join"}} {
		if script := systemRegistryProbeScript(input); script != "edo_check system_registry warning\n" || strings.Contains(script, "curl") {
			t.Fatal("默认或未知继承仓库被伪装已探测", script)
		}
	}
}

func TestSourcesChinaChannelProbeNeverFallsBackToUnselectedUpdateServer(t *testing.T) {
	input := Input{DownloadSource: "official_cn", Installer: map[string]any{"INSTALL_K3S_CHANNEL": "v1.37", "INSTALL_K3S_CHANNEL_URL": "https://unselected.example.test/channels"}}
	script := preflightProbeScript(Host{Role: "agent"}, input, false)
	if !strings.Contains(script, chinaArtifactURL+"/channels/v1.37") || strings.Contains(script, "update.k3s.io") || strings.Contains(script, "github.com") || strings.Contains(script, "unselected.example.test") || strings.Contains(script, "https://get.k3s.io") {
		t.Fatal("国内频道预检使用了未选择的其他来源")
	}
}

func TestSourcesFrozenEndpointsDriveInstallerEnvironmentAndPreflight(t *testing.T) {
	frozen := &Sources{DownloadSource: "official_cn", InstallerURL: "https://frozen.example.test/k3s/install.sh", ArtifactURL: "https://frozen.example.test/k3s", Mirror: "cn"}
	input := Input{Action: "create", DownloadSource: "official_cn", sources: frozen, Installer: map[string]any{"INSTALL_K3S_VERSION": CatalogK3sVersion}}
	script, err := installScript(Host{Role: "server"}, input, "d67b480f-35d0-4af4-bb11-9117b517208f", "fixture-only-token")
	if err != nil {
		t.Fatal(err)
	}
	installer, err := installerForSource(input)
	if err != nil || installer["INSTALL_K3S_MIRROR_URL"] != "https://frozen.example.test" {
		t.Fatal("国内安装器环境没有采用已冻结来源", installer, err)
	}
	stage, err := serverInstallerDownloadScript("d67b480f-35d0-4af4-bb11-9117b517208f", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, generated := range []string{script, stage, preflightProbeScript(Host{Role: "server"}, input, false)} {
		if !strings.Contains(generated, "https://frozen.example.test") || strings.Contains(generated, chinaMirrorURL) || strings.Contains(generated, officialInstallerURL) || strings.Contains(generated, "github.com") {
			t.Fatal("计划冻结后仍混入当前默认来源")
		}
	}
	if channel := sourceChannelURL(frozen, nil); channel != "https://frozen.example.test/k3s/channels" {
		t.Fatal("频道源未采用冻结地址", channel)
	}
}
