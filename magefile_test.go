//go:build mage

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseStartOptions(t *testing.T) {
	options, err := parseStartOptions([]string{"--dev", "--server"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.dev || !options.server || options.docker || options.web {
		t.Fatalf("参数解析结果不正确: %+v", options)
	}
}

func TestParseStartOptionsRejectsUnknownArgument(t *testing.T) {
	if _, err := parseStartOptions([]string{"--unknown"}); err == nil {
		t.Fatal("未知参数应返回错误")
	}
}

func TestSelectedComponents(t *testing.T) {
	tests := []struct {
		name       string
		server     bool
		web        bool
		wantServer bool
		wantWeb    bool
	}{
		{name: "默认启动全部", wantServer: true, wantWeb: true},
		{name: "只启动后端", server: true, wantServer: true},
		{name: "只启动 Web", web: true, wantWeb: true},
		{name: "同时指定仍启动全部", server: true, web: true, wantServer: true, wantWeb: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotServer, gotWeb := selectedComponents(test.server, test.web)
			if gotServer != test.wantServer || gotWeb != test.wantWeb {
				t.Fatalf("selectedComponents() = (%t, %t), want (%t, %t)", gotServer, gotWeb, test.wantServer, test.wantWeb)
			}
		})
	}
}

func TestSetDevelopmentSecretsKeyUsesComposeDefault(t *testing.T) {
	t.Setenv("ZRT_SECRETS_KEY", "")
	if err := setDevelopmentSecretsKey(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ZRT_SECRETS_KEY"); got != developmentSecretsKey {
		t.Fatalf("开发密钥 = %q, want %q", got, developmentSecretsKey)
	}
}

func TestSetDevelopmentSecretsKeyPreservesExplicitValue(t *testing.T) {
	const configured = "user-configured-key"
	t.Setenv("ZRT_SECRETS_KEY", configured)
	if err := setDevelopmentSecretsKey(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ZRT_SECRETS_KEY"); got != configured {
		t.Fatalf("显式配置被覆盖: %q", got)
	}
}

func TestDevelopmentSecretsKeyMatchesCompose(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("deploy", "compose.dev.yml"))
	if err != nil {
		t.Fatal(err)
	}
	expected := "${ZRT_SECRETS_KEY:-" + developmentSecretsKey + "}"
	if !strings.Contains(string(content), expected) {
		t.Fatal("Mage 与开发 Compose 的默认加密密钥不一致")
	}
}

func TestCopyEmbeddedWebAssets(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(source, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("ZRT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "assets", "app.js"), []byte("app"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "stale.js"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyEmbeddedWebAssets(source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "assets", "app.js")); err != nil {
		t.Fatalf("内嵌资源未复制: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "stale.js")); !os.IsNotExist(err) {
		t.Fatalf("旧的内嵌资源未清理: %v", err)
	}
}

func TestWaitForLocalServiceReady(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 3 {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stopped, err := waitForLocalServiceReady(context.Background(), localService{
		name:         "测试服务",
		readyURL:     server.URL,
		readyTimeout: time.Second,
	}, make(chan localServiceResult))
	if err != nil {
		t.Fatal(err)
	}
	if stopped != nil {
		t.Fatalf("服务不应提前退出: %+v", stopped)
	}
	if requests.Load() < 3 {
		t.Fatalf("就绪检查次数不足: %d", requests.Load())
	}
}

func TestWaitForLocalServiceReadyReturnsEarlyExit(t *testing.T) {
	results := make(chan localServiceResult, 1)
	results <- localServiceResult{name: "后端", err: context.Canceled}

	stopped, err := waitForLocalServiceReady(context.Background(), localService{
		name:         "后端",
		readyURL:     "http://127.0.0.1:1",
		readyTimeout: time.Second,
	}, results)
	if err != nil {
		t.Fatal(err)
	}
	if stopped == nil || stopped.name != "后端" || stopped.err != context.Canceled {
		t.Fatalf("未返回提前退出的进程: %+v", stopped)
	}
}

func TestWaitForLocalServiceReadyTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	stopped, err := waitForLocalServiceReady(context.Background(), localService{
		name:         "测试服务",
		readyURL:     server.URL,
		readyTimeout: 50 * time.Millisecond,
	}, make(chan localServiceResult))
	if err == nil {
		t.Fatal("服务未就绪时应返回超时错误")
	}
	if stopped != nil {
		t.Fatalf("超时时不应报告进程退出: %+v", stopped)
	}
}
