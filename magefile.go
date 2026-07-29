//go:build mage

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const environmentFile = ".env"

// Start 启动 ZRT；可使用 --dev、--docker、--server 和 --web 调整启动方式。
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

// Help 显示 ZRT 常用开发和启动命令。
func Help() {
	fmt.Println(`ZRT Mage 命令

mage start                       构建内嵌 Web 的单文件程序并启动
mage start --server              只启动后端
mage start --web                 只启动 Web
mage start --dev                 启动开发依赖和迁移，再运行 go run 与 npm start
mage start --dev --server        开发模式，只运行后端
mage start --dev --web           开发模式，只运行 Web
mage start --docker              使用 Docker Compose 启动完整环境
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

不指定参数时，构建内嵌 Web 的单文件程序，迁移数据库后启动。
--dev     启动 Redis、NATS 和迁移，再使用 go run 与 npm start 开发运行
--docker  使用 Docker Compose 启动
--server  只启动后端
--web     只启动 Web`)
}

func loadEnvironment() error {
	return loadEnvironmentFile(environmentFile)
}

func loadEnvironmentFile(path string) error {
	if err := godotenv.Load(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("未找到根目录 .env，请先复制 .env.example 并填写必要配置")
		}
		return fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	return nil
}

func validateStartSecretsKey(runServer bool) error {
	if !runServer {
		return nil
	}
	encoded := strings.TrimSpace(os.Getenv("ZRT_SECRETS_KEY"))
	if encoded == "" {
		return errors.New("请在根目录 .env 中配置 ZRT_SECRETS_KEY")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(key) != 32 {
		return errors.New(".env 中的 ZRT_SECRETS_KEY 必须是 32 字节随机密钥的 Base64 编码")
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
	return runCommand(ctx, "docker", args...)
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
		if err := os.Setenv("ZRT_WEB_ROOT", ""); err != nil {
			return fmt.Errorf("设置 Web 目录失败: %w", err)
		}
		if err := runCommand(ctx, backend, "migrate"); err != nil {
			return fmt.Errorf("数据库迁移失败: %w", err)
		}
		fmt.Println("ZRT 后端：http://127.0.0.1:8080")
		if runWeb {
			fmt.Println("ZRT Web：http://127.0.0.1:8080")
		}
		return runCommand(ctx, backend, "server")
	}

	fmt.Println("ZRT Web：http://127.0.0.1:5173")
	return runCommand(ctx, npmExecutable(), "--prefix", "web", "run", "preview", "--", "--host", "127.0.0.1", "--port", "5173")
}

func startDevelopment(ctx context.Context, runServer, runWeb bool) error {
	if runServer {
		if err := startDevelopmentDependencies(ctx); err != nil {
			return err
		}
		if err := os.Setenv("ZRT_WEB_ROOT", ""); err != nil {
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
		fmt.Println("ZRT 后端：http://127.0.0.1:8080")
		fmt.Println("ZRT Web：http://127.0.0.1:5173")
		return runLocalServices(ctx, []localService{
			{
				name:         "后端",
				executable:   "go",
				args:         []string{"run", "./cmd/zrt", "server"},
				readyURL:     "http://127.0.0.1:8080/api/v1/health/ready",
				readyTimeout: 90 * time.Second,
			},
			{name: "Web", executable: npmExecutable(), args: []string{"--prefix", "web", "start"}},
		})
	}
	if runServer {
		fmt.Println("ZRT 后端：http://127.0.0.1:8080")
		return runCommand(ctx, "go", "run", "./cmd/zrt", "server")
	}
	fmt.Println("ZRT Web：http://127.0.0.1:5173")
	return runCommand(ctx, npmExecutable(), "--prefix", "web", "start")
}

func migrateFromSource(ctx context.Context) error {
	if err := runCommand(ctx, "go", "run", "./cmd/zrt", "migrate"); err != nil {
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
	if err := os.Unsetenv("ZRT_DOCKER_BUILDER_HOST"); err != nil {
		return fmt.Errorf("切换到宿主机 Docker 构建运行时失败: %w", err)
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
	binary := filepath.Join("bin", "zrt")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// 本地单文件构建与容器发布构建保持一致，移除生产运行不需要的符号表和 DWARF 调试信息。
	args := []string{"build", "-trimpath", "-ldflags", "-s -w"}
	if embedWeb {
		args = append(args, "-tags", "zrt_web")
	}
	args = append(args, "-o", binary, "./cmd/zrt")
	if err := runCommand(ctx, "go", args...); err != nil {
		return "", fmt.Errorf("构建 ZRT 后端失败: %w", err)
	}
	absolute, err := filepath.Abs(binary)
	if err != nil {
		return "", fmt.Errorf("解析 ZRT 后端路径失败: %w", err)
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
	vite := filepath.Join("web", "node_modules", ".bin", "vite")
	if runtime.GOOS == "windows" {
		vite += ".cmd"
	}
	if _, err := os.Stat(vite); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查 Web 依赖失败: %w", err)
	}
	if err := runCommand(ctx, npmExecutable(), "--prefix", "web", "ci"); err != nil {
		return fmt.Errorf("安装 Web 依赖失败: %w", err)
	}
	return nil
}

type localService struct {
	name         string
	executable   string
	args         []string
	readyURL     string
	readyTimeout time.Duration
}

type localServiceResult struct {
	name string
	err  error
}

func runLocalServices(ctx context.Context, services []localService) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan localServiceResult, len(services))
	started := 0
	for _, service := range services {
		command := exec.CommandContext(runCtx, service.executable, service.args...)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		command.Stdin = os.Stdin
		if err := command.Start(); err != nil {
			cancel()
			for range started {
				<-results
			}
			return fmt.Errorf("启动%s失败: %w", service.name, err)
		}
		started++
		go func(name string, command *exec.Cmd) {
			results <- localServiceResult{name: name, err: command.Wait()}
		}(service.name, command)

		if service.readyURL == "" {
			continue
		}
		fmt.Printf("等待%s就绪...\n", service.name)
		stopped, err := waitForLocalServiceReady(runCtx, service, results)
		if stopped != nil || err != nil {
			cancel()
			remaining := started
			if stopped != nil {
				remaining--
			}
			for range remaining {
				<-results
			}
			if ctx.Err() != nil {
				return nil
			}
			if stopped != nil {
				if stopped.err == nil {
					return fmt.Errorf("%s进程在就绪前退出", stopped.name)
				}
				return fmt.Errorf("%s进程在就绪前退出: %w", stopped.name, stopped.err)
			}
			return err
		}
		fmt.Printf("%s已就绪，继续启动其他服务\n", service.name)
	}

	first := <-results
	cancel()
	for range started - 1 {
		<-results
	}
	if ctx.Err() != nil {
		return nil
	}
	if first.err == nil {
		return nil
	}
	return fmt.Errorf("%s进程退出: %w", first.name, first.err)
}

func waitForLocalServiceReady(
	ctx context.Context,
	service localService,
	results <-chan localServiceResult,
) (*localServiceResult, error) {
	timeout := service.readyTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		if localServiceIsReady(readyCtx, client, service.readyURL) {
			return nil, nil
		}
		select {
		case result := <-results:
			return &result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-readyCtx.Done():
			return nil, fmt.Errorf("等待%s就绪超时（%s）", service.name, timeout)
		case <-ticker.C:
		}
	}
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
	if runtime.GOOS == "windows" {
		return "npm.cmd"
	}
	return "npm"
}

func selectedComponents(server, web bool) (bool, bool) {
	if !server && !web {
		return true, true
	}
	return server, web
}
