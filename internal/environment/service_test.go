package environment

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
	assertHostEnvironment(t, db, hostA.ID, "")
	assertHostEnvironment(t, db, hostB.ID, created.Environment.ID)

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

func TestEnvironmentMovesAndClearsHostMembership(t *testing.T) {
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
	assertHostEnvironment(t, db, host.ID, second.Environment.ID)
	oldEnvironment, err := service.Get(context.Background(), first.Environment.ID)
	if err != nil || len(oldEnvironment.Hosts) != 0 {
		t.Fatalf("主机移动后旧环境仍包含该主机: detail=%+v err=%v", oldEnvironment, err)
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
	assertHostEnvironment(t, db, host.ID, "")
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
	assertHostEnvironment(t, db, host.ID, "")
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
	assertHostEnvironment(t, db, host.ID, created.Environment.ID)
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
	if err := db.AutoMigrate(&model.Environment{}, &model.Host{}, &model.DeploymentTarget{}); err != nil {
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
		SSHCredentialCiphertext: "encrypted", EnvironmentID: environmentID,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatalf("创建环境测试主机失败: %v", err)
	}
	return host
}

func assertHostEnvironment(t *testing.T, db *gorm.DB, hostID, environmentID string) {
	t.Helper()
	var host model.Host
	if err := db.First(&host, "id = ?", hostID).Error; err != nil {
		t.Fatalf("读取环境测试主机失败: %v", err)
	}
	if host.EnvironmentID != environmentID {
		t.Fatalf("主机环境归属错误: got=%q want=%q", host.EnvironmentID, environmentID)
	}
}
