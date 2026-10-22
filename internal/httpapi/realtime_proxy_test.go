package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"edo/internal/model"
)

func TestPipelineLogsThroughRealViteProxy(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未安装 Node.js，跳过 Vite 集成测试")
	}
	webDirectory, err := filepath.Abs("../../web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(webDirectory, "node_modules/vite/package.json")); err != nil {
		t.Skip("未安装前端依赖，跳过 Vite 集成测试")
	}
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	run := seedRealtimeRun(t, db, model.PipelineRunSucceeded)
	if err := db.Create(&model.PipelineRunLog{PipelineRunID: run.ID, Stage: "build", Level: "info", Message: "代理日志输出验证"}).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "tests/viteWebSocketProxy.mjs", server.URL)
	command.Dir = webDirectory
	var output bytes.Buffer
	command.Stderr = &output
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = stdin.Write([]byte("stop\n"))
		_ = stdin.Close()
		if err := command.Wait(); err != nil {
			t.Errorf("Vite 代理测试未正常结束: %v\n%s", err, output.String())
		}
		if strings.Contains(output.String(), "EPIPE") || strings.Contains(output.String(), "ws proxy") {
			t.Errorf("真实代理仍发生 WebSocket 错误:\n%s", output.String())
		}
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("Vite 未返回监听地址")
	}
	proxyURL := scanner.Text()
	cookie := realtimeLogin(t, router)
	for range 5 {
		connection := dialRealtime(t, proxyURL, "/api/v1/pipeline-runs/"+run.ID+"/logs/ws", pipelineLogSubprotocol, cookie)
		logCount := 0
		for {
			var event pipelineLogEvent
			if err := connection.ReadJSON(&event); err != nil {
				t.Fatal(err)
			}
			logCount += len(event.Logs)
			if event.Type == "complete" {
				break
			}
		}
		if logCount != 1 {
			t.Fatalf("代理日志数量不正确: %d", logCount)
		}
		_, _, err := connection.ReadMessage()
		if !websocket.IsCloseError(err, 1000) {
			t.Fatalf("代理完成关闭握手失败: %v", err)
		}
		connection.Close()
	}
	if err := db.Model(&run).Update("status", model.PipelineRunRunning).Error; err != nil {
		t.Fatal(err)
	}
	for range 5 {
		connection := dialRealtime(t, proxyURL, "/api/v1/pipeline-runs/"+run.ID+"/logs/ws", pipelineLogSubprotocol, cookie)
		var event pipelineLogEvent
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if err := connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "用户关闭"), time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		_, _, err := connection.ReadMessage()
		if !websocket.IsCloseError(err, 1000) {
			t.Fatalf("代理客户端关闭握手失败: %v", err)
		}
		connection.Close()
	}
}
