package access

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/casbin/casbin/v3"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/account"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

type currentPermissionFixture struct {
	db                        *gorm.DB
	service                   *Service
	user                      *model.User
	platformRole, projectRole string
	project                   model.Project
	ctx                       context.Context
}

func newCurrentPermissionFixture(t *testing.T) currentPermissionFixture {
	t.Helper()
	db := testutil.NewDatabase(t)
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := service.CreateRole(context.Background(), RoleInput{Name: "profile-platform", DisplayName: "平台权限", Permissions: []string{PermissionAuditRead, PermissionRoleRead, "application.read"}})
	if err != nil {
		t.Fatal(err)
	}
	projectRole, err := service.CreateRole(context.Background(), RoleInput{Name: "profile-project", DisplayName: "项目权限", Permissions: []string{"application.read", "workflow.update"}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := service.CreateUser(context.Background(), "profile-member", "权限快照用户", "correct horse battery staple", []string{platform.ID})
	if err != nil {
		t.Fatal(err)
	}
	project := model.Project{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, Name: "当前身份测试", IsActive: true}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ProjectMember{ProjectID: project.ID, UserID: user.ID, RoleID: projectRole.ID, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.enforcer.LoadPolicy(); err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: project.ID})
	return currentPermissionFixture{db: db, service: service, user: user, platformRole: platform.ID, projectRole: projectRole.ID, project: project, ctx: ctx}
}

func TestCurrentUserPermissionsRechecksIdentityMembershipAndDeny(t *testing.T) {
	for _, candidate := range []string{"原授权", "平台角色撤权未同步", "项目角色撤权未同步", "个人拒绝未同步", "成员已移除", "项目已停用", "用户已停用", "密码已修改", "部门已变更", "用户不存在", "旧身份提权", "范围身份不匹配", "未选择项目"} {
		t.Run(candidate, func(t *testing.T) {
			f := newCurrentPermissionFixture(t)
			other, err := NewService(f.db)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"application.read", PermissionAuditRead, PermissionRoleRead, "workflow.update"}
			denied := false
			switch candidate {
			case "平台角色撤权未同步":
				_, err = other.UpdateRolePermissions(context.Background(), f.platformRole, []string{"application.read"})
				want = []string{"application.read", "workflow.update"}
			case "项目角色撤权未同步":
				_, err = other.UpdateRolePermissions(context.Background(), f.projectRole, []string{"application.read"})
				want = []string{"application.read", PermissionAuditRead, PermissionRoleRead}
			case "个人拒绝未同步":
				err = other.SetUserPermissions(context.Background(), f.user.ID, UserPermissionOverrides{Deny: []string{PermissionAuditRead, "application.read"}})
				want = []string{PermissionRoleRead, "workflow.update"}
			case "成员已移除":
				err = f.db.Where("project_id = ? AND user_id = ?", f.project.ID, f.user.ID).Delete(&model.ProjectMember{}).Error
				denied = true
			case "项目已停用":
				err = f.db.Model(&model.Project{}).Where("uuid = ?", f.project.ID).Update("is_active", false).Error
				denied = true
			case "用户已停用":
				err = account.NewService(f.db).SetActive(context.Background(), f.user.ID, false)
				denied = true
			case "密码已修改":
				err = account.NewService(f.db).ChangePassword(context.Background(), f.user.ID, "correct horse battery staple", "different correct password 123")
				denied = true
			case "部门已变更":
				department := model.Department{ID: uuid.NewString(), Name: "新部门", IsActive: true}
				if err := f.db.Create(&department).Error; err != nil {
					t.Fatal(err)
				}
				err = account.NewService(f.db).SetDepartment(context.Background(), f.user.ID, department.ID)
				denied = true
			case "用户不存在":
				err = f.db.Where("uuid = ?", f.user.ID).Delete(&model.User{}).Error
				denied = true
			case "旧身份提权":
				f.user.IsSuperuser = true
				denied = true
			case "范围身份不匹配":
				f.ctx = database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: uuid.NewString(), DepartmentID: f.user.DepartmentID, ProjectID: f.project.ID})
				denied = true
			case "未选择项目":
				f.ctx = database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: f.user.ID, DepartmentID: f.user.DepartmentID})
				err = f.service.SetUserPermissions(context.Background(), f.user.ID, UserPermissionOverrides{Allow: []string{"application.read"}})
				want = []string{PermissionAuditRead, PermissionRoleRead}
			}
			if err != nil {
				t.Fatal(err)
			}
			if candidate == "平台角色撤权未同步" || candidate == "项目角色撤权未同步" || candidate == "个人拒绝未同步" {
				cached, err := f.service.UserPermissions(f.ctx, f.user)
				if err != nil || !slices.Contains(cached, PermissionAuditRead) || !slices.Contains(cached, "workflow.update") {
					t.Fatal("未保留当前实例的旧策略，不能验证跨实例重载窗口")
				}
			}
			got, err := f.service.CurrentUserPermissions(f.ctx, f.user)
			if denied {
				if !errors.Is(err, ErrAccessDelegationDenied) || len(got) != 0 {
					t.Fatalf("失效身份或项目仍返回权限: err=%v", err)
				}
				return
			}
			slices.Sort(want)
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("当前权限不符: got=%v want=%v err=%v", got, want, err)
			}
		})
	}
}

