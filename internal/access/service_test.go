package access

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

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

func TestSetUserAccessIsAtomicAndDepartmentScoped(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:access_atomic_update_test?mode=memory&cache=shared",
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
	readerRole, err := service.CreateRole(ctx, RoleInput{
		Name: "atomic-reader", DisplayName: "原角色", Permissions: []string{PermissionUserRead},
	})
	if err != nil {
		t.Fatalf("创建原角色失败: %v", err)
	}
	operatorRole, err := service.CreateRole(ctx, RoleInput{
		Name: "atomic-operator", DisplayName: "新角色", Permissions: []string{PermissionRoleUpdate},
	})
	if err != nil {
		t.Fatalf("创建新角色失败: %v", err)
	}
	user, err := service.CreateUser(ctx, "atomic-user", "原子配置用户", "correct horse battery staple", []string{readerRole.ID})
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	watcher := &countingAccessWatcher{}
	service.watcher = watcher

	if err := service.SetUserAccess(ctx, user.ID, []string{operatorRole.ID}, UserPermissionOverrides{
		Allow: []string{PermissionAuditRead}, Deny: []string{PermissionRoleUpdate},
	}); err != nil {
		t.Fatalf("原子保存用户访问配置失败: %v", err)
	}
	if watcher.updates != 1 {
		t.Fatalf("原子保存只应同步一次 Casbin 策略: updates=%d", watcher.updates)
	}
	assertUserAccess := func(wantRoles, wantAllow, wantDeny []string) {
		t.Helper()
		roleIDs, roleErr := service.UserRoleIDs(ctx, user.ID)
		if roleErr != nil || !slices.Equal(roleIDs, wantRoles) {
			t.Fatalf("用户角色不符合预期: got=%v want=%v err=%v", roleIDs, wantRoles, roleErr)
		}
		overrides, overrideErr := service.UserPermissionOverrides(ctx, user.ID)
		if overrideErr != nil || !slices.Equal(overrides.Allow, wantAllow) || !slices.Equal(overrides.Deny, wantDeny) {
			t.Fatalf("用户权限例外不符合预期: got=%+v want_allow=%v want_deny=%v err=%v", overrides, wantAllow, wantDeny, overrideErr)
		}
	}
	assertUserAccess([]string{operatorRole.ID}, []string{PermissionAuditRead}, []string{PermissionRoleUpdate})
	allowed, err := service.HasPermission(ctx, user, PermissionAuditRead)
	if err != nil || !allowed {
		t.Fatalf("用户直接允许权限未生效: allowed=%v err=%v", allowed, err)
	}
	allowed, err = service.HasPermission(ctx, user, PermissionRoleUpdate)
	if err != nil || allowed {
		t.Fatalf("用户拒绝权限未覆盖角色授权: allowed=%v err=%v", allowed, err)
	}

	if err := service.SetUserAccess(ctx, user.ID, []string{"missing-role"}, UserPermissionOverrides{
		Allow: []string{PermissionUserDelete},
	}); !errors.Is(err, ErrInvalidUserRoles) {
		t.Fatalf("不存在的角色未被拒绝: %v", err)
	}
	assertUserAccess([]string{operatorRole.ID}, []string{PermissionAuditRead}, []string{PermissionRoleUpdate})

	if err := service.SetUserAccess(ctx, user.ID, []string{readerRole.ID}, UserPermissionOverrides{
		Allow: []string{PermissionAuditRead}, Deny: []string{PermissionAuditRead},
	}); !errors.Is(err, ErrInvalidUserPermissions) {
		t.Fatalf("冲突的用户权限例外未被拒绝: %v", err)
	}
	assertUserAccess([]string{operatorRole.ID}, []string{PermissionAuditRead}, []string{PermissionRoleUpdate})

	otherDepartment := database.WithDepartmentScope(ctx, database.DepartmentScope{
		UserID: "other-manager", DepartmentID: "other-department",
	})
	if err := service.SetUserAccess(otherDepartment, user.ID, nil, UserPermissionOverrides{}); !errors.Is(err, account.ErrUserNotFound) {
		t.Fatalf("跨部门配置用户权限未按用户不存在处理: %v", err)
	}
	assertUserAccess([]string{operatorRole.ID}, []string{PermissionAuditRead}, []string{PermissionRoleUpdate})

	admin, err := account.NewService(db).CreateAdmin(ctx, "atomic-admin", "原子配置管理员", "correct horse battery staple")
	if err != nil {
		t.Fatalf("创建超级管理员失败: %v", err)
	}
	if err := service.SetUserAccess(ctx, admin.ID, []string{readerRole.ID}, UserPermissionOverrides{}); !errors.Is(err, account.ErrSuperuserImmutable) {
		t.Fatalf("超级管理员访问配置未被保护: %v", err)
	}
	if watcher.updates != 1 {
		t.Fatalf("失败的访问配置不应发布 Casbin 策略更新: updates=%d", watcher.updates)
	}

	var beforeSyncFailure model.User
	if err := db.First(&beforeSyncFailure, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("读取同步失败前用户状态失败: %v", err)
	}
	service.watcher = &countingAccessWatcher{err: errors.New("测试策略广播失败")}
	if err := service.SetUserAccess(ctx, user.ID, []string{readerRole.ID}, UserPermissionOverrides{}); !errors.Is(err, ErrUserAccessSyncDisabled) {
		t.Fatalf("权限同步失败未返回已保存且停用提示: %v", err)
	}
	var afterSyncFailure model.User
	if err := db.First(&afterSyncFailure, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("读取同步失败后用户状态失败: %v", err)
	}
	if afterSyncFailure.IsActive || afterSyncFailure.AuthVersion <= beforeSyncFailure.AuthVersion {
		t.Fatalf("撤权同步失败后未安全停用账户: before=%+v after=%+v", beforeSyncFailure, afterSyncFailure)
	}
	createdWithPendingSync, err := service.CreateUser(ctx, "sync-pending-user", "同步待恢复用户", "correct horse battery staple", []string{readerRole.ID})
	if !errors.Is(err, ErrUserCreatedSyncPending) || createdWithPendingSync == nil {
		t.Fatalf("创建后同步失败未保留已创建结果: user=%+v err=%v", createdWithPendingSync, err)
	}
	var storedPendingUser model.User
	if err := db.First(&storedPendingUser, "id = ?", createdWithPendingSync.ID).Error; err != nil || !storedPendingUser.IsActive {
		t.Fatalf("同步待恢复用户未正确保存: user=%+v err=%v", storedPendingUser, err)
	}
}

