package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultMaxAttempts       = 4
	maxAllowedAttempts       = 20
	defaultNATSURL           = "nats://127.0.0.1:4222"
	defaultNATSStream        = "zrt_tasks"
	defaultNATSDeadStream    = "zrt_dead"
	defaultNATSSubjectPrefix = "zrt.task"
	defaultNATSDeadSubject   = "zrt.dead.task.v1"
	defaultNATSMaxAge        = 7 * 24 * time.Hour
	defaultNATSMaxBytes      = 512 * 1024 * 1024
	defaultNATSDeadMaxBytes  = 256 * 1024 * 1024
	defaultNATSReplicas      = 1
	defaultWorkerConcurrency = 8
	defaultWorkerTaskTimeout = 30 * time.Minute
	defaultWorkerLease       = 45 * time.Second
	defaultWorkerShutdown    = 30 * time.Second
	defaultSchedulerPoll     = 15 * time.Second
)

type Config struct {
	Environment string
	LogLevel    string
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
}

type Auth struct {
	SessionTTL      time.Duration
	CookieName      string
	CookieSecure    bool
	LoginMaxFailure int
	LoginWindow     time.Duration
}

type Server struct {
	Address         string
	WebRoot         string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

type Database struct {
	Driver          string
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

type Redis struct {
	URL       string
	KeyPrefix string
	Timeout   time.Duration
}

type NATS struct {
	URL           string
	Stream        string
	DeadStream    string
	SubjectPrefix string
	DeadSubject   string
	MaxAttempts   int
	Timeout       time.Duration
	MaxAge        time.Duration
	MaxBytes      int64
	DeadMaxBytes  int64
	Replicas      int
}

type Worker struct {
	Concurrency     int
	TaskTimeout     time.Duration
	LeaseDuration   time.Duration
	ShutdownTimeout time.Duration
}

type Secrets struct {
	Key string
}

type Git struct {
	Command        string
	Timeout        time.Duration
	KnownHostsFile string
}

type Runtime struct {
	ConnectTimeout      time.Duration
	RequestTimeout      time.Duration
	TerminalMaxDuration time.Duration
}

type Scheduler struct {
	PollInterval time.Duration
}

func Load() (Config, error) {
	environment := env("ZRT_ENV", "development")
	cookieSecure, err := envBool("ZRT_AUTH_COOKIE_SECURE", environment == "production")
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Environment: environment,
		LogLevel:    env("ZRT_LOG_LEVEL", "info"),
		Server: Server{
			Address:         env("ZRT_SERVER_ADDRESS", ":8080"),
			WebRoot:         env("ZRT_WEB_ROOT", "web/dist"),
			ReadTimeout:     envDuration("ZRT_SERVER_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    envDuration("ZRT_SERVER_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:     envDuration("ZRT_SERVER_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: envDuration("ZRT_SERVER_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		Auth: Auth{
			SessionTTL:      envDuration("ZRT_AUTH_SESSION_TTL", 8*time.Hour),
			CookieName:      env("ZRT_AUTH_COOKIE_NAME", "zrt_session"),
			CookieSecure:    cookieSecure,
			LoginMaxFailure: envInt("ZRT_AUTH_LOGIN_MAX_FAILURE", 5),
			LoginWindow:     envDuration("ZRT_AUTH_LOGIN_WINDOW", 15*time.Minute),
		},
		Database: Database{
			Driver:          strings.ToLower(env("ZRT_DATABASE_DRIVER", "sqlite")),
			DSN:             env("ZRT_DATABASE_DSN", "data/zrt.db"),
			MaxOpenConns:    envInt("ZRT_DATABASE_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    envInt("ZRT_DATABASE_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: envDuration("ZRT_DATABASE_CONN_MAX_LIFETIME", time.Hour),
		},
		Redis: Redis{
			URL:       env("ZRT_REDIS_URL", "redis://127.0.0.1:6379/0"),
			KeyPrefix: env("ZRT_REDIS_KEY_PREFIX", "zrt:"),
			Timeout:   envDuration("ZRT_REDIS_TIMEOUT", 3*time.Second),
		},
		NATS: NATS{
			URL:           env("ZRT_NATS_URL", defaultNATSURL),
			Stream:        env("ZRT_NATS_STREAM", defaultNATSStream),
			DeadStream:    env("ZRT_NATS_DEAD_STREAM", defaultNATSDeadStream),
			SubjectPrefix: env("ZRT_NATS_SUBJECT_PREFIX", defaultNATSSubjectPrefix),
			DeadSubject:   env("ZRT_NATS_DEAD_SUBJECT", defaultNATSDeadSubject),
			MaxAttempts:   envInt("ZRT_NATS_MAX_ATTEMPTS", DefaultMaxAttempts),
			Timeout:       envDuration("ZRT_NATS_TIMEOUT", 5*time.Second),
			MaxAge:        envDuration("ZRT_NATS_MAX_AGE", defaultNATSMaxAge),
			MaxBytes:      envInt64("ZRT_NATS_MAX_BYTES", defaultNATSMaxBytes),
			DeadMaxBytes:  envInt64("ZRT_NATS_DEAD_MAX_BYTES", defaultNATSDeadMaxBytes),
			Replicas:      envInt("ZRT_NATS_REPLICAS", defaultNATSReplicas),
		},
		Worker: Worker{
			Concurrency:     envInt("ZRT_WORKER_CONCURRENCY", defaultWorkerConcurrency),
			TaskTimeout:     envDuration("ZRT_WORKER_TASK_TIMEOUT", defaultWorkerTaskTimeout),
			LeaseDuration:   envDuration("ZRT_WORKER_LEASE_DURATION", defaultWorkerLease),
			ShutdownTimeout: envDuration("ZRT_WORKER_SHUTDOWN_TIMEOUT", defaultWorkerShutdown),
		},
		Secrets: Secrets{Key: env("ZRT_SECRETS_KEY", "")},
		Git: Git{
			Command: env("ZRT_GIT_COMMAND", "git"), Timeout: envDuration("ZRT_GIT_TIMEOUT", 30*time.Second),
			KnownHostsFile: env("ZRT_GIT_KNOWN_HOSTS_FILE", ""),
		},
		Runtime: Runtime{
			ConnectTimeout:      envDuration("ZRT_RUNTIME_CONNECT_TIMEOUT", 10*time.Second),
			RequestTimeout:      envDuration("ZRT_RUNTIME_REQUEST_TIMEOUT", 30*time.Second),
			TerminalMaxDuration: envDuration("ZRT_RUNTIME_TERMINAL_MAX_DURATION", 2*time.Hour),
		},
		Scheduler: Scheduler{PollInterval: envDuration("ZRT_SCHEDULER_POLL_INTERVAL", defaultSchedulerPoll)},
	}

	if cfg.Database.Driver == "sqlite" {
		cfg.Database.MaxOpenConns = 1
		cfg.Database.MaxIdleConns = 1
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
	if !strings.HasPrefix(c.Auth.CookieName, "zrt_") {
		return errors.New("登录 Cookie 名称必须以 zrt_ 开头")
	}
	if c.Redis.Timeout <= 0 || c.NATS.Timeout <= 0 || c.NATS.MaxAge <= 0 {
		return errors.New("依赖服务超时或保留时间配置无效")
	}
	if !strings.HasPrefix(c.Redis.KeyPrefix, "zrt:") {
		return errors.New("Redis Key 前缀必须以 zrt: 开头")
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
	if !strings.HasPrefix(c.NATS.Stream, "zrt_") {
		return errors.New("NATS Stream 名称必须以 zrt_ 开头")
	}
	if !strings.HasPrefix(c.NATS.DeadStream, "zrt_") {
		return errors.New("NATS 死信 Stream 名称必须以 zrt_ 开头")
	}
	if !strings.HasPrefix(c.NATS.SubjectPrefix, "zrt.") {
		return errors.New("NATS Subject 前缀必须以 zrt. 开头")
	}
	if !strings.HasPrefix(c.NATS.DeadSubject, "zrt.") {
		return errors.New("NATS 死信主题必须以 zrt. 开头")
	}
	if c.Worker.Concurrency < 1 || c.Worker.Concurrency > 100 {
		return errors.New("Worker 并发数必须在 1 到 100 之间")
	}
	if c.Worker.TaskTimeout <= 0 || c.Worker.LeaseDuration < 15*time.Second || c.Worker.ShutdownTimeout <= 0 {
		return errors.New("Worker 超时或租约配置无效")
	}
	if c.Git.Command == "" || c.Git.Timeout <= 0 {
		return errors.New("Git 命令或超时配置无效")
	}
	if c.Runtime.ConnectTimeout <= 0 || c.Runtime.RequestTimeout <= 0 ||
		c.Runtime.TerminalMaxDuration < time.Minute || c.Runtime.TerminalMaxDuration > 24*time.Hour {
		return errors.New("容器运行时连接或请求超时配置无效")
	}
	if c.Scheduler.PollInterval < time.Second || c.Scheduler.PollInterval > time.Minute {
		return errors.New("定时任务扫描间隔必须在 1 秒到 1 分钟之间")
	}
	return nil
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return -1
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return -1
	}
	return parsed
}

func envBool(key string, fallback bool) (bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("环境变量 %s 必须是布尔值", key)
	}
	return parsed, nil
}
