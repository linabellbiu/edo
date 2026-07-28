package config

import (
	"testing"
	"time"
)

func TestLoadRejectsExplicitEmptyDatabaseConfig(t *testing.T) {
	t.Setenv("ZRT_DATABASE_DRIVER", "sqlite")
	t.Setenv("ZRT_DATABASE_DSN", "")
	if _, err := Load(); err == nil {
		t.Fatal("显式空数据库配置应返回错误")
	}
}

func TestLoadParsesTypedEnvironmentValues(t *testing.T) {
	t.Setenv("ZRT_ENV", "production")
	t.Setenv("ZRT_AUTH_COOKIE_SECURE", "")
	t.Setenv("ZRT_SERVER_READ_TIMEOUT", "23s")
	t.Setenv("ZRT_NATS_MAX_ATTEMPTS", "6")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("解析类型化环境变量失败: %v", err)
	}
	if !cfg.Auth.CookieSecure || cfg.Server.ReadTimeout != 23*time.Second || cfg.NATS.MaxAttempts != 6 {
		t.Fatalf("类型化环境变量结果错误: secure=%v timeout=%s attempts=%d",
			cfg.Auth.CookieSecure, cfg.Server.ReadTimeout, cfg.NATS.MaxAttempts)
	}
}

func TestLoadRejectsInvalidBoolean(t *testing.T) {
	t.Setenv("ZRT_AUTH_COOKIE_SECURE", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("非法布尔环境变量必须返回错误")
	}
}

func TestValidateSupportedDatabases(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres", "mysql"} {
		cfg := validConfig()
		cfg.Database.Driver = driver
		if err := cfg.Validate(); err != nil {
			t.Fatalf("数据库 %s 应被支持: %v", driver, err)
		}
	}
}

func TestValidateRejectsUnlimitedAttempts(t *testing.T) {
	cfg := validConfig()
	cfg.NATS.MaxAttempts = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("无限重试配置必须被拒绝")
	}
}

func TestValidateRejectsInvalidStreamCapacity(t *testing.T) {
	cfg := validConfig()
	cfg.NATS.MaxBytes = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("无效的 NATS Stream 容量必须被拒绝")
	}
}

func TestValidateRejectsInvalidDockerBuilderHost(t *testing.T) {
	cfg := validConfig()
	cfg.Runtime.DockerBuilderHost = "ssh://builder.example.com"
	if err := cfg.Validate(); err == nil {
		t.Fatal("非 Docker API 地址不应作为 Docker 构建运行时")
	}
}

func TestLoadUsesLocalDockerByDefault(t *testing.T) {
	t.Setenv("ZRT_DOCKER_BUILDER_HOST", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载本地 Docker 默认配置失败: %v", err)
	}
	if cfg.Runtime.DockerBuilderHost != "" {
		t.Fatalf("二进制默认不应指向 DinD: %q", cfg.Runtime.DockerBuilderHost)
	}
}

func validConfig() Config {
	return Config{
		Server: Server{
			ReadTimeout: time.Second, WriteTimeout: time.Second,
			IdleTimeout: time.Second, ShutdownTimeout: time.Second,
		},
		Auth: Auth{
			SessionTTL: time.Hour, CookieName: "zrt_session",
			LoginMaxFailure: 5, LoginWindow: time.Minute,
		},
		Database: Database{Driver: "sqlite", DSN: "data/zrt.db", MaxOpenConns: 1, MaxIdleConns: 1},
		Redis:    Redis{KeyPrefix: "zrt:", Timeout: time.Second},
		NATS: NATS{
			Stream:        "zrt_tasks",
			DeadStream:    "zrt_dead",
			SubjectPrefix: "zrt.task",
			DeadSubject:   "zrt.dead.task.v1",
			MaxAttempts:   DefaultMaxAttempts,
			Replicas:      1,
			Timeout:       time.Second,
			MaxAge:        time.Hour,
			MaxBytes:      512 * 1024 * 1024,
			DeadMaxBytes:  256 * 1024 * 1024,
		},
		Worker: Worker{
			Concurrency: 1, TaskTimeout: time.Minute,
			LeaseDuration: 30 * time.Second, ShutdownTimeout: time.Second,
		},
		Git: Git{Timeout: time.Second},
		Runtime: Runtime{
			ConnectTimeout: time.Second, RequestTimeout: time.Second,
			TerminalMaxDuration: time.Hour, DockerBuilderHost: "tcp://docker-builder:2375",
		},
		Scheduler: Scheduler{PollInterval: 15 * time.Second},
	}
}
