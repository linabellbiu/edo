package access

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"edo/internal/account"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
)

func TestRolePermissionAndAtomicUserCreation(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:access_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}

	service, err := NewService(db)
	if err != nil {
		t.Fatalf("初始化 Casbin 权限服务失败: %v", err)
	}
	role, err := service.CreateRole(ctx, RoleInput{
		Name: "auditor", DisplayName: "审计员", Permissions: []string{PermissionAuditRead},
	})
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	user, err := service.CreateUser(ctx, "reviewer", "审核用户", "correct horse battery staple", []string{role.ID})
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	allowed, err := service.HasPermission(ctx, user, PermissionAuditRead)
	if err != nil || !allowed {
		t.Fatalf("用户权限未生效: allowed=%v err=%v", allowed, err)
	}
	allowed, err = service.HasPermission(ctx, user, PermissionRoleUpdate)
	if err != nil || allowed {
		t.Fatalf("用户获得了未分配权限: allowed=%v err=%v", allowed, err)
	}
	if err := service.SetUserPermissions(ctx, user.ID, UserPermissionOverrides{
		Allow: []string{PermissionRoleUpdate}, Deny: []string{PermissionAuditRead},
	}); err != nil {
		t.Fatalf("配置用户权限覆盖失败: %v", err)
	}
	allowed, err = service.HasPermission(ctx, user, PermissionRoleUpdate)
	if err != nil || !allowed {
		t.Fatalf("用户直接授权未生效: allowed=%v err=%v", allowed, err)
	}
	allowed, err = service.HasPermission(ctx, user, PermissionAuditRead)
	if err != nil || allowed {
		t.Fatalf("用户显式拒绝未覆盖角色授权: allowed=%v err=%v", allowed, err)
	}
	effective, err := service.UserPermissions(ctx, user)
	if err != nil || len(effective) != 1 || effective[0] != PermissionRoleUpdate {
		t.Fatalf("用户有效权限计算错误: permissions=%v err=%v", effective, err)
	}
	legacyRole, err := service.CreateRole(ctx, RoleInput{
		Name: "legacy-identity", DisplayName: "旧版登录方式管理员", Permissions: []string{PermissionIdentityManage},
	})
	if err != nil {
		t.Fatalf("旧权限创建角色失败: %v", err)
	}
	wantLegacyRolePermissions := []string{PermissionIdentityCreate, PermissionIdentityUpdate}
	if len(legacyRole.Permissions) != len(wantLegacyRolePermissions) ||
		legacyRole.Permissions[0] != wantLegacyRolePermissions[0] || legacyRole.Permissions[1] != wantLegacyRolePermissions[1] {
		t.Fatalf("旧角色权限未按真实 API 能力展开: got=%v want=%v", legacyRole.Permissions, wantLegacyRolePermissions)
	}
	if err := service.SetUserPermissions(ctx, user.ID, UserPermissionOverrides{
		Allow: []string{PermissionRepositoryManage}, Deny: []string{PermissionRepositoryExecute},
	}); !errors.Is(err, ErrInvalidUserPermissions) {
		t.Fatalf("旧权限展开后的允许/拒绝冲突未被拒绝: %v", err)
	}
	if err := service.SetUserPermissions(ctx, user.ID, UserPermissionOverrides{
		Allow: []string{PermissionCredentialManage, PermissionCredentialCreate},
	}); err != nil {
		t.Fatalf("旧权限覆盖写入失败: %v", err)
	}
	overrides, err := service.UserPermissionOverrides(ctx, user.ID)
	if err != nil {
		t.Fatalf("读取展开后的用户权限覆盖失败: %v", err)
	}
	wantOverrides := []string{PermissionCredentialCreate, PermissionCredentialDelete, PermissionCredentialUpdate}
	if len(overrides.Allow) != len(wantOverrides) {
		t.Fatalf("旧用户权限未展开为新权限: got=%v want=%v", overrides.Allow, wantOverrides)
	}
	for index := range wantOverrides {
		if overrides.Allow[index] != wantOverrides[index] {
			t.Fatalf("旧用户权限展开结果错误: got=%v want=%v", overrides.Allow, wantOverrides)
		}
	}

	_, err = service.CreateUser(ctx, "orphan", "不应创建", "correct horse battery staple", []string{"missing-role"})
	if !errors.Is(err, ErrInvalidUserRoles) {
		t.Fatalf("无效角色未被拒绝: %v", err)
	}
	if _, err := account.NewService(db).FindByUsername(ctx, "orphan"); err == nil {
		t.Fatal("事务失败后仍留下用户记录")
	}
}

