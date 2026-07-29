package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bsm/redislock"
	"golang.org/x/term"
	"gorm.io/gorm"

	"zrt/internal/access"
	"zrt/internal/account"
	"zrt/internal/audit"
	"zrt/internal/auth"
	"zrt/internal/bootstrap"
	"zrt/internal/config"
	"zrt/internal/configuration"
	"zrt/internal/credential"
	"zrt/internal/database"
	"zrt/internal/deployment"
	dnsmanager "zrt/internal/dns"
	"zrt/internal/dockerengine"
	environmentmanager "zrt/internal/environment"
	hostmanager "zrt/internal/host"
	"zrt/internal/httpapi"
	"zrt/internal/identity"
	"zrt/internal/kube"
	"zrt/internal/legacyimport"
	"zrt/internal/logging"
	"zrt/internal/logretention"
	"zrt/internal/model"
	"zrt/internal/monitor"
	"zrt/internal/notification"
	"zrt/internal/outbox"
	"zrt/internal/pipeline"
	"zrt/internal/repository"
	"zrt/internal/scheduler"
	"zrt/internal/secret"
	"zrt/internal/sshdeploy"
	"zrt/internal/systemmetrics"
	"zrt/internal/task"
	"zrt/internal/terminal"
	"zrt/internal/worker"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		logger := logging.New("info")
		logger.Error("ZRT 进程退出", "operation", "process_run", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	logger, runtimeLogs := logging.NewRuntime(cfg.LogLevel)
	slog.SetDefault(logger)
	command := "server"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "version" {
		fmt.Printf("ZRT %s\n", version)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch command {
	case "migrate":
		return runMigrate(ctx, cfg, logger)
	case "server":
		return runServer(ctx, cfg, logger, runtimeLogs)
	case "admin":
		return runAdmin(ctx, cfg, logger, os.Args[2:])
	case "legacy-import":
		return runLegacyImport(ctx, cfg, logger, os.Args[2:])
	default:
		return fmt.Errorf("未知启动命令 %q，可用命令: server、migrate、admin、legacy-import、version", command)
	}
}

func runAdmin(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errors.New("管理员命令格式：zrt admin create 或 zrt admin reset-password")
	}
	switch args[0] {
	case "create":
		return runAdminCreate(ctx, cfg, logger, args[1:])
	case "reset-password":
		return runAdminResetPassword(ctx, cfg, logger, args[1:])
	default:
		return errors.New("管理员命令格式：zrt admin create 或 zrt admin reset-password")
	}
}

func runAdminCreate(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("admin create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	username := flags.String("username", "", "管理员用户名")
	nickname := flags.String("nickname", "管理员", "管理员昵称")
	if err := flags.Parse(args); err != nil {
		return errors.New("管理员命令参数无效")
	}
	password, err := readAdminPassword()
	if err != nil {
		return err
	}
	db, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		logger.Error("打开管理员数据库失败", "operation", "admin_create_open", "driver", cfg.Database.Driver, "err", err)
		return errors.New("管理员创建失败")
	}
	defer func() {
		if err := database.Close(db); err != nil {
			logger.Error("创建管理员后关闭数据库失败", "operation", "admin_create_close", "err", err)
		}
	}()
	if err := database.VerifyMigrations(ctx, db); err != nil {
		logger.Error("管理员创建前数据库迁移检查失败", "operation", "admin_create_migration", "err", err)
		return errors.New("请先执行数据库迁移")
	}
	user, err := account.NewService(db).CreateAdmin(ctx, *username, *nickname, password)
	password = ""
	if err != nil {
		logger.Error("创建管理员失败", "operation", "admin_create", "username", *username, "err", err)
		return errors.New("管理员创建失败，请检查用户名是否重复及密码是否符合要求")
	}
	logger.Info("管理员创建完成", "operation", "admin_create", "user_id", user.ID, "username", user.Username)
	return nil
}

