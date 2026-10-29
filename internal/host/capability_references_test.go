package host

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"edo/internal/dockerengine"
	"edo/internal/model"
)

func TestLocalExecRemovalIgnoresRetainedDockerTarget(t *testing.T) {
	for _, workload := range []string{"app", ""} {
		name := "Docker"
		if workload == "" {
			name = "Compose"
		}
		t.Run(name, func(t *testing.T) {
			service, db, _ := newHostTestService(t)
			ctx := context.Background()
			before, err := service.Update(ctx, model.BuiltinLocalHostID, Input{
				Name: "本地", CapabilityKinds: []model.HostCapabilityKind{model.HostCapabilityDocker, model.HostCapabilityLocalExec},
			})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			target := model.DeploymentTarget{
				ID: "retained-docker-target", Name: name + " 发布", Platform: model.DeploymentDocker,
				HostID: before.Host.ID, RuntimeID: dockerengine.LocalEndpointID, WorkloadName: workload,
				RolloutTimeout: 60, IsActive: true, CreatedBy: "operator", CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&target).Error; err != nil {
				t.Fatal(err)
			}
			updated, err := service.Update(ctx, before.Host.ID, Input{
				Name: before.Host.Name, CapabilityKinds: []model.HostCapabilityKind{model.HostCapabilityDocker},
			})
			if err != nil {
				t.Fatalf("保留 Docker 能力时，其发布目标不应阻止关闭本地执行: %v", err)
			}
			if len(updated.Capabilities) != 1 || updated.Capabilities[0].Kind != model.HostCapabilityDocker ||
				updated.Capabilities[0].RuntimeID != dockerengine.LocalEndpointID {
				t.Fatalf("关闭本地执行改变了 Docker 能力: %+v", updated.Capabilities)
			}
			var stored model.DeploymentTarget
			if err := db.First(&stored, "uuid = ?", target.ID).Error; err != nil || stored != target {
				t.Fatalf("能力变更不应修改发布目标: %+v err=%v", stored, err)
			}
		})
	}
}

func TestLocalCapabilityRemovalStillProtectsExecutionReferences(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform model.DeploymentPlatform
		keep     model.HostCapabilityKind
		active   bool
	}{
		{name: "本地脚本", platform: model.DeploymentSSH, keep: model.HostCapabilityDocker, active: true},
		{name: "停用本地脚本", platform: model.DeploymentSSH, keep: model.HostCapabilityDocker},
		{name: "Docker 运行时", platform: model.DeploymentDocker, keep: model.HostCapabilityLocalExec, active: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, db, _ := newHostTestService(t)
			ctx := context.Background()
			before, err := service.Update(ctx, model.BuiltinLocalHostID, Input{
				Name: "本地", CapabilityKinds: []model.HostCapabilityKind{model.HostCapabilityDocker, model.HostCapabilityLocalExec},
			})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			target := model.DeploymentTarget{
				ID: "referenced-target", Name: tc.name, Platform: tc.platform,
				HostID: before.Host.ID, WorkloadName: "app", RolloutTimeout: 60, IsActive: tc.active,
				CreatedBy: "operator", CreatedAt: now, UpdatedAt: now,
			}
			if tc.platform == model.DeploymentDocker {
				target.HostID, target.RuntimeID = "", dockerengine.LocalEndpointID
			}
			if err := db.Create(&target).Error; err != nil {
				t.Fatal(err)
			}
			// GORM 的布尔默认值会将 Create 中的 false 改为 true，明确保留停用场景。
			if !tc.active {
				if err := db.Model(&target).Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.Update(ctx, before.Host.ID, Input{
				Name: "不应保存的名称", CapabilityKinds: []model.HostCapabilityKind{tc.keep},
			}); !errors.Is(err, ErrHostReferenced) {
				t.Fatalf("真正被引用的能力不应被关闭: %v", err)
			}
			unchanged, err := service.Get(ctx, before.Host.ID)
			if err != nil {
				t.Fatal(err)
			}
			if unchanged.Host.Name != before.Host.Name || !slices.Equal(unchanged.Capabilities, before.Capabilities) {
				t.Fatalf("引用检查失败没有完整回滚主机与能力: %+v", unchanged)
			}
		})
	}
}

func TestSSHCapabilityRemovalIgnoresRetainedDockerTarget(t *testing.T) {
	service, db, _ := newHostTestService(t)
	ctx := context.Background()
	input := hostTestInput()
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilitySSH, model.HostCapabilityDocker}
	input.KubernetesClusterID = ""
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	before, err := service.Create(ctx, "operator", input)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeID string
	for _, capability := range before.Capabilities {
		if capability.Kind == model.HostCapabilityDocker {
			runtimeID = capability.RuntimeID
		}
	}
	if runtimeID == "" {
		t.Fatal("测试主机缺少 Docker 运行时")
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "remote-docker-target", Name: "远程 Docker 发布", Platform: model.DeploymentDocker,
		HostID: before.Host.ID, RuntimeID: runtimeID, WorkloadName: "app", RolloutTimeout: 60, IsActive: true,
		CreatedBy: "operator", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilityDocker}
	tested, err = service.TestExisting(ctx, before.Host.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	updated, err := service.Update(ctx, before.Host.ID, input)
	if err != nil {
		t.Fatalf("Docker 引用不应阻止移除独立 SSH 命令能力: %v", err)
	}
	if len(updated.Capabilities) != 1 || updated.Capabilities[0].Kind != model.HostCapabilityDocker || updated.Capabilities[0].RuntimeID != runtimeID {
		t.Fatalf("Docker 引用及能力被改变: %+v", updated.Capabilities)
	}
	if err := service.Remove(ctx, before.Host.ID); !errors.Is(err, ErrHostReferenced) {
		t.Fatalf("移除单项能力后不应放宽整台主机的引用保护: %v", err)
	}
}
