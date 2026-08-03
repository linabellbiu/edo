//go:build mage

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	environmentFile          = ".env"
	localProcessDirectory    = "data/run"
	localLogDirectory        = "logs"
	localGracefulStopTimeout = 70 * time.Second
	localForcefulStopTimeout = 5 * time.Second
	dockerStartMaxAttempts   = 3
	localServiceBackend      = "backend"
	localServiceWeb          = "web"
)

// Start 启动 EDO；可使用 --dev、--docker、--server 和 --web 调整启动方式。
func Start(ctx context.Context) error {
	options, err := readStartOptions()
	if err != nil {
		return err
	}
	if options.help {
		printStartHelp()
		return finishStart(nil, options.provided)
	}
	if options.dev && options.docker {
		return errors.New("--dev 和 --docker 不能同时使用")
	}
	runServer, runWeb := selectedComponents(options.server, options.web)
	if err := loadEnvironment(); err != nil {
		return err
	}
	if err := validateStartSecretsKey(runServer); err != nil {
		return err
	}
	if options.docker {
		return finishStart(startDocker(ctx, runServer, runWeb), options.provided)
	}
	if options.dev {
		return finishStart(startDevelopment(ctx, runServer, runWeb), options.provided)
	}
	return finishStart(startBuilt(ctx, runServer, runWeb), options.provided)
}

// Help 显示 EDO 常用开发和启动命令。
func Help() {
	fmt.Println(`EDO Mage 命令

mage start                       构建后在后台启动，日志写入 logs/
mage start --server              只在后台启动后端
mage start --web                 只在后台启动 Web
mage start --dev                 启动开发依赖和迁移，在前台运行后端与 Vite
mage start --dev --server        开发模式，只在前台运行后端
mage start --dev --web           开发模式，只在前台运行 Web
mage start --docker              使用 Docker Compose 在后台启动完整环境
mage stop                        安全停止本项目的前后端
mage kill                        强制结束本项目的前后端
mage status                      查看本地进程和 Compose 服务状态
mage log --tail 100              从日志最后 100 行开始持续监听
mage migrate                     迁移 .env 指定的数据库
mage start --help                查看 start 参数说明
mage -l                          查看全部 Mage 任务`)
}

// Migrate 迁移 .env 指定的数据库，不启动其他服务。
func Migrate(ctx context.Context) error {
	if err := loadEnvironment(); err != nil {
		return err
	}
	return migrateFromSource(ctx)
}

// Stop 安全停止 Mage 启动的本地前后端和 Compose 前后端容器。
func Stop(ctx context.Context) error {
	return controlFrontendAndBackend(ctx, false)
}

// Kill 强制结束 Mage 启动的本地前后端和 Compose 前后端容器。
func Kill(ctx context.Context) error {
	return controlFrontendAndBackend(ctx, true)
}

// Status 显示 Mage 管理的本地进程以及 Compose 服务状态。
func Status(ctx context.Context) error {
	if err := printLocalServiceStatus(ctx, localProcessDirectory); err != nil {
		return err
	}
	return printComposeServiceStatus(ctx)
}

// Log 从指定行数开始持续监听 logs 目录中的本地服务日志。
func Log(ctx context.Context) error {
	options, err := readLogOptions()
	if err != nil {
		return err
	}
	if options.help {
		printLogHelp()
		return finishLog(nil, options.provided)
	}
	paths, err := localLogFiles(localLogDirectory)
	if err != nil {
		return err
	}
	err = followLocalLogs(ctx, paths, options.tail)
	return finishLog(err, options.provided)
}

type startOptions struct {
	dev      bool
	docker   bool
	server   bool
	web      bool
	help     bool
	provided bool
}

func readStartOptions() (startOptions, error) {
	for index, arg := range os.Args[1:] {
		if strings.EqualFold(arg, "start") {
			return parseStartOptions(os.Args[index+2:])
		}
	}
	return startOptions{}, nil
}

func finishStart(err error, optionsProvided bool) error {
	if err == nil && optionsProvided {
		// Mage 会把目标后的自定义参数继续当作目标；任务完成后直接结束，避免再次解析。
		os.Exit(0)
	}
	return err
}

func parseStartOptions(args []string) (startOptions, error) {
	options := startOptions{provided: len(args) > 0}
	for _, arg := range args {
		switch arg {
		case "--dev", "-dev":
			options.dev = true
		case "--docker", "-docker":
			options.docker = true
		case "--server", "-server":
			options.server = true
		case "--web", "-web":
			options.web = true
		case "--help", "-h":
			options.help = true
		default:
			return startOptions{}, fmt.Errorf("未知启动参数 %q", arg)
		}
	}
	return options, nil
}

func printStartHelp() {
	fmt.Println(`用法：mage start [--dev | --docker] [--server | --web]

不指定参数时，构建内嵌 Web 的单文件程序，迁移数据库后在后台启动。
启动器日志写入 logs/backend.log 和 logs/web.log，后端结构化日志写入可滚动的 logs/edo.log。
--dev     启动 Redis、NATS 和迁移，再使用 go run 与 npm start 在当前终端运行
--docker  使用 Docker Compose 在后台启动
--server  只启动后端
--web     只启动 Web`)
}

type logOptions struct {
	tail     int
	help     bool
	provided bool
}

func readLogOptions() (logOptions, error) {
	for index, arg := range os.Args[1:] {
		if strings.EqualFold(arg, "log") {
			return parseLogOptions(os.Args[index+2:])
		}
	}
	return logOptions{tail: 100}, nil
}

func parseLogOptions(args []string) (logOptions, error) {
	options := logOptions{tail: 100, provided: len(args) > 0}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			options.help = true
		case arg == "--tail" || arg == "-tail" || arg == "-n":
			if index+1 >= len(args) {
				return logOptions{}, fmt.Errorf("%s 需要提供行数", arg)
			}
			index++
			value, err := parseLogTail(args[index])
			if err != nil {
				return logOptions{}, err
			}
			options.tail = value
		case strings.HasPrefix(arg, "--tail="):
			value, err := parseLogTail(strings.TrimPrefix(arg, "--tail="))
			if err != nil {
				return logOptions{}, err
			}
			options.tail = value
		default:
			return logOptions{}, fmt.Errorf("未知日志参数 %q", arg)
		}
	}
	return options, nil
}

