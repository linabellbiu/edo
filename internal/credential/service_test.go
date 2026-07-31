package credential

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
)

func TestCredentialOwnershipEncryptionAndReveal(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:credential_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatalf("初始化密钥管理器失败: %v", err)
	}
	service := NewService(db, manager)
	plaintext := "github-personal-token"
	item, err := service.Create(ctx, "user-a", Input{
		Name: "个人 GitHub", Provider: model.GitProviderGitHub, AuthType: model.GitAuthToken, Secret: &plaintext,
	})
	if err != nil {
		t.Fatalf("创建个人令牌失败: %v", err)
	}
	if item.SecretCiphertext == plaintext || item.SecretHint == plaintext {
		t.Fatal("个人令牌未加密或列表提示泄露明文")
	}
	items, err := service.List(ctx, "user-b")
	if err != nil || len(items) != 0 {
		t.Fatalf("其他用户看到了不属于自己的令牌: items=%+v err=%v", items, err)
	}
	if _, err := service.RevealOwned(ctx, "user-b", item.ID); err != ErrCredentialNotFound {
		t.Fatalf("其他用户读取令牌未被拒绝: %v", err)
	}
	revealed, err := service.RevealOwned(ctx, "user-a", item.ID)
	if err != nil || revealed != plaintext {
		t.Fatalf("令牌所有者无法读取明文: value=%q err=%v", revealed, err)
	}
	now := time.Now().UTC()
	repository := model.GitRepository{
		ID: "repository-with-api-token", Name: "API 令牌引用", Provider: model.GitProviderGitHub,
		CloneURL: "https://github.com/example/repository.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
		APICredentialID: &item.ID, IsActive: true, CreatedBy: "user-a", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatalf("创建 API 令牌引用仓库失败: %v", err)
	}
	if err := service.Delete(ctx, "user-a", item.ID); err != ErrCredentialInUse {
		t.Fatalf("仍被平台 API 引用的令牌可以删除: %v", err)
	}
}
