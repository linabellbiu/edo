package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

const (
	DefaultMaxAttempts        = 4
	maxAllowedAttempts        = 20
	defaultNATSURL            = "nats://127.0.0.1:4222"
	defaultNATSStream         = "edo_tasks"
	defaultNATSDeadStream     = "edo_dead"
	defaultNATSSubjectPrefix  = "edo.task"
	defaultNATSDeadSubject    = "edo.dead.task.v1"
	defaultNATSMaxAge         = 7 * 24 * time.Hour
	defaultNATSMaxBytes       = 512 * 1024 * 1024
	defaultNATSDeadMaxBytes   = 256 * 1024 * 1024
	defaultNATSReplicas       = 1
	defaultWorkerConcurrency  = 8
	defaultWorkerTaskTimeout  = 30 * time.Minute
	defaultWorkerLease        = 45 * time.Second
	defaultWorkerShutdown     = 30 * time.Second
	defaultSchedulerPoll      = 15 * time.Second
	defaultArtifactsDirectory = "data/artifacts"
	defaultArtifactsMaxBytes  = int64(1024 * 1024 * 1024)
	defaultGitDirectory       = "data/repositories"
	defaultLogDirectory       = "logs"
	defaultLogMaxFileSizeMB   = 100
	defaultLogCompressDays    = 3
)

type Config struct {
	Environment string `env:"EDO_ENV"`
	LogLevel    string `env:"EDO_LOG_LEVEL"`
	Logging     Logging
	Server      Server
	Auth        Auth
	Database    Database
	Redis       Redis
	NATS        NATS
	Worker      Worker
	Secrets     Secrets
	Git         Git
	Runtime     Runtime
	Scheduler   Scheduler
	Artifacts   Artifacts
}

type Logging struct {
	FileEnabled       bool   `env:"EDO_LOG_FILE_ENABLED"`
	Directory         string `env:"EDO_LOG_DIRECTORY"`
	MaxFileSizeMB     int    `env:"EDO_LOG_MAX_FILE_SIZE_MB"`
	CompressAfterDays int    `env:"EDO_LOG_COMPRESS_AFTER_DAYS"`
}

type Auth struct {
	SessionTTL      time.Duration `env:"EDO_AUTH_SESSION_TTL"`
	CookieName      string        `env:"EDO_AUTH_COOKIE_NAME"`
	CookieSecure    bool          `env:"EDO_AUTH_COOKIE_SECURE"`
	LoginMaxFailure int           `env:"EDO_AUTH_LOGIN_MAX_FAILURE"`
	LoginWindow     time.Duration `env:"EDO_AUTH_LOGIN_WINDOW"`
}

type Server struct {
	Address         string        `env:"EDO_SERVER_ADDRESS"`
	WebRoot         string        `env:"EDO_WEB_ROOT"`
	ReadTimeout     time.Duration `env:"EDO_SERVER_READ_TIMEOUT"`
	WriteTimeout    time.Duration `env:"EDO_SERVER_WRITE_TIMEOUT"`
	IdleTimeout     time.Duration `env:"EDO_SERVER_IDLE_TIMEOUT"`
	ShutdownTimeout time.Duration `env:"EDO_SERVER_SHUTDOWN_TIMEOUT"`
}

type Database struct {
	Driver          string        `env:"EDO_DATABASE_DRIVER"`
	DSN             string        `env:"EDO_DATABASE_DSN"`
	MaxOpenConns    int           `env:"EDO_DATABASE_MAX_OPEN_CONNS"`
	MaxIdleConns    int           `env:"EDO_DATABASE_MAX_IDLE_CONNS"`
	ConnMaxLifetime time.Duration `env:"EDO_DATABASE_CONN_MAX_LIFETIME"`
}

type Redis struct {
	URL       string        `env:"EDO_REDIS_URL"`
	KeyPrefix string        `env:"EDO_REDIS_KEY_PREFIX"`
	Timeout   time.Duration `env:"EDO_REDIS_TIMEOUT"`
}

type NATS struct {
	URL           string        `env:"EDO_NATS_URL"`
	Stream        string        `env:"EDO_NATS_STREAM"`
	DeadStream    string        `env:"EDO_NATS_DEAD_STREAM"`
	SubjectPrefix string        `env:"EDO_NATS_SUBJECT_PREFIX"`
	DeadSubject   string        `env:"EDO_NATS_DEAD_SUBJECT"`
	MaxAttempts   int           `env:"EDO_NATS_MAX_ATTEMPTS"`
	Timeout       time.Duration `env:"EDO_NATS_TIMEOUT"`
	MaxAge        time.Duration `env:"EDO_NATS_MAX_AGE"`
	MaxBytes      int64         `env:"EDO_NATS_MAX_BYTES"`
	DeadMaxBytes  int64         `env:"EDO_NATS_DEAD_MAX_BYTES"`
	Replicas      int           `env:"EDO_NATS_REPLICAS"`
}

