package credential

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"edo/internal/testutil"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
)

func TestCredentialOwnershipEncryptionAndReveal(t *testing.T) {
	ctx := context.Background()
	db, err := testutil.OpenDatabase(t)
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
	now := time.Now().UTC()
	if err := db.Create(&model.User{ID: "user-a", Username: "credential-owner", Nickname: "凭据所有者",
		DepartmentID: database.DefaultDepartmentID, IsActive: true, AuthVersion: 1,
		PasswordHash: "unused-unit-test-hash", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
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
	now = time.Now().UTC()
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

func TestCredentialCreateRejectsMissingOwner(t *testing.T) {
	db := testutil.NewDatabase(t)
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	value := "synthetic-missing-owner-token"
	_, err = NewService(db, manager).Create(context.Background(), "missing-owner", Input{Name: "missing owner", Provider: model.GitProviderGeneric, AuthType: model.GitAuthToken, Secret: &value})
	var count int64
	if readErr := db.Model(&model.GitCredential{}).Count(&count).Error; readErr != nil || count != 0 || !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("凭据创建绕过了所有者存在检查: count=%d err=%v read_err=%v", count, err, readErr)
	}
}