func TestCurrentUserPermissionsNeverAddsUnsynchronizedGrant(t *testing.T) {
	f := newCurrentPermissionFixture(t)
	other, err := NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetUserPermissions(context.Background(), f.user.ID, UserPermissionOverrides{Allow: []string{PermissionCredentialSecretRead}}); err != nil {
		t.Fatal(err)
	}
	got, err := f.service.CurrentUserPermissions(f.ctx, f.user)
	if err != nil || slices.Contains(got, PermissionCredentialSecretRead) {
		t.Fatalf("当前数据库不能绕过 Casbin 扩大授权: err=%v", err)
	}
	if err := f.service.enforcer.LoadPolicy(); err != nil {
		t.Fatal(err)
	}
	got, err = f.service.CurrentUserPermissions(f.ctx, f.user)
	if err != nil || !slices.Contains(got, PermissionCredentialSecretRead) {
		t.Fatalf("同步完成后授权未恢复: err=%v", err)
	}
}

func TestCurrentUserPermissionsPreservesStorageFailure(t *testing.T) {
	for _, stage := range []string{"身份", "平台角色", "个人覆盖"} {
		t.Run(stage, func(t *testing.T) {
			db, mock := testutil.NewSQLMock(t)
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			recording := &currentPermissionBeginRecorder{DB: pool}
			db.ConnPool, db.Statement.ConnPool = recording, recording
			policy, err := newAuthorizationModel()
			if err != nil {
				t.Fatal(err)
			}
			enforcer, err := casbin.NewSyncedEnforcer(policy)
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{db: db, enforcer: enforcer, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			storageErr := errors.New("模拟存储读取失败")
			mock.ExpectBegin()
			if stage == "身份" {
				mock.ExpectQuery(`SELECT .* FROM "users"`).WillReturnError(storageErr)
			} else {
				mock.ExpectQuery(`SELECT .* FROM "users"`).WillReturnRows(sqlmock.NewRows([]string{"uuid", "department_id", "is_active", "auth_version"}).AddRow("member", database.DefaultDepartmentID, true, 7))
				if stage == "平台角色" {
					mock.ExpectQuery(`SELECT .* FROM "role_permissions"`).WillReturnError(storageErr)
				} else {
					mock.ExpectQuery(`SELECT .* FROM "role_permissions"`).WillReturnRows(sqlmock.NewRows([]string{"permission"}))
					mock.ExpectQuery(`SELECT .* FROM "user_permissions"`).WillReturnError(storageErr)
				}
			}
			mock.ExpectRollback()
			got, err := service.CurrentUserPermissions(context.Background(), &model.User{ID: "member", DepartmentID: database.DefaultDepartmentID, IsActive: true, AuthVersion: 7})
			if !errors.Is(err, storageErr) || len(got) != 0 {
				t.Fatalf("存储失败被误报为空权限: err=%v", err)
			}
			if len(recording.options) != 1 || recording.options[0] != (sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}) {
				t.Fatal("没有向真实事务入口传递一致只读快照选项")
			}
		})
	}
}

type currentPermissionBeginRecorder struct {
	*sql.DB
	options []sql.TxOptions
}

func (r *currentPermissionBeginRecorder) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	if options != nil {
		r.options = append(r.options, *options)
	}
	return r.DB.BeginTx(ctx, options)
}