type Worker struct {
	Concurrency     int           `env:"EDO_WORKER_CONCURRENCY"`
	TaskTimeout     time.Duration `env:"EDO_WORKER_TASK_TIMEOUT"`
	LeaseDuration   time.Duration `env:"EDO_WORKER_LEASE_DURATION"`
	ShutdownTimeout time.Duration `env:"EDO_WORKER_SHUTDOWN_TIMEOUT"`
}

type Secrets struct {
	Key string `env:"EDO_SECRETS_KEY"`
}

type Git struct {
	Timeout        time.Duration `env:"EDO_GIT_TIMEOUT"`
	KnownHostsFile string        `env:"EDO_GIT_KNOWN_HOSTS_FILE"`
	Directory      string        `env:"EDO_GIT_DIRECTORY"`
}

type Runtime struct {
	ConnectTimeout           time.Duration `env:"EDO_RUNTIME_CONNECT_TIMEOUT"`
	RequestTimeout           time.Duration `env:"EDO_RUNTIME_REQUEST_TIMEOUT"`
	TerminalMaxDuration      time.Duration `env:"EDO_RUNTIME_TERMINAL_MAX_DURATION"`
	DockerBuilderHost        string        `env:"EDO_DOCKER_BUILDER_HOST"`
	DockerBuilderTLSCertPath string        `env:"EDO_DOCKER_BUILDER_TLS_CERT_PATH"`
}

type Scheduler struct {
	PollInterval time.Duration `env:"EDO_SCHEDULER_POLL_INTERVAL"`
}

type Artifacts struct {
	Directory string `env:"EDO_ARTIFACTS_DIRECTORY"`
	MaxBytes  int64  `env:"EDO_ARTIFACTS_MAX_BYTES"`
}

