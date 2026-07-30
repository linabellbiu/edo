package environment

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
)

func TestEnvironmentLifecycleAndHostReplacement(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	hostA := createEnvironmentTestHost(t, db, "host-a", "主机 A", "")
	hostB := createEnvironmentTestHost(t, db, "host-b", "主机 B", "")

	created, err := service.Create(context.Background(), "admin", Input{
		Name: " 测试环境 ", Description: " 测试服务 ",
		HostIDs: []string{hostB.ID, hostA.ID, hostA.ID},
	})
	if err != nil {
		t.Fatalf("创建环境失败: %v", err)
	}
	if created.Environment.Name != "测试环境" || created.Environment.Description != "测试服务" ||
		!created.Environment.IsActive || len(created.Hosts) != 2 ||
		created.Hosts[0].ID != hostA.ID || created.Hosts[1].ID != hostB.ID {
		t.Fatalf("创建后的环境详情错误: %+v", created)
	}
	if created.Environment.Level != "" {
		t.Fatalf("新环境不应写入安全级别: %q", created.Environment.Level)
	}
	if err := db.Model(&model.Environment{}).Where("id = ?", created.Environment.ID).
		Update("level", model.EnvironmentProduction).Error; err != nil {
		t.Fatalf("准备旧版安全级别失败: %v", err)
	}

	updated, err := service.Update(context.Background(), created.Environment.ID, Input{
		Name: "集成环境", HostIDs: []string{hostB.ID},
	})
	if err != nil {
		t.Fatalf("更新环境失败: %v", err)
	}
	if updated.Environment.Name != "集成环境" || updated.Environment.Level != model.EnvironmentProduction ||
		len(updated.Hosts) != 1 || updated.Hosts[0].ID != hostB.ID {
		t.Fatalf("更新环境时不应改写旧数据库兼容列: %+v", updated)
	}
	assertHostEnvironments(t, db, hostA.ID)
	assertHostEnvironments(t, db, hostB.ID, created.Environment.ID)

	if err := service.SetActive(context.Background(), created.Environment.ID, false); err != nil {
		t.Fatalf("停用环境失败: %v", err)
	}
	found, err := service.Get(context.Background(), created.Environment.ID)
	if err != nil || found.Environment.IsActive {
		t.Fatalf("读取停用环境失败: detail=%+v err=%v", found, err)
	}
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 || list[0].Environment.ID != created.Environment.ID {
		t.Fatalf("环境列表错误: list=%+v err=%v", list, err)
	}
}

