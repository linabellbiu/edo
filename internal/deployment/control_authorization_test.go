package deployment

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bsm/redislock"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

type runtimeAuthorizationFixture struct {
	service *Service
	db      *gorm.DB
	ctx     context.Context
	record  model.DeploymentRecord
	roleID  string
	runner  *hostScriptRunnerStub
	deletes atomic.Int32
}

func newRuntimeAuthorizationFixture(t *testing.T, platform model.DeploymentPlatform) *runtimeAuthorizationFixture {
	t.Helper()
	service, db, endpointID := newDeploymentTestService(t)
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	ctx, cancel := context.WithTimeout(database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: projectID, DepartmentID: database.DefaultDepartmentID, UserID: "operator"}), 10*time.Second)
	t.Cleanup(cancel)
	f := &runtimeAuthorizationFixture{service: service, db: db, ctx: ctx}
	f.roleID = configureDeploymentAuthorizationTestActor(t, db, projectID, database.DefaultDepartmentID, "operator", access.PermissionDeploymentExecute)
	now := time.Now().UTC()
	f.record = model.DeploymentRecord{ID: uuid.NewString(), ProjectID: projectID, DepartmentID: database.DefaultDepartmentID, PipelineRunID: uuid.NewString(), ApplicationID: uuid.NewString(), TargetID: uuid.NewString(), TargetName: "运行控制鉴权目标", Platform: platform, HostID: dockerTestHostID, EnvironmentID: dockerTestEnvironmentID, WorkingDirectory: "/test-only", RolloutTimeout: 30, CommandTimeout: 30, Operation: model.DeploymentRelease, DeploymentPlanID: uuid.NewString(), Status: model.DeploymentSucceeded, RequestedBy: "operator", CreatedAt: now, UpdatedAt: now, FinishedAt: &now}
	if platform == model.DeploymentDocker {
		f.record.DeploymentPlanKind, f.record.RuntimeID, f.record.WorkloadName = model.DeploymentPlanDocker, endpointID, "test-managed-container"
		fakeDocker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/_ping") {
				w.Header().Set("API-Version", "1.54")
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/test-managed-container") {
				f.deletes.Add(1)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"test fixture missing route"}`))
		}))
		t.Cleanup(fakeDocker.Close)
		if err := db.Model(&model.DockerEndpoint{}).Where("uuid = ?", endpointID).Update("host", fakeDocker.URL).Error; err != nil {
			t.Fatal(err)
		}
	} else {
		f.record.DeploymentPlanKind = model.DeploymentPlanScript
		f.runner = &hostScriptRunnerStub{result: sshdeploy.Result{ExitCode: 0, Started: true}}
		service.ssh = f.runner
	}
	if err := db.Create(&f.record).Error; err != nil {
		t.Fatal(err)
	}
	if platform == model.DeploymentSSH {
		if _, err := service.SaveRuntimeConfiguration(ctx, f.record.ID, "operator", InstanceControlConfigurationInput{RestartScript: "true", StopScript: "true", TimeoutSeconds: 30}); err != nil {
			t.Fatal(err)
		}
	}
	current, err := access.FreshProjectPermissions(db, ctx, "operator", projectID)
	if err != nil || !current.Permissions[access.PermissionDeploymentExecute] {
		t.Fatalf("运行控制基线授权无效: %v", err)
	}
	return f
}

// runtimeLockAttemptHook 在锁脚本已经尝试获取被占用的目标锁后通知测试，避免用延时猜测执行阶段。
type runtimeLockAttemptHook struct {
	attempted chan struct{}
	once      sync.Once
}

func (h *runtimeLockAttemptHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *runtimeLockAttemptHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *runtimeLockAttemptHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		err := next(ctx, command)
		if strings.HasPrefix(command.Name(), "eval") {
			h.once.Do(func() { close(h.attempted) })
		}
		return err
	}
}

func (f *runtimeAuthorizationFixture) startBlocked(t *testing.T, action string) (<-chan error, func()) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	f.service.locks, f.service.lockKeyPrefix = redislock.New(client), "edo:test:runtime-authorization"
	held, err := f.service.acquireTargetLock(f.ctx, &f.record)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { _ = held.Release(context.Background()) }) }
	t.Cleanup(release)
	hook := &runtimeLockAttemptHook{attempted: make(chan struct{})}
	client.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		if action == "remove" {
			done <- f.service.RemoveRuntime(f.ctx, f.record.ID, "operator")
			return
		}
		_, err := f.service.ControlRuntime(f.ctx, f.record.ID, "operator", RuntimeControlInput{Action: action})
		done <- err
	}()
	select {
	case <-hook.attempted:
	case <-f.ctx.Done():
		t.Fatal("运行控制未进入目标锁等待")
	}
	select {
	case err := <-done:
		t.Fatalf("目标锁尚被占用时操作已结束: %v", err)
	default:
	}
	return done, release
}

func (f *runtimeAuthorizationFixture) revoke(t *testing.T, mutation string) {
	t.Helper()
	var err error
	switch mutation {
	case "撤销角色动作":
		err = f.db.Where("role_id = ? AND permission = ?", f.roleID, access.PermissionDeploymentExecute).Delete(&model.RolePermission{}).Error
	case "个人显式拒绝":
		err = f.db.Create(&model.UserPermission{UserID: "operator", Permission: access.PermissionDeploymentExecute, Effect: model.PermissionDeny}).Error
	case "移出项目":
		err = f.db.Where("project_id = ? AND user_id = ?", f.record.ProjectID, "operator").Delete(&model.ProjectMember{}).Error
	case "停用账户":
		err = f.db.Model(&model.User{}).Where("uuid = ?", "operator").Update("is_active", false).Error
	case "停用项目":
		err = f.db.Model(&model.Project{}).Where("uuid = ?", f.record.ProjectID).Update("is_active", false).Error
	}
	if err != nil {
		t.Fatal(err)
	}
	current, err := access.FreshProjectPermissions(f.db, f.ctx, "operator", f.record.ProjectID)
	allowed := err == nil && current.Permissions[access.PermissionDeploymentExecute]
	if allowed != (mutation == "当前授权有效") {
		t.Fatalf("当前数据库授权与测试场景不符: allowed=%t err=%v", allowed, err)
	}
}

func TestRuntimeControlRejectsRevocationWhileWaitingForTargetLock(t *testing.T) {
	for _, action := range []string{"stop", "restart"} {
		for _, mutation := range []string{"当前授权有效", "撤销角色动作", "个人显式拒绝", "移出项目", "停用账户", "停用项目"} {
			t.Run(action+"/"+mutation, func(t *testing.T) {
				f := newRuntimeAuthorizationFixture(t, model.DeploymentSSH)
				done, release := f.startBlocked(t, action)
				f.revoke(t, mutation)
				release()
				var err error
				select {
				case err = <-done:
				case <-f.ctx.Done():
					t.Fatal("放锁后运行控制未结束")
				}
				called := f.runner.input.Script != ""
				if mutation == "当前授权有效" {
					if err != nil || !called {
						t.Fatalf("合法运行控制失败: called=%t err=%v", called, err)
					}
					return
				}
				if !errors.Is(err, ErrExecutionAuthorization) || called {
					t.Fatalf("锁等待期间撤权后仍执行运行命令: called=%t err=%v", called, err)
				}
				var stored model.DeploymentRecord
				if loadErr := f.db.First(&stored, "uuid = ?", f.record.ID).Error; loadErr != nil || stored.Status != model.DeploymentSucceeded {
					t.Fatalf("拒绝运行控制不应改写成功发布历史: status=%s err=%v", stored.Status, loadErr)
				}
			})
		}
	}
}

func TestRuntimeRemovalRejectsRevocationWhileWaitingForTargetLock(t *testing.T) {
	for _, mutation := range []string{"当前授权有效", "撤销角色动作", "个人显式拒绝", "移出项目", "停用账户", "停用项目"} {
		t.Run(mutation, func(t *testing.T) {
			f := newRuntimeAuthorizationFixture(t, model.DeploymentDocker)
			done, release := f.startBlocked(t, "remove")
			f.revoke(t, mutation)
			release()
			var err error
			select {
			case err = <-done:
			case <-f.ctx.Done():
				t.Fatal("放锁后删除实例未结束")
			}
			var stored model.DeploymentRecord
			if loadErr := f.db.First(&stored, "uuid = ?", f.record.ID).Error; loadErr != nil {
				t.Fatal(loadErr)
			}
			if mutation == "当前授权有效" {
				if err != nil || f.deletes.Load() != 1 || stored.RuntimeDeletedAt == nil {
					t.Fatalf("合法删除失败: deletes=%d removed=%t err=%v", f.deletes.Load(), stored.RuntimeDeletedAt != nil, err)
				}
				return
			}
			if !errors.Is(err, ErrExecutionAuthorization) || f.deletes.Load() != 0 || stored.RuntimeDeletedAt != nil {
				t.Fatalf("锁等待期间撤权后仍删除容器或修改历史: deletes=%d removed=%t err=%v", f.deletes.Load(), stored.RuntimeDeletedAt != nil, err)
			}
		})
	}
}

func TestRuntimeControlRequiresMatchingActorAndProjectScope(t *testing.T) {
	for _, mutation := range []string{"缺少范围", "缺少用户", "身份不一致", "项目不一致", "部门不一致", "合法超级管理员"} {
		t.Run(mutation, func(t *testing.T) {
			f := newRuntimeAuthorizationFixture(t, model.DeploymentSSH)
			scope, _ := database.DepartmentScopeFromContext(f.ctx)
			ctx, actorID := f.ctx, "operator"
			switch mutation {
			case "缺少范围":
				ctx = context.Background()
			case "缺少用户":
				scope.UserID = ""
				ctx = database.WithDepartmentScope(ctx, scope)
			case "身份不一致":
				scope.UserID = uuid.NewString()
				ctx = database.WithDepartmentScope(ctx, scope)
			case "项目不一致":
				scope.ProjectID = uuid.NewString()
				ctx = database.WithDepartmentScope(ctx, scope)
			case "部门不一致":
				scope.DepartmentID = uuid.NewString()
				ctx = database.WithDepartmentScope(ctx, scope)
			case "合法超级管理员":
				actorID = uuid.NewString()
				if err := f.db.Create(&model.User{ID: actorID, Username: "root-" + actorID[:8], DepartmentID: scope.DepartmentID, IsActive: true, IsSuperuser: true}).Error; err != nil {
					t.Fatal(err)
				}
				scope.UserID, scope.AllDepartments = actorID, true
				ctx = database.WithDepartmentScope(ctx, scope)
			}
			_, err := f.service.ControlRuntime(ctx, f.record.ID, actorID, RuntimeControlInput{Action: "stop"})
			called := f.runner.input.Script != ""
			if mutation == "合法超级管理员" {
				if err != nil || !called {
					t.Fatalf("合法管理员无法执行运行控制: called=%t err=%v", called, err)
				}
				return
			}
			if err == nil || called {
				t.Fatalf("缺失或不一致的调用身份仍可执行: called=%t err=%v", called, err)
			}
		})
	}
}
