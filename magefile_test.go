//go:build mage

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestParseLogOptions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "默认一百行", want: 100},
		{name: "分离参数", args: []string{"--tail", "25"}, want: 25},
		{name: "等号参数", args: []string{"--tail=50"}, want: 50},
		{name: "只监听新增", args: []string{"-n", "0"}, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := parseLogOptions(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if options.tail != test.want {
				t.Fatalf("tail = %d，want %d", options.tail, test.want)
			}
		})
	}
}

func TestParseLogOptionsRejectsInvalidTail(t *testing.T) {
	for _, args := range [][]string{
		{"--tail"},
		{"--tail", "-1"},
		{"--tail=abc"},
		{"--unknown"},
	} {
		if _, err := parseLogOptions(args); err == nil {
			t.Fatalf("无效日志参数应返回错误: %v", args)
		}
	}
}

func TestLocalLogFiles(t *testing.T) {
	directory := t.TempDir()
	for name, content := range map[string]string{
		"backend.log":                        "backend\n",
		"edo.log":                            "application\n",
		"edo-2026-08-02T12-00-00.000.log":    "rotated\n",
		"edo-2026-07-30T12-00-00.000.log.gz": "compressed\n",
		"web.LOG":                            "web\n",
		"notes.txt":                          "ignored\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := localLogFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("日志文件数量 = %d，want 3: %v", len(paths), paths)
	}
	if filepath.Base(paths[0]) != "backend.log" || filepath.Base(paths[1]) != "edo.log" || filepath.Base(paths[2]) != "web.LOG" {
		t.Fatalf("日志文件顺序或过滤不正确: %v", paths)
	}
}

func TestLocalLogFilesRejectsEmptyDirectory(t *testing.T) {
	if _, err := localLogFiles(t.TempDir()); err == nil {
		t.Fatal("空日志目录应返回错误")
	}
}

