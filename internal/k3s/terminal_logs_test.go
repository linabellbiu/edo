package k3s

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/sshclient"
)

func TestTerminalOutputRetainsRealProgressAndRedactsChunkedSecrets(t *testing.T) {
	f, record, hosts := runningProgressFixture(t)
	err := f.service.runHostOperation(f.ctx, record, hosts, 0, "k3d_create", func(_ context.Context, out io.Writer) error {
		for _, part := range []string{
			"EDO_K3S_STAGE\tk3d_create\nPulling image 'registry.example/rancher/k3s:v1'\n",
			"{\"status\":\"Downloading\",\"id\":\"e12abc\",\"progress\":\"1MB/10MB\"}\r",
			"time=2026-11-16T10:00:00Z level=info msg=\"Starting k3s\"\nK3S_TO", "KEN=generated-secret\n",
			"ClusterToken: generated-k3d-secret\nAuthorization: Bearer auth-secret\n",
			"curl: (28) Connection timed out\n[ERROR] fixture-sudo-secret", // 无换行失败尾部
		} {
			if _, err := io.WriteString(out, part); err != nil {
				return err
			}
		}
		return ErrRemote
	}, "generated-secret", "fixture-sudo-secret")
	if !errors.Is(err, ErrRemote) {
		t.Fatal(err)
	}
	var saved model.K3sInstallation
	if err := f.db.First(&saved, "uuid = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Pulling image", `"Downloading"`, `"id":"e12abc"`, "Starting k3s", "curl: (28)", "[已隐藏]"} {
		if !strings.Contains(saved.Logs, want) {
			t.Errorf("缺少真实输出 %q: %s", want, saved.Logs)
		}
	}
	for _, secret := range []string{"generated-secret", "generated-k3d-secret", "auth-secret", "fixture-sudo-secret", "EDO_K3S_", "创建 k3d 容器节点", "仍在执行"} {
		if strings.Contains(saved.Logs, secret) {
			t.Errorf("泄露凭据或混入合成日志: %s", secret)
		}
	}
}

func TestTerminalRedactorCredentialsAndSafeDetails(t *testing.T) {
	r := newInstallationLogRedactor([]string{"exact-password", "escaped\"secret"})
	lines := []string{
		"password=exact-password", `{\"ClusterToken\":\"random-credential\"}`, "--token flag-credential",
		"https://user:uri-password@example.test/path", "Authorization: Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==",
		"K10" + strings.Repeat("a", 64) + "::server:k3s-secret", "client-key-data: base64-key",
		"token: |", "  yaml-block-secret", "visible-again",
		"-----BEGIN OPENSSH PRIVATE KEY-----", "pem-payload", "-----END OPENSSH PRIVATE KEY-----",
		"normal " + "escaped\\\"secret", "\x1b[31mvisible-error\x1b[0m", "docker layer abc: Download complete",
	}
	var result strings.Builder
	for _, line := range lines {
		result.WriteString(r.line(line))
		result.WriteByte('\n')
	}
	for _, secret := range []string{"exact-password", "random-credential", "flag-credential", "uri-password", "QWxhZ", "k3s-secret", "base64-key", "yaml-block-secret", "pem-payload", "escaped\\\"secret", "\x1b"} {
		if strings.Contains(result.String(), secret) {
			t.Errorf("未脱敏 %s: %s", secret, result.String())
		}
	}
	for _, want := range []string{"visible-again", "visible-error", "docker layer abc: Download complete", "example.test/path"} {
		if !strings.Contains(result.String(), want) {
			t.Errorf("正常细节被吞: %s", want)
		}
	}
}

func TestTerminalStreamsPreserveLineBoundariesAndOmitOversizeLines(t *testing.T) {
	var output strings.Builder
	writer := &terminalLineWriter{output: &output, prefix: "[server] "}
	for _, part := range []string{"a", "bc\rdef\n", strings.Repeat("x", 40*1024), "\nlast"} {
		if _, err := writer.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "[server] abc\n[server] def\n") || !strings.HasSuffix(output.String(), "[server] last\n") || strings.Contains(output.String(), strings.Repeat("x", 100)) {
		t.Fatal("分块或超长行处理错误", output.String())
	}
}

