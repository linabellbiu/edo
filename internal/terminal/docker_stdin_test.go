package terminal

import (
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

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"

	"edo/internal/config"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/testutil"
)

// 此测试只覆盖正式 SDK 的 HTTP 101 接缝，不替代真实 Docker 或浏览器终端验收。
func TestDockerTTYStdinPreservesBinaryInputThroughMobyHijack(t *testing.T) {
	containerID := strings.Repeat("e", 64)
	const execID = "tty-stdin-exec"
	payload := []byte("EDO SDK stdin fixture\x00\x1b[31m\xff\r")
	ack := []byte("EDO TTY ACK\r\n")
	wantSize := [2]uint{24, 100}
	received := make(chan []byte, 1)
	peerClosed := make(chan struct{})
	var inspectCalls, createCalls, attachCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.55")
			_, _ = io.WriteString(w, "OK")
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/"+containerID+"/json"):
			inspectCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": containerID, "State": map[string]any{"Running": true}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/"+containerID+"/exec"):
			createCalls.Add(1)
			var input container.ExecCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error("无法读取终端创建请求", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if inspectCalls.Load() == 0 || !input.Tty || !input.AttachStdin || !input.AttachStdout || !input.AttachStderr || input.Privileged || len(input.Cmd) != 1 || input.Cmd[0] != "/bin/sh" || input.ConsoleSize == nil || *input.ConsoleSize != wantSize {
				t.Error("终端创建未固定目标、TTY、标准流、Shell 或尺寸")
				http.Error(w, "invalid terminal config", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"Id":%q}`, execID)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec/"+execID+"/start"):
			if attachCalls.Add(1) != 1 {
				t.Error("终端连接发生重复提交")
				http.Error(w, "duplicate attach", http.StatusConflict)
				return
			}
			var input container.ExecStartRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error("无法读取终端连接请求", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if createCalls.Load() != 1 || !input.Tty || input.Detach || input.ConsoleSize == nil || *input.ConsoleSize != wantSize || !strings.EqualFold(r.Header.Get("Connection"), "Upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "tcp") {
				t.Error("正式 SDK 未按交互 TTY 发起 TCP 升级")
				http.Error(w, "invalid attach", http.StatusBadRequest)
				return
			}
			connection, buffer, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error("测试服务无法升级连接", err)
				return
			}
			defer connection.Close()
			defer close(peerClosed)
			if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Error("无法限制测试连接时长", err)
				return
			}
			if _, err := buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n"); err != nil {
				t.Error("无法写入升级响应", err)
				return
			}
			if err := buffer.Flush(); err != nil {
				t.Error("无法发送升级响应", err)
				return
			}
			// 按单字节读取，避免把 TCP 分段误当成浏览器二进制消息边界。
			actual := make([]byte, len(payload))
			for offset := range actual {
				if _, err := io.ReadFull(buffer.Reader, actual[offset:offset+1]); err != nil {
					t.Error("未收齐固定测试输入", err)
					return
				}
			}
			received <- actual
			if _, err := connection.Write(ack); err != nil {
				t.Error("无法返回固定 TTY 确认", err)
				return
			}
			var trailing [1]byte
			if n, err := buffer.Read(trailing[:]); n != 0 || err != io.EOF {
				t.Errorf("终端关闭没有释放升级连接：额外字节=%d，err=%v", n, err)
			}
		default:
			t.Errorf("终端请求未固定到预期目标：%s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	db := testutil.NewDatabase(t)
	manager, err := secret.New("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := model.DockerEndpoint{ID: uuid.NewString(), Name: "docker-tty-stdin", Host: strings.Replace(server.URL, "http://", "tcp://", 1), IsActive: true, CreatedBy: uuid.NewString(), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(dockerengine.NewService(db, manager, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second}), nil, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := service.OpenDocker(ctx, endpoint.ID, containerID, "sh", Size{Columns: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if inspectCalls.Load() < 2 {
		t.Fatal("连接升级后未重新核验完整目标身份")
	}
	for _, part := range [][]byte{payload[:3], payload[3 : len(payload)-1], payload[len(payload)-1:]} {
		if n, err := session.Write(part); err != nil || n != len(part) {
			t.Fatalf("终端未完整写入固定输入分片：n=%d，want=%d，err=%v", n, len(part), err)
		}
	}
	select {
	case actual := <-received:
		if !bytes.Equal(actual, payload) || actual[len(actual)-1] != '\r' {
			t.Fatal("固定二进制输入或末尾回车被改变")
		}
	case <-ctx.Done():
		t.Fatal("升级连接未收到完整固定输入")
	}
	actualAck := make([]byte, len(ack))
	if _, err := io.ReadFull(session, actualAck); err != nil || !bytes.Equal(actualAck, ack) {
		t.Fatal("TTY 返回流被截断或错误添加复用帧头", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peerClosed:
	case <-ctx.Done():
		t.Fatal("关闭终端后升级连接仍未退出")
	}
	if createCalls.Load() != 1 || attachCalls.Load() != 1 {
		t.Fatal("终端创建或连接被重复提交")
	}
}
