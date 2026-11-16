package dockerengine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScriptContainerFailurePreservesTerminalTailAndExitCode(t *testing.T) {
	for _, scenario := range []string{"exit", "broken-output", "wait-error"} {
		t.Run(scenario, func(t *testing.T) {
			started := make(chan struct{})
			var startOnce sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("OSType", "linux")
				if pushStreamTestPing(w, r) {
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/images/alpine:3.22/json"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:" + strings.Repeat("a", 64), "Os": "linux", "Architecture": "amd64"})
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "output-fixture"})
				case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/containers/output-fixture/archive"):
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusOK)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/output-fixture/start"):
					startOnce.Do(func() { close(started) })
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/output-fixture/wait"):
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					select {
					case <-started:
					case <-r.Context().Done():
						return
					}
					if scenario == "wait-error" {
						_, _ = io.WriteString(w, "fixture wait response failure")
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": 7})
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/output-fixture/attach"):
					conn, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					_, _ = buffer.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
					_ = buffer.Flush()
					select {
					case <-started:
					case <-time.After(3 * time.Second):
						t.Error("容器未启动")
						return
					}
					if scenario == "wait-error" {
						time.Sleep(75 * time.Millisecond)
					}
					for i, text := range []string{"program stdout\n", "program fatal tail without newline"} {
						header := []byte{byte(i + 1), 0, 0, 0, 0, 0, 0, 0}
						binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
						_, _ = conn.Write(append(header, []byte(text)...))
					}
					if scenario == "broken-output" {
						_, _ = conn.Write([]byte{99, 0, 0, 0, 0, 0, 0, 1, 'x'})
					}
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/output-fixture"):
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("额外Docker操作: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			service, _ := pushStreamTestService(t, server.URL)
			var stdout, stderr bytes.Buffer
			result, err := service.RunScriptContainer(t.Context(), ScriptContainerInput{Image: "alpine:3.22", Script: "fixture command", SourceDirectory: t.TempDir(), WorkingDirectory: ".", Timeout: 30 * time.Second, Stdout: &stdout, Stderr: &stderr})
			if err == nil {
				t.Fatal("真实Docker失败协议被误判为成功")
			}
			if stdout.String() != "program stdout\n" || !strings.Contains(stderr.String(), "program fatal tail without newline") {
				t.Fatalf("失败时丢失终端尾部: stdout=%q stderr=%q err=%v", stdout.String(), stderr.String(), err)
			}
			if scenario == "wait-error" {
				if result.ExitCode != -1 {
					t.Fatalf("未获得退出码不应伪造为0: %+v", result)
				}
				return
			}
			var exit *ScriptContainerExitError
			if result.ExitCode != 7 || !errors.As(err, &exit) || exit.ExitCode != 7 {
				t.Fatalf("真实退出码丢失: %+v %v", result, err)
			}
			if scenario == "broken-output" && !strings.Contains(err.Error(), "读取脚本输出失败") {
				t.Fatalf("输出协议损坏被忽略: %v", err)
			}
		})
	}
}