func parseLogTail(value string) (int, error) {
	tail, err := strconv.Atoi(value)
	if err != nil || tail < 0 {
		return 0, fmt.Errorf("--tail 必须是大于或等于 0 的整数，实际为 %q", value)
	}
	return tail, nil
}

func finishLog(err error, optionsProvided bool) error {
	if err == nil && optionsProvided {
		// Mage 会把目标后的自定义参数继续当作目标；任务完成后直接结束，避免再次解析。
		os.Exit(0)
	}
	return err
}

func printLogHelp() {
	fmt.Println(`用法：mage log [--tail N]

监听 logs 目录中的全部 .log 文件；先显示每个文件最后 N 行，再持续显示新增日志。
--tail N  起始显示行数，默认 100；使用 0 表示只监听新日志
按 Ctrl+C 停止监听。`)
}

func loadEnvironment() error {
	return loadEnvironmentFile(environmentFile)
}

func loadEnvironmentFile(path string) error {
	created, err := ensureEnvironmentFile(path)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("未找到 %s，已根据 %s 创建默认配置并生成固定密钥\n", path, path+".example")
	}
	if err := godotenv.Load(path); err != nil {
		return fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	return nil
}

func ensureEnvironmentFile(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("检查 %s 失败: %w", path, err)
	}

	templatePath := path + ".example"
	template, err := os.ReadFile(templatePath)
	if err != nil {
		return false, fmt.Errorf("读取默认配置模板 %s 失败: %w", templatePath, err)
	}
	encodedKey, err := initialSecretsKey()
	if err != nil {
		return false, err
	}
	content, err := fillEnvironmentSecretsKey(template, encodedKey)
	if err != nil {
		return false, fmt.Errorf("生成默认配置失败: %w", err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// 另一个同时启动的 Mage 已创建文件，后续直接读取该文件。
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("创建 %s 失败: %w", path, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("保存 %s 失败: %w", path, err)
	}
	return true, nil
}

func initialSecretsKey() (string, error) {
	if encoded := strings.TrimSpace(os.Getenv("EDO_SECRETS_KEY")); encoded != "" {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			key, err = base64.RawStdEncoding.DecodeString(encoded)
		}
		if err != nil || len(key) != 32 {
			return "", errors.New("进程环境中的 EDO_SECRETS_KEY 必须是 32 字节随机密钥的 Base64 编码")
		}
		return encoded, nil
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("生成 EDO_SECRETS_KEY 失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func fillEnvironmentSecretsKey(template []byte, key string) ([]byte, error) {
	lines := strings.Split(string(template), "\n")
	found := false
	for index, line := range lines {
		lineWithoutCR := strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(lineWithoutCR)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		separator := strings.IndexByte(lineWithoutCR, '=')
		if separator < 0 || strings.TrimSpace(lineWithoutCR[:separator]) != "EDO_SECRETS_KEY" {
			continue
		}
		if found {
			return nil, errors.New(".env.example 中存在重复的 EDO_SECRETS_KEY")
		}
		if strings.TrimSpace(lineWithoutCR[separator+1:]) != "" {
			return nil, errors.New(".env.example 中的 EDO_SECRETS_KEY 必须留空")
		}
		lineEnding := ""
		if strings.HasSuffix(line, "\r") {
			lineEnding = "\r"
		}
		lines[index] = lineWithoutCR[:separator+1] + key + lineEnding
		found = true
	}
	if !found {
		return nil, errors.New(".env.example 缺少 EDO_SECRETS_KEY")
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func validateStartSecretsKey(runServer bool) error {
	if !runServer {
		return nil
	}
	encoded := strings.TrimSpace(os.Getenv("EDO_SECRETS_KEY"))
	if encoded == "" {
		return errors.New("请在根目录 .env 中配置 EDO_SECRETS_KEY")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(key) != 32 {
		return errors.New(".env 中的 EDO_SECRETS_KEY 必须是 32 字节随机密钥的 Base64 编码")
	}
	return nil
}

func startDocker(ctx context.Context, runServer, runWeb bool) error {
	args := []string{"compose", "--env-file", environmentFile, "-f", "deploy/compose.dev.yml", "up", "-d", "--build"}
	if runServer && !runWeb {
		args = append(args, "api")
	} else if runWeb && !runServer {
		args = append(args, "--no-deps", "web")
	}
	return retryDockerComposeStart(ctx, func() error {
		return runCommand(ctx, "docker", args...)
	}, waitForDockerStartRetry)
}

func retryDockerComposeStart(
	ctx context.Context,
	run func() error,
	wait func(context.Context, time.Duration) error,
) error {
	if run == nil || wait == nil {
		return errors.New("Docker Compose 启动重试配置无效")
	}
	var lastErr error
	for attempt := 1; attempt <= dockerStartMaxAttempts; attempt++ {
		if lastErr = run(); lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == dockerStartMaxAttempts {
			break
		}
		delay := time.Duration(attempt*2) * time.Second
		fmt.Printf("Docker Compose 构建或启动未完成，%s 后进行第 %d 次尝试\n", delay, attempt+1)
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
	return fmt.Errorf("Docker Compose 构建或启动失败: %w", lastErr)
}

func waitForDockerStartRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func startBuilt(ctx context.Context, runServer, runWeb bool) error {
	if runServer {
		if err := useHostDockerBuilder(); err != nil {
			return err
		}
	}
	if runWeb {
		if err := ensureWebDependencies(ctx); err != nil {
			return err
		}
		if err := runCommand(ctx, npmExecutable(), "--prefix", "web", "run", "build"); err != nil {
			return fmt.Errorf("构建 Web 失败: %w", err)
		}
	}
	backend := ""
	if runServer {
		var err error
		backend, err = buildBackend(ctx, runWeb)
		if err != nil {
			return err
		}
	}
	if runServer {
		if err := os.Setenv("EDO_WEB_ROOT", ""); err != nil {
			return fmt.Errorf("设置 Web 目录失败: %w", err)
		}
		if err := runCommand(ctx, backend, "migrate"); err != nil {
			return fmt.Errorf("数据库迁移失败: %w", err)
		}
		fmt.Println("EDO 后端：http://127.0.0.1:8080")
		if runWeb {
			fmt.Println("EDO Web：http://127.0.0.1:8080")
		}
		return runLocalServices(ctx, []localService{{
			id:           localServiceBackend,
			name:         "后端",
			executable:   backend,
			args:         []string{"server"},
			readyURL:     "http://127.0.0.1:8080/api/v1/health/ready",
			readyTimeout: 90 * time.Second,
		}})
	}

	fmt.Println("EDO Web：http://127.0.0.1:5173")
	return runLocalServices(ctx, []localService{{
		id:           localServiceWeb,
		name:         "Web",
		executable:   npmExecutable(),
		args:         []string{"--prefix", "web", "run", "preview", "--", "--host", "127.0.0.1", "--port", "5173"},
		readyURL:     "http://127.0.0.1:5173",
		readyTimeout: 30 * time.Second,
	}})
}

func startDevelopment(ctx context.Context, runServer, runWeb bool) error {
	if runServer {
		if err := startDevelopmentDependencies(ctx); err != nil {
			return err
		}
		if err := os.Setenv("EDO_WEB_ROOT", ""); err != nil {
			return fmt.Errorf("设置开发环境 Web 目录失败: %w", err)
		}
		if err := migrateFromSource(ctx); err != nil {
			return err
		}
	}
	if runWeb {
		if err := ensureWebDependencies(ctx); err != nil {
			return err
		}
	}
	if runServer && runWeb {
		fmt.Println("EDO 后端：http://127.0.0.1:8080")
		fmt.Println("EDO Web：http://127.0.0.1:5173")
		return runForegroundLocalServices(ctx, []localService{
			{
				id:           localServiceBackend,
				name:         "后端",
				executable:   "go",
				args:         []string{"run", "./cmd/edo", "server"},
				readyURL:     "http://127.0.0.1:8080/api/v1/health/ready",
				readyTimeout: 90 * time.Second,
			},
			{
				id:           localServiceWeb,
				name:         "Web",
				executable:   npmExecutable(),
				args:         []string{"--prefix", "web", "start"},
				readyURL:     "http://127.0.0.1:5173",
				readyTimeout: 30 * time.Second,
			},
		})
	}
	if runServer {
		fmt.Println("EDO 后端：http://127.0.0.1:8080")
		return runForegroundLocalServices(ctx, []localService{{
			id:           localServiceBackend,
			name:         "后端",
			executable:   "go",
			args:         []string{"run", "./cmd/edo", "server"},
			readyURL:     "http://127.0.0.1:8080/api/v1/health/ready",
			readyTimeout: 90 * time.Second,
		}})
	}
	fmt.Println("EDO Web：http://127.0.0.1:5173")
	return runForegroundLocalServices(ctx, []localService{{
		id:           localServiceWeb,
		name:         "Web",
		executable:   npmExecutable(),
		args:         []string{"--prefix", "web", "start"},
		readyURL:     "http://127.0.0.1:5173",
		readyTimeout: 30 * time.Second,
	}})
}

func migrateFromSource(ctx context.Context) error {
	if err := runCommand(ctx, "go", "run", "./cmd/edo", "migrate"); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	return nil
}

func startDevelopmentDependencies(ctx context.Context) error {
	// 之前通过 --docker 启动的 api/web 不会被 compose up redis nats 自动停止，会与本机
	// go run 竞争同一 NATS Consumer，并发访问同一 SQLite 文件。开发模式必须先收敛为单后端。
	if err := runCommand(
		ctx,
		"docker",
		"compose",
		"--env-file",
		environmentFile,
		"-f",
		"deploy/compose.dev.yml",
		"stop",
		"api",
		"web",
		"docker-builder",
	); err != nil {
		return fmt.Errorf("停止遗留的容器后端失败: %w", err)
	}

	// 宿主机开发直接使用当前 Docker CLI/daemon；DinD 只属于 Compose 内的容器后端。
	if err := useHostDockerBuilder(); err != nil {
		return err
	}
	fmt.Println("启动开发依赖：Redis、NATS JetStream")
	if err := runCommand(
		ctx,
		"docker",
		"compose",
		"--env-file",
		environmentFile,
		"-f",
		"deploy/compose.dev.yml",
		"up",
		"-d",
		"--wait",
		"--wait-timeout",
		"60",
		"redis",
		"nats",
	); err != nil {
		return fmt.Errorf("启动开发依赖失败: %w", err)
	}
	return nil
}

func useHostDockerBuilder() error {
	if err := os.Unsetenv("EDO_DOCKER_BUILDER_HOST"); err != nil {
		return fmt.Errorf("切换到宿主机 Docker 构建运行时失败: %w", err)
	}
	if err := os.Unsetenv("EDO_DOCKER_BUILDER_TLS_CERT_PATH"); err != nil {
		return fmt.Errorf("清理容器 Docker 客户端证书配置失败: %w", err)
	}
	return nil
}

func buildBackend(ctx context.Context, embedWeb bool) (string, error) {
	if err := os.MkdirAll("bin", 0o755); err != nil {
		return "", fmt.Errorf("创建 bin 目录失败: %w", err)
	}
	if embedWeb {
		if err := prepareEmbeddedWebAssets(); err != nil {
			return "", err
		}
	}
	binary := filepath.Join("bin", "edo")
	// 本地单文件构建与容器发布构建保持一致，移除生产运行不需要的符号表和 DWARF 调试信息。
	args := []string{"build", "-trimpath", "-ldflags", "-s -w"}
	if embedWeb {
		args = append(args, "-tags", "edo_web")
	}
	args = append(args, "-o", binary, "./cmd/edo")
	if err := runCommand(ctx, "go", args...); err != nil {
		return "", fmt.Errorf("构建 EDO 后端失败: %w", err)
	}
	absolute, err := filepath.Abs(binary)
	if err != nil {
		return "", fmt.Errorf("解析 EDO 后端路径失败: %w", err)
	}
	return absolute, nil
}

func prepareEmbeddedWebAssets() error {
	source := filepath.Join("web", "dist")
	target := filepath.Join("internal", "webui", "dist")
	return copyEmbeddedWebAssets(source, target)
}

func copyEmbeddedWebAssets(source, target string) error {
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("清理内嵌 Web 资源失败: %w", err)
	}
	if err := os.CopyFS(target, os.DirFS(source)); err != nil {
		return fmt.Errorf("准备内嵌 Web 资源失败: %w", err)
	}
	return nil
}

func ensureWebDependencies(ctx context.Context) error {
	const webRoot = "web"
	current, dependencyStamp, err := webDependenciesCurrent(webRoot)
	if err != nil {
		return err
	}
	nativeBinding, err := rolldownNativeBindingPackage()
	if err != nil {
		return err
	}
	if current && webRuntimeDependenciesAvailable(ctx, webRoot, nativeBinding) {
		return nil
	}

	if !current {
		fmt.Println("Web 依赖缺失、平台已切换或锁文件已变更，执行 npm ci")
		if err := runCommand(ctx, npmExecutable(), "--prefix", webRoot, "ci", "--include=dev"); err != nil {
			return fmt.Errorf("安装 Web 依赖失败: %w", err)
		}
	}
	present, err := webDependencyFilesPresent(webRoot)
	if err != nil {
		return err
	}
	if !present {
		return errors.New("npm ci 完成后 Web 依赖仍不完整")
	}
	if !webRuntimeDependenciesAvailable(ctx, webRoot, nativeBinding) {
		nativeBindingSpec, err := rolldownNativeBindingSpec(webRoot, nativeBinding)
		if err != nil {
			return err
		}
		fmt.Printf("npm 未安装当前平台的 Rolldown 原生依赖，修复安装 %s\n", nativeBindingSpec)
		if err := runCommand(ctx, npmExecutable(), "--prefix", webRoot, "install", "--no-save", "--package-lock=false", "--include=optional", nativeBindingSpec); err != nil {
			return fmt.Errorf("修复 Web 原生依赖失败: %w", err)
		}
	}
	if !webRuntimeDependenciesAvailable(ctx, webRoot, nativeBinding) {
		return fmt.Errorf("Web 原生依赖 %s 仍无法加载", nativeBinding)
	}
	if err := os.WriteFile(webDependencyStampPath(webRoot), []byte(dependencyStamp+"\n"), 0o644); err != nil {
		return fmt.Errorf("记录 Web 依赖版本失败: %w", err)
	}
	return nil
}

func webDependenciesCurrent(webRoot string) (bool, string, error) {
	lockfile, err := os.ReadFile(filepath.Join(webRoot, "package-lock.json"))
	if err != nil {
		return false, "", fmt.Errorf("读取 Web 依赖锁文件失败: %w", err)
	}
	lockDigest := fmt.Sprintf("%x", sha256.Sum256(lockfile))
	dependencyStamp := lockDigest + "\n" + runtime.GOOS + "/" + runtime.GOARCH
	stamp, err := os.ReadFile(webDependencyStampPath(webRoot))
	if errors.Is(err, os.ErrNotExist) {
		return false, dependencyStamp, nil
	}
	if err != nil {
		return false, "", fmt.Errorf("读取 Web 依赖版本失败: %w", err)
	}
	if strings.TrimSpace(string(stamp)) != dependencyStamp {
		return false, dependencyStamp, nil
	}
	present, err := webDependencyFilesPresent(webRoot)
	if err != nil {
		return false, "", err
	}
	return present, dependencyStamp, nil
}

func webRuntimeDependenciesAvailable(ctx context.Context, webRoot, nativeBinding string) bool {
	command := exec.CommandContext(ctx, "node", "--eval", "require(process.argv[1])", nativeBinding)
	command.Dir = webRoot
	return command.Run() == nil
}

func rolldownNativeBindingPackage() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			return "@rolldown/binding-darwin-x64", nil
		case "arm64":
			return "@rolldown/binding-darwin-arm64", nil
		}
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "@rolldown/binding-linux-x64-" + linuxLibcVariant(), nil
		case "arm64":
			return "@rolldown/binding-linux-arm64-" + linuxLibcVariant(), nil
		case "arm":
			return "@rolldown/binding-linux-arm-gnueabihf", nil
		case "ppc64", "ppc64le":
			return "@rolldown/binding-linux-ppc64-gnu", nil
		case "s390x":
			return "@rolldown/binding-linux-s390x-gnu", nil
		}
	}
	return "", fmt.Errorf("Rolldown 不支持当前平台 %s/%s", runtime.GOOS, runtime.GOARCH)
}

func linuxLibcVariant() string {
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return "musl"
	}
	output, _ := exec.Command("ldd", "--version").CombinedOutput()
	if strings.Contains(strings.ToLower(string(output)), "musl") {
		return "musl"
	}
	return "gnu"
}

