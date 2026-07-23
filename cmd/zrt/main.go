package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"gorm.io/gorm"

	"zrt/internal/access"
	"zrt/internal/account"
	"zrt/internal/audit"
	"zrt/internal/auth"
	"zrt/internal/bootstrap"
	"zrt/internal/config"
	"zrt/internal/configuration"
	"zrt/internal/database"
	"zrt/internal/deployment"
	"zrt/internal/dockerengine"
	"zrt/internal/httpapi"
	"zrt/internal/kube"
	"zrt/internal/legacyimport"
	"zrt/internal/logging"
	"zrt/internal/monitor"
	"zrt/internal/notification"
	"zrt/internal/outbox"
	"zrt/internal/repository"
	"zrt/internal/scheduler"
	"zrt/internal/secret"
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
	logger := logging.New(cfg.LogLevel)
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
		return runServer(ctx, cfg, logger)
	case "worker":
		return runWorker(ctx, cfg, logger)
	case "admin":
		return runAdmin(ctx, cfg, logger, os.Args[2:])
	case "legacy-import":
		return runLegacyImport(ctx, cfg, logger, os.Args[2:])
	default:
		return fmt.Errorf("未知启动命令 %q，可用命令: server、worker、migrate、admin、legacy-import、version", command)
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

func runServer(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	resources, err := bootstrap.Open(ctx, cfg, logger)
	if err != nil {
		logger.Error("初始化服务依赖失败", "operation", "server_bootstrap", "err", err)
		return errors.New("服务依赖初始化失败")
	}
	defer resources.Close()
	// 默认单机模式由 API 进程投递 Outbox；独立 Worker 可用于后续横向扩容。
	publisher := outbox.New(resources.Database, resources.NATS, logger, cfg.NATS.MaxAttempts)
	go func() {
		if err := publisher.Run(ctx); err != nil {
			logger.Error("API 进程的 Outbox Publisher 退出", "operation", "server_outbox", "err", err)
		}
	}()
	accounts := account.NewService(resources.Database)
	accessService := access.NewService(resources.Database)
	auditService := audit.NewService(resources.Database)
	sessions := auth.NewSessionStore(resources.Redis, cfg.Auth.SessionTTL)
	limiter := auth.NewLoginRateLimiter(resources.Redis, cfg.Auth.LoginMaxFailure, cfg.Auth.LoginWindow)
	loginService, err := account.NewLoginService(accounts, sessions, limiter, logger)
	if err != nil {
		logger.Error("初始化登录服务失败", "operation", "auth_bootstrap", "err", err)
		return errors.New("登录服务初始化失败")
	}
	repositoryService, err := newRepositoryService(resources.Database, cfg)
	if err != nil {
		logger.Error("初始化代码仓库服务失败", "operation", "repository_bootstrap", "err", err)
		return errors.New("代码仓库服务初始化失败")
	}
	secretManager, err := secret.New(cfg.Secrets.Key)
	if err != nil {
		logger.Error("初始化基础设施密钥管理器失败", "operation", "cluster_secret_bootstrap", "err", err)
		return errors.New("容器与集群服务初始化失败")
	}
	dockerService := dockerengine.NewService(resources.Database, secretManager, cfg.Runtime)
	kubernetesService := kube.NewService(resources.Database, secretManager, cfg.Runtime)
	deploymentService := deployment.NewService(resources.Database, dockerService, kubernetesService, logger)
	terminalService := terminal.NewService(dockerService, kubernetesService, cfg.Runtime.TerminalMaxDuration)
	configurationService := configuration.NewService(resources.Database, secretManager)
	notificationService := notification.NewService(resources.Database, secretManager, nil, cfg.NATS.MaxAttempts)
	monitorService := monitor.NewService(resources.Database, secretManager, notificationService, nil, cfg.NATS.MaxAttempts, logger)
	schedulerService := scheduler.NewService(resources.Database, notificationService, cfg.NATS.MaxAttempts, logger)
	taskService := task.NewService(resources.Database, cfg.NATS.MaxAttempts)

	router := httpapi.NewRouter(httpapi.Dependencies{
		Environment:    cfg.Environment,
		Database:       resources.SQL,
		Redis:          resources.Redis,
		NATS:           resources.NATS,
		Logger:         logger,
		Version:        version,
		WebRoot:        cfg.Server.WebRoot,
		AuthConfig:     cfg.Auth,
		Accounts:       accounts,
		Login:          loginService,
		Sessions:       sessions,
		Access:         accessService,
		Audits:         auditService,
		Repositories:   repositoryService,
		Docker:         dockerService,
		Kubernetes:     kubernetesService,
		Deployments:    deploymentService,
		Terminal:       terminalService,
		Configurations: configurationService,
		Notifications:  notificationService,
		Monitors:       monitorService,
		Scheduler:      schedulerService,
		Tasks:          taskService,
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
	errCh := make(chan error, 1)
	go func() {
		logger.Info("ZRT HTTP 服务已启动", "operation", "http_listen", "address", cfg.Server.Address)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP 服务优雅退出失败", "operation", "http_shutdown", "err", err)
			return errors.New("HTTP 服务退出失败")
		}
		logger.Info("ZRT HTTP 服务已停止", "operation", "http_shutdown")
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		logger.Error("HTTP 服务监听失败", "operation", "http_listen", "address", cfg.Server.Address, "err", err)
		return errors.New("HTTP 服务启动失败")
	}
}

func runWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	resources, err := bootstrap.Open(ctx, cfg, logger)
	if err != nil {
		logger.Error("初始化 Worker 依赖失败", "operation", "worker_bootstrap", "err", err)
		return errors.New("Worker 依赖初始化失败")
	}
	defer resources.Close()
	logger.Info("ZRT Worker 已启动", "operation", "worker_start")
	publisher := outbox.New(resources.Database, resources.NATS, logger, cfg.NATS.MaxAttempts)
	go func() {
		if err := publisher.Run(ctx); err != nil {
			logger.Error("Worker 的 Outbox Publisher 退出", "operation", "worker_outbox", "err", err)
		}
	}()
	registry := worker.NewRegistry()
	if err := registry.Register("system.noop", func(context.Context, task.Message) error { return nil }); err != nil {
		logger.Error("注册内置任务处理器失败", "operation", "worker_register", "kind", "system.noop", "err", err)
		return errors.New("Worker 初始化失败")
	}
	repositoryService, err := newRepositoryService(resources.Database, cfg)
	if err != nil {
		logger.Error("初始化 Worker 代码仓库服务失败", "operation", "worker_repository_bootstrap", "err", err)
		return errors.New("Worker 初始化失败")
	}
	if err := registry.Register("repository.webhook", func(ctx context.Context, message task.Message) error {
		if err := repositoryService.ProcessWebhookTask(ctx, message.Payload); err != nil {
			if errors.Is(err, repository.ErrInvalidTaskPayload) {
				return worker.NewPermanentError("invalid_webhook_task", repository.ErrInvalidTaskPayload.Error(), err)
			}
			return worker.NewRetryableError("webhook_database_unavailable", "Webhook 事件处理暂时失败", err)
		}
		return nil
	}); err != nil {
		logger.Error("注册 Webhook 任务处理器失败", "operation", "worker_register", "kind", "repository.webhook", "err", err)
		return errors.New("Worker 初始化失败")
	}
	secretManager, err := secret.New(cfg.Secrets.Key)
	if err != nil {
		logger.Error("初始化 Worker 基础设施密钥管理器失败", "operation", "worker_cluster_secret_bootstrap", "err", err)
		return errors.New("Worker 初始化失败")
	}
	dockerService := dockerengine.NewService(resources.Database, secretManager, cfg.Runtime)
	kubernetesService := kube.NewService(resources.Database, secretManager, cfg.Runtime)
	deploymentService := deployment.NewService(resources.Database, dockerService, kubernetesService, logger)
	notificationService := notification.NewService(resources.Database, secretManager, nil, cfg.NATS.MaxAttempts)
	monitorService := monitor.NewService(resources.Database, secretManager, notificationService, nil, cfg.NATS.MaxAttempts, logger)
	schedulerService := scheduler.NewService(resources.Database, notificationService, cfg.NATS.MaxAttempts, logger)
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
			return errors.New("Worker 初始化失败")
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
		return errors.New("Worker 初始化失败")
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
		return errors.New("Worker 初始化失败")
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
		return errors.New("Worker 初始化失败")
	}
	go monitorService.Run(ctx, cfg.Scheduler.PollInterval)
	go schedulerService.Run(ctx, cfg.Scheduler.PollInterval)
	taskWorker := worker.New(resources.Database, resources.NATS, registry, logger, cfg.NATS, cfg.Worker)
	if err := taskWorker.Run(ctx); err != nil {
		logger.Error("任务 Worker 运行失败", "operation", "worker_run", "err", err)
		return errors.New("Worker 运行失败")
	}
	logger.Info("ZRT Worker 已停止", "operation", "worker_stop", "time", time.Now().UTC())
	return nil
}

func newRepositoryService(db *gorm.DB, cfg config.Config) (*repository.Service, error) {
	secretManager, err := secret.New(cfg.Secrets.Key)
	if err != nil {
		return nil, err
	}
	return repository.NewService(
		db, secretManager, repository.NewGitClient(cfg.Git), cfg.NATS.MaxAttempts,
	), nil
}