func TestDelegatedUserAccessCannotExceedActorPermissions(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:access_delegation_test?mode=memory&cache=shared",
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
	managerRole, err := service.CreateRole(ctx, RoleInput{
		Name: "delegation-manager", DisplayName: "受限用户管理员",
		Permissions: []string{PermissionUserCreate, PermissionUserUpdate, PermissionUserRead},
	})
	if err != nil {
		t.Fatalf("创建受限管理员角色失败: %v", err)
	}
	readerRole, err := service.CreateRole(ctx, RoleInput{
		Name: "delegation-reader", DisplayName: "允许委派角色", Permissions: []string{PermissionUserRead},
	})
	if err != nil {
		t.Fatalf("创建允许委派角色失败: %v", err)
	}
	strongRole, err := service.CreateRole(ctx, RoleInput{
		Name: "delegation-strong", DisplayName: "越权角色", Permissions: []string{PermissionUserDelete},
	})
	if err != nil {
		t.Fatalf("创建越权角色失败: %v", err)
	}
	manager, err := service.CreateUser(ctx, "delegation-manager", "受限用户管理员", "correct horse battery staple", []string{managerRole.ID})
	if err != nil {
		t.Fatalf("创建受限管理员失败: %v", err)
	}
	target, err := service.CreateUser(ctx, "delegation-target", "待配置用户", "correct horse battery staple", nil)
	if err != nil {
		t.Fatalf("创建待配置用户失败: %v", err)
	}

	if _, err := service.CreateUserInDepartmentAs(ctx, manager, "delegation-bad-create", "越权创建用户", "correct horse battery staple", manager.DepartmentID, []string{strongRole.ID}); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("创建用户时分配越权角色未被拒绝: %v", err)
	}
	if _, err := account.NewService(db).FindByUsername(ctx, "delegation-bad-create"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("越权创建失败后仍留下用户: %v", err)
	}
	if _, err := service.CreateUserInDepartmentAs(ctx, manager, "delegation-reader-user", "允许创建用户", "correct horse battery staple", manager.DepartmentID, []string{readerRole.ID}); err != nil {
		t.Fatalf("受限管理员不能委派自身权限子集: %v", err)
	}
	if err := service.SetUserRolesAs(ctx, manager, manager.ID, []string{readerRole.ID}); !errors.Is(err, ErrSelfAccessUpdate) {
		t.Fatalf("非超级管理员修改自身角色未被拒绝: %v", err)
	}
	if err := service.SetUserRolesAs(ctx, manager, target.ID, []string{strongRole.ID}); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("分步角色接口允许委派越权角色: %v", err)
	}
	if err := service.SetUserPermissionsAs(ctx, manager, target.ID, UserPermissionOverrides{Allow: []string{PermissionUserDelete}}); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("分步权限接口允许直接授予越权权限: %v", err)
	}
	if err := service.SetUserPermissionsAs(ctx, manager, manager.ID, UserPermissionOverrides{}); !errors.Is(err, ErrSelfAccessUpdate) {
		t.Fatalf("非超级管理员修改自身权限例外未被拒绝: %v", err)
	}
	if err := service.SetUserAccess(ctx, target.ID, []string{strongRole.ID}, UserPermissionOverrides{Deny: []string{PermissionUserDelete}}); err != nil {
		t.Fatalf("准备被 deny 遮蔽的高权限角色失败: %v", err)
	}
	if err := service.SetUserPermissionsAs(ctx, manager, target.ID, UserPermissionOverrides{}); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("分步权限接口允许通过移除 deny 激活越权角色: %v", err)
	}
	if err := service.SetUserAccessAs(ctx, manager, target.ID, []string{strongRole.ID}, UserPermissionOverrides{Deny: []string{PermissionUserDelete}}); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("原子接口允许使用 deny 掩盖越权角色: %v", err)
	}
	if err := service.SetUserAccessAs(ctx, manager, target.ID, []string{readerRole.ID}, UserPermissionOverrides{Allow: []string{PermissionUserDelete}}); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("原子接口允许 direct allow 绕过委派边界: %v", err)
	}
	if err := service.SetUserAccessAs(ctx, manager, target.ID, []string{readerRole.ID}, UserPermissionOverrides{Allow: []string{PermissionUserRead}}); err != nil {
		t.Fatalf("原子接口不能保存操作者权限子集: %v", err)
	}
}

type countingAccessWatcher struct {
	updates int
	err     error
}

func (*countingAccessWatcher) SetUpdateCallback(func(string)) error { return nil }

func (w *countingAccessWatcher) Update() error {
	w.updates++
	return w.err
}

func (*countingAccessWatcher) Close() {}

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