func rolldownNativeBindingSpec(webRoot, nativeBinding string) (string, error) {
	manifest, err := os.ReadFile(filepath.Join(webRoot, "node_modules", "rolldown", "package.json"))
	if err != nil {
		return "", fmt.Errorf("读取 Rolldown 依赖信息失败: %w", err)
	}
	metadata := struct {
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}{}
	if err := json.Unmarshal(manifest, &metadata); err != nil {
		return "", fmt.Errorf("解析 Rolldown 依赖信息失败: %w", err)
	}
	version := strings.TrimSpace(metadata.OptionalDependencies[nativeBinding])
	if version == "" {
		return "", fmt.Errorf("Rolldown 未声明当前平台依赖 %s", nativeBinding)
	}
	return nativeBinding + "@" + version, nil
}

func webDependencyFilesPresent(webRoot string) (bool, error) {
	for _, path := range webDependencyFiles(webRoot) {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("检查 Web 依赖 %s 失败: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return false, nil
		}
	}
	return true, nil
}

func webDependencyFiles(webRoot string) []string {
	vite := filepath.Join(webRoot, "node_modules", ".bin", "vite")
	return []string{
		vite,
		filepath.Join(webRoot, "node_modules", "vite", "package.json"),
		filepath.Join(webRoot, "node_modules", "vue", "package.json"),
		filepath.Join(webRoot, "node_modules", "@vitejs", "plugin-vue", "package.json"),
		filepath.Join(webRoot, "node_modules", "unplugin-vue-components", "package.json"),
	}
}

