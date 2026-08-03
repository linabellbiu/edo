package account

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/auth"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
)

func TestEnsureInitialAdmin(t *testing.T) {
	db := openAccountTestDatabase(t, "initial-admin")
	service := NewService(db)

	admin, created, err := service.EnsureInitialAdmin(context.Background())
	if err != nil {
		t.Fatalf("初始化管理员失败: %v", err)
	}
	if !created || admin.Username != "admin" || !admin.IsActive || !admin.IsSuperuser || admin.AuthVersion != 1 {
		t.Fatalf("初始化管理员状态错误: created=%v admin=%+v", created, admin)
	}
	matched, err := auth.ComparePassword("123456", admin.PasswordHash)
	if err != nil || !matched {
		t.Fatalf("初始化管理员密码无效: matched=%v err=%v", matched, err)
	}

	again, created, err := service.EnsureInitialAdmin(context.Background())
	if err != nil || created || again != nil {
		t.Fatalf("重复初始化不应创建账户: created=%v admin=%+v err=%v", created, again, err)
	}
}

func TestChangePasswordVerifiesCurrentPasswordAndInvalidatesSessions(t *testing.T) {
	db := openAccountTestDatabase(t, "change-password")
	service := NewService(db)
	user, err := service.CreateUser(context.Background(), "operator", "运维人员", "old-password-123")
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	if err := service.ChangePassword(context.Background(), user.ID, "wrong-password", "new-password-456"); !errors.Is(err, ErrCurrentPassword) {
		t.Fatalf("错误的当前密码未被拒绝: %v", err)
	}
	if err := service.ChangePassword(context.Background(), user.ID, "old-password-123", "old-password-123"); !errors.Is(err, ErrPasswordUnchanged) {
		t.Fatalf("重复使用旧密码未被拒绝: %v", err)
	}
	if err := service.ChangePassword(context.Background(), user.ID, "old-password-123", "new-password-456"); err != nil {
		t.Fatalf("修改密码失败: %v", err)
	}
	updated, err := service.FindByID(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("读取修改后用户失败: %v", err)
	}
	if updated.AuthVersion != user.AuthVersion+1 {
		t.Fatalf("认证版本未递增: got %d want %d", updated.AuthVersion, user.AuthVersion+1)
	}
	matched, err := auth.ComparePassword("new-password-456", updated.PasswordHash)
	if err != nil || !matched {
		t.Fatalf("新密码无法通过校验: matched=%v err=%v", matched, err)
	}
}

func TestEnsureInitialAdminDoesNotModifyExistingUsers(t *testing.T) {
	db := openAccountTestDatabase(t, "existing-user")
	now := time.Now().UTC()
	existing := model.User{
		ID: "existing-user", Username: "operator", Nickname: "运维人员", PasswordHash: "existing-hash",
		IsActive: true, AuthVersion: 7, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("创建已有用户失败: %v", err)
	}

	admin, created, err := NewService(db).EnsureInitialAdmin(context.Background())
	if err != nil || created || admin != nil {
		t.Fatalf("已有账户时不应创建管理员: created=%v admin=%+v err=%v", created, admin, err)
	}
	var count int64
	if err := db.Model(&model.User{}).Where("username = ?", "admin").Count(&count).Error; err != nil {
		t.Fatalf("检查默认管理员失败: %v", err)
	}
	if count != 0 {
		t.Fatal("已有账户的数据库不应补建默认管理员")
	}
	var stored model.User
	if err := db.First(&stored, "id = ?", existing.ID).Error; err != nil {
		t.Fatalf("读取已有用户失败: %v", err)
	}
	if stored.PasswordHash != existing.PasswordHash || stored.AuthVersion != existing.AuthVersion {
		t.Fatalf("已有用户被意外修改: %+v", stored)
	}
}

func TestSetDepartmentRequiresRepositoryCredentialHandover(t *testing.T) {
	db := openAccountTestDatabase(t, "department-credential-handover")
	ctx := context.Background()
	now := time.Now().UTC()
	for _, department := range []model.Department{
		{ID: "department-source", Name: "原部门", IsActive: true, CreatedAt: now, UpdatedAt: now},
		{ID: "department-target", Name: "新部门", IsActive: true, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&department).Error; err != nil {
			t.Fatalf("创建测试部门失败: %v", err)
		}
	}
	service := NewService(db)
	user, err := service.CreateUserInDepartment(ctx, "transferuser", "调动用户", "correct horse battery staple", "department-source")
	if err != nil {
		t.Fatalf("创建调动用户失败: %v", err)
	}
	credential := model.GitCredential{
		ID: "transfer-credential", UserID: user.ID, Name: "部门仓库令牌",
		Provider: model.GitProviderGitea, AuthType: model.GitAuthToken,
		SecretCiphertext: "encrypted-placeholder", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatalf("创建个人令牌失败: %v", err)
	}
	repository := model.GitRepository{
		ID: "transfer-repository", DepartmentID: "department-source", Name: "部门仓库",
		Provider: model.GitProviderGitea, CloneURL: "https://git.example.com/team/repository.git",
		DefaultBranch: "main", AuthType: model.GitAuthToken, CredentialID: &credential.ID,
		CredentialCiphertext: "", WebhookSecretCiphertext: "", IsActive: true,
		CreatedBy: user.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatalf("创建引用个人令牌的仓库失败: %v", err)
	}
	if err := service.SetDepartment(ctx, user.ID, "department-target"); !errors.Is(err, ErrUserCredentialUsed) {
		t.Fatalf("仓库令牌尚未交接时仍允许调动部门: %v", err)
	}
	if err := db.Model(&model.GitRepository{}).Where("id = ?", repository.ID).
		Updates(map[string]any{"credential_id": nil, "api_credential_id": nil}).Error; err != nil {
		t.Fatalf("解除仓库令牌引用失败: %v", err)
	}
	if err := service.SetDepartment(ctx, user.ID, "department-target"); err != nil {
		t.Fatalf("完成令牌交接后调整部门失败: %v", err)
	}
	updated, err := service.FindByID(ctx, user.ID)
	if err != nil || updated.DepartmentID != "department-target" || updated.AuthVersion != user.AuthVersion+1 {
		t.Fatalf("用户部门或认证版本未更新: user=%+v err=%v", updated, err)
	}
}

func openAccountTestDatabase(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{
		Driver:          "sqlite",
		DSN:             "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	return db
}