func TestTerminalLogsBatchPersistAndRestoreWithoutHeartbeatText(t *testing.T) {
	f, record, hosts := runningProgressFixture(t)
	updates := 0
	if err := f.db.Callback().Update().After("gorm:update").Register("fixture_terminal_batch_updates", func(*gorm.DB) { updates++ }); err != nil {
		t.Fatal(err)
	}
	if err := f.service.runHostOperation(f.ctx, record, hosts, 0, "installer", func(ctx context.Context, out io.Writer) error {
		for i := 0; i < 100; i++ {
			if _, err := fmt.Fprintf(out, "[INFO] layer-%d Download complete\n", i); err != nil {
				return err
			}
		}
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return errors.New("终端输出未在运行期间落库")
			case <-ticker.C:
				var current model.K3sInstallation
				if err := f.db.First(&current, "uuid = ?", record.ID).Error; err != nil {
					return err
				}
				if strings.Contains(current.Logs, "layer-99") {
					return nil
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if updates < 2 || updates > 5 {
		t.Fatalf("100 行终端输出没有批量持久化: %d 次写入", updates)
	}
	result, err := f.service.Get(f.ctx, record.ID)
	if err != nil || !strings.Contains(result.Logs, "layer-99") || strings.Contains(result.Logs, "仍在执行") {
		t.Fatal("刷新后真实输出丢失", err)
	}
}

func TestK3dPreflightRetainsCurlDiagnosticWithoutCredentials(t *testing.T) {
	f, transport, _, input := k3dFixture(t)
	secret := "fixture-private-diagnostic"
	out := "$ curl --head https://github.com/k3d-io/k3d/releases/download/v5.9.0/k3d-linux-amd64\ncurl: (28) Connection timed out\npassword=" + secret + "\n"
	wrapper := &preflightTerminalTransport{Transport: f.service.transport, diagnostic: out}
	f.service.WithTransport(wrapper)
	report, err := f.service.Check(f.ctx, f.actor.ID, input)
	if err != nil || len(report.Hosts) != 1 || !strings.Contains(report.Hosts[0].Logs, "curl: (28)") || strings.Contains(report.Hosts[0].Logs, secret) {
		t.Fatalf("检测实际错误未安全返回: %+v %v", report, err)
	}
	if len(transport.writes) != 0 {
		t.Fatal("检测日志触发写操作")
	}
}

type preflightTerminalTransport struct {
	Transport
	diagnostic string
}

func (t *preflightTerminalTransport) Read(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
	out, err := t.Transport.Read(ctx, host, bundle, script)
	return "EDO_K3S_TERMINAL\t" + base64.StdEncoding.EncodeToString([]byte(t.diagnostic)) + "\n" + out, err
}

func TestK3dLogObserversVerifyExactTargetAndCancelReadOnlyStream(t *testing.T) {
	input := Input{K3d: &K3dOptions{}}
	id := "1ec72535-b388-4165-82d8-10cafb3f8793"
	sources := k3dTerminalObservations(input, id, true)
	if len(sources) != 2 {
		t.Fatal(sources)
	}
	for _, source := range sources {
		if !strings.Contains(source.script, id) || !strings.Contains(source.script, "k3d.cluster") || !strings.Contains(source.script, "exec env -i") || !strings.Contains(source.script, "logs --timestamps --tail 200 --follow") || strings.Contains(source.script, "kubeconfig") || strings.Contains(source.script, "docker pull") {
			t.Fatal("日志观察越过准确目标和只读范围", source.script)
		}
	}
	output := &synchronizedTerminalBuffer{}
	transport := &observationOnlyTransport{started: make(chan struct{}, len(sources)), stopped: make(chan struct{}, len(sources))}
	service := &Service{transport: transport}
	err := service.withTerminalObservations(context.Background(), model.Host{}, sshclient.Bundle{}, sources, output, func(context.Context) error {
		for range sources {
			<-transport.started
		}
		return ErrReadiness
	})
	if !errors.Is(err, ErrReadiness) || len(transport.stopped) != len(sources) || !strings.Contains(output.text(), "Starting k3s") {
		t.Fatal("日志采集改变结果或未取消退出", err)
	}
}

type synchronizedTerminalBuffer struct {
	mu     sync.Mutex
	buffer strings.Builder
}

func (w *synchronizedTerminalBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}
func (w *synchronizedTerminalBuffer) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

type observationOnlyTransport struct {
	Transport
	started, stopped chan struct{}
}

func (t *observationOnlyTransport) Run(ctx context.Context, _ model.Host, _ sshclient.Bundle, _ string, out io.Writer) error {
	_, _ = io.WriteString(out, "2026-11-16T10:00:00.000Z Starting k3s\n")
	t.started <- struct{}{}
	<-ctx.Done()
	t.stopped <- struct{}{}
	return ctx.Err()
}

func TestK3dTerminalShellUsesImmutableContainerIDAndRejectsWrongOwner(t *testing.T) {
	id := "1ec72535-b388-4165-82d8-10cafb3f8793"
	input := Input{K3d: &K3dOptions{}}
	source := k3dTerminalObservations(input, id, false)[0]
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			owner := id
			if !valid {
				owner = "other-installation"
			}
			containerID := strings.Repeat("a", 64)
			docker := "case \"$2\" in\ninspect) printf '%s\\n' " + shellQuote(containerID+" "+owner+" "+k3dResourceName(id)) + ";;\nlogs) printf '%s\\n' \"$*\" >> " + shellQuote(calls) + "; printf '2026-11-16T10:00:00Z Starting k3s\\n';;\n*) exit 90;;\nesac\n"
			writePreflightCommand(t, dir, "docker", docker)
			command := exec.Command("/bin/sh", "-se")
			command.Stdin = strings.NewReader(source.script)
			command.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			read, _ := os.ReadFile(calls)
			if valid {
				if err != nil || !strings.Contains(string(output), "Starting k3s") || !strings.HasSuffix(strings.TrimSpace(string(read)), containerID) || strings.Contains(string(read), source.name) {
					t.Fatalf("没有按不可变 ID 读取真实终端: %v %s", err, output)
				}
			} else if err == nil || len(read) != 0 {
				t.Fatal("归属不匹配仍读取了容器日志")
			}
		})
	}
}

func TestTerminalTimestampedYamlAndSeparateSourcesStayRedacted(t *testing.T) {
	r := newInstallationLogRedactor(nil)
	result := r.line("2026-11-16T10:00:00Z client-key-data: |") + r.line("2026-11-16T10:00:01Z   private-base64-payload") + r.line("2026-11-16T10:00:02Z Startup complete")
	if strings.Contains(result, "private-base64-payload") || !strings.Contains(result, "Startup complete") {
		t.Fatal("时间戳破坏了多行秘密的脱敏")
	}
}