func TestDistributedCasbinPolicySync(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:access_distributed_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}

	redisServer := miniredis.RunT(t)
	firstRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	secondRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		_ = firstRedis.Close()
		_ = secondRedis.Close()
	})
	first, err := NewDistributedService(db, firstRedis, "edo:test:casbin", slog.Default())
	if err != nil {
		t.Fatalf("初始化第一个分布式权限服务失败: %v", err)
	}
	second, err := NewDistributedService(db, secondRedis, "edo:test:casbin", slog.Default())
	if err != nil {
		t.Fatalf("初始化第二个分布式权限服务失败: %v", err)
	}

	role, err := first.CreateRole(ctx, RoleInput{
		Name: "operator", DisplayName: "运维人员", Permissions: []string{PermissionSystemRead},
	})
	if err != nil {
		t.Fatalf("创建同步测试角色失败: %v", err)
	}
	user, err := first.CreateUser(ctx, "operator", "运维人员", "correct horse battery staple", []string{role.ID})
	if err != nil {
		t.Fatalf("创建同步测试用户失败: %v", err)
	}
	waitForPermission(t, second, user, PermissionSystemRead, true)

	if err := first.SetUserPermissions(ctx, user.ID, UserPermissionOverrides{Deny: []string{PermissionSystemRead}}); err != nil {
		t.Fatalf("设置同步测试拒绝权限失败: %v", err)
	}
	waitForPermission(t, second, user, PermissionSystemRead, false)
}

func TestDeleteUserCleansIdentityButKeepsSharedRoles(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:access_delete_user_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatalf("初始化权限服务失败: %v", err)
	}
	role, err := service.CreateRole(ctx, RoleInput{
		Name: "developer", DisplayName: "开发人员", Permissions: []string{PermissionDeliveryRead},
	})
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	user, err := service.CreateUser(ctx, "leaver", "离职用户", "correct horse battery staple", []string{role.ID})
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := service.SetUserPermissions(ctx, user.ID, UserPermissionOverrides{Allow: []string{PermissionDeliveryUpdate}}); err != nil {
		t.Fatalf("创建用户权限覆盖失败: %v", err)
	}
	now := time.Now().UTC()
	credential := model.GitCredential{
		ID: "credential-delete-test", UserID: user.ID, Name: "待清理令牌",
		Provider: model.GitProviderGitea, AuthType: model.GitAuthToken,
		SecretCiphertext: "encrypted-placeholder", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatalf("创建用户令牌失败: %v", err)
	}
	repository := model.GitRepository{
		ID: uuid.NewString(), DepartmentID: user.DepartmentID, Name: "离职交接仓库",
		Provider: model.GitProviderGitea, CloneURL: "https://git.example.com/team/service.git",
		DefaultBranch: "main", AuthType: model.GitAuthToken, CredentialID: &credential.ID,
		CredentialCiphertext: "", WebhookSecretCiphertext: "", IsActive: true,
		CreatedBy: user.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatalf("创建令牌引用仓库失败: %v", err)
	}
	if err := service.DeleteUser(ctx, user.ID); !errors.Is(err, account.ErrUserCredentialUsed) {
		t.Fatalf("仍被仓库引用的用户令牌未阻止删除: %v", err)
	}
	if err := db.Model(&model.GitRepository{}).Where("id = ?", repository.ID).
		Updates(map[string]any{"credential_id": nil, "api_credential_id": nil}).Error; err != nil {
		t.Fatalf("解除仓库令牌引用失败: %v", err)
	}
	if err := service.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("删除用户失败: %v", err)
	}
	for label, value := range map[string]any{
		"用户": &model.User{}, "用户角色": &model.UserRole{},
		"用户权限": &model.UserPermission{}, "个人令牌": &model.GitCredential{},
	} {
		var count int64
		query := db.Model(value)
		if label == "用户" {
			query = query.Where("id = ?", user.ID)
		} else {
			query = query.Where("user_id = ?", user.ID)
		}
		if err := query.Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s未清理: count=%d err=%v", label, count, err)
		}
	}
	var roleCount int64
	if err := db.Model(&model.Role{}).Where("id = ?", role.ID).Count(&roleCount).Error; err != nil || roleCount != 1 {
		t.Fatalf("删除用户不应删除共享角色: count=%d err=%v", roleCount, err)
	}
	var repositoryCount int64
	if err := db.Model(&model.GitRepository{}).Where("id = ?", repository.ID).Count(&repositoryCount).Error; err != nil || repositoryCount != 1 {
		t.Fatalf("删除用户不应删除部门业务资源: count=%d err=%v", repositoryCount, err)
	}

	admin, err := account.NewService(db).CreateAdmin(ctx, "rootadmin", "超级管理员", "correct horse battery staple")
	if err != nil {
		t.Fatalf("创建超级管理员失败: %v", err)
	}
	if err := service.DeleteUser(ctx, admin.ID); !errors.Is(err, account.ErrSuperuserImmutable) {
		t.Fatalf("超级管理员删除未被拒绝: %v", err)
	}
}

func waitForPermission(t *testing.T, service *Service, user *model.User, permission string, expected bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		allowed, err := service.HasPermission(context.Background(), user, permission)
		if err == nil && allowed == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	allowed, err := service.HasPermission(context.Background(), user, permission)
	t.Fatalf("等待多实例权限同步超时: permission=%s expected=%v actual=%v err=%v", permission, expected, allowed, err)
}