func webDependencyStampPath(webRoot string) string {
	return filepath.Join(webRoot, "node_modules", ".edo-package-lock.sha256")
}

type localProcessSnapshot struct {
	processGroupID int
	identity       string
}

type localProcessRecord struct {
	Service        string   `json:"service"`
	PID            int      `json:"pid"`
	ProcessGroupID int      `json:"process_group_id"`
	Identity       string   `json:"identity"`
	ProjectRoot    string   `json:"project_root"`
	Command        []string `json:"command"`
	LogPath        string   `json:"log_path"`
}

func controlFrontendAndBackend(ctx context.Context, force bool) error {
	timeout := localGracefulStopTimeout
	if force {
		timeout = localForcefulStopTimeout
	}
	localCount, localErr := stopRecordedLocalProcesses(ctx, localProcessDirectory, force, timeout)
	composeCount, composeErr := controlComposeFrontendAndBackend(ctx, force)
	if err := errors.Join(localErr, composeErr); err != nil {
		return err
	}
	verb := "已安全停止"
	if force {
		verb = "已强制结束"
	}
	if localCount == 0 && composeCount == 0 {
		fmt.Println("未发现由本项目启动的前后端进程或容器")
		return nil
	}
	fmt.Printf("%s前后端：本地进程组 %d 个，Compose 服务 %d 个\n", verb, localCount, composeCount)
	return nil
}