func Load() (Config, error) {
	if err := rejectExplicitEmptyValues(
		"EDO_AUTH_COOKIE_NAME",
		"EDO_DATABASE_DRIVER",
		"EDO_DATABASE_DSN",
		"EDO_REDIS_URL",
		"EDO_REDIS_KEY_PREFIX",
		"EDO_NATS_URL",
		"EDO_NATS_STREAM",
		"EDO_NATS_DEAD_STREAM",
		"EDO_NATS_SUBJECT_PREFIX",
		"EDO_NATS_DEAD_SUBJECT",
		"EDO_LOG_DIRECTORY",
		"EDO_ARTIFACTS_DIRECTORY",
		"EDO_GIT_DIRECTORY",
	); err != nil {
		return Config{}, err
	}
	probe := struct {
		Environment string `env:"EDO_ENV" envDefault:"development"`
	}{}
	if err := env.Parse(&probe); err != nil {
		return Config{}, fmt.Errorf("读取运行环境配置失败: %w", err)
	}
	environment := strings.TrimSpace(probe.Environment)
	if environment == "" {
		environment = "development"
	}
	cfg := Config{
		Environment: environment,
		LogLevel:    "info",
		Logging: Logging{
			FileEnabled:       true,
			Directory:         defaultLogDirectory,
			MaxFileSizeMB:     defaultLogMaxFileSizeMB,
			CompressAfterDays: defaultLogCompressDays,
		},
		Server: Server{
			Address:         ":8080",
			WebRoot:         "web/dist",
			ReadTimeout:     15 * time.Second,
			WriteTimeout:    30 * time.Second,
			IdleTimeout:     60 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		Auth: Auth{
			SessionTTL:      8 * time.Hour,
			CookieName:      "edo_session",
			CookieSecure:    environment == "production",
			LoginMaxFailure: 5,
			LoginWindow:     15 * time.Minute,
		},
		Database: Database{
			Driver:          "sqlite",
			DSN:             "data/edo.db",
			MaxOpenConns:    25,
			MaxIdleConns:    5,
			ConnMaxLifetime: time.Hour,
		},
		Redis: Redis{
			URL:       "redis://127.0.0.1:6379/0",
			KeyPrefix: "edo:",
			Timeout:   3 * time.Second,
		},
		NATS: NATS{
			URL:           defaultNATSURL,
			Stream:        defaultNATSStream,
			DeadStream:    defaultNATSDeadStream,
			SubjectPrefix: defaultNATSSubjectPrefix,
			DeadSubject:   defaultNATSDeadSubject,
			MaxAttempts:   DefaultMaxAttempts,
			Timeout:       5 * time.Second,
			MaxAge:        defaultNATSMaxAge,
			MaxBytes:      defaultNATSMaxBytes,
			DeadMaxBytes:  defaultNATSDeadMaxBytes,
			Replicas:      defaultNATSReplicas,
		},
		Worker: Worker{
			Concurrency:     defaultWorkerConcurrency,
			TaskTimeout:     defaultWorkerTaskTimeout,
			LeaseDuration:   defaultWorkerLease,
			ShutdownTimeout: defaultWorkerShutdown,
		},
		Secrets: Secrets{},
		Git: Git{
			Timeout:   30 * time.Second,
			Directory: defaultGitDirectory,
		},
		Runtime: Runtime{
			ConnectTimeout:      10 * time.Second,
			RequestTimeout:      30 * time.Second,
			TerminalMaxDuration: 2 * time.Hour,
		},
		Scheduler: Scheduler{PollInterval: defaultSchedulerPoll},
		Artifacts: Artifacts{
			Directory: defaultArtifactsDirectory,
			MaxBytes:  defaultArtifactsMaxBytes,
		},
	}
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("读取 EDO 环境变量失败: %w", err)
	}
	cfg.normalizeStrings()

	if cfg.Database.Driver == "sqlite" {
		cfg.Database.MaxOpenConns = 1
		cfg.Database.MaxIdleConns = 1
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func rejectExplicitEmptyValues(keys ...string) error {
	for _, key := range keys {
		if value, exists := os.LookupEnv(key); exists && strings.TrimSpace(value) == "" {
			return fmt.Errorf("环境变量 %s 不能为空", key)
		}
	}
	return nil
}

func (c Config) Validate() error {
	switch c.Database.Driver {
	case "sqlite", "postgres", "mysql":
	default:
		return fmt.Errorf("不支持的数据库驱动 %q", c.Database.Driver)
	}
	if strings.TrimSpace(c.Database.DSN) == "" {
		return errors.New("数据库连接配置不能为空")
	}
	if c.Database.MaxOpenConns < 1 || c.Database.MaxIdleConns < 0 {
		return errors.New("数据库连接池配置无效")
	}
	if c.Server.ReadTimeout <= 0 || c.Server.WriteTimeout <= 0 || c.Server.IdleTimeout <= 0 || c.Server.ShutdownTimeout <= 0 {
		return errors.New("HTTP 服务超时配置无效")
	}
	if c.Auth.SessionTTL <= 0 || c.Auth.LoginWindow <= 0 || c.Auth.LoginMaxFailure < 1 || c.Auth.LoginMaxFailure > 100 {
		return errors.New("登录会话或限流配置无效")
	}
	if !strings.HasPrefix(c.Auth.CookieName, "edo_") {
		return errors.New("登录 Cookie 名称必须以 edo_ 开头")
	}
	if c.Redis.Timeout <= 0 || c.NATS.Timeout <= 0 || c.NATS.MaxAge <= 0 {
		return errors.New("依赖服务超时或保留时间配置无效")
	}
	if !strings.HasPrefix(c.Redis.KeyPrefix, "edo:") {
		return errors.New("Redis Key 前缀必须以 edo: 开头")
	}
	if c.NATS.MaxAttempts < 1 || c.NATS.MaxAttempts > maxAllowedAttempts {
		return fmt.Errorf("NATS 最大执行次数必须在 1 到 %d 之间", maxAllowedAttempts)
	}
	if c.NATS.Replicas < 1 || c.NATS.Replicas > 5 {
		return errors.New("NATS 副本数必须在 1 到 5 之间")
	}
	if c.NATS.MaxBytes < 1024*1024 || c.NATS.MaxBytes > 1024*1024*1024*1024 ||
		c.NATS.DeadMaxBytes < 1024*1024 || c.NATS.DeadMaxBytes > 1024*1024*1024*1024 {
		return errors.New("NATS Stream 容量必须在 1 MiB 到 1 TiB 之间")
	}
	if !strings.HasPrefix(c.NATS.Stream, "edo_") {
		return errors.New("NATS Stream 名称必须以 edo_ 开头")
	}
	if !strings.HasPrefix(c.NATS.DeadStream, "edo_") {
		return errors.New("NATS 死信 Stream 名称必须以 edo_ 开头")
	}
	if !strings.HasPrefix(c.NATS.SubjectPrefix, "edo.") {
		return errors.New("NATS Subject 前缀必须以 edo. 开头")
	}
	if !strings.HasPrefix(c.NATS.DeadSubject, "edo.") {
		return errors.New("NATS 死信主题必须以 edo. 开头")
	}
	if c.Worker.Concurrency < 1 || c.Worker.Concurrency > 100 {
		return errors.New("Worker 并发数必须在 1 到 100 之间")
	}
	if c.Worker.TaskTimeout <= 0 || c.Worker.LeaseDuration < 15*time.Second || c.Worker.ShutdownTimeout <= 0 {
		return errors.New("Worker 超时或租约配置无效")
	}
	if c.Git.Timeout <= 0 {
		return errors.New("Git 查询超时配置无效")
	}
	if c.Runtime.ConnectTimeout <= 0 || c.Runtime.RequestTimeout <= 0 ||
		c.Runtime.TerminalMaxDuration < time.Minute || c.Runtime.TerminalMaxDuration > 24*time.Hour {
		return errors.New("容器运行时连接或请求超时配置无效")
	}
	if !validDockerBuilderRuntime(c.Runtime.DockerBuilderHost, c.Runtime.DockerBuilderTLSCertPath) {
		return errors.New("Docker 构建运行时地址无效")
	}
	if c.Scheduler.PollInterval < time.Second || c.Scheduler.PollInterval > time.Minute {
		return errors.New("定时任务扫描间隔必须在 1 秒到 1 分钟之间")
	}
	if c.Logging.FileEnabled && (strings.TrimSpace(c.Logging.Directory) == "" ||
		c.Logging.MaxFileSizeMB < 1 || c.Logging.MaxFileSizeMB > 10*1024 ||
		c.Logging.CompressAfterDays < 1 || c.Logging.CompressAfterDays > 3650) {
		return errors.New("运行日志文件设置无效")
	}
	if strings.TrimSpace(c.Artifacts.Directory) == "" {
		return errors.New("制品存储目录不能为空")
	}
	if strings.TrimSpace(c.Git.Directory) == "" {
		return errors.New("Git 仓库缓存目录不能为空")
	}
	if c.Artifacts.MaxBytes < 1 || c.Artifacts.MaxBytes > 1024*1024*1024*1024 {
		return errors.New("单个制品大小限制必须在 1 字节到 1 TiB 之间")
	}
	return nil
}

func (c *Config) normalizeStrings() {
	c.Environment = strings.TrimSpace(c.Environment)
	c.LogLevel = strings.TrimSpace(c.LogLevel)
	c.Logging.Directory = strings.TrimSpace(c.Logging.Directory)
	c.Server.Address = strings.TrimSpace(c.Server.Address)
	c.Server.WebRoot = strings.TrimSpace(c.Server.WebRoot)
	c.Auth.CookieName = strings.TrimSpace(c.Auth.CookieName)
	c.Database.Driver = strings.ToLower(strings.TrimSpace(c.Database.Driver))
	c.Database.DSN = strings.TrimSpace(c.Database.DSN)
	c.Redis.URL = strings.TrimSpace(c.Redis.URL)
	c.Redis.KeyPrefix = strings.TrimSpace(c.Redis.KeyPrefix)
	c.NATS.URL = strings.TrimSpace(c.NATS.URL)
	c.NATS.Stream = strings.TrimSpace(c.NATS.Stream)
	c.NATS.DeadStream = strings.TrimSpace(c.NATS.DeadStream)
	c.NATS.SubjectPrefix = strings.TrimSpace(c.NATS.SubjectPrefix)
	c.NATS.DeadSubject = strings.TrimSpace(c.NATS.DeadSubject)
	c.Secrets.Key = strings.TrimSpace(c.Secrets.Key)
	c.Git.KnownHostsFile = strings.TrimSpace(c.Git.KnownHostsFile)
	c.Git.Directory = filepath.Clean(strings.TrimSpace(c.Git.Directory))
	c.Runtime.DockerBuilderHost = strings.TrimSpace(c.Runtime.DockerBuilderHost)
	c.Runtime.DockerBuilderTLSCertPath = strings.TrimSpace(c.Runtime.DockerBuilderTLSCertPath)
	c.Artifacts.Directory = strings.TrimSpace(c.Artifacts.Directory)
}

func validDockerBuilderRuntime(host, certPath string) bool {
	host, certPath = strings.TrimSpace(host), strings.TrimSpace(certPath)
	if !validDockerBuilderHost(host) || strings.ContainsRune(certPath, '\x00') {
		return false
	}
	if host == "" {
		// 未显式配置时由 Docker 标准环境变量决定本机连接及其 TLS 参数。
		return certPath == ""
	}
	parsed, _ := url.Parse(host)
	switch parsed.Scheme {
	case "tcp":
		// 显式 TCP 构建运行时必须使用包含 ca.pem、cert.pem、key.pem 的 mTLS 目录。
		return certPath != "" && filepath.IsAbs(certPath)
	case "unix":
		return certPath == ""
	default:
		return false
	}
}

func validDockerBuilderHost(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		// 未配置时使用当前进程可访问的宿主机 Docker；Compose 会显式指向 DinD。
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	switch parsed.Scheme {
	case "tcp":
		return parsed.Host != "" && parsed.Path == ""
	case "unix":
		return parsed.Host == "" && strings.HasPrefix(parsed.Path, "/") && parsed.Path != "/"
	default:
		return false
	}
}
