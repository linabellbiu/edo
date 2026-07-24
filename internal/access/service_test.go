package access

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"zrt/internal/account"
	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
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
	allowed, err = service.HasPermission(ctx, user, PermissionRoleManage)
	if err != nil || allowed {
		t.Fatalf("用户获得了未分配权限: allowed=%v err=%v", allowed, err)
	}
	if err := service.SetUserPermissions(ctx, user.ID, UserPermissionOverrides{
		Allow: []string{PermissionRoleManage}, Deny: []string{PermissionAuditRead},
	}); err != nil {
		t.Fatalf("配置用户权限覆盖失败: %v", err)
	}
	allowed, err = service.HasPermission(ctx, user, PermissionRoleManage)
	if err != nil || !allowed {
		t.Fatalf("用户直接授权未生效: allowed=%v err=%v", allowed, err)
	}
	allowed, err = service.HasPermission(ctx, user, PermissionAuditRead)
	if err != nil || allowed {
		t.Fatalf("用户显式拒绝未覆盖角色授权: allowed=%v err=%v", allowed, err)
	}
	effective, err := service.UserPermissions(ctx, user)
	if err != nil || len(effective) != 1 || effective[0] != PermissionRoleManage {
		t.Fatalf("用户有效权限计算错误: permissions=%v err=%v", effective, err)
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
	first, err := NewDistributedService(db, firstRedis, "zrt:test:casbin", slog.Default())
	if err != nil {
		t.Fatalf("初始化第一个分布式权限服务失败: %v", err)
	}
	second, err := NewDistributedService(db, secondRedis, "zrt:test:casbin", slog.Default())
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
