//go:build mage

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/joho/godotenv"
)

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
	if options.docker {
		return finishStart(startDocker(ctx, runServer, runWeb), options.provided)
	}
	if err := loadEnvironment(); err != nil {
		return err
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
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取 .env 失败: %w", err)
	}
	return nil
}

func startDocker(ctx context.Context, runServer, runWeb bool) error {
	args := []string{"compose", "-f", "deploy/compose.dev.yml", "up", "-d", "--build"}
	if runServer && !runWeb {
		args = append(args, "api")
	} else if runWeb && !runServer {
		args = append(args, "--no-deps", "web")
	}
	return runCommand(ctx, "docker", args...)
}

func startBuilt(ctx context.Context, runServer, runWeb bool) error {
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
			{name: "后端", executable: "go", args: []string{"run", "./cmd/zrt", "server"}},
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
	fmt.Println("启动开发依赖：Redis、NATS JetStream")
	if err := runCommand(
		ctx,
		"docker",
		"compose",
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

func buildBackend(ctx context.Context, embedWeb bool) (string, error) {
	if err := os.MkdirAll("bin", 0o755); err != nil {
		return "", fmt.Errorf("创建 bin 目录失败: %w", err)
	}
	binary := filepath.Join("bin", "zrt")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	args := []string{"build", "-trimpath"}
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
	name       string
	executable string
	args       []string
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