func controlComposeFrontendAndBackend(ctx context.Context, force bool) (int, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return 0, nil
	}
	baseArgs := developmentComposeArgs()
	output, err := runComposeOutput(ctx, append(baseArgs, "ps", "--services", "--status", "running", "api", "web")...)
	if err != nil {
		return 0, fmt.Errorf("检查 Compose 前后端失败: %w", err)
	}
	services := strings.Fields(output)
	if len(services) == 0 {
		return 0, nil
	}
	args := append([]string{}, baseArgs...)
	if force {
		args = append(args, "stop", "--timeout", "0")
	} else {
		args = append(args, "stop", "--timeout", fmt.Sprintf("%.0f", localGracefulStopTimeout.Seconds()))
	}
	args = append(args, services...)
	if err := runComposeCommand(ctx, args...); err != nil {
		action := "安全停止"
		if force {
			action = "强制停止"
		}
		return 0, fmt.Errorf("%s Compose 前后端失败: %w", action, err)
	}
	return len(services), nil
}

func developmentComposeArgs() []string {
	args := []string{"compose"}
	if _, err := os.Stat(environmentFile); err == nil {
		args = append(args, "--env-file", environmentFile)
	}
	return append(args, "-f", "deploy/compose.dev.yml")
}

func runComposeOutput(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	command.Env = composeCommandEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return string(output), nil
}

func runComposeCommand(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "docker", args...)
	command.Env = composeCommandEnvironment()
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	return command.Run()
}

