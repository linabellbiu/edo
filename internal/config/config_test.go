package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadComposeRuntimeUsesCodeDefaultsAndExplicitOverrides(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("EDO_CONTAINER_DATA_DIRECTORY", directory)
	for _, key := range []string{"EDO_BUILD_DIRECTORY", "EDO_ARTIFACTS_DIRECTORY", "EDO_GIT_DIRECTORY", "EDO_GIT_CACHE_DIRECTORY", "EDO_LOG_FILE_ENABLED", "EDO_ARTIFACTS_MAX_BYTES"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logging.Directory != filepath.Join(directory, "logs") || !cfg.Logging.FileEnabled ||
		cfg.Artifacts.BuildDirectory != filepath.Join(directory, "builds") || cfg.Artifacts.Directory != filepath.Join(directory, "artifacts") ||
		cfg.Git.Directory != filepath.Join(directory, "repositories") || cfg.Git.CacheDirectory != filepath.Join(directory, "cache") ||
		cfg.Artifacts.MaxBytes != defaultArtifactsMaxBytes {
		t.Fatalf("容器默认目录或上限错误：%+v %+v %+v", cfg.Logging, cfg.Artifacts, cfg.Git)
	}
	customBuild := filepath.Join(directory, "custom-builds")
	t.Setenv("EDO_BUILD_DIRECTORY", customBuild)
	t.Setenv("EDO_LOG_FILE_ENABLED", "false")
	t.Setenv("EDO_ARTIFACTS_MAX_BYTES", "1048576")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Artifacts.BuildDirectory != customBuild || cfg.Logging.FileEnabled || cfg.Artifacts.MaxBytes != 1048576 {
		t.Fatal("显式容器覆盖未生效")
	}
}

func TestLoadRejectsExplicitEmptyDatabaseConfig(t *testing.T) {
	t.Setenv("EDO_DATABASE_DRIVER", "sqlite")
	t.Setenv("EDO_DATABASE_DSN", "")
	if _, err := Load(); err == nil {
		t.Fatal("显式空数据库配置应返回错误")
	}
}

func TestLoadParsesTypedEnvironmentValues(t *testing.T) {
	t.Setenv("EDO_ENV", "production")
	t.Setenv("EDO_AUTH_COOKIE_SECURE", "")
	t.Setenv("EDO_SERVER_READ_TIMEOUT", "23s")
	t.Setenv("EDO_NATS_MAX_ATTEMPTS", "6")
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
	t.Setenv("EDO_AUTH_COOKIE_SECURE", "invalid")
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

func TestValidateRequiresMTLSForExplicitDockerBuilderTCP(t *testing.T) {
	for _, test := range []struct {
		name     string
		host     string
		certPath string
	}{
		{name: "TCP 缺少证书", host: "tcp://docker-builder:2376"},
		{name: "TCP 相对证书目录", host: "tcp://docker-builder:2376", certPath: "certs/client"},
		{name: "Unix 不应配置证书", host: "unix:///var/run/docker.sock", certPath: "/certs/client"},
		{name: "本机自动连接不应配置专用证书", certPath: "/certs/client"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Runtime.DockerBuilderHost = test.host
			cfg.Runtime.DockerBuilderTLSCertPath = test.certPath
			if err := cfg.Validate(); err == nil {
				t.Fatal("不安全或不一致的 Docker 构建运行时配置必须被拒绝")
			}
		})
	}
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("显式 TCP mTLS 构建运行时应通过校验: %v", err)
	}
}

func TestLoadUsesLocalDockerByDefault(t *testing.T) {
	t.Setenv("EDO_DOCKER_BUILDER_HOST", "")
	t.Setenv("EDO_DOCKER_BUILDER_TLS_CERT_PATH", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载本地 Docker 默认配置失败: %v", err)
	}
	if cfg.Runtime.DockerBuilderHost != "" || cfg.Runtime.DockerBuilderTLSCertPath != "" {
		t.Fatalf("二进制默认不应指向 DinD: host=%q cert_path=%q",
			cfg.Runtime.DockerBuilderHost, cfg.Runtime.DockerBuilderTLSCertPath)
	}
}

func TestLoadUsesArtifactDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载制品默认配置失败: %v", err)
	}
	if cfg.Artifacts.BuildDirectory != "data/builds" || cfg.Artifacts.Directory != "data/artifacts" || cfg.Artifacts.MaxBytes != 1024*1024*1024 {
		t.Fatalf("构建与制品默认配置错误: build_directory=%q directory=%q max_bytes=%d", cfg.Artifacts.BuildDirectory, cfg.Artifacts.Directory, cfg.Artifacts.MaxBytes)
	}
	if cfg.Git.Directory != "data/repositories" || cfg.Git.CacheDirectory != "data/cache" {
		t.Fatalf("仓库目录默认配置错误: build=%q cache=%q", cfg.Git.Directory, cfg.Git.CacheDirectory)
	}
}

func TestLoadUsesGitCacheDefault(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Git.Directory != "data/repositories" {
		t.Fatalf("Git 仓库缓存默认目录错误: %q", cfg.Git.Directory)
	}
}

func TestLoadUsesAndValidatesFileLoggingDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载运行日志默认配置失败: %v", err)
	}
	if !cfg.Logging.FileEnabled || cfg.Logging.Directory != "logs" ||
		cfg.Logging.MaxFileSizeMB != 100 || cfg.Logging.CompressAfterDays != 3 {
		t.Fatalf("运行日志默认配置错误: %+v", cfg.Logging)
	}

	t.Setenv("EDO_LOG_MAX_FILE_SIZE_MB", "0")
	if _, err := Load(); err == nil {
		t.Fatal("无效的日志文件大小上限必须被拒绝")
	}
}

func TestValidateRejectsInvalidArtifactLimit(t *testing.T) {
	cfg := validConfig()
	cfg.Artifacts.MaxBytes = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("无效的制品大小限制必须被拒绝")
	}
}

func validConfig() Config {
	return Config{
		Server: Server{
			ReadTimeout: time.Second, WriteTimeout: time.Second,
			IdleTimeout: time.Second, ShutdownTimeout: time.Second,
		},
		Auth: Auth{
			SessionTTL: time.Hour, CookieName: "edo_session",
			LoginMaxFailure: 5, LoginWindow: time.Minute,
		},
		Database: Database{Driver: "sqlite", DSN: "data/edo.db", MaxOpenConns: 1, MaxIdleConns: 1},
		Redis:    Redis{KeyPrefix: "edo:", Timeout: time.Second},
		NATS: NATS{
			Stream:        "edo_tasks",
			DeadStream:    "edo_dead",
			SubjectPrefix: "edo.task",
			DeadSubject:   "edo.dead.task.v1",
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
		Git: Git{Timeout: time.Second, Directory: "data/repositories", CacheDirectory: "data/cache"},
		Runtime: Runtime{
			ConnectTimeout: time.Second, RequestTimeout: time.Second,
			TerminalMaxDuration: time.Hour, DockerBuilderHost: "tcp://docker-builder:2376",
			DockerBuilderTLSCertPath: "/certs/client",
		},
		Scheduler: Scheduler{PollInterval: 15 * time.Second},
		Artifacts: Artifacts{BuildDirectory: "data/builds", Directory: "data/artifacts", MaxBytes: 1024 * 1024 * 1024},
	}
}