func TestUpdateEnvironmentProfileKeepsHostMembership(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	host := createEnvironmentTestHost(t, db, "host-a", "主机 A", "")
	created, err := service.Create(context.Background(), "admin", Input{
		Name: "测试环境", Description: "原说明", HostIDs: []string{host.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := service.UpdateProfile(context.Background(), created.Environment.ID, ProfileInput{
		Name: " 集成环境 ", Description: " 新说明 ",
	})
	if err != nil {
		t.Fatalf("更新环境基本信息失败: %v", err)
	}
	if updated.Environment.Name != "集成环境" || updated.Environment.Description != "新说明" {
		t.Fatalf("环境基本信息未正确更新: %+v", updated.Environment)
	}
	if len(updated.Hosts) != 1 || updated.Hosts[0].ID != host.ID {
		t.Fatalf("更新基本信息不应改变主机归属: %+v", updated.Hosts)
	}
	assertHostEnvironments(t, db, host.ID, created.Environment.ID)
}

func TestReplaceEnvironmentHostsKeepsProfileAndRollsBack(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	hostA := createEnvironmentTestHost(t, db, "host-a", "主机 A", "")
	hostB := createEnvironmentTestHost(t, db, "host-b", "主机 B", "")
	created, err := service.Create(context.Background(), "admin", Input{
		Name: "测试环境", Description: "固定说明", HostIDs: []string{hostA.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := service.ReplaceHosts(context.Background(), created.Environment.ID, []string{hostB.ID, hostB.ID})
	if err != nil {
		t.Fatalf("调整环境主机失败: %v", err)
	}
	if updated.Environment.Name != "测试环境" || updated.Environment.Description != "固定说明" {
		t.Fatalf("调整主机不应改变环境基本信息: %+v", updated.Environment)
	}
	if len(updated.Hosts) != 1 || updated.Hosts[0].ID != hostB.ID {
		t.Fatalf("主机归属未正确替换: %+v", updated.Hosts)
	}
	assertHostEnvironments(t, db, hostA.ID)
	assertHostEnvironments(t, db, hostB.ID, created.Environment.ID)

	if _, err := service.ReplaceHosts(context.Background(), created.Environment.ID, []string{hostA.ID, "missing-host"}); !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("引用不存在主机时未返回稳定错误: %v", err)
	}
	unchanged, err := service.Get(context.Background(), created.Environment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Environment.Name != "测试环境" || unchanged.Environment.Description != "固定说明" ||
		len(unchanged.Hosts) != 1 || unchanged.Hosts[0].ID != hostB.ID {
		t.Fatalf("调整主机失败后事务未完整回滚: %+v", unchanged)
	}
	assertHostEnvironments(t, db, hostA.ID)
	assertHostEnvironments(t, db, hostB.ID, created.Environment.ID)
}

func TestActiveWorkflowPreventsEnvironmentDisableAndDelete(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	created, err := service.Create(context.Background(), "admin", Input{Name: "流水线引用环境"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "environment-reference-target", Name: "环境引用目标", Platform: model.DeploymentSSH,
		EnvironmentID: created.Environment.ID, HostID: "environment-reference-host", WorkingDirectory: "/srv/app",
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	plan := model.DeploymentPlan{
		ID: "environment-reference-plan", Name: "环境引用方案", Kind: model.DeploymentPlanScript,
		DeploymentTargetID: target.ID, Script: "echo deploy", TimeoutSeconds: 120,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	workflow := model.ReleaseWorkflow{
		ID: "environment-reference-workflow", ApplicationID: "environment-reference-app",
		Name: "环境引用流水线", Revision: 1, IsActive: true, SchemaVersion: model.WorkflowSchemaVersion,
		Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"},
		Stages: []model.WorkflowStage{{
			ID: "deploy-stage", Name: "部署",
			Tasks: []model.WorkflowNode{{
				ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "部署",
				Config: model.WorkflowNodeConfig{DeploymentPlanID: plan.ID},
			}},
		}},
		CreatedBy: "admin", UpdatedBy: "admin",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SetActive(context.Background(), created.Environment.ID, false); !errors.Is(err, ErrEnvironmentReferenced) {
		t.Fatalf("停用流水线正在使用的环境未被拒绝: %v", err)
	}
	if err := service.Remove(context.Background(), created.Environment.ID); !errors.Is(err, ErrEnvironmentReferenced) {
		t.Fatalf("删除流水线正在使用的环境未被拒绝: %v", err)
	}
	if err := db.Model(&workflow).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SetActive(context.Background(), created.Environment.ID, false); err != nil {
		t.Fatalf("流水线停用后仍不能停用环境: %v", err)
	}
}

func TestHostCanBelongToMultipleEnvironments(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	host := createEnvironmentTestHost(t, db, "host-a", "主机 A", "")
	first, err := service.Create(context.Background(), "admin", Input{
		Name: "开发环境", HostIDs: []string{host.ID},
	})
	if err != nil {
		t.Fatalf("创建开发环境失败: %v", err)
	}
	second, err := service.Create(context.Background(), "admin", Input{
		Name: "预发布环境", HostIDs: []string{host.ID},
	})
	if err != nil {
		t.Fatalf("创建预发布环境失败: %v", err)
	}
	assertHostEnvironments(t, db, host.ID, first.Environment.ID, second.Environment.ID)
	oldEnvironment, err := service.Get(context.Background(), first.Environment.ID)
	if err != nil || len(oldEnvironment.Hosts) != 1 || oldEnvironment.Hosts[0].ID != host.ID {
		t.Fatalf("主机加入其他环境后应继续保留原环境关系: detail=%+v err=%v", oldEnvironment, err)
	}

	cleared, err := service.Update(context.Background(), second.Environment.ID, Input{
		Name: second.Environment.Name, HostIDs: []string{},
	})
	if err != nil {
		t.Fatalf("清空环境主机失败: %v", err)
	}
	if len(cleared.Hosts) != 0 {
		t.Fatalf("清空后环境仍包含主机: %+v", cleared.Hosts)
	}
	assertHostEnvironments(t, db, host.ID, first.Environment.ID)
}

func TestReplaceHostsRejectsReferencedMembership(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	host := createEnvironmentTestHost(t, db, "host-a", "主机 A", "")
	created, err := service.Create(context.Background(), "admin", Input{
		Name: "部署环境", HostIDs: []string{host.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "ssh-target", Name: "命令部署", Platform: model.DeploymentSSH,
		EnvironmentID: created.Environment.ID, HostID: host.ID,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.ReplaceHosts(context.Background(), created.Environment.ID, nil); !errors.Is(err, ErrHostMembershipReferenced) {
		t.Fatalf("移除被部署配置引用的主机关联未返回稳定错误: %v", err)
	}
	assertHostEnvironments(t, db, host.ID, created.Environment.ID)
}

func TestReplaceHostsRejectsDockerWorkflowMembership(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	host := createEnvironmentTestHost(t, db, "docker-host", "Docker 主机", "")
	created, err := service.Create(context.Background(), "admin", Input{
		Name: "Docker 部署环境", HostIDs: []string{host.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "docker-target", Name: "Docker 部署", Platform: model.DeploymentDocker,
		EnvironmentID: created.Environment.ID, HostID: host.ID, RuntimeID: "docker-endpoint", WorkloadName: "api", RolloutTimeout: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	plan := model.DeploymentPlan{
		ID: "docker-plan", Name: "Docker 方案", Kind: model.DeploymentPlanDocker,
		DeploymentTargetID: target.ID, ServiceName: "api", TimeoutSeconds: 300,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	workflow := model.ReleaseWorkflow{
		ID: "docker-environment-workflow", ApplicationID: "docker-environment-app",
		Name: "Docker 环境流水线", Revision: 1, IsActive: true, SchemaVersion: model.WorkflowSchemaVersion,
		Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"},
		Stages: []model.WorkflowStage{{
			ID: "deploy-stage", Name: "部署",
			Tasks: []model.WorkflowNode{{
				ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "部署",
				Config: model.WorkflowNodeConfig{DeploymentPlanID: plan.ID},
			}},
		}},
		CreatedBy: "admin", UpdatedBy: "admin",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.ReplaceHosts(context.Background(), created.Environment.ID, nil); !errors.Is(err, ErrHostMembershipReferenced) {
		t.Fatalf("移除启用流水线使用的 Docker 主机关联未被拒绝: %v", err)
	}
	assertHostEnvironments(t, db, host.ID, created.Environment.ID)
}

func TestEnvironmentMutationRollsBackWhenHostIsMissing(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	host := createEnvironmentTestHost(t, db, "host-a", "主机 A", "")

	if _, err := service.Create(context.Background(), "admin", Input{
		Name: "无效环境", HostIDs: []string{"missing-host"},
	}); !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("引用不存在主机时未返回稳定错误: %v", err)
	}
	var environmentCount int64
	if err := db.Model(&model.Environment{}).Count(&environmentCount).Error; err != nil {
		t.Fatalf("统计环境失败: %v", err)
	}
	if environmentCount != 0 {
		t.Fatal("引用不存在主机时环境创建未回滚")
	}

	created, err := service.Create(context.Background(), "admin", Input{
		Name: "开发环境", Description: "原说明", HostIDs: []string{host.ID},
	})
	if err != nil {
		t.Fatalf("创建有效环境失败: %v", err)
	}
	if _, err := service.Update(context.Background(), created.Environment.ID, Input{
		Name: "已被错误更新", HostIDs: []string{"missing-host"},
	}); !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("更新引用不存在主机时未返回稳定错误: %v", err)
	}
	unchanged, err := service.Get(context.Background(), created.Environment.ID)
	if err != nil {
		t.Fatalf("读取回滚后的环境失败: %v", err)
	}
	if unchanged.Environment.Name != "开发环境" ||
		len(unchanged.Hosts) != 1 || unchanged.Hosts[0].ID != host.ID {
		t.Fatalf("失败更新未完整回滚: %+v", unchanged)
	}
}

func TestEnvironmentValidationAndStableErrors(t *testing.T) {
	service, _ := newEnvironmentTestService(t)
	if _, err := service.Create(context.Background(), "admin", Input{
		Name: "-无效名称",
	}); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("无效环境名称未返回稳定错误: %v", err)
	}
	created, err := service.Create(context.Background(), "admin", Input{
		Name: "生产环境",
	})
	if err != nil {
		t.Fatalf("创建生产环境失败: %v", err)
	}
	if _, err := service.Create(context.Background(), "admin", Input{
		Name: created.Environment.Name,
	}); !errors.Is(err, ErrEnvironmentExists) {
		t.Fatalf("重复环境名称未返回稳定错误: %v", err)
	}
	if _, err := service.Get(context.Background(), "missing"); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("查询不存在环境未返回稳定错误: %v", err)
	}
	if _, err := service.Update(context.Background(), "missing", Input{
		Name: "其他环境",
	}); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("更新不存在环境未返回稳定错误: %v", err)
	}
	if err := service.SetActive(context.Background(), "missing", false); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("修改不存在环境状态未返回稳定错误: %v", err)
	}
}

func TestEnvironmentRemoveUnassignsHosts(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	host := createEnvironmentTestHost(t, db, "host-a", "主机 A", "")
	created, err := service.Create(context.Background(), "admin", Input{
		Name: "待删除环境", HostIDs: []string{host.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(context.Background(), created.Environment.ID); err != nil {
		t.Fatalf("删除环境失败: %v", err)
	}
	assertHostEnvironments(t, db, host.ID)
	if _, err := service.Get(context.Background(), created.Environment.ID); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("删除后的环境仍可读取: %v", err)
	}
	if err := service.Remove(context.Background(), created.Environment.ID); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("重复删除环境未返回稳定错误: %v", err)
	}
}

func TestEnvironmentRemoveRejectsInactiveSSHDeploymentTargetReference(t *testing.T) {
	service, db := newEnvironmentTestService(t)
	host := createEnvironmentTestHost(t, db, "host-a", "主机 A", "")
	created, err := service.Create(context.Background(), "admin", Input{
		Name: "被引用环境", HostIDs: []string{host.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := model.DeploymentTarget{
		ID: "ssh-target", Name: "已停用 SSH 目标", Platform: model.DeploymentSSH,
		EnvironmentID: created.Environment.ID, HostID: host.ID, IsActive: false,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("创建 SSH 发布目标失败: %v", err)
	}

	if err := service.Remove(context.Background(), created.Environment.ID); !errors.Is(err, ErrEnvironmentReferenced) {
		t.Fatalf("删除被停用发布目标引用的环境未返回稳定错误: %v", err)
	}
	if _, err := service.Get(context.Background(), created.Environment.ID); err != nil {
		t.Fatalf("删除被拒绝后环境不应丢失: %v", err)
	}
	assertHostEnvironments(t, db, host.ID, created.Environment.ID)
}

func newEnvironmentTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver:          "sqlite",
		DSN:             "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开环境测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := db.AutoMigrate(
		&model.Environment{}, &model.Host{}, &model.EnvironmentHost{}, &model.DeploymentTarget{},
		&model.DeploymentPlan{}, &model.ReleaseWorkflow{}, &model.ReleaseWorkflowTemplate{},
	); err != nil {
		t.Fatalf("初始化环境测试表失败: %v", err)
	}
	return NewService(db), db
}

func createEnvironmentTestHost(t *testing.T, db *gorm.DB, id, name, environmentID string) model.Host {
	t.Helper()
	now := time.Now().UTC()
	host := model.Host{
		ID: id, Name: name, Mode: model.HostModeSSH, Address: "192.0.2.10", SSHPort: 22,
		SSHUsername: "deploy", SSHAuthType: model.SSHAuthPassword,
		SSHCredentialCiphertext: "encrypted",
		IsActive:                true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatalf("创建环境测试主机失败: %v", err)
	}
	if environmentID != "" {
		if err := db.Create(&model.EnvironmentHost{
			EnvironmentID: environmentID, HostID: host.ID, CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("创建环境主机关联失败: %v", err)
		}
	}
	return host
}

func assertHostEnvironments(t *testing.T, db *gorm.DB, hostID string, environmentIDs ...string) {
	t.Helper()
	var memberships []model.EnvironmentHost
	if err := db.Where("host_id = ?", hostID).Order("environment_id ASC").Find(&memberships).Error; err != nil {
		t.Fatalf("读取环境主机关联失败: %v", err)
	}
	slices.Sort(environmentIDs)
	if len(memberships) != len(environmentIDs) {
		t.Fatalf("主机环境归属数量错误: got=%+v want=%+v", memberships, environmentIDs)
	}
	for i := range memberships {
		if memberships[i].EnvironmentID != environmentIDs[i] {
			t.Fatalf("主机环境归属错误: got=%+v want=%+v", memberships, environmentIDs)
		}
	}
}