func TestFollowLocalLogsStopsWhenContextCanceled(t *testing.T) {
	if _, err := exec.LookPath("tail"); err != nil {
		t.Skip("当前系统没有 tail")
	}
	path := filepath.Join(t.TempDir(), "backend.log")
	if err := os.WriteFile(path, []byte("last line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- followLocalLogs(ctx, []string{path}, 1)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消上下文后日志监听未退出")
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
	if !strings.HasSuffix(digest, "\n"+runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("Web 依赖摘要缺少平台身份: %q", digest)
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

func TestRolldownNativeBindingSpec(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "node_modules", "rolldown", "package.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const binding = "@rolldown/binding-linux-x64-gnu"
	if err := os.WriteFile(manifestPath, []byte(`{"optionalDependencies":{"`+binding+`":"1.2.3"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := rolldownNativeBindingSpec(root, binding)
	if err != nil {
		t.Fatal(err)
	}
	if spec != binding+"@1.2.3" {
		t.Fatalf("原生依赖规格 = %q", spec)
	}
	if _, err := rolldownNativeBindingSpec(root, "@rolldown/missing"); err == nil {
		t.Fatal("未声明的原生依赖必须返回错误")
	}
}

func TestOpenLocalServiceLogAppends(t *testing.T) {
	directory := t.TempDir()
	service := localService{id: localServiceWeb, name: "Web", executable: "npm", args: []string{"start"}}
	var path string
	for range 2 {
		file, currentPath, err := openLocalServiceLog(directory, service)
		if err != nil {
			t.Fatal(err)
		}
		path = currentPath
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(content), "===== Web 启动于"); count != 2 {
		t.Fatalf("日志启动标记数量 = %d，want 2", count)
	}
	if !strings.Contains(string(content), "命令：npm start") {
		t.Fatal("日志缺少启动命令")
	}
}

func TestInspectLocalProcessTreatsZombieAsStopped(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("本地进程检查只在 Linux 和 macOS 上启用")
	}
	command := exec.Command("sh", "-c", "exit 0")
	if err := configureLocalProcessCommand(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := inspectLocalProcess(command.Process.Pid)
		if errors.Is(err, os.ErrProcessDone) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("已退出但尚未 Wait 的进程未被识别为 zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestForegroundLocalCommandStopsProcessGroupOnCancel(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("本地进程组只在 Linux 和 macOS 上启用")
	}
	ctx, cancel := context.WithCancel(context.Background())
	command, err := foregroundLocalCommand(ctx, localService{
		id:         localServiceBackend,
		name:       "后端",
		executable: "sh",
		args:       []string{"-c", "while :; do sleep 1; done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	record, err := registerLocalProcess(t.TempDir(), localService{
		id:         localServiceBackend,
		name:       "后端",
		executable: "sh",
	}, command, "")
	if err != nil {
		_ = signalLocalProcessGroup(command.Process.Pid, true)
		_ = command.Wait()
		t.Fatal(err)
	}
	if record.LogPath != "" {
		t.Fatalf("前台进程不应记录日志路径，实际为 %q", record.LogPath)
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	cancel()
	select {
	case waitErr := <-done:
		if !localProcessExitWasSignaled(waitErr) {
			t.Fatalf("取消前台命令后退出原因 = %v，want signal", waitErr)
		}
	case <-time.After(3 * time.Second):
		_ = signalLocalProcessGroup(record.ProcessGroupID, true)
		<-done
		t.Fatal("取消前台命令后进程组未退出")
	}
}

func TestWaitForBackgroundLocalServiceReady(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("本地进程组只在 Linux 和 macOS 上启用")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 3 {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	directory := t.TempDir()
	command := exec.Command("sh", "-c", "while :; do sleep 1; done")
	if err := configureLocalProcessCommand(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	record, err := registerLocalProcess(directory, localService{id: localServiceBackend, name: "后端", executable: "sh"}, command, filepath.Join(directory, "backend.log"))
	if err != nil {
		_ = signalLocalProcessGroup(command.Process.Pid, true)
		_ = command.Wait()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		_, _ = stopLocalProcessRecords(context.Background(), directory, []localProcessRecord{record}, true, 3*time.Second)
		<-done
	}()

	err = waitForBackgroundLocalServiceReady(context.Background(), directory, localService{
		name:         "测试服务",
		readyURL:     server.URL,
		readyTimeout: 2 * time.Second,
	}, record)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 3 {
		t.Fatalf("就绪检查次数不足: %d", requests.Load())
	}
}

func TestWaitForBackgroundLocalServiceReadyReturnsEarlyExit(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("本地进程组只在 Linux 和 macOS 上启用")
	}
	directory := t.TempDir()
	command := exec.Command("sh", "-c", "while :; do sleep 1; done")
	if err := configureLocalProcessCommand(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	record, err := registerLocalProcess(directory, localService{id: localServiceBackend, name: "后端", executable: "sh"}, command, filepath.Join(directory, "backend.log"))
	if err != nil {
		_ = signalLocalProcessGroup(command.Process.Pid, true)
		_ = command.Wait()
		t.Fatal(err)
	}
	_ = signalLocalProcessGroup(record.ProcessGroupID, true)
	_ = command.Wait()

	err = waitForBackgroundLocalServiceReady(context.Background(), directory, localService{
		name:         "后端",
		readyURL:     "http://127.0.0.1:1",
		readyTimeout: time.Second,
	}, record)
	if err == nil || !strings.Contains(err.Error(), "启动后退出") {
		t.Fatalf("未报告提前退出的后台进程: %v", err)
	}
}

func TestWaitForBackgroundLocalServiceReadyTimesOut(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("本地进程组只在 Linux 和 macOS 上启用")
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	directory := t.TempDir()
	command := exec.Command("sh", "-c", "while :; do sleep 1; done")
	if err := configureLocalProcessCommand(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	record, err := registerLocalProcess(directory, localService{id: localServiceWeb, name: "Web", executable: "sh"}, command, filepath.Join(directory, "web.log"))
	if err != nil {
		_ = signalLocalProcessGroup(command.Process.Pid, true)
		_ = command.Wait()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		_, _ = stopLocalProcessRecords(context.Background(), directory, []localProcessRecord{record}, true, 3*time.Second)
		<-done
	}()

	err = waitForBackgroundLocalServiceReady(context.Background(), directory, localService{
		name:         "测试服务",
		readyURL:     server.URL,
		readyTimeout: 700 * time.Millisecond,
	}, record)
	if err == nil {
		t.Fatal("服务未就绪时应返回超时错误")
	}
}

func TestStopRecordedLocalProcessesGracefully(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("本地进程组只在 Linux 和 macOS 上启用")
	}
	directory := t.TempDir()
	command := exec.Command("sh", "-c", "trap 'exit 0' TERM; while :; do sleep 1; done")
	if err := configureLocalProcessCommand(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	record, err := registerLocalProcess(directory, localService{
		id:         localServiceBackend,
		name:       "后端",
		executable: "sh",
		args:       []string{"-c", "test service"},
	}, command, filepath.Join(directory, "backend.log"))
	if err != nil {
		_ = signalLocalProcessGroup(command.Process.Pid, true)
		_ = command.Wait()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if !finished {
			_ = signalLocalProcessGroup(record.ProcessGroupID, true)
			<-done
		}
	})

	count, err := stopRecordedLocalProcesses(context.Background(), directory, false, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("安全停止数量 = %d，want 1", count)
	}
	select {
	case <-done:
		finished = true
	case <-time.After(3 * time.Second):
		t.Fatal("收到 SIGTERM 后进程组未退出")
	}
	if _, exists, err := readLocalProcessRecord(directory, localServiceBackend); err != nil || exists {
		t.Fatalf("安全停止后进程记录未清理: exists=%t err=%v", exists, err)
	}
}

func TestKillRecordedLocalProcessesAfterGracefulTimeout(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("本地进程组只在 Linux 和 macOS 上启用")
	}
	directory := t.TempDir()
	readyPath := filepath.Join(directory, "ready")
	command := exec.Command("sh", "-c", "trap '' TERM; touch \"$EDO_TEST_READY\"; while :; do sleep 1; done")
	command.Env = append(os.Environ(), "EDO_TEST_READY="+readyPath)
	if err := configureLocalProcessCommand(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			_ = signalLocalProcessGroup(command.Process.Pid, true)
			_ = command.Wait()
			t.Fatal("测试进程未完成信号处理初始化")
		}
		time.Sleep(10 * time.Millisecond)
	}
	record, err := registerLocalProcess(directory, localService{
		id:         localServiceWeb,
		name:       "Web",
		executable: "sh",
		args:       []string{"-c", "test service"},
	}, command, filepath.Join(directory, "web.log"))
	if err != nil {
		_ = signalLocalProcessGroup(command.Process.Pid, true)
		_ = command.Wait()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if !finished {
			_ = signalLocalProcessGroup(record.ProcessGroupID, true)
			<-done
		}
	})

	if _, err := stopRecordedLocalProcesses(context.Background(), directory, false, 100*time.Millisecond); err == nil || !strings.Contains(err.Error(), "mage kill") {
		t.Fatalf("忽略 SIGTERM 时应提示 mage kill，实际错误: %v", err)
	}
	count, err := stopRecordedLocalProcesses(context.Background(), directory, true, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("强制结束数量 = %d，want 1", count)
	}
	select {
	case <-done:
		finished = true
	case <-time.After(3 * time.Second):
		t.Fatal("收到 SIGKILL 后进程组未退出")
	}
}
