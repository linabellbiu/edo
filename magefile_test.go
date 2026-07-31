//go:build mage

package main

import (
	"context"
	"encoding/base64"
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

func TestValidateStartSecretsKey(t *testing.T) {
	const validKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

	t.Run("有效密钥", func(t *testing.T) {
		t.Setenv("EDO_SECRETS_KEY", validKey)
		if err := validateStartSecretsKey(true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("只启动 Web", func(t *testing.T) {
		t.Setenv("EDO_SECRETS_KEY", "")
		if err := validateStartSecretsKey(false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("空密钥", func(t *testing.T) {
		t.Setenv("EDO_SECRETS_KEY", "")
		if err := validateStartSecretsKey(true); err == nil {
			t.Fatal("含后端启动时必须拒绝空密钥")
		}
	})

	t.Run("格式错误", func(t *testing.T) {
		t.Setenv("EDO_SECRETS_KEY", "not-a-valid-key")
		if err := validateStartSecretsKey(true); err == nil {
			t.Fatal("含后端启动时必须拒绝无效密钥")
		}
	})
}

func TestLoadEnvironmentFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	if err := os.WriteFile(path, []byte("EDO_MAGE_ENV_TEST=loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("EDO_MAGE_ENV_TEST")
	t.Cleanup(func() { _ = os.Unsetenv("EDO_MAGE_ENV_TEST") })
	if err := loadEnvironmentFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("EDO_MAGE_ENV_TEST"); got != "loaded" {
		t.Fatalf("未从指定 .env 加载配置: %q", got)
	}
	if err := loadEnvironmentFile(filepath.Join(t.TempDir(), ".env")); err == nil {
		t.Fatal("缺少 .env 和 .env.example 时必须返回错误")
	}
}

func TestLoadEnvironmentFileCreatesDefault(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	template := "EDO_MAGE_CREATED_TEST=loaded\nEDO_SECRETS_KEY=\n"
	if err := os.WriteFile(path+".example", []byte(template), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("EDO_MAGE_CREATED_TEST")
	_ = os.Unsetenv("EDO_SECRETS_KEY")
	t.Cleanup(func() {
		_ = os.Unsetenv("EDO_MAGE_CREATED_TEST")
		_ = os.Unsetenv("EDO_SECRETS_KEY")
	})

	if err := loadEnvironmentFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("EDO_MAGE_CREATED_TEST"); got != "loaded" {
		t.Fatalf("未加载自动创建的默认配置: %q", got)
	}
	encoded := os.Getenv("EDO_SECRETS_KEY")
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		t.Fatalf("自动生成的 EDO_SECRETS_KEY 无效: %q", encoded)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "EDO_SECRETS_KEY="+encoded) {
		t.Fatal("自动生成的密钥未持久化到 .env")
	}

	before := string(content)
	if err := loadEnvironmentFile(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatal("再次加载时不得覆盖或轮换已有 .env")
	}
}

func TestFillEnvironmentSecretsKeyRejectsUnsafeTemplate(t *testing.T) {
	for _, template := range []string{
		"EDO_ENV=development\n",
		"EDO_SECRETS_KEY=hard-coded\n",
		"EDO_SECRETS_KEY=\nEDO_SECRETS_KEY=\n",
	} {
		if _, err := fillEnvironmentSecretsKey([]byte(template), "generated"); err == nil {
			t.Fatalf("应拒绝不安全的默认配置模板: %q", template)
		}
	}
}

func TestInitialSecretsKeyUsesProcessEnvironment(t *testing.T) {
	const existing = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	t.Setenv("EDO_SECRETS_KEY", existing)
	got, err := initialSecretsKey()
	if err != nil {
		t.Fatal(err)
	}
	if got != existing {
		t.Fatalf("未复用进程环境中的固定密钥: %q", got)
	}

	t.Setenv("EDO_SECRETS_KEY", "invalid")
	if _, err := initialSecretsKey(); err == nil {
		t.Fatal("不得用随机密钥替代进程环境中的无效密钥")
	}
}

func TestDevelopmentComposeRequiresDotEnvSecretsKey(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("deploy", "compose.dev.yml"))
	if err != nil {
		t.Fatal(err)
	}
	value := string(content)
	if !strings.Contains(value, "${EDO_SECRETS_KEY:?") || strings.Contains(value, "${EDO_SECRETS_KEY:-") {
		t.Fatal("开发 Compose 必须从 .env 读取必填密钥且不得提供硬编码回退")
	}
}

func TestEnvironmentExampleDeclaresRuntimeConnections(t *testing.T) {
	content, err := os.ReadFile(".env.example")
	if err != nil {
		t.Fatal(err)
	}
	value := string(content)
	for _, variable := range []string{
		"EDO_DATABASE_DRIVER=",
		"EDO_DATABASE_DSN=",
		"EDO_REDIS_URL=",
		"EDO_NATS_URL=",
		"EDO_COMPOSE_DATABASE_DRIVER=",
		"EDO_COMPOSE_DATABASE_DSN=",
		"EDO_COMPOSE_REDIS_URL=",
		"EDO_COMPOSE_NATS_URL=",
	} {
		if !strings.Contains(value, variable) {
			t.Fatalf(".env.example 缺少连接配置 %s", strings.TrimSuffix(variable, "="))
		}
	}
}

func TestComposeMapsDotEnvConnections(t *testing.T) {
	for _, path := range []string{"docker-compose.yml", filepath.Join("deploy", "compose.dev.yml")} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		value := string(content)
		for _, variable := range []string{
			"EDO_COMPOSE_DATABASE_DRIVER",
			"EDO_COMPOSE_DATABASE_DSN",
			"EDO_COMPOSE_REDIS_URL",
			"EDO_COMPOSE_NATS_URL",
		} {
			if !strings.Contains(value, "${"+variable+":-") {
				t.Fatalf("%s 未从 .env 映射 %s", path, variable)
			}
		}
	}
}

func TestComposeBuilderAlwaysUsesMTLS(t *testing.T) {
	for _, path := range []string{"docker-compose.yml", filepath.Join("deploy", "compose.dev.yml")} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		value := string(content)
		if !strings.Contains(value, "EDO_DOCKER_BUILDER_HOST: tcp://docker-builder:2376") ||
			!strings.Contains(value, "EDO_DOCKER_BUILDER_TLS_CERT_PATH: /certs/client") {
			t.Fatalf("%s 未固定使用 Docker 构建器 mTLS", path)
		}
		if strings.Contains(value, "EDO_COMPOSE_DOCKER_BUILDER_") {
			t.Fatalf("%s 不得允许 .env 降级 Docker 构建器连接", path)
		}
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
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("EDO"), 0o644); err != nil {
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

func TestWebDependenciesCurrent(t *testing.T) {
	root := t.TempDir()
	lockfile := filepath.Join(root, "package-lock.json")
	if err := os.WriteFile(lockfile, []byte("first lock"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range webDependencyFiles(root) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("installed"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	current, digest, err := webDependenciesCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	if current {
		t.Fatal("没有锁文件摘要时不得把 Web 依赖视为最新")
	}
	if err := os.WriteFile(webDependencyStampPath(root), []byte(digest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	current, _, err = webDependenciesCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Fatal("依赖文件完整且锁文件摘要一致时应视为最新")
	}

	if err := os.WriteFile(lockfile, []byte("second lock"), 0o644); err != nil {
		t.Fatal(err)
	}
	current, digest, err = webDependenciesCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	if current {
		t.Fatal("package-lock.json 变化后必须重新安装 Web 依赖")
	}
	if err := os.WriteFile(webDependencyStampPath(root), []byte(digest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(webDependencyFiles(root)[0]); err != nil {
		t.Fatal(err)
	}
	current, _, err = webDependenciesCurrent(root)
	if err != nil {
		t.Fatal(err)
	}
	if current {
		t.Fatal("关键 Web 依赖缺失时必须重新安装")
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
