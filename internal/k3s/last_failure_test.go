package k3s

import (
	"testing"
	"time"

	"edo/internal/model"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

func TestLastFailureFiltersBeforeLimitAndKeepsProjectActorAndActionScope(t *testing.T) {
	f := newFixture(t)
	seed := func(status, action, actor, project, logs string) model.K3sInstallation {
		t.Helper()
		now := time.Now().UTC()
		value := model.K3sInstallation{ID: uuid.NewString(), ProjectID: project, DepartmentID: f.host.DepartmentID, CreatedBy: actor, Action: action, Name: "日志测试", Status: status, InputCiphertext: "fixture-only", Preview: datatypes.JSON(`{}`), Hosts: datatypes.JSON(`[]`), Logs: logs, ExpiresAt: now, CreatedAt: now, UpdatedAt: now}
		if err := f.db.Create(&value).Error; err != nil {
			t.Fatal(err)
		}
		return value
	}
	seed("failed", "create", f.actor.ID, f.host.ProjectID, "较早的失败")
	latest := seed("failed", "create", f.actor.ID, f.host.ProjectID, "上一次安装失败日志")
	if err := f.db.Model(&latest).Update("created_at", time.Now().Add(-24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		seed("succeeded", "create", f.actor.ID, f.host.ProjectID, "不能代替失败日志")
	}
	seed("failed", "join", f.actor.ID, f.host.ProjectID, "其他动作")
	seed("failed", "create", uuid.NewString(), f.host.ProjectID, "其他用户")
	seed("failed", "create", f.actor.ID, uuid.NewString(), "其他项目")
	items, err := f.service.List(f.ctx, "", 1, "create", "failed")
	if err != nil || len(items) != 1 || items[0].ID != latest.ID || items[0].Logs != latest.Logs {
		t.Fatalf("最后失败记录不正确：%+v %v", items, err)
	}
	items, err = f.service.List(f.ctx, "missing-cluster", 1, "join", "failed")
	if err != nil || len(items) != 0 {
		t.Fatalf("目标集群筛选失效：%+v %v", items, err)
	}
}

func TestLastFailureDoesNotExposeManagementLinkForRemovedCluster(t *testing.T) {
	f, source, _ := uninstallFixture(t)
	cluster := seedUninstallConnection(t, f, source)
	record, err := f.service.Get(f.ctx, source.ID)
	if err != nil || record.Cluster == nil || record.Cluster.ID != cluster.ID {
		t.Fatalf("存在的连接未返回：%+v %v", record, err)
	}
	if err := f.db.Where("uuid = ?", cluster.ID).Delete(&model.KubernetesCluster{}).Error; err != nil {
		t.Fatal(err)
	}
	record, err = f.service.Get(f.ctx, source.ID)
	if err != nil || record.Cluster != nil || record.ClusterID != cluster.ID || record.Status != "failed" {
		t.Fatalf("已删除连接仍提供管理入口或历史被改写：%+v %v", record, err)
	}
}
