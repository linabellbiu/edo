package sqlchange

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/model"
)

type freshSQLAuthorizationFixture struct {
	*domainFixture
	access  *access.Service
	roleIDs map[string]string
}

func newFreshSQLAuthorizationFixture(t *testing.T) *freshSQLAuthorizationFixture {
	t.Helper()
	f := &freshSQLAuthorizationFixture{domainFixture: fixture(t), roleIDs: map[string]string{}}
	scope, _ := scopeFrom(f.ctx)
	for _, actorID := range []string{f.owner, f.reviewer, f.other} {
		var member model.ProjectMember
		if err := f.s.db.Where("project_id = ? AND user_id = ?", scope.ProjectID, actorID).First(&member).Error; err != nil {
			t.Fatal(err)
		}
		f.roleIDs[actorID] = member.RoleID
	}
	var err error
	f.access, err = access.NewService(f.s.db)
	if err != nil {
		t.Fatal(err)
	}
	f.s.permissions = f.access
	return f
}

func (f *freshSQLAuthorizationFixture) revokeWithoutReload(t *testing.T, actorID, permission, change string) {
	t.Helper()
	// 保留真实 Casbin 实例的旧策略，只在本用例独立数据库提交权限变更。
	if change == "role_action_removed" {
		result := f.s.db.Where("role_id = ? AND permission = ?", f.roleIDs[actorID], permission).Delete(&model.RolePermission{})
		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf("隔离测试库未撤销角色动作: rows=%d err=%v", result.RowsAffected, result.Error)
		}
	} else if err := f.s.db.Create(&model.UserPermission{UserID: actorID, Permission: permission, Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	var user model.User
	if err := f.s.db.First(&user, "uuid = ?", actorID).Error; err != nil {
		t.Fatal(err)
	}
	stale, err := f.access.HasPermission(f.actor(actorID), &user, permission)
	if err != nil || !stale {
		t.Fatalf("夹具必须保留尚未重载的 Casbin 允许: permission=%s allowed=%v err=%v", permission, stale, err)
	}
	scope, _ := scopeFrom(f.ctx)
	fresh, err := access.FreshProjectPermissions(f.s.db, f.actor(actorID), actorID, scope.ProjectID)
	if err != nil || fresh.Permissions[permission] {
		t.Fatalf("测试库的当前有效权限必须已经拒绝动作: permission=%s fresh=%+v err=%v", permission, fresh, err)
	}
}

func TestSQLChangeFreshAuthorizationPositiveLifecycleUsesRealCasbin(t *testing.T) {
	f := newFreshSQLAuthorizationFixture(t)
	d := f.approved(t)
	if _, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.HandleExecute(context.Background(), f.message(t, "sql_change.execute")); err != nil {
		t.Fatal(err)
	}
	if f.runner.checks.Load() != 1 || f.runner.executions.Load() != 1 {
		t.Fatalf("真实授权正向控制未完成正常检查和执行: checks=%d executions=%d", f.runner.checks.Load(), f.runner.executions.Load())
	}
}

func TestSQLChangeQueuedCheckRejectsRevokedActionBeforeCasbinReload(t *testing.T) {
	for _, permission := range []string{"sql_change.create", "sql_change.read"} {
		for _, change := range []string{"role_action_removed", "personal_deny_added"} {
			t.Run(permission+"/"+change, func(t *testing.T) {
				f := newFreshSQLAuthorizationFixture(t)
				d := f.saved(t)
				f.revokeWithoutReload(t, f.owner, permission, change)
				err := f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check"))
				if !errors.Is(err, ErrCheckFailed) || err.Error() != ErrCheckFailed.Error() {
					t.Errorf("排队后检查动作撤权应以安全提示终止检查: err=%v", err)
				}
				if f.runner.checks.Load() != 0 {
					t.Errorf("数据库已撤权仍启动 SQL 检查器: checks=%d", f.runner.checks.Load())
				}
				var stored model.SQLChange
				if err := f.s.db.First(&stored, "uuid = ?", d.Change.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Status != "check_failed" {
					t.Errorf("失权检查任务不得进入待审批: status=%s", stored.Status)
				}
			})
		}
	}
}

func TestSQLChangeQueuedExecutionRejectsRevokedActionBeforeCasbinReload(t *testing.T) {
	for _, subject := range []struct{ name, permission string }{
		{"actor", "sql_change.execute"}, {"actor", "sql_change.read"}, {"actor", "application_database.execute"},
		{"reviewer", "sql_change.review"}, {"reviewer", "sql_change.read"},
	} {
		for _, change := range []string{"role_action_removed", "personal_deny_added"} {
			t.Run(subject.name+"/"+subject.permission+"/"+change, func(t *testing.T) {
				f := newFreshSQLAuthorizationFixture(t)
				d := f.approved(t)
				if _, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest}); err != nil {
					t.Fatal(err)
				}
				actorID := f.owner
				if subject.name == "reviewer" {
					actorID = f.reviewer
				}
				f.revokeWithoutReload(t, actorID, subject.permission, change)
				err := f.s.HandleExecute(context.Background(), f.message(t, "sql_change.execute"))
				if !errors.Is(err, ErrForbidden) || err.Error() != ErrForbidden.Error() {
					t.Errorf("排队后执行或审核动作撤权应返回安全拒绝: err=%v", err)
				}
				if f.runner.executions.Load() != 0 {
					t.Errorf("数据库已撤权仍调用 SQL 执行器: executions=%d", f.runner.executions.Load())
				}
				var stored model.SQLChange
				if err := f.s.db.First(&stored, "uuid = ?", d.Change.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Status != "failed" || stored.SuccessAt != nil {
					t.Errorf("失权任务不得记为执行成功: status=%s success_at=%v", stored.Status, stored.SuccessAt)
				}
			})
		}
	}
}

func TestSQLChangeRunningExecutionStopsAfterFreshRevocationBeforeCasbinReload(t *testing.T) {
	for _, subject := range []string{"actor", "reviewer"} {
		for _, change := range []string{"role_action_removed", "personal_deny_added"} {
			t.Run(subject+"/"+change, func(t *testing.T) {
				f := newFreshSQLAuthorizationFixture(t)
				f.s.executionCheckInterval = 20 * time.Millisecond
				d := f.approved(t)
				if _, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest}); err != nil {
					t.Fatal(err)
				}
				f.runner.enter, f.runner.release = make(chan struct{}), make(chan struct{})
				ctx, cancel := context.WithCancel(context.Background())
				result, exited := make(chan error, 1), make(chan struct{})
				message := f.message(t, "sql_change.execute")
				go func() {
					defer close(exited)
					result <- f.s.HandleExecute(ctx, message)
				}()
				defer func() {
					cancel()
					select {
					case <-exited:
					case <-time.After(5 * time.Second):
						t.Error("测试后台执行未随取消退出")
					}
				}()
				select {
				case <-f.runner.enter:
				case <-time.After(5 * time.Second):
					t.Fatal("正常授权的测试任务未进入执行")
				}
				actorID, permission := f.owner, "sql_change.execute"
				if subject == "reviewer" {
					actorID, permission = f.reviewer, "sql_change.review"
				}
				f.revokeWithoutReload(t, actorID, permission, change)
				select {
				case err := <-result:
					if !errors.Is(err, ErrExecutionUncertain) {
						t.Fatalf("运行中失权应停止并保守记录结果不确定: err=%v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("当前数据库已撤权，运行任务仍沿用旧 Casbin 授权")
				}
				var stored model.SQLChange
				if err := f.s.db.First(&stored, "uuid = ?", d.Change.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Status != "uncertain" || stored.SuccessAt != nil {
					t.Fatalf("执行中撤权不得记为成功: status=%s success_at=%v", stored.Status, stored.SuccessAt)
				}
			})
		}
	}
}

func TestSQLChangeFreshAuthorizationStorageFailureIsSafeAndUsesReviewerTransaction(t *testing.T) {
	for _, subject := range []string{"actor", "reviewer"} {
		t.Run(subject, func(t *testing.T) {
			f := newFreshSQLAuthorizationFixture(t)
			var logs bytes.Buffer
			f.s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			storageErr := errors.New("role_permissions SELECT failed at private-database.internal")
			callback := "test:sql_fresh_authorization_storage_error"
			if err := f.s.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "role_permissions" {
					tx.AddError(storageErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.s.db.Callback().Query().Remove(callback) })
			var err error
			actorID := f.owner
			if subject == "reviewer" {
				actorID = f.reviewer
				err = f.s.db.WithContext(f.ctx).Transaction(func(tx *gorm.DB) error {
					return f.s.validateReviewer(f.ctx, tx, f.reviewer, f.owner)
				})
			} else {
				err = f.s.authorize(f.ctx, f.owner, "sql_change.execute")
			}
			if !errors.Is(err, ErrStorage) || err.Error() != ErrStorage.Error() {
				t.Errorf("当前授权存储故障必须返回稳定安全的中文错误: err=%v", err)
			}
			scope, _ := scopeFrom(f.ctx)
			for _, expected := range []string{"sql_change_current_permission", scope.ProjectID, actorID, storageErr.Error()} {
				if !strings.Contains(logs.String(), expected) {
					t.Errorf("当前授权故障缺少必要日志上下文 %q: logs=%s", expected, logs.String())
				}
			}
		})
	}
}
