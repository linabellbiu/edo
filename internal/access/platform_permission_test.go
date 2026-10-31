package access

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

type platformPermissionQueryCounter struct {
	*sql.DB
	queries atomic.Int64
}

func (p *platformPermissionQueryCounter) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	p.queries.Add(1)
	return p.DB.QueryContext(ctx, query, args...)
}

func (p *platformPermissionQueryCounter) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.queries.Add(1)
	return p.DB.QueryRowContext(ctx, query, args...)
}

func (p *platformPermissionQueryCounter) GetDBConn() (*sql.DB, error) { return p.DB, nil }

func TestPlatformPermissionChecksFreshGrantDenyAndIdentityInOneQuery(t *testing.T) {
	for _, candidate := range []struct {
		name    string
		mutate  func(*testing.T, *currentPermissionFixture)
		want    bool
		queries int
	}{
		{"现有角色授权", nil, true, 1},
		{"角色权限已撤销", func(t *testing.T, f *currentPermissionFixture) {
			if err := f.db.Where("role_id = ? AND permission = ?", f.platformRole, PermissionAuditRead).Delete(&model.RolePermission{}).Error; err != nil {
				t.Fatal(err)
			}
		}, false, 1},
		{"角色关联已移除", func(t *testing.T, f *currentPermissionFixture) {
			if err := f.db.Where("user_id = ? AND role_id = ?", f.user.ID, f.platformRole).Delete(&model.UserRole{}).Error; err != nil {
				t.Fatal(err)
			}
		}, false, 1},
		{"个人拒绝覆盖角色", func(t *testing.T, f *currentPermissionFixture) {
			if err := f.db.Create(&model.UserPermission{UserID: f.user.ID, Permission: PermissionAuditRead, Effect: model.PermissionDeny}).Error; err != nil {
				t.Fatal(err)
			}
		}, false, 1},
		{"个人允许保留旧候选", func(t *testing.T, f *currentPermissionFixture) {
			if err := f.db.Where("user_id = ? AND role_id = ?", f.user.ID, f.platformRole).Delete(&model.UserRole{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Create(&model.UserPermission{UserID: f.user.ID, Permission: PermissionAuditRead, Effect: model.PermissionAllow}).Error; err != nil {
				t.Fatal(err)
			}
		}, true, 1},
		{"当前用户已停用", func(t *testing.T, f *currentPermissionFixture) {
			if err := f.db.Model(&model.User{}).Where("uuid = ?", f.user.ID).Update("is_active", false).Error; err != nil {
				t.Fatal(err)
			}
		}, false, 1},
		{"认证版本已变化", func(t *testing.T, f *currentPermissionFixture) { f.user.AuthVersion++ }, false, 1},
		{"用户部门已变化", func(t *testing.T, f *currentPermissionFixture) {
			if err := f.db.Model(&model.User{}).Where("uuid = ?", f.user.ID).Update("department_id", "changed-department").Error; err != nil {
				t.Fatal(err)
			}
		}, false, 1},
		{"用户不存在", func(t *testing.T, f *currentPermissionFixture) {
			if err := f.db.Where("uuid = ?", f.user.ID).Delete(&model.User{}).Error; err != nil {
				t.Fatal(err)
			}
		}, false, 1},
		{"存储身份已变为管理员", func(t *testing.T, f *currentPermissionFixture) {
			if err := f.db.Model(&model.User{}).Where("uuid = ?", f.user.ID).Update("is_superuser", true).Error; err != nil {
				t.Fatal(err)
			}
		}, false, 1},
		{"上下文身份不匹配", func(_ *testing.T, f *currentPermissionFixture) {
			f.ctx = database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "other-user", DepartmentID: f.user.DepartmentID})
		}, false, 0},
		{"上下文部门不匹配", func(_ *testing.T, f *currentPermissionFixture) {
			f.ctx = database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: f.user.ID, DepartmentID: "other-department"})
		}, false, 0},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			f := newCurrentPermissionFixture(t)
			if candidate.mutate != nil {
				candidate.mutate(t, &f)
			}
			pool, err := f.db.DB()
			if err != nil {
				t.Fatal(err)
			}
			// 统计真实数据库调用，不把 GORM DryRun 子查询构造当成额外执行。
			counter := &platformPermissionQueryCounter{DB: pool}
			f.db.ConnPool, f.db.Statement.ConnPool = counter, counter
			allowed, err := f.service.HasPermission(f.ctx, f.user, PermissionAuditRead)
			if err != nil || allowed != candidate.want || counter.queries.Load() != int64(candidate.queries) {
				t.Fatalf("平台单次判定不正确: allowed=%v want=%v queries=%d want_queries=%d err=%v", allowed, candidate.want, counter.queries.Load(), candidate.queries, err)
			}
		})
	}
}

