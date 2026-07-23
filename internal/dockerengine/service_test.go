package dockerengine

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/secret"
)

func TestDockerEndpointSecurityDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:docker_endpoint_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开 Docker 测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移 Docker 测试数据库失败: %v", err)
	}
	manager, _ := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	service := NewService(db, manager, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second})

	endpoint, err := service.Create(ctx, "admin", Input{Name: "local-docker", Host: "unix:///var/run/docker.sock"})
	if err != nil || endpoint.TLSCiphertext != "" {
		t.Fatalf("创建本地 Docker Socket 失败: endpoint=%+v err=%v", endpoint, err)
	}
	_, err = service.Create(ctx, "admin", Input{Name: "unsafe-remote", Host: "tcp://docker.example.com:2375"})
	if !errors.Is(err, ErrTLSRequired) {
		t.Fatalf("远程明文 Docker API 未被拒绝: %v", err)
	}
	_, err = service.Create(ctx, "admin", Input{Name: "bad-scheme", Host: "http://docker.example.com:2375"})
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("不受支持的 Docker URL 未被拒绝: %v", err)
	}
}
