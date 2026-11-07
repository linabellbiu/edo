package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/bsm/redislock"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type containerControlFixture struct {
	*runtimeAuthorizationFixture
	writes                 atomic.Int32
	states                 map[string]string
	generations            map[string]int
	configurationRevisions map[string]string
	mu                     sync.Mutex
}

func newContainerControlFixture(t *testing.T) *containerControlFixture {
	t.Helper()
	f := &containerControlFixture{runtimeAuthorizationFixture: newRuntimeAuthorizationFixture(t, model.DeploymentDocker), states: map[string]string{strings.Repeat("a", 64): "running", strings.Repeat("b", 64): "running", strings.Repeat("c", 64): "running", strings.Repeat("d", 64): "exited"}}
	endpoint := f.record.RuntimeID
	f.generations = map[string]int{}
	f.configurationRevisions = map[string]string{}
	f.record.Platform = model.DeploymentSSH
	f.record.DeploymentPlanKind = model.DeploymentPlanComposeStack
	f.record.RuntimeID = ""
	content := "services:\n  api:\n    image: alpine:3.22\n    depends_on:\n      setup:\n        condition: service_completed_successfully\n  worker:\n    image: alpine:3.22\n  setup:\n    image: alpine:3.22\n"
	frozen := frozenComposeRecord(content)
	f.record.CommandScript = frozen.CommandScript
	f.record.CommandDigest = frozen.CommandDigest
	f.record.CommandTimeout = frozen.CommandTimeout
	if err := f.db.Save(&f.record).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]any{"schema_version": model.WorkflowSchemaVersion, "stages": []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: f.record.DeploymentPlanID, Compose: &model.ComposeDeploymentSettings{ProjectName: "current-project"}}}}}}, "deployment_plans": map[string]any{"deploy": map[string]any{"id": f.record.DeploymentPlanID, "kind": "compose_stack", "compose_yaml": content}}, "deployment_targets": map[string]any{"deploy": map[string]any{"id": f.record.TargetID, "project_id": f.record.ProjectID, "department_id": f.record.DepartmentID, "platform": "ssh", "host_id": f.record.HostID, "environment_id": f.record.EnvironmentID, "working_directory": f.record.WorkingDirectory}}}
	f.record.WorkflowNodeID = "deploy"
	if err := f.db.Save(&f.record).Error; err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snapshot)
	repository := model.GitRepository{ID: uuid.NewString(), Name: "容器操作测试仓库", CloneURL: "https://example.test/control.git", IsActive: true}
	if err := f.db.Create(&repository).Error; err != nil {
		t.Fatal("创建测试仓库", err)
	}
	if err := f.db.Create(&model.Application{ID: f.record.ApplicationID, RepositoryID: repository.ID, Name: "container_control", IsActive: true}).Error; err != nil {
		t.Fatal("创建测试应用", err)
	}
	if err := f.db.Create(&model.PipelineRun{ID: f.record.PipelineRunID, ApplicationID: f.record.ApplicationID, ProjectID: f.record.ProjectID, DepartmentID: f.record.DepartmentID, WorkflowSnapshot: string(encoded)}).Error; err != nil {
		t.Fatal(err)
	}
	configureDeploymentAuthorizationTestActor(t, f.db, f.record.ProjectID, f.record.DepartmentID, "operator", access.PermissionDeploymentRead)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		label := func(id string) map[string]string {
			service := "api"
			if id[0] == 'c' {
				service = "worker"
			}
			if id[0] == 'd' {
				service = "setup"
			}
			return map[string]string{dockerengine.ConfigurationRevisionLabel: f.configurationRevisions[id], "com.docker.compose.project": "current-project", "com.docker.compose.service": service, "com.docker.compose.project.working_dir": f.record.WorkingDirectory}
		}
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.54")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			items := []any{}
			for id := range f.states {
				items = append(items, map[string]any{"Id": id, "Labels": label(id)})
			}
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		for id, state := range f.states {
			if strings.HasSuffix(r.URL.Path, "/containers/"+id+"/json") {
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Name": "/container-" + id[:1], "Image": "sha256:" + strings.Repeat("f", 64), "Config": map[string]any{"Labels": label(id), "Image": "alpine:3.22"}, "State": map[string]any{"Status": state, "Running": state == "running", "Health": map[string]any{"Status": "healthy"}, "ExitCode": 0, "StartedAt": fmt.Sprintf("2026-11-06T00:00:%02dZ", f.generations[id])}})
				return
			}
			if r.Method == "POST" && strings.Contains(r.URL.Path, "/containers/"+id+"/") {
				f.writes.Add(1)
				f.generations[id]++
				if strings.HasSuffix(r.URL.Path, "/stop") {
					f.states[id] = "exited"
				} else {
					f.states[id] = "running"
				}
				w.WriteHeader(204)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	if err := f.db.Model(&model.DockerEndpoint{}).Where("uuid = ?", endpoint).Update("host", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func TestContainerControlSeparatePermissionsAndConfirmation(t *testing.T) {
	f := newContainerControlFixture(t)
	id := strings.Repeat("a", 64)
	if _, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, id, "operator", "stop"); !errors.Is(err, ErrExecutionAuthorization) {
		t.Fatal("普通部署执行权限隐式获得单容器停止", err)
	}
	configureDeploymentAuthorizationTestActor(t, f.db, f.record.ProjectID, f.record.DepartmentID, "operator", access.PermissionContainerStop)
	if _, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, id, "operator", "restart"); !errors.Is(err, ErrExecutionAuthorization) {
		t.Fatal("停止权限获得重启权限", err)
	}
	plan, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, id, "operator", "stop")
	if err != nil || len(plan.Containers) != 1 {
		t.Fatal("停止确认范围错误", plan, err)
	}
	result, err := f.service.ControlContainer(f.ctx, f.record.ID, id, "operator", "stop", ContainerControlConfirmation{Digest: plan.Digest})
	if err != nil || result.Runtime.Kind != "compose" || f.writes.Load() != 1 {
		t.Fatal("准确容器停止失败", err)
	}
	f.mu.Lock()
	other := f.states[strings.Repeat("b", 64)]
	f.mu.Unlock()
	if other != "running" {
		t.Fatal("停止影响其他副本")
	}
	if _, err = f.service.ControlContainer(f.ctx, f.record.ID, id, "operator", "stop", ContainerControlConfirmation{Digest: plan.Digest}); !errors.Is(err, dockerengine.ErrContainerControlChanged) || f.writes.Load() != 1 {
		t.Fatal("陈旧确认被复用", err)
	}
	configureDeploymentAuthorizationTestActor(t, f.db, f.record.ProjectID, f.record.DepartmentID, "operator", access.PermissionContainerRecreate)
	recreate, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, id, "operator", "recreate")
	if err != nil || len(recreate.Containers) != 2 || recreate.Service != "api" {
		t.Fatal("重建未准确列出全部副本", recreate, err)
	}
	if _, err = f.service.ContainerControlPreview(f.ctx, f.record.ID, strings.Repeat("e", 64), "operator", "stop"); !errors.Is(err, dockerengine.ErrContainerControlChanged) {
		t.Fatal("接受了范围外容器", err)
	}
	otherProject := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "other-project", DepartmentID: f.record.DepartmentID, UserID: "operator"})
	if _, err = f.service.ContainerControlPreview(otherProject, f.record.ID, id, "operator", "stop"); err == nil {
		t.Fatal("跨项目读取容器确认")
	}
}

