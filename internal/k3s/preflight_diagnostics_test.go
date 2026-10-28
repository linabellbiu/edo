package k3s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
	"edo/internal/sshclient"
)

func capturePreflightDiagnostics(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}

func TestPreflightDiagnosticsKeepContextAndExcludeRemoteOutput(t *testing.T) {
	for _, mode := range []string{"native", "k3d"} {
		for _, stage := range []string{"load_host", "read"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				fixture := newFixture(t)
				buffer := capturePreflightDiagnostics(t)
				host := Host{ID: fixture.host.ID, Name: fixture.host.Name, Role: "server"}
				if stage == "load_host" {
					host.ID = "missing-fixture-host"
				}
				fixture.service.WithTransport(preflightReadTransport{read: func(context.Context, model.Host, sshclient.Bundle, string) (string, error) {
					return "PRIVATE_REMOTE_OUTPUT", errors.New("fixture transport failure")
				}})
				input := Input{Mode: mode}
				if mode == "k3d" {
					input.K3d = &K3dOptions{}
				}
				report, err := fixture.service.runPreflight(fixture.ctx, input, []Host{host})
				if err == nil && (report == nil || report.Passed) {
					t.Fatal("预检错误必须保持阻断")
				}
				var entry map[string]any
				if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &entry); err != nil {
					t.Fatalf("未记录必要的结构化诊断: %v", err)
				}
				prefix := "k3s"
				if mode == "k3d" {
					prefix = "k3d"
				}
				if entry["operation"] != prefix+"_preflight_"+stage || entry["host_id"] != host.ID || entry["err"] == "" || entry["err"] == nil {
					t.Fatalf("诊断缺少操作、准确主机或原始错误: %+v", entry)
				}
				encoded, marshalErr := json.Marshal(report)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if strings.Contains(buffer.String()+string(encoded), "PRIVATE_REMOTE_OUTPUT") || strings.Contains(string(encoded), "fixture transport failure") {
					t.Fatal("远端输出进入诊断，或内部错误泄露给页面")
				}
			})
		}
	}
}

func TestSSHTransportDiagnosticsKeepStageWithoutCredentialsOrScript(t *testing.T) {
	for _, stage := range []string{"connector", "wait"} {
		t.Run(stage, func(t *testing.T) {
			buffer := capturePreflightDiagnostics(t)
			host := model.Host{ID: "diagnostic-fixture-host", Address: "127.0.0.1", SSHPort: 22, SSHUsername: "root"}
			bundle := sshclient.Bundle{Password: "PRIVATE_AUTH_PASSWORD", PrivateKey: "PRIVATE_SSH_KEY"}
			if stage == "wait" {
				connected, _ := sshPeer(t, false, 42)
				host.Address, host.SSHPort, host.SSHHostKeyFingerprint = connected.Address, connected.SSHPort, connected.SSHHostKeyFingerprint
				bundle = sshclient.Bundle{Password: "fixture-password"}
			}
			transport := &sshTransport{timeout: time.Second}
			err := transport.Run(context.Background(), host, bundle, "PRIVATE_INSTALLATION_SCRIPT", io.Discard)
			if err == nil {
				t.Fatal("模拟失败未被拒绝")
			}
			var entry map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &entry); err != nil {
				t.Fatalf("SSH 缺少内部诊断: %v", err)
			}
			if entry["operation"] != "k3s_ssh" || entry["stage"] != stage || entry["host_id"] != host.ID || entry["err"] == nil || entry["err"] == "" {
				t.Fatalf("SSH 诊断缺少阶段、主机或错误: %+v", entry)
			}
			for _, private := range []string{"PRIVATE_AUTH_PASSWORD", "PRIVATE_SSH_KEY", "PRIVATE_INSTALLATION_SCRIPT", "fixture-password", "installed"} {
				if strings.Contains(buffer.String(), private) {
					t.Fatalf("SSH 诊断包含受保护内容: %s", private)
				}
			}
		})
	}
}
