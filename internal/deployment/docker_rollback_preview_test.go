package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"

	"gorm.io/gorm"
)

type rollbackDockerDaemon struct {
	mu                                           sync.Mutex
	containerID, imageID, deploymentID, targetID string
	exists                                       bool
	mutateAt, reads                              int
	writes                                       atomic.Int32
}

func completeDockerRollbackSource(t *testing.T, service *Service, db *gorm.DB, endpointID string, source *model.DeploymentRecord) *rollbackDockerDaemon {
	t.Helper()
	if source.ApplicationID == "" {
		source.ApplicationID = source.ID + "-app"
	}
	if source.PipelineRunID == "" {
		source.PipelineRunID = source.ID + "-run"
	}
	if source.ExpectedImageID == "" {
		source.ExpectedImageID = "sha256:" + strings.Repeat("f", 64)
	}
	source.HostID, source.EnvironmentID = dockerTestHostID, dockerTestEnvironmentID
	for _, item := range []any{
		&model.GitRepository{ID: source.ID + "-repo", Name: "回退测试仓库", IsActive: true},
		&model.Application{ID: source.ApplicationID, Name: "rollback_test", RepositoryID: source.ID + "-repo", IsActive: true},
		&model.PipelineRun{ID: source.PipelineRunID, ApplicationID: source.ApplicationID, Status: model.PipelineRunSucceeded, CreatedBy: "operator"},
		&model.DeploymentTarget{ID: source.TargetID, Name: source.TargetName, Platform: model.DeploymentDocker, RuntimeID: endpointID, HostID: source.HostID, EnvironmentID: source.EnvironmentID, WorkloadName: source.WorkloadName, IsActive: true},
		&model.DeploymentPlan{ID: source.DeploymentPlanID, Name: "回退测试方案", Kind: model.DeploymentPlanDocker, DeploymentTargetID: source.TargetID, IsActive: true},
	} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Save(source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(source, "uuid = ?", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := *source
	d := &rollbackDockerDaemon{containerID: strings.Repeat("d", 64), imageID: source.ExpectedImageID, deploymentID: source.ID, targetID: source.TargetID, exists: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		w.Header().Set("API-Version", "1.53")
		w.Header().Set("OSType", "linux")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			d.writes.Add(1)
			w.WriteHeader(500)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			_, _ = w.Write([]byte("OK"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/containers/") {
			d.reads++
			if d.mutateAt > 0 && d.reads >= d.mutateAt {
				d.containerID = strings.Repeat("e", 64)
			}
			if !d.exists {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"message":"fixture absent"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": d.containerID, "Image": d.imageID, "Name": "/" + snapshot.WorkloadName, "Config": map[string]any{"Image": snapshot.Image, "Labels": map[string]string{"edo.managed": "true", "edo.deployment.id": d.deploymentID, "edo.deployment.target.id": d.targetID}}, "HostConfig": map[string]any{}, "State": map[string]any{"Running": true}})
			return
		}
		if strings.Contains(r.URL.Path, "/images/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": snapshot.PreviousImageID})
			return
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"fixture absent"}`))
	}))
	t.Cleanup(server.Close)
	if err := db.Model(&model.DockerEndpoint{}).Where("uuid = ?", endpointID).Update("host", "tcp://"+strings.TrimPrefix(server.URL, "http://")).Error; err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDockerRollbackCheckedAndExecutionRejectDrift(t *testing.T) {
	for _, name := range []string{"确认过期", "确认镜像变化", "请求时镜像变化", "未确认接口仍复验", "排队后来源变化", "排队后归属变化", "排队后镜像变化", "排队后新发布", "执行入口实例变化", "停容器前实例变化"} {
		t.Run(name, func(t *testing.T) {
			service, db, endpointID := newDeploymentTestService(t)
			now := time.Now().UTC()
			source := model.DeploymentRecord{ID: "rollback-source", TargetID: "rollback-target", TargetName: "测试目标", Platform: model.DeploymentDocker, RuntimeID: endpointID, WorkloadName: "rollback-app", RolloutTimeout: 120, Operation: model.DeploymentRelease, Image: "edo.local/rollback:v2", PreviousImage: "edo.local/rollback:v1", PreviousImageID: "sha256:" + strings.Repeat("a", 64), DeploymentPlanID: "rollback-plan", DeploymentPlanKind: model.DeploymentPlanDocker, DockerConfig: model.DockerContainerConfig{Network: "bridge", RestartPolicy: "unless-stopped", EnvironmentVariables: map[string]string{}}, Status: model.DeploymentSucceeded, CreatedAt: now, UpdatedAt: now}
			source.DockerConfigDigest = model.DockerContainerConfigDigest(source.DockerConfig)
			if err := db.Create(&source).Error; err != nil {
				t.Fatal(err)
			}
			d := completeDockerRollbackSource(t, service, db, endpointID, &source)
			ctx := deploymentExecutionTestContext()
			confirmation := RollbackConfirmation{ExpectedCurrentImage: source.Image, ExpectedTargetImage: source.PreviousImage, ExpectedSourceUpdatedAt: source.UpdatedAt.Format(time.RFC3339Nano)}
			switch name {
			case "确认过期":
				confirmation.ExpectedSourceUpdatedAt = "stale"
			case "确认镜像变化":
				confirmation.ExpectedCurrentImage = "different"
			case "请求时镜像变化", "未确认接口仍复验":
				d.mu.Lock()
				d.imageID = "sha256:" + strings.Repeat("c", 64)
				d.mu.Unlock()
			}
			var record *model.DeploymentRecord
			var err error
			if name == "未确认接口仍复验" {
				record, err = service.Rollback(ctx, source.ID, "operator")
			} else {
				record, err = service.RollbackChecked(ctx, source.ID, "operator", confirmation)
			}
			if strings.HasPrefix(name, "确认") || name == "请求时镜像变化" || name == "未确认接口仍复验" {
				if !errors.Is(err, ErrRollbackTargetChanged) || record != nil || d.writes.Load() != 0 {
					t.Fatalf("不安全请求未被拒绝: record=%+v err=%v writes=%d", record, err, d.writes.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "排队后来源变化":
				err = db.Model(&source).Update("updated_at", time.Now().UTC().Add(time.Second)).Error
			case "排队后归属变化":
				d.mu.Lock()
				d.deploymentID = "another-deployment"
				d.mu.Unlock()
			case "排队后镜像变化":
				d.mu.Lock()
				d.imageID = "sha256:" + strings.Repeat("c", 64)
				d.mu.Unlock()
			case "排队后新发布":
				newer := source
				newer.RowID = 0
				newer.ID = "newer-release"
				err = db.Create(&newer).Error
			case "执行入口实例变化", "停容器前实例变化":
				d.mu.Lock()
				d.mutateAt = d.reads + 3
				if name == "停容器前实例变化" {
					d.mutateAt++
				}
				d.mu.Unlock()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = service.Run(ctx, record.ID); err == nil {
				t.Fatal("漂移后的回退仍执行成功")
			}
			var stored model.DeploymentRecord
			if err := db.First(&stored, "uuid = ?", record.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != model.DeploymentFailed || d.writes.Load() != 0 {
				t.Fatalf("漂移后未失败或触发Docker写操作: status=%s writes=%d", stored.Status, d.writes.Load())
			}
		})
	}
}

func TestDockerRollbackPreviewUsesCurrentOwnedImmutableImage(t *testing.T) {
	for _, name := range []string{"可回退", "自动容器名", "其他项目", "同名外来容器", "镜像变化", "上一镜像缺失", "实例已移除", "历史记录", "方案已换目标"} {
		t.Run(name, func(t *testing.T) {
			service, db, endpointID := newDeploymentTestService(t)
			projectID := database.DefaultProjectID(database.DefaultDepartmentID)
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "operator", DepartmentID: database.DefaultDepartmentID, ProjectID: projectID})
			now := time.Now().UTC()
			target := model.DeploymentTarget{ID: "docker-preview-target", Name: "本轮目标", Platform: model.DeploymentDocker, RuntimeID: endpointID, HostID: dockerTestHostID, EnvironmentID: dockerTestEnvironmentID, WorkloadName: "docker-preview-app", IsActive: true, CreatedAt: now, UpdatedAt: now}
			plan := model.DeploymentPlan{ID: "docker-preview-plan", Name: "本轮方案", Kind: model.DeploymentPlanDocker, DeploymentTargetID: target.ID, IsActive: true, CreatedAt: now, UpdatedAt: now}
			for _, item := range []any{
				&model.GitRepository{ID: "docker-preview-repo", Name: "本轮仓库", IsActive: true},
				&model.Application{ID: "docker-preview-application", Name: "docker_preview", RepositoryID: "docker-preview-repo", IsActive: true},
				&model.PipelineRun{ID: "docker-preview-run", ApplicationID: "docker-preview-application", Status: model.PipelineRunSucceeded, CreatedBy: "operator"},
				&target, &plan,
			} {
				if err := db.WithContext(ctx).Create(item).Error; err != nil {
					t.Fatal(err)
				}
			}
			currentID, previousID := "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("a", 64)
			source := model.DeploymentRecord{ID: "docker-preview-source", ApplicationID: "docker-preview-application", PipelineRunID: "docker-preview-run", DeploymentPlanID: plan.ID, DeploymentPlanKind: model.DeploymentPlanDocker, Image: "edo.local/preview:v2", ExpectedImageID: currentID, PreviousImage: "edo.local/preview:v1", PreviousImageID: previousID, Status: model.DeploymentSucceeded, CreatedAt: now, UpdatedAt: now}
			source.TargetID, source.TargetName, source.Platform = target.ID, target.Name, target.Platform
			source.RuntimeID, source.HostID, source.EnvironmentID, source.WorkloadName = target.RuntimeID, target.HostID, target.EnvironmentID, target.WorkloadName
			if err := db.WithContext(ctx).Create(&source).Error; err != nil {
				t.Fatal(err)
			}
			var lock sync.Mutex
			calls := 0
			managed, imageID, imageAvailable := true, currentID, true
			switch name {
			case "自动容器名":
				if err := db.WithContext(ctx).Model(&target).Update("workload_name", "").Error; err != nil {
					t.Fatal(err)
				}
			case "其他项目":
				ctx = database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "operator", DepartmentID: database.DefaultDepartmentID, ProjectID: "foreign-project"})
			case "同名外来容器":
				managed = false
			case "镜像变化":
				imageID = "sha256:" + strings.Repeat("c", 64)
			case "上一镜像缺失":
				imageAvailable = false
			case "实例已移除":
				if err := db.WithContext(ctx).Model(&source).Update("runtime_deleted_at", now).Error; err != nil {
					t.Fatal(err)
				}
			case "历史记录":
				newer := source
				newer.RowID = 0
				newer.ID = "docker-preview-later"
				newer.UpdatedAt = now.Add(time.Second)
				if err := db.WithContext(ctx).Create(&newer).Error; err != nil {
					t.Fatal(err)
				}
			case "方案已换目标":
				if err := db.WithContext(ctx).Model(&target).Update("workload_name", "different-app").Error; err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				lock.Lock()
				defer lock.Unlock()
				calls++
				if request.Method != http.MethodGet && request.Method != http.MethodHead {
					t.Errorf("预览触发了写操作: %s", request.Method)
					w.WriteHeader(405)
					return
				}
				w.Header().Set("API-Version", "1.53")
				w.Header().Set("OSType", "linux")
				if strings.HasSuffix(request.URL.Path, "/_ping") {
					_, _ = w.Write([]byte("OK"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(request.URL.Path, "/containers/") {
					labels := map[string]string{"edo.deployment.id": source.ID, "edo.deployment.target.id": target.ID}
					if managed {
						labels["edo.managed"] = "true"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": strings.Repeat("d", 64), "Image": imageID, "Config": map[string]any{"Labels": labels}, "State": map[string]any{"Running": true}})
					return
				}
				if strings.Contains(request.URL.Path, "/images/") && imageAvailable {
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": previousID})
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"fixture absent"}`))
			}))
			t.Cleanup(server.Close)
			if err := db.WithContext(database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: projectID})).Model(&model.DockerEndpoint{}).Where("uuid = ?", endpointID).Update("host", "tcp://"+strings.TrimPrefix(server.URL, "http://")).Error; err != nil {
				t.Fatal(err)
			}
			preview, err := service.RollbackPreview(ctx, source.ID)
			lock.Lock()
			observedCalls := calls
			lock.Unlock()
			if name == "其他项目" {
				if !errors.Is(err, ErrDeploymentNotFound) || observedCalls != 0 {
					t.Fatalf("跨项目预览没有在Docker之前拒绝: err=%v calls=%d", err, observedCalls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "可回退" || name == "自动容器名" {
				if !preview.CanRollback || preview.Platform != model.DeploymentDocker || preview.CurrentImageID != currentID || preview.TargetImageID != previousID || observedCalls == 0 {
					t.Fatalf("真实可回退Docker未正确预览: %+v calls=%d", preview, observedCalls)
				}
				confirmation := RollbackConfirmation{ExpectedCurrentImage: preview.CurrentImage, ExpectedTargetImage: preview.TargetImage, ExpectedSourceUpdatedAt: "stale"}
				if _, err := service.RollbackChecked(deploymentExecutionTestContext(), source.ID, "operator", confirmation); !errors.Is(err, ErrRollbackTargetChanged) {
					t.Fatalf("Docker 回退忽略了预检确认快照: %v", err)
				}
			} else if preview.CanRollback || preview.Reason == "" {
				t.Fatalf("不安全来源被显示为可回退: %+v", preview)
			}
		})
	}
}
