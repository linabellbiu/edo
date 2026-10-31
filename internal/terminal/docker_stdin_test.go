package terminal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"edo/internal/terminalwire"
)

// 此测试覆盖正式 SDK 与内部 PTY 协议接缝；真实 isatty、作业控制和回收须独立 Linux 验收。
func TestDockerPTYProtocolPreservesBinaryInputThroughNonTTYHijack(t *testing.T) {
	id, execID := strings.Repeat("e", 64), strings.Repeat("a", 64)
	payload := []byte("EDO SDK input\x00\x1b[31m\xff\r")
	ack := []byte("EDO PTY output\r\n")
	received := make(chan []byte, 1)
	peerClosed := make(chan struct{})
	var inspections, creates, attaches, resizes atomic.Int32
	var stopped atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.55")
			_, _ = io.WriteString(w, "OK")
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/"+id+"/json"):
			inspections.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "State": map[string]any{"Running": true, "Pid": 101, "StartedAt": "2026-10-31T00:00:00Z"}, "HostConfig": map[string]any{"Init": true, "PidMode": ""}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/exec/"+execID+"/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ID": execID, "ContainerID": id, "Running": !stopped.Load(), "ExitCode": 0})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/"+id+"/exec"):
			creates.Add(1)
			var v container.ExecCreateRequest
			if json.NewDecoder(r.Body).Decode(&v) != nil || inspections.Load() == 0 || v.Tty || v.ConsoleSize != nil || !v.AttachStdin || !v.AttachStdout || !v.AttachStderr || v.Privileged || len(v.Env) != 0 || v.WorkingDir != "" || v.User != "" || len(v.Cmd) != 2 || v.Cmd[0] != terminalwire.HelperPath || v.Cmd[1] != "controller" {
				t.Error("未固定非 TTY 协议载体，或改写容器原环境")
				http.Error(w, "invalid", 400)
				return
			}
			_, _ = fmt.Fprintf(w, `{"Id":%q}`, execID)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec/"+execID+"/start"):
			if attaches.Add(1) != 1 {
				t.Error("重复连接")
				return
			}
			var v container.ExecStartRequest
			if json.NewDecoder(r.Body).Decode(&v) != nil || v.Tty || v.Detach || v.ConsoleSize != nil {
				t.Error("外层不应创建伪 TTY")
				return
			}
			conn, buffer, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			defer close(peerClosed)
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
			if buffer.Flush() != nil {
				return
			}
			reader := bufio.NewReaderSize(buffer.Reader, terminalwire.MaxLine)
			out := dockerTestStdout{conn}
			start, err := terminalwire.Read(reader)
			if err != nil || !terminalwire.ValidStart(start) || start.Shell != "sh" || start.Rows != 24 || start.Cols != 100 || start.MaxMS != int64(time.Hour/time.Millisecond) {
				t.Error("启动协议没有绑定服务端时长与内部 PTY 尺寸")
				return
			}
			if terminalwire.Write(out, terminalwire.Message{Type: "ready", NativeABI: true}) != nil {
				return
			}
			var input []byte
			for {
				m, err := terminalwire.Read(reader)
				if err != nil {
					t.Error("协议流意外结束", err)
					return
				}
				if !terminalwire.ValidControl(m) {
					t.Error("非法控制帧")
					return
				}
				switch m.Type {
				case "ping":
				case "resize":
					if m.Rows != 42 || m.Cols != 132 {
						t.Error("内部 PTY resize 错误")
					}
					resizes.Add(1)
				case "data":
					input = append(input, m.Data...)
					if len(input) == len(payload) {
						received <- input
						if terminalwire.Write(out, terminalwire.Message{Type: "data", Data: ack}) != nil {
							return
						}
					}
				case "close":
					stopped.Store(true)
					_ = terminalwire.Write(out, terminalwire.Message{Type: "closed", Released: true, Clean: true, Tracked: 5})
					return
				}
			}
		default:
			t.Errorf("意外 SDK 请求：%s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	service, endpointID := dockerSDKService(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := service.OpenDocker(ctx, endpointID, id, "sh", Size{Columns: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if inspections.Load() < 2 {
		t.Fatal("升级后未重新核验原资源身份")
	}
	for _, part := range [][]byte{payload[:3], payload[3 : len(payload)-1], payload[len(payload)-1:]} {
		if n, err := session.Write(part); err != nil || n != len(part) {
			t.Fatal(n, err)
		}
	}
	if err := session.Resize(ctx, Size{Columns: 132, Rows: 42}); err != nil {
		t.Fatal(err)
	}
	select {
	case actual := <-received:
		if !bytes.Equal(actual, payload) {
			t.Fatal("协议改变了输入")
		}
	case <-ctx.Done():
		t.Fatal("未收到完整输入")
	}
	got := make([]byte, len(ack))
	if _, err := io.ReadFull(session, got); err != nil || !bytes.Equal(got, ack) {
		t.Fatal("Docker 8 字节复用头污染了内部 PTY 输出", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peerClosed:
	case <-ctx.Done():
		t.Fatal("正式 SDK 未关闭")
	}
	if creates.Load() != 1 || attaches.Load() != 1 || resizes.Load() != 1 || !stopped.Load() {
		t.Fatal("SDK 调用或停止证据不完整")
	}
}