func TestCurrentUserPermissionsDoesNotMixRoleAndDenySnapshots(t *testing.T) {
	f := newCurrentPermissionFixture(t)
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(2)
	other, err := NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetUserPermissions(context.Background(), f.user.ID, UserPermissionOverrides{Deny: []string{PermissionAuditRead}}); err != nil {
		t.Fatal(err)
	}
	// 旧 Casbin 仍允许，但当前数据库中“角色有权限 + 个人拒绝”始终不允许。
	cached, err := f.service.UserPermissions(context.Background(), f.user)
	if err != nil || !slices.Contains(cached, PermissionAuditRead) {
		t.Fatal("旧策略基线无效")
	}
	var triggered atomic.Bool
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	name := "current-profile-snapshot-" + uuid.NewString()
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != "role_permissions" || !triggered.CompareAndSwap(false, true) {
			return
		}
		go func() {
			// 正常服务事务原子移除角色及个人拒绝，新的已提交状态也不允许该权限。
			done <- other.SetUserAccess(ctx, f.user.ID, []string{}, UserPermissionOverrides{})
		}()
		select {
		case err := <-done:
			done <- err
			if err != nil {
				tx.AddError(err)
			}
		case <-ctx.Done():
			tx.AddError(ctx.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Query().Remove(name)
	platformContext := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: f.user.ID, DepartmentID: f.user.DepartmentID})
	got, readErr := f.service.CurrentUserPermissions(platformContext, f.user)
	if !triggered.Load() {
		t.Fatal("未实际交错权限读取与并发事务")
	}
	select {
	case writeErr := <-done:
		if writeErr != nil {
			t.Fatalf("并发正常权限事务失败: %v", writeErr)
		}
	case <-ctx.Done():
		t.Fatal("并发权限事务未结束")
	}
	if readErr != nil || slices.Contains(got, PermissionAuditRead) {
		t.Fatalf("拼接角色旧值与拒绝新值产生了从未允许的权限: err=%v", readErr)
	}
	latest, err := other.UserPermissions(platformContext, f.user)
	if err != nil || slices.Contains(latest, PermissionAuditRead) {
		t.Fatal("并发提交后的权限状态不正确")
	}
}

func TestCurrentUserPermissionsHandlesNoUserAndSuperuser(t *testing.T) {
	service := &Service{}
	if got, err := service.CurrentUserPermissions(context.Background(), nil); err != nil || got == nil || len(got) != 0 {
		t.Fatal("空用户应返回明确空权限")
	}
	f := newCurrentPermissionFixture(t)
	admin, err := account.NewService(f.db).CreateAdmin(context.Background(), "profile-admin", "管理员", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.service.CurrentUserPermissions(context.Background(), admin)
	if err != nil || !slices.Equal(got, []string{"*"}) {
		t.Fatalf("有效超级管理员恢复入口失效: err=%v", err)
	}
	department := model.Department{ID: uuid.NewString(), Name: "管理员跨部门恢复", IsActive: true}
	if err := f.db.Create(&department).Error; err != nil {
		t.Fatal(err)
	}
	project := model.Project{ID: uuid.NewString(), DepartmentID: department.ID, Name: "跨部门项目", IsActive: true}
	if err := f.db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	crossDepartment := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: admin.ID, DepartmentID: department.ID, ProjectID: project.ID, AllDepartments: true})
	got, err = f.service.CurrentUserPermissions(crossDepartment, admin)
	if err != nil || !slices.Equal(got, []string{"*"}) {
		t.Fatalf("管理员跨部门项目恢复入口失效: err=%v", err)
	}
	if err := f.db.Model(&model.Project{}).Where("uuid = ?", project.ID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := f.service.CurrentUserPermissions(crossDepartment, admin); !errors.Is(err, ErrAccessDelegationDenied) || len(got) != 0 {
		t.Fatalf("管理员身份不能让停用项目恢复业务访问: err=%v", err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if got, err := f.service.CurrentUserPermissions(ctx, f.user); !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatalf("取消未保留: err=%v", err)
	}
}

func TestCurrentUserPermissionsDoesNotReturnFailedCommitSnapshot(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	storageErr := errors.New("模拟只读快照提交失败")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "users"`).WillReturnRows(sqlmock.NewRows([]string{"uuid", "department_id", "is_active", "is_superuser", "auth_version"}).AddRow("admin", database.DefaultDepartmentID, true, true, 4))
	mock.ExpectCommit().WillReturnError(storageErr)
	service := &Service{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	got, err := service.CurrentUserPermissions(context.Background(), &model.User{ID: "admin", DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: true, AuthVersion: 4})
	if !errors.Is(err, storageErr) || len(got) != 0 {
		t.Fatalf("提交失败仍返回了超级管理员权限: err=%v", err)
	}
}

func TestCurrentUserPermissionsRejectsExistingOuterTransaction(t *testing.T) {
	f := newCurrentPermissionFixture(t)
	if err := f.db.WithContext(f.ctx).Transaction(func(tx *gorm.DB) error {
		nested := &Service{db: tx, enforcer: f.service.enforcer, logger: f.service.logger}
		got, err := nested.CurrentUserPermissions(f.ctx, f.user)
		if err == nil || len(got) != 0 {
			t.Fatal("SAVEPOINT 不能冒充已升级外层事务的隔离级别")
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted}); err != nil {
		t.Fatal(err)
	}
}