func runAdminResetPassword(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("admin reset-password", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	username := flags.String("username", "", "待重置密码的用户名")
	if err := flags.Parse(args); err != nil || strings.TrimSpace(*username) == "" {
		return errors.New("密码重置命令格式：zrt admin reset-password --username 用户名")
	}
	password, err := readAdminPassword()
	if err != nil {
		return err
	}
	db, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		logger.Error("打开密码重置数据库失败", "operation", "admin_reset_password_open", "driver", cfg.Database.Driver, "err", err)
		return errors.New("密码重置失败")
	}
	defer func() {
		if err := database.Close(db); err != nil {
			logger.Error("密码重置后关闭数据库失败", "operation", "admin_reset_password_close", "err", err)
		}
	}()
	if err := database.VerifyMigrations(ctx, db); err != nil {
		logger.Error("密码重置前数据库迁移检查失败", "operation", "admin_reset_password_migration", "err", err)
		return errors.New("请先执行数据库迁移")
	}
	user, err := account.NewService(db).ResetPassword(ctx, *username, password)
	password = ""
	if err != nil {
		logger.Error("重置用户密码失败", "operation", "admin_reset_password", "username", *username, "err", err)
		return errors.New("密码重置失败，请检查用户名和密码格式")
	}
	logger.Info("用户密码已重置并启用账户", "operation", "admin_reset_password", "user_id", user.ID, "username", user.Username)
	return nil
}

func runLegacyImport(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("legacy-import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "只检查和统计，不写入 ZRT 数据库")
	if err := flags.Parse(args); err != nil {
		return errors.New("旧数据迁移命令格式：zrt legacy-import [--dry-run]")
	}
	sourceDriver := strings.ToLower(strings.TrimSpace(os.Getenv("ZRT_LEGACY_DATABASE_DRIVER")))
	sourceDSN := strings.TrimSpace(os.Getenv("ZRT_LEGACY_DATABASE_DSN"))
	if sourceDriver == "" || sourceDSN == "" {
		return errors.New("请通过 ZRT_LEGACY_DATABASE_DRIVER 和 ZRT_LEGACY_DATABASE_DSN 配置只读来源数据库")
	}
	if sameDatabase(sourceDriver, sourceDSN, cfg.Database.Driver, cfg.Database.DSN) {
		return errors.New("旧数据来源数据库不能与 ZRT 目标数据库相同")
	}
	destination, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		logger.Error("打开旧数据迁移目标数据库失败", "operation", "legacy_import_destination_open", "driver", cfg.Database.Driver, "err", err)
		return errors.New("旧数据迁移启动失败")
	}
	defer func() {
		if err := database.Close(destination); err != nil {
			logger.Error("旧数据迁移后关闭目标数据库失败", "operation", "legacy_import_destination_close", "err", err)
		}
	}()
	if err := database.VerifyMigrations(ctx, destination); err != nil {
		logger.Error("旧数据迁移前目标数据库迁移检查失败", "operation", "legacy_import_migration", "err", err)
		return errors.New("请先对 ZRT 目标数据库执行数据库迁移")
	}
	sourceConfig := cfg.Database
	sourceConfig.Driver = sourceDriver
	sourceConfig.DSN = sourceDSN
	if sourceDriver == "sqlite" {
		var err error
		sourceConfig.DSN, err = readOnlySQLiteDSN(sourceDSN)
		if err != nil {
			return err
		}
		sourceConfig.MaxOpenConns, sourceConfig.MaxIdleConns = 1, 1
	}
	source, err := database.Open(ctx, sourceConfig, logger)
	if err != nil {
		logger.Error("打开旧数据来源数据库失败", "operation", "legacy_import_source_open", "driver", sourceDriver, "err", err)
		return errors.New("无法连接旧数据来源数据库")
	}
	defer func() {
		if err := database.Close(source); err != nil {
			logger.Error("旧数据迁移后关闭来源数据库失败", "operation", "legacy_import_source_close", "err", err)
		}
	}()
	secretManager, err := secret.New(cfg.Secrets.Key)
	if err != nil {
		logger.Error("初始化旧数据迁移密钥管理器失败", "operation", "legacy_import_secret", "err", err)
		return errors.New("旧数据迁移密钥配置无效")
	}
	report, err := legacyimport.New(source, destination, secretManager, *dryRun).Run(ctx)
	if err != nil {
		logger.Error("执行旧数据迁移失败", "operation", "legacy_import", "source_driver", sourceDriver, "dry_run", *dryRun, "err", err)
		switch {
		case errors.Is(err, legacyimport.ErrNotLegacyDatabase):
			return legacyimport.ErrNotLegacyDatabase
		case errors.Is(err, legacyimport.ErrSecretsRequired):
			return legacyimport.ErrSecretsRequired
		default:
			return errors.New("旧数据迁移失败，请查看服务日志")
		}
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		logger.Error("序列化旧数据迁移报告失败", "operation", "legacy_import_report", "err", err)
		return errors.New("旧数据迁移完成，但生成报告失败")
	}
	fmt.Println(string(encoded))
	logger.Info("旧数据迁移完成", "operation", "legacy_import", "source_driver", sourceDriver, "dry_run", *dryRun)
	return nil
}