func composeCommandEnvironment() []string {
	environment := os.Environ()
	if strings.TrimSpace(os.Getenv("EDO_SECRETS_KEY")) == "" {
		// stop/kill 只操作已有容器，但 Compose 解析配置时仍要求该变量存在。
		environment = append(environment, "EDO_SECRETS_KEY=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	}
	return environment
}

func printLocalServiceStatus(ctx context.Context, directory string) error {
	fmt.Println("本地服务：")
	for _, service := range []string{localServiceBackend, localServiceWeb} {
		record, exists, err := readLocalProcessRecord(directory, service)
		if err != nil {
			return err
		}
		if !exists {
			fmt.Printf("- %s：未运行（无 Mage 进程记录）\n", localServiceDisplayName(service))
			continue
		}
		running, err := managedLocalProcessRunning(record)
		if err != nil {
			return err
		}
		if !running {
			fmt.Printf("- %s：未运行（存在已失效的 PID %d 记录）\n", localServiceDisplayName(service), record.PID)
			continue
		}
		mode := "前台"
		logDescription := "当前启动终端"
		if record.LogPath != "" {
			mode = "后台"
			logDescription = record.LogPath
		}
		fmt.Printf(
			"- %s：运行中 | %s | PID %d | %s | 日志：%s\n",
			localServiceDisplayName(service),
			mode,
			record.PID,
			localServiceReadiness(ctx, service),
			logDescription,
		)
	}
	return nil
}

func localServiceReadiness(ctx context.Context, service string) string {
	readyURL := ""
	switch service {
	case localServiceBackend:
		readyURL = "http://127.0.0.1:8080/api/v1/health/ready"
	case localServiceWeb:
		readyURL = "http://127.0.0.1:5173"
	default:
		return "就绪状态未知"
	}
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	if localServiceIsReady(readyCtx, client, readyURL) {
		return "已就绪"
	}
	return "未就绪"
}

func printComposeServiceStatus(ctx context.Context) error {
	fmt.Println("\nCompose 服务：")
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Println("- 未安装 Docker，无法检查")
		return nil
	}
	args := append(developmentComposeArgs(), "ps", "--all")
	if err := runComposeCommand(ctx, args...); err != nil {
		return fmt.Errorf("检查 Compose 服务状态失败: %w", err)
	}
	return nil
}

func localLogFiles(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("日志目录 %s 不存在，请先执行 mage start", directory)
	}
	if err != nil {
		return nil, fmt.Errorf("读取日志目录 %s 失败: %w", directory, err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if entry.IsDir() || (name != "backend.log" && name != "web.log" && name != "edo.log") {
			continue
		}
		path, err := filepath.Abs(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("解析日志路径失败: %w", err)
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("日志目录 %s 中没有 .log 文件，请先执行 mage start", directory)
	}
	return paths, nil
}

func followLocalLogs(ctx context.Context, paths []string, tail int) error {
	if len(paths) == 0 {
		return errors.New("没有可监听的日志文件")
	}
	if _, err := exec.LookPath("tail"); err != nil {
		return errors.New("当前系统未安装 tail，无法持续监听日志")
	}
	fmt.Printf("从每个日志文件最后 %d 行开始监听，按 Ctrl+C 停止：\n", tail)
	for _, path := range paths {
		fmt.Printf("- %s\n", path)
	}
	followCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	args := []string{"-n", strconv.Itoa(tail), "-F"}
	args = append(args, paths...)
	command := exec.CommandContext(followCtx, "tail", args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	if err := command.Run(); err != nil && followCtx.Err() == nil && !localProcessExitWasSignaled(err) {
		return fmt.Errorf("监听日志失败: %w", err)
	}
	return nil
}

func backgroundLocalCommand(service localService, output *os.File) (*exec.Cmd, error) {
	command := exec.Command(service.executable, service.args...)
	if err := configureLocalProcessCommand(command); err != nil {
		return nil, err
	}
	command.Stdout = output
	command.Stderr = output
	return command, nil
}

func foregroundLocalCommand(ctx context.Context, service localService) (*exec.Cmd, error) {
	command := exec.CommandContext(ctx, service.executable, service.args...)
	if err := configureLocalProcessCommand(command); err != nil {
		return nil, err
	}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return signalLocalProcessGroup(command.Process.Pid, false)
	}
	command.WaitDelay = localGracefulStopTimeout
	return command, nil
}

func openLocalServiceLog(directory string, service localService) (*os.File, string, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, "", fmt.Errorf("创建日志目录失败: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(directory, service.id+".log"))
	if err != nil {
		return nil, "", fmt.Errorf("解析%s日志路径失败: %w", service.name, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("打开%s日志失败: %w", service.name, err)
	}
	if _, err := fmt.Fprintf(file, "\n===== %s 启动于 %s =====\n命令：%s\n", service.name, time.Now().Format(time.RFC3339), strings.Join(append([]string{service.executable}, service.args...), " ")); err != nil {
		_ = file.Close()
		return nil, "", fmt.Errorf("写入%s日志起始标记失败: %w", service.name, err)
	}
	return file, path, nil
}

func ensureLocalServiceCanStart(directory, service string) error {
	record, exists, err := readLocalProcessRecord(directory, service)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	running, err := managedLocalProcessRunning(record)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("%s进程已经运行（PID %d），请先执行 mage stop 或 mage kill", localServiceDisplayName(service), record.PID)
	}
	return removeLocalProcessRecord(record, directory)
}

func registerLocalProcess(directory string, service localService, command *exec.Cmd, logPath string) (localProcessRecord, error) {
	if command.Process == nil {
		return localProcessRecord{}, errors.New("无法记录尚未启动的本地进程")
	}
	snapshot, err := inspectLocalProcess(command.Process.Pid)
	if err != nil {
		return localProcessRecord{}, fmt.Errorf("检查%s进程失败: %w", service.name, err)
	}
	projectRoot, err := filepath.Abs(".")
	if err != nil {
		return localProcessRecord{}, fmt.Errorf("解析项目根目录失败: %w", err)
	}
	cleanLogPath := ""
	if logPath != "" {
		cleanLogPath = filepath.Clean(logPath)
	}
	record := localProcessRecord{
		Service:        service.id,
		PID:            command.Process.Pid,
		ProcessGroupID: snapshot.processGroupID,
		Identity:       snapshot.identity,
		ProjectRoot:    filepath.Clean(projectRoot),
		Command:        append([]string{service.executable}, service.args...),
		LogPath:        cleanLogPath,
	}
	if err := writeLocalProcessRecord(directory, record); err != nil {
		return localProcessRecord{}, err
	}
	return record, nil
}

func readLocalProcessRecord(directory, service string) (localProcessRecord, bool, error) {
	path, err := localProcessRecordPath(directory, service)
	if err != nil {
		return localProcessRecord{}, false, err
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return localProcessRecord{}, false, nil
	}
	if err != nil {
		return localProcessRecord{}, false, fmt.Errorf("读取%s进程记录失败: %w", localServiceDisplayName(service), err)
	}
	var record localProcessRecord
	if err := json.Unmarshal(content, &record); err != nil {
		return localProcessRecord{}, false, fmt.Errorf("%s进程记录已损坏: %w", localServiceDisplayName(service), err)
	}
	if err := validateLocalProcessRecord(record, service); err != nil {
		return localProcessRecord{}, false, err
	}
	return record, true, nil
}

func writeLocalProcessRecord(directory string, record localProcessRecord) error {
	path, err := localProcessRecordPath(directory, record.Service)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("创建本地进程记录目录失败: %w", err)
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("编码%s进程记录失败: %w", localServiceDisplayName(record.Service), err)
	}
	temporary, err := os.CreateTemp(directory, ".process-*.tmp")
	if err != nil {
		return fmt.Errorf("创建%s进程临时记录失败: %w", localServiceDisplayName(record.Service), err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("设置%s进程记录权限失败: %w", localServiceDisplayName(record.Service), err)
	}
	if _, err := temporary.Write(append(content, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("写入%s进程记录失败: %w", localServiceDisplayName(record.Service), err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("保存%s进程记录失败: %w", localServiceDisplayName(record.Service), err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("更新%s进程记录失败: %w", localServiceDisplayName(record.Service), err)
	}
	return nil
}

func validateLocalProcessRecord(record localProcessRecord, expectedService string) error {
	if record.Service != expectedService || record.PID <= 0 || record.ProcessGroupID <= 0 || strings.TrimSpace(record.Identity) == "" {
		return fmt.Errorf("%s进程记录内容无效", localServiceDisplayName(expectedService))
	}
	projectRoot, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("解析项目根目录失败: %w", err)
	}
	if filepath.Clean(record.ProjectRoot) != filepath.Clean(projectRoot) {
		return fmt.Errorf("%s进程记录不属于当前项目，拒绝发送信号", localServiceDisplayName(expectedService))
	}
	return nil
}

func managedLocalProcessRunning(record localProcessRecord) (bool, error) {
	snapshot, err := inspectLocalProcess(record.PID)
	if errors.Is(err, os.ErrProcessDone) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查%s进程状态失败: %w", localServiceDisplayName(record.Service), err)
	}
	return snapshot.processGroupID == record.ProcessGroupID && snapshot.identity == record.Identity, nil
}

func removeLocalProcessRecord(record localProcessRecord, directory string) error {
	current, exists, err := readLocalProcessRecord(directory, record.Service)
	if err != nil {
		return err
	}
	if !exists || !sameLocalProcess(current, record) {
		return nil
	}
	path, err := localProcessRecordPath(directory, record.Service)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除%s进程记录失败: %w", localServiceDisplayName(record.Service), err)
	}
	return nil
}

func sameLocalProcess(left, right localProcessRecord) bool {
	return left.Service == right.Service && left.PID == right.PID && left.ProcessGroupID == right.ProcessGroupID && left.Identity == right.Identity
}

func localProcessRecordPath(directory, service string) (string, error) {
	switch service {
	case localServiceBackend, localServiceWeb:
		return filepath.Join(directory, service+".json"), nil
	default:
		return "", fmt.Errorf("未知本地服务 %q", service)
	}
}

func localServiceDisplayName(service string) string {
	if service == localServiceBackend {
		return "后端"
	}
	if service == localServiceWeb {
		return "Web"
	}
	return service
}

func stopRecordedLocalProcesses(ctx context.Context, directory string, force bool, timeout time.Duration) (int, error) {
	records := make([]localProcessRecord, 0, 2)
	for _, service := range []string{localServiceWeb, localServiceBackend} {
		record, exists, err := readLocalProcessRecord(directory, service)
		if err != nil {
			return 0, err
		}
		if !exists {
			continue
		}
		records = append(records, record)
	}
	return stopLocalProcessRecords(ctx, directory, records, force, timeout)
}

func stopLocalProcessRecords(ctx context.Context, directory string, records []localProcessRecord, force bool, timeout time.Duration) (int, error) {
	active := make([]localProcessRecord, 0, len(records))
	for _, record := range records {
		current, exists, err := readLocalProcessRecord(directory, record.Service)
		if err != nil {
			return 0, err
		}
		if !exists || !sameLocalProcess(current, record) {
			continue
		}
		running, err := managedLocalProcessRunning(current)
		if err != nil {
			return 0, err
		}
		if !running {
			if err := removeLocalProcessRecord(current, directory); err != nil {
				return 0, err
			}
			continue
		}
		if err := signalLocalProcessGroup(current.ProcessGroupID, force); err != nil {
			return 0, fmt.Errorf("向%s进程组发送信号失败: %w", localServiceDisplayName(current.Service), err)
		}
		active = append(active, current)
	}
	if len(active) == 0 {
		return 0, nil
	}
	if timeout <= 0 {
		timeout = localGracefulStopTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		remaining := 0
		for _, record := range active {
			running, err := localProcessGroupRunning(record.ProcessGroupID)
			if err != nil {
				return 0, fmt.Errorf("检查%s进程组状态失败: %w", localServiceDisplayName(record.Service), err)
			}
			if running {
				remaining++
			}
		}
		if remaining == 0 {
			for _, record := range active {
				if err := removeLocalProcessRecord(record, directory); err != nil {
					return 0, err
				}
			}
			return len(active), nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-deadline.C:
			if force {
				return 0, fmt.Errorf("强制结束后仍有 %d 个本地进程组未退出", remaining)
			}
			return 0, fmt.Errorf("等待本地前后端安全退出超时（%s），请执行 mage kill", timeout)
		case <-ticker.C:
		}
	}
}

type localService struct {
	id           string
	name         string
	executable   string
	args         []string
	readyURL     string
	readyTimeout time.Duration
}

func validateLocalServicesToStart(services []localService) error {
	seen := make(map[string]struct{}, len(services))
	for _, service := range services {
		if _, err := localProcessRecordPath(localProcessDirectory, service.id); err != nil {
			return err
		}
		if _, duplicate := seen[service.id]; duplicate {
			return fmt.Errorf("本地服务 %q 重复", service.id)
		}
		seen[service.id] = struct{}{}
		if err := ensureLocalServiceCanStart(localProcessDirectory, service.id); err != nil {
			return err
		}
	}
	return nil
}

type foregroundLocalServiceResult struct {
	service localService
	err     error
}

func runForegroundLocalServices(ctx context.Context, services []localService) error {
	if err := validateLocalServicesToStart(services); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan foregroundLocalServiceResult, len(services))
	started := 0

	for _, service := range services {
		if err := runCtx.Err(); err != nil {
			cancelAndWaitForForegroundLocalServices(cancel, results, started)
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		command, err := foregroundLocalCommand(runCtx, service)
		if err != nil {
			cancelAndWaitForForegroundLocalServices(cancel, results, started)
			return fmt.Errorf("准备%s进程失败: %w", service.name, err)
		}
		if err := command.Start(); err != nil {
			cancelAndWaitForForegroundLocalServices(cancel, results, started)
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("启动%s失败: %w", service.name, err)
		}
		record, err := registerLocalProcess(localProcessDirectory, service, command, "")
		if err != nil {
			_ = signalLocalProcessGroup(command.Process.Pid, true)
			_ = command.Wait()
			cancelAndWaitForForegroundLocalServices(cancel, results, started)
			return fmt.Errorf("记录%s进程失败: %w", service.name, err)
		}
		started++
		go func() {
			waitErr := command.Wait()
			waitErr = errors.Join(waitErr, removeLocalProcessRecord(record, localProcessDirectory))
			results <- foregroundLocalServiceResult{service: service, err: waitErr}
		}()

		fmt.Printf("%s已在前台启动（PID %d）\n", service.name, record.PID)
		fmt.Printf("等待%s就绪...\n", service.name)
		if err := waitForForegroundLocalServiceReady(runCtx, service, record); err != nil {
			cancelAndWaitForForegroundLocalServices(cancel, results, started)
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		fmt.Printf("%s已就绪，继续启动其他服务\n", service.name)
	}

	fmt.Println("EDO 前后端正在前台运行；按 Ctrl+C 停止，也可在另一终端执行 mage stop 或 mage kill")
	var unexpected []error
	for completed := 0; completed < started; completed++ {
		result := <-results
		if completed == 0 {
			cancel()
		}
		if ctx.Err() != nil || localProcessExitWasSignaled(result.err) {
			continue
		}
		if result.err == nil {
			unexpected = append(unexpected, fmt.Errorf("%s进程已退出", result.service.name))
			continue
		}
		unexpected = append(unexpected, fmt.Errorf("%s进程退出: %w", result.service.name, result.err))
	}
	if ctx.Err() != nil {
		return nil
	}
	return errors.Join(unexpected...)
}

func cancelAndWaitForForegroundLocalServices(cancel context.CancelFunc, results <-chan foregroundLocalServiceResult, count int) {
	cancel()
	for range count {
		<-results
	}
}

func waitForForegroundLocalServiceReady(ctx context.Context, service localService, record localProcessRecord) error {
	timeout := service.readyTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	stabilizedAt := time.Now().Add(500 * time.Millisecond)
	for {
		running, err := managedLocalProcessRunning(record)
		if err != nil {
			return err
		}
		if !running {
			return fmt.Errorf("%s进程已退出", service.name)
		}
		if time.Now().After(stabilizedAt) && (service.readyURL == "" || localServiceIsReady(readyCtx, client, service.readyURL)) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-readyCtx.Done():
			return fmt.Errorf("等待%s就绪超时（%s）", service.name, timeout)
		case <-ticker.C:
		}
	}
}

func localProcessExitWasSignaled(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && exitError.ExitCode() == -1
}

func runLocalServices(ctx context.Context, services []localService) error {
	if err := validateLocalServicesToStart(services); err != nil {
		return err
	}

	started := make([]localProcessRecord, 0, len(services))
	for _, service := range services {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, rollbackLocalServices(started))
		}
		logFile, logPath, err := openLocalServiceLog(localLogDirectory, service)
		if err != nil {
			return errors.Join(err, rollbackLocalServices(started))
		}
		command, err := backgroundLocalCommand(service, logFile)
		if err != nil {
			_ = logFile.Close()
			return errors.Join(fmt.Errorf("准备%s进程失败: %w", service.name, err), rollbackLocalServices(started))
		}
		if err := command.Start(); err != nil {
			_ = logFile.Close()
			return errors.Join(fmt.Errorf("启动%s失败: %w", service.name, err), rollbackLocalServices(started))
		}
		record, err := registerLocalProcess(localProcessDirectory, service, command, logPath)
		if err != nil {
			_ = signalLocalProcessGroup(command.Process.Pid, true)
			_ = command.Wait()
			_ = logFile.Close()
			return errors.Join(fmt.Errorf("记录%s进程失败: %w", service.name, err), rollbackLocalServices(started))
		}
		if err := command.Process.Release(); err != nil {
			_ = signalLocalProcessGroup(record.ProcessGroupID, true)
			_ = command.Wait()
			_ = logFile.Close()
			_ = removeLocalProcessRecord(record, localProcessDirectory)
			return errors.Join(fmt.Errorf("将%s转入后台失败: %w", service.name, err), rollbackLocalServices(started))
		}
		if err := logFile.Close(); err != nil {
			return errors.Join(fmt.Errorf("关闭%s日志句柄失败: %w", service.name, err), rollbackLocalServices(append(started, record)))
		}
		started = append(started, record)
		fmt.Printf("%s已在后台启动（PID %d），日志：%s\n", service.name, record.PID, record.LogPath)
		fmt.Printf("等待%s就绪...\n", service.name)
		if err := waitForBackgroundLocalServiceReady(ctx, localProcessDirectory, service, record); err != nil {
			return errors.Join(err, rollbackLocalServices(started))
		}
		fmt.Printf("%s已就绪，继续启动其他服务\n", service.name)
	}
	fmt.Println("EDO 前后端已在后台运行")
	return nil
}

func waitForBackgroundLocalServiceReady(ctx context.Context, directory string, service localService, record localProcessRecord) error {
	timeout := service.readyTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	stabilizedAt := time.Now().Add(500 * time.Millisecond)
	for {
		running, err := managedLocalProcessRunning(record)
		if err != nil {
			return err
		}
		if !running {
			_ = removeLocalProcessRecord(record, directory)
			return fmt.Errorf("%s进程启动后退出，请查看日志 %s", service.name, record.LogPath)
		}
		if time.Now().After(stabilizedAt) && (service.readyURL == "" || localServiceIsReady(readyCtx, client, service.readyURL)) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-readyCtx.Done():
			return fmt.Errorf("等待%s就绪超时（%s），请查看日志 %s", service.name, timeout, record.LogPath)
		case <-ticker.C:
		}
	}
}

func rollbackLocalServices(records []localProcessRecord) error {
	if len(records) == 0 {
		return nil
	}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), localGracefulStopTimeout)
	defer cancel()
	if _, err := stopLocalProcessRecords(rollbackCtx, localProcessDirectory, records, false, localGracefulStopTimeout); err == nil {
		return nil
	}
	forceCtx, forceCancel := context.WithTimeout(context.Background(), localForcefulStopTimeout)
	defer forceCancel()
	_, err := stopLocalProcessRecords(forceCtx, localProcessDirectory, records, true, localForcefulStopTimeout)
	if err != nil {
		return fmt.Errorf("回滚后台服务失败: %w", err)
	}
	return nil
}

func localServiceIsReady(ctx context.Context, client *http.Client, readyURL string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
}

func runCommand(ctx context.Context, executable string, args ...string) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	return command.Run()
}

func npmExecutable() string {
	return "npm"
}

func selectedComponents(server, web bool) (bool, bool) {
	if !server && !web {
		return true, true
	}
	return server, web
}