func TestPlatformPermissionDoesNotExpandCasbinOrChangeProjectAndSuperuserPaths(t *testing.T) {
	f := newCurrentPermissionFixture(t)
	if err := f.db.Create(&model.UserPermission{UserID: f.user.ID, Permission: PermissionSystemRead, Effect: model.PermissionAllow}).Error; err != nil {
		t.Fatal(err)
	}
	queries := 0
	const callback = "test:platform_candidate_query_count"
	if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(*gorm.DB) { queries++ }); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Query().Remove(callback)
	if allowed, err := f.service.HasPermission(f.ctx, f.user, PermissionSystemRead); err != nil || allowed || queries != 0 {
		t.Fatal("新的存储授权不得绕过旧Casbin拒绝")
	}
	if allowed, err := f.service.HasPermission(f.ctx, f.user, "application.read"); err != nil || !allowed || queries != 0 {
		t.Fatal("项目候选路径不应加入平台查询")
	}
	admin := *f.user
	admin.IsSuperuser = true
	if allowed, err := f.service.HasPermission(f.ctx, &admin, PermissionSystemRead); err != nil || !allowed || queries != 0 {
		t.Fatal("管理员恢复路径被额外查询改变")
	}
}

func TestPlatformPermissionCancellationAndStorageFailureAreSafe(t *testing.T) {
	f := newCurrentPermissionFixture(t)
	var logs bytes.Buffer
	f.service.logger = slog.New(slog.NewTextHandler(&logs, nil))
	queries := 0
	const marker = "a03-private-storage-diagnostic"
	const callback = "test:platform_storage_failure"
	if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) { queries++; tx.AddError(errors.New(marker)) }); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Query().Remove(callback)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if allowed, err := f.service.HasPermission(ctx, f.user, PermissionAuditRead); allowed || !errors.Is(err, context.Canceled) || queries != 0 {
		t.Fatal("已取消的判定不得读取存储或放行")
	}
	allowed, err := f.service.HasPermission(f.ctx, f.user, PermissionAuditRead)
	if allowed || err == nil || strings.Contains(err.Error(), marker) || queries != 1 {
		t.Fatal("存储故障不得放行、泄漏诊断或多次读取")
	}
	if !strings.Contains(logs.String(), "platform_permission_recheck") || !strings.Contains(logs.String(), marker) || !strings.Contains(logs.String(), "permission_check_canceled") {
		t.Fatal("故障缺少安全操作诊断")
	}
}

func TestUserPermissionsMetadataDoesNotExecutePerGrantQueries(t *testing.T) {
	f := newCurrentPermissionFixture(t)
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	counter := &platformPermissionQueryCounter{DB: pool}
	f.db.ConnPool, f.db.Statement.ConnPool = counter, counter
	permissions, err := f.service.UserPermissions(f.ctx, f.user)
	if err != nil || !slices.Contains(permissions, PermissionAuditRead) || !slices.Contains(permissions, PermissionRoleRead) {
		t.Fatal("多授权候选元数据不完整")
	}
	if counter.queries.Load() != 0 {
		t.Errorf("候选枚举不应按grant逐次读取数据库: queries=%d", counter.queries.Load())
	}
	counter.queries.Store(0)
	managerContext := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "metadata-manager", DepartmentID: f.user.DepartmentID, ProjectID: f.project.ID})
	permissions, err = f.service.UserPermissions(managerContext, f.user)
	if err != nil || !slices.Contains(permissions, PermissionAuditRead) || !slices.Contains(permissions, PermissionRoleRead) || counter.queries.Load() != 0 {
		t.Error("管理页枚举目标用户候选权限不应误用操作者身份或触发逐grant查询")
	}
}