func sameDatabase(sourceDriver, sourceDSN, destinationDriver, destinationDSN string) bool {
	if sourceDriver != destinationDriver {
		return false
	}
	if sourceDriver != "sqlite" {
		return strings.TrimSpace(sourceDSN) == strings.TrimSpace(destinationDSN)
	}
	sourcePath, sourceOK := sqliteFilePath(sourceDSN)
	destinationPath, destinationOK := sqliteFilePath(destinationDSN)
	return sourceOK && destinationOK && sourcePath == destinationPath
}

func sqliteFilePath(dsn string) (string, bool) {
	value := strings.TrimSpace(dsn)
	if value == "" || value == ":memory:" || strings.Contains(value, "mode=memory") {
		return value, value != ""
	}
	value = strings.TrimPrefix(value, "file:")
	if index := strings.IndexByte(value, '?'); index >= 0 {
		value = value[:index]
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", false
	}
	return filepath.Clean(absolute), true
}

func readOnlySQLiteDSN(dsn string) (string, error) {
	value := strings.TrimSpace(dsn)
	if value == "" || value == ":memory:" || strings.Contains(value, "mode=memory") {
		return "", errors.New("SQLite 旧数据来源必须是已有的数据库文件")
	}
	if strings.Contains(value, "mode=") && !strings.Contains(value, "mode=ro") {
		return "", errors.New("SQLite 旧数据来源必须使用只读 mode=ro")
	}
	if !strings.HasPrefix(value, "file:") {
		value = "file:" + value
	}
	if !strings.Contains(value, "mode=ro") {
		separator := "?"
		if strings.Contains(value, "?") {
			separator = "&"
		}
		value += separator + "mode=ro"
	}
	return value, nil
}

func readAdminPassword() (string, error) {
	if password, ok := os.LookupEnv("ZRT_ADMIN_PASSWORD"); ok {
		_ = os.Unsetenv("ZRT_ADMIN_PASSWORD")
		if err := account.ValidatePassword(password); err != nil {
			return "", err
		}
		return password, nil
	}
	stdin := int(os.Stdin.Fd())
	if !term.IsTerminal(stdin) {
		return "", errors.New("非交互环境请通过临时变量 ZRT_ADMIN_PASSWORD 提供管理员密码")
	}
	fmt.Fprint(os.Stderr, "请输入管理员密码：")
	first, err := term.ReadPassword(stdin)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", errors.New("读取管理员密码失败")
	}
	fmt.Fprint(os.Stderr, "请再次输入管理员密码：")
	second, err := term.ReadPassword(stdin)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", errors.New("读取管理员密码失败")
	}
	if string(first) != string(second) {
		return "", errors.New("两次输入的管理员密码不一致")
	}
	password := string(first)
	for i := range first {
		first[i] = 0
	}
	for i := range second {
		second[i] = 0
	}
	if err := account.ValidatePassword(password); err != nil {
		return "", err
	}
	return password, nil
}

func runMigrate(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	db, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		logger.Error("打开迁移数据库失败", "operation", "database_migrate_open", "driver", cfg.Database.Driver, "err", err)
		return errors.New("数据库迁移启动失败")
	}
	defer func() {
		if err := database.Close(db); err != nil {
			logger.Error("迁移后关闭数据库失败", "operation", "database_migrate_close", "err", err)
		}
	}()
	if err := database.Migrate(ctx, db); err != nil {
		logger.Error("执行数据库迁移失败", "operation", "database_migrate", "driver", cfg.Database.Driver, "err", err)
		return errors.New("数据库迁移失败")
	}
	logger.Info("数据库迁移完成", "operation", "database_migrate", "driver", cfg.Database.Driver)
	return nil
}

