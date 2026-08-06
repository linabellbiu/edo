package access

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/database"
)

func TestListRolesCountsOnlyDepartmentVisibleMembers(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:access_role_member_count_test?mode=memory&cache=shared",
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
	reader, err := service.CreateRole(ctx, RoleInput{
		Name: "member-reader", DisplayName: "成员查看员", Permissions: []string{PermissionUserRead},
	})
	if err != nil {
		t.Fatalf("创建第一个角色失败: %v", err)
	}
	operator, err := service.CreateRole(ctx, RoleInput{
		Name: "member-operator", DisplayName: "成员操作员", Permissions: []string{PermissionUserUpdate},
	})
	if err != nil {
		t.Fatalf("创建第二个角色失败: %v", err)
	}
	emptyRole, err := service.CreateRole(ctx, RoleInput{
		Name: "member-empty", DisplayName: "空角色", Permissions: []string{},
	})
	if err != nil {
		t.Fatalf("创建空角色失败: %v", err)
	}

	if _, err := service.CreateUserInDepartment(
		ctx, "membera", "甲部门成员", "correct horse battery staple", "department-a", []string{reader.ID, operator.ID},
	); err != nil {
		t.Fatalf("创建甲部门成员失败: %v", err)
	}
	if _, err := service.CreateUserInDepartment(
		ctx, "memberb", "乙部门成员", "correct horse battery staple", "department-b", []string{reader.ID},
	); err != nil {
		t.Fatalf("创建乙部门成员失败: %v", err)
	}

	assertCounts := func(testCtx context.Context, want map[string]int64) {
		t.Helper()
		roles, err := service.ListRoles(testCtx)
		if err != nil {
			t.Fatalf("查询角色成员计数失败: %v", err)
		}
		if len(roles) != 3 {
			t.Fatalf("角色数量错误: got=%d want=3", len(roles))
		}
		for i := range roles {
			if roles[i].VisibleMemberCount != want[roles[i].ID] {
				t.Fatalf("角色 %s 可见成员计数错误: got=%d want=%d", roles[i].Name, roles[i].VisibleMemberCount, want[roles[i].ID])
			}
			wantInUse := roles[i].ID != emptyRole.ID
			if roles[i].InUse != wantInUse {
				t.Fatalf("角色 %s 全局使用状态错误: got=%t want=%t", roles[i].Name, roles[i].InUse, wantInUse)
			}
		}
	}

	assertCounts(database.WithDepartmentScope(ctx, database.DepartmentScope{
		UserID: "department-a-admin", DepartmentID: "department-a",
	}), map[string]int64{reader.ID: 1, operator.ID: 1, emptyRole.ID: 0})
	assertCounts(database.WithDepartmentScope(ctx, database.DepartmentScope{
		UserID: "department-b-admin", DepartmentID: "department-b",
	}), map[string]int64{reader.ID: 1, operator.ID: 0, emptyRole.ID: 0})
	assertCounts(database.WithDepartmentScope(ctx, database.DepartmentScope{
		UserID: "superuser", DepartmentID: database.DefaultDepartmentID, AllDepartments: true,
	}), map[string]int64{reader.ID: 2, operator.ID: 1, emptyRole.ID: 0})
}