func TestContainerControlRevocationAfterTargetLockWait(t *testing.T) {
	f := newContainerControlFixture(t)
	id := strings.Repeat("a", 64)
	configureDeploymentAuthorizationTestActor(t, f.db, f.record.ProjectID, f.record.DepartmentID, "operator", access.PermissionContainerStop)
	plan, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, id, "operator", "stop")
	if err != nil {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	f.service.locks, f.service.lockKeyPrefix = redislock.New(client), "edo:test:container-control"
	held, err := f.service.acquireTargetLock(f.ctx, &f.record)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release(context.Background())
	hook := &runtimeLockAttemptHook{attempted: make(chan struct{})}
	client.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		_, err := f.service.ControlContainer(f.ctx, f.record.ID, id, "operator", "stop", ContainerControlConfirmation{Digest: plan.Digest})
		done <- err
	}()
	select {
	case <-hook.attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("操作未进入锁等待")
	}
	if err = f.db.Create(&model.UserPermission{UserID: "operator", Permission: access.PermissionContainerStop, Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	_ = held.Release(context.Background())
	select {
	case err = <-done:
		if !errors.Is(err, ErrExecutionAuthorization) || f.writes.Load() != 0 {
			t.Fatal("等锁后撤权仍产生副作用", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("撤权操作未退出")
	}
}