func runServer(ctx context.Context, cfg config.Config, logger *slog.Logger, runtimeLogs *logging.RuntimeController) error {
	// 先占用监听地址，再初始化数据库、消息队列和后台消费者。端口冲突时应立即退出，
	// 避免启动一半后产生一串 context canceled 的连带错误。
	listener, err := net.Listen("tcp", cfg.Server.Address)
	if err != nil {
		logger.Error("HTTP 服务绑定端口失败", "operation", "http_listen", "address", cfg.Server.Address, "err", err)
		return errors.New("HTTP 服务启动失败")
	}
	defer listener.Close()

	serviceCtx, cancelService := context.WithCancel(ctx)
	defer cancelService()
	resources, err := bootstrap.Open(serviceCtx, cfg, logger)
	if err != nil {
		logger.Error("初始化服务依赖失败", "operation", "server_bootstrap", "err", err)
		return errors.New("服务依赖初始化失败")
	}
	defer resources.Close()
	accounts := account.NewService(resources.Database)
	initialAdmin, created, err := accounts.EnsureInitialAdmin(serviceCtx)
	if err != nil {
		logger.Error("初始化管理员账户失败", "operation", "account_bootstrap", "err", err)
		return errors.New("管理员账户初始化失败")
	}
	if created {
		logger.Info("初始化管理员账户已创建", "operation", "account_bootstrap", "user_id", initialAdmin.ID, "username", initialAdmin.Username)
	}
	accessService, err := access.NewDistributedService(
		resources.Database,
		resources.Redis.Client(),
		resources.Redis.Key("casbin", "policy"),
		logger,
	)
	if err != nil {
		logger.Error("初始化 Casbin 权限服务失败", "operation", "rbac_bootstrap", "err", err)
		return errors.New("权限服务初始化失败")
	}
	auditService := audit.NewService(resources.Database)
	secretManager, err := secret.New(cfg.Secrets.Key)
	if err != nil {
		logger.Error("初始化基础设施密钥管理器失败", "operation", "secret_bootstrap", "err", err)
		return errors.New("密钥服务初始化失败")
	}
	configurationService := configuration.NewService(resources.Database, secretManager)
	runtimeLogSettings, err := configurationService.GetRuntimeLoggingSettings(serviceCtx, cfg.LogLevel, true)
	if err != nil {
		logger.Error("读取运行日志设置失败，继续使用启动配置", "operation", "runtime_logging_bootstrap", "err", err)
	} else if err := runtimeLogs.Apply(runtimeLogSettings.Level, runtimeLogSettings.HTTPAccessEnabled); err != nil {
		logger.Error("应用运行日志设置失败，继续使用启动配置", "operation", "runtime_logging_bootstrap", "err", err)
	}
	logRetentionService := logretention.NewService(resources.Database, configurationService, logger)
	databaseTransferService := database.NewTransferService(serviceCtx, resources.Database, cfg.Database.Driver, logger)
	sessions := auth.NewSessionStore(resources.Redis, cfg.Auth.SessionTTL)
	limiter := auth.NewLoginRateLimiter(resources.Redis, cfg.Auth.LoginMaxFailure, cfg.Auth.LoginWindow, configurationService)
	loginService, err := account.NewLoginService(accounts, sessions, limiter, logger)
	if err != nil {
		logger.Error("初始化登录服务失败", "operation", "auth_bootstrap", "err", err)
		return errors.New("登录服务初始化失败")
	}
	credentialService := credential.NewService(resources.Database, secretManager)
	dnsService := dnsmanager.NewService(resources.Database, secretManager, dnsmanager.NewRegistry())
	repositoryService, err := newRepositoryService(resources.Database, cfg, secretManager, credentialService, configurationService)
	if err != nil {
		logger.Error("初始化代码仓库服务失败", "operation", "repository_bootstrap", "err", err)
		return errors.New("代码仓库服务初始化失败")
	}
	identityService := identity.NewService(resources.Database, resources.Redis, secretManager, accounts, loginService, limiter)
	pipelineService := pipeline.NewService(resources.Database, repositoryService, secretManager)
	dockerService := dockerengine.NewService(resources.Database, secretManager, cfg.Runtime)
	kubernetesService := kube.NewService(resources.Database, secretManager, cfg.Runtime)
	sshDeploymentService := sshdeploy.NewService(resources.Database, secretManager, cfg.Runtime)
	hostService := hostmanager.NewService(resources.Database, secretManager, dockerService, kubernetesService)
	if err := hostService.RefreshLocalCapabilities(serviceCtx); err != nil {
		logger.Warn("刷新本地主机能力失败", "operation", "local_host_capability_refresh", "err", err)
	}
	environmentService := environmentmanager.NewService(resources.Database)
	deploymentService := deployment.NewService(
		resources.Database,
		dockerService,
		kubernetesService,
		sshDeploymentService,
		redislock.New(resources.Redis.Client()),
		resources.Redis.Key("lock", "deployment"),
		logger,
	)
	pipelineService.ConfigureExecution(dockerService, deploymentService, logger)
	terminalService := terminal.NewService(dockerService, kubernetesService, cfg.Runtime.TerminalMaxDuration)
	notificationService := notification.NewService(resources.Database, secretManager, nil, cfg.NATS.MaxAttempts)
	monitorService := monitor.NewService(resources.Database, secretManager, notificationService, nil, cfg.NATS.MaxAttempts, logger)
	schedulerService := scheduler.NewService(resources.Database, notificationService, cfg.NATS.MaxAttempts, logger)
	taskService := task.NewService(resources.Database, cfg.NATS.MaxAttempts)
	taskWorker, err := newBackgroundTaskWorker(
		resources, cfg, logger, repositoryService, pipelineService, deploymentService,
		notificationService, monitorService, schedulerService,
	)
	if err != nil {
		logger.Error("初始化后台任务服务失败", "operation", "server_background_bootstrap", "err", err)
		return errors.New("后台任务服务初始化失败")
	}
	systemMetricsService := systemmetrics.New(resources.Database, resources.SQL, taskWorker, resources.NATS, logger)

	router := httpapi.NewRouter(httpapi.Dependencies{
		Environment:      cfg.Environment,
		Database:         resources.SQL,
		Redis:            resources.Redis,
		NATS:             resources.NATS,
		Logger:           logger,
		RuntimeLogs:      runtimeLogs,
		Version:          version,
		WebRoot:          cfg.Server.WebRoot,
		AuthConfig:       cfg.Auth,
		Accounts:         accounts,
		Login:            loginService,
		LoginLimiter:     limiter,
		Sessions:         sessions,
		Access:           accessService,
		Audits:           auditService,
		Identities:       identityService,
		Repositories:     repositoryService,
		Pipelines:        pipelineService,
		Docker:           dockerService,
		Kubernetes:       kubernetesService,
		Hosts:            hostService,
		Environments:     environmentService,
		Deployments:      deploymentService,
		Terminal:         terminalService,
		Configurations:   configurationService,
		Credentials:      credentialService,
		DNS:              dnsService,
		Notifications:    notificationService,
		Monitors:         monitorService,
		Scheduler:        schedulerService,
		Tasks:            taskService,
		SystemMetrics:    systemMetricsService,
		LogRetention:     logRetentionService,
		DatabaseTransfer: databaseTransferService,
	})
	server := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           router,
		ReadHeaderTimeout: cfg.Server.ReadTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}

	// HTTP、任务消费者、Outbox、监控和调度共享同一个取消信号，确保单进程一致启停。
	backgroundErrCh := make(chan error, 1)
	var background sync.WaitGroup
	startBackground := func(operation string, run func() error) {
		background.Add(1)
		go func() {
			defer background.Done()
			if err := run(); err != nil {
				if serviceCtx.Err() != nil {
					logger.Warn("后台协程停止时返回错误", "operation", operation, "err", err)
					return
				}
				logger.Error("关键后台协程异常退出", "operation", operation, "err", err)
				select {
				case backgroundErrCh <- err:
				default:
				}
			}
		}()
	}
	publisher := outbox.New(resources.Database, resources.NATS, logger, cfg.NATS.MaxAttempts)
	startBackground("server_outbox", func() error { return publisher.Run(serviceCtx) })
	startBackground("server_task_consumer", func() error { return taskWorker.Run(serviceCtx) })
	startBackground("server_application_watcher", func() error {
		pipelineService.RunWatcher(serviceCtx, cfg.Scheduler.PollInterval)
		return nil
	})
	startBackground("server_release_plan_reconciler", func() error {
		pipelineService.RunReleasePlanExecutionReconciler(serviceCtx, time.Second)
		return nil
	})
	startBackground("server_pipeline_commit_backfill", func() error {
		if err := pipelineService.BackfillCommitMessages(serviceCtx, 50); err != nil {
			logger.Warn("补全历史流水线提交说明失败", "operation", "pipeline_commit_backfill", "err", err)
		}
		return nil
	})
	startBackground("server_monitor_scanner", func() error {
		monitorService.Run(serviceCtx, cfg.Scheduler.PollInterval)
		return nil
	})
	startBackground("server_scheduler_scanner", func() error {
		schedulerService.Run(serviceCtx, cfg.Scheduler.PollInterval)
		return nil
	})
	startBackground("server_host_runtime_monitor", func() error {
		type refreshResult struct {
			changes []hostmanager.RuntimeStatusChange
			err     error
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		results := make(chan refreshResult, 4)
		refreshSlots := make(chan struct{}, 4)
		var refreshes sync.WaitGroup
		scheduleRefresh := func() {
			select {
			case refreshSlots <- struct{}{}:
			case <-serviceCtx.Done():
				return
			default:
				return
			}
			refreshes.Add(1)
			go func() {
				defer refreshes.Done()
				defer func() { <-refreshSlots }()
				changes, err := hostService.RefreshRuntimeStatuses(serviceCtx)
				select {
				case results <- refreshResult{changes: changes, err: err}:
				case <-serviceCtx.Done():
				}
			}()
		}
		scheduleRefresh()
		statusReadFailed := false
		for {
			select {
			case result := <-results:
				if result.err != nil {
					if serviceCtx.Err() != nil {
						refreshes.Wait()
						return nil
					}
					if !statusReadFailed {
						logger.Warn("刷新主机运行时状态失败", "operation", "host_runtime_status_refresh", "err", result.err)
					}
					statusReadFailed = true
				} else {
					if statusReadFailed {
						logger.Info("主机运行时状态刷新已恢复", "operation", "host_runtime_status_refresh")
					}
					statusReadFailed = false
				}
				for _, change := range result.changes {
					if change.Status == model.HostCapabilityUnreachable {
						logger.Warn("主机运行时连接失败", "operation", "host_runtime_probe", "host_id", change.HostID, "capability", change.Kind, "err", change.Err)
					} else {
						logger.Info("主机运行时连接正常", "operation", "host_runtime_probe", "host_id", change.HostID, "capability", change.Kind)
					}
				}
			case <-ticker.C:
				scheduleRefresh()
			case <-serviceCtx.Done():
				refreshes.Wait()
				return nil
			}
		}
	})
	startBackground("server_log_retention", func() error { return logRetentionService.Run(serviceCtx) })

	httpErrCh := make(chan error, 1)
	go func() {
		httpErrCh <- server.Serve(listener)
	}()
	logger.Info("ZRT HTTP 服务已启动", "operation", "http_listen", "address", listener.Addr().String())
	logger.Info("ZRT 后台任务协程已启动", "operation", "server_background_start", "concurrency", cfg.Worker.Concurrency)

	var resultErr error
	select {
	case <-ctx.Done():
	case err := <-httpErrCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP 服务监听失败", "operation", "http_listen", "address", cfg.Server.Address, "err", err)
			resultErr = errors.New("HTTP 服务启动失败")
		}
	case <-backgroundErrCh:
		resultErr = errors.New("后台任务服务异常退出")
	}

	// 先广播取消信号停止接收新任务，再关闭 HTTP，并等待在途任务在配置上限内结束。
	cancelService()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP 服务优雅退出失败", "operation", "http_shutdown", "err", err)
		if resultErr == nil {
			resultErr = errors.New("HTTP 服务退出失败")
		}
	}
	cancelShutdown()

	backgroundDone := make(chan struct{})
	go func() {
		background.Wait()
		close(backgroundDone)
	}()
	waitTimer := time.NewTimer(cfg.Worker.ShutdownTimeout + cfg.Server.ShutdownTimeout)
	defer waitTimer.Stop()
	select {
	case <-backgroundDone:
	case <-waitTimer.C:
		logger.Error("等待后台协程退出超时", "operation", "server_background_shutdown")
		if resultErr == nil {
			resultErr = errors.New("后台任务服务退出超时")
		}
	}
	logger.Info("ZRT 服务已停止", "operation", "server_shutdown", "time", time.Now().UTC())
	return resultErr
}

func newBackgroundTaskWorker(
	resources *bootstrap.Resources,
	cfg config.Config,
	logger *slog.Logger,
	repositoryService *repository.Service,
	pipelineService *pipeline.Service,
	deploymentService *deployment.Service,
	notificationService *notification.Service,
	monitorService *monitor.Service,
	schedulerService *scheduler.Service,
) (*worker.Worker, error) {
	registry := worker.NewRegistry()
	if err := registry.Register("system.noop", func(context.Context, task.Message) error { return nil }); err != nil {
		logger.Error("注册内置任务处理器失败", "operation", "worker_register", "kind", "system.noop", "err", err)
		return nil, errors.New("后台任务初始化失败")
	}
	if err := registry.Register("repository.webhook", func(ctx context.Context, message task.Message) error {
		if err := repositoryService.ProcessWebhookTask(ctx, message.Payload); err != nil {
			if errors.Is(err, repository.ErrInvalidTaskPayload) {
				return worker.NewPermanentError("invalid_webhook_task", repository.ErrInvalidTaskPayload.Error(), err)
			}
			return worker.NewRetryableError("webhook_database_unavailable", "Webhook 事件处理暂时失败", err)
		}
		var payload repository.WebhookTaskPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return worker.NewPermanentError("invalid_webhook_task", repository.ErrInvalidTaskPayload.Error(), err)
		}
		if err := pipelineService.HandleRepositoryEvent(ctx, payload); err != nil {
			return worker.NewRetryableError("application_webhook_update_failed", "应用监听状态暂时更新失败", err)
		}
		return nil
	}); err != nil {
		logger.Error("注册 Webhook 任务处理器失败", "operation", "worker_register", "kind", "repository.webhook", "err", err)
		return nil, errors.New("后台任务初始化失败")
	}
	if err := registry.Register("pipeline.deploy", func(ctx context.Context, message task.Message) error {
		var payload pipeline.DeployTaskPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return worker.NewPermanentError("invalid_pipeline_deploy_task", "流水线执行任务参数无效", err)
		}
		if payload.PipelineRunID == "" || payload.WorkflowNodeID == "" {
			return worker.NewPermanentError("invalid_pipeline_deploy_task", "流水线执行任务参数无效", errors.New("流水线运行或节点为空"))
		}
		if err := pipelineService.ExecuteDeployTask(ctx, payload, message.JobID); err != nil {
			return worker.NewPermanentError("pipeline_deploy_failed", "流水线真实构建或发布失败", err)
		}
		return nil
	}); err != nil {
		logger.Error("注册流水线执行任务处理器失败", "operation", "worker_register", "kind", "pipeline.deploy", "err", err)
		return nil, errors.New("后台任务初始化失败")
	}
	registerDeployment := func(kind string) error {
		return registry.Register(kind, func(ctx context.Context, message task.Message) error {
			var payload deployment.TaskPayload
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				return worker.NewPermanentError("invalid_deployment_task", "发布任务参数无效", err)
			}
			if payload.DeploymentID == "" {
				return worker.NewPermanentError("invalid_deployment_task", "发布任务参数无效", errors.New("deployment_id 为空"))
			}
			if err := deploymentService.Run(ctx, payload.DeploymentID); err != nil {
				return worker.NewPermanentError("deployment_failed", "发布执行失败，需人工确认目标状态", err)
			}
			return nil
		})
	}
	for _, kind := range []string{"deploy.runtime", "rollback.runtime"} {
		if err := registerDeployment(kind); err != nil {
			logger.Error("注册发布任务处理器失败", "operation", "worker_register", "kind", kind, "err", err)
			return nil, errors.New("后台任务初始化失败")
		}
	}
	if err := registry.Register("notification.dispatch", func(ctx context.Context, message task.Message) error {
		var payload notification.TaskPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return worker.NewPermanentError("invalid_notification_task", notification.ErrInvalidTaskPayload.Error(), err)
		}
		if payload.NotificationID == "" {
			return worker.NewPermanentError("invalid_notification_task", notification.ErrInvalidTaskPayload.Error(), errors.New("notification_id 为空"))
		}
		if err := notificationService.Dispatch(ctx, payload.NotificationID, message.JobID); err != nil {
			switch {
			case notification.IsRetryable(err):
				return worker.NewRetryableError("notification_temporary_failure", "通知发送暂时失败", err)
			case notification.IsDispatchError(err), errors.Is(err, notification.ErrNotificationNotFound):
				return worker.NewPermanentError("notification_delivery_failed", "通知发送失败，请检查渠道配置", err)
			default:
				return worker.NewRetryableError("notification_storage_unavailable", "通知状态暂时不可用", err)
			}
		}
		return nil
	}); err != nil {
		logger.Error("注册通知任务处理器失败", "operation", "worker_register", "kind", "notification.dispatch", "err", err)
		return nil, errors.New("后台任务初始化失败")
	}
	if err := registry.Register("monitor.check", func(ctx context.Context, message task.Message) error {
		var payload monitor.TaskPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return worker.NewPermanentError("invalid_monitor_task", monitor.ErrInvalidTaskPayload.Error(), err)
		}
		if err := monitorService.Execute(ctx, payload, message.JobID); err != nil {
			if errors.Is(err, monitor.ErrInvalidTaskPayload) || errors.Is(err, monitor.ErrInvalidRule) ||
				errors.Is(err, monitor.ErrRuleNotFound) || errors.Is(err, secret.ErrUnavailable) ||
				errors.Is(err, notification.ErrChannelNotFound) {
				return worker.NewPermanentError("monitor_configuration_invalid", "监控任务配置无效", err)
			}
			return worker.NewRetryableError("monitor_temporary_failure", "监控任务暂时失败", err)
		}
		return nil
	}); err != nil {
		logger.Error("注册监控任务处理器失败", "operation", "worker_register", "kind", "monitor.check", "err", err)
		return nil, errors.New("后台任务初始化失败")
	}
	if err := registry.Register("scheduler.execute", func(ctx context.Context, message task.Message) error {
		var payload scheduler.TaskPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return worker.NewPermanentError("invalid_scheduler_task", scheduler.ErrInvalidTaskPayload.Error(), err)
		}
		if err := schedulerService.Execute(ctx, payload); err != nil {
			if errors.Is(err, scheduler.ErrInvalidTaskPayload) || errors.Is(err, notification.ErrChannelNotFound) ||
				errors.Is(err, notification.ErrInvalidNotification) {
				return worker.NewPermanentError("scheduler_configuration_invalid", "定时任务配置无效", err)
			}
			return worker.NewRetryableError("scheduler_temporary_failure", "定时任务暂时失败", err)
		}
		return nil
	}); err != nil {
		logger.Error("注册定时任务处理器失败", "operation", "worker_register", "kind", "scheduler.execute", "err", err)
		return nil, errors.New("后台任务初始化失败")
	}
	return worker.New(resources.Database, resources.NATS, registry, logger, cfg.NATS, cfg.Worker), nil
}

func newRepositoryService(
	db *gorm.DB,
	cfg config.Config,
	secretManager *secret.Manager,
	credentialService *credential.Service,
	configurationService *configuration.Service,
) (*repository.Service, error) {
	return repository.NewService(
		db, secretManager, credentialService, repository.NewGitClient(cfg.Git), cfg.NATS.MaxAttempts,
		repository.WithWebhookGate(configurationService),
	), nil
}
