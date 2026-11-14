package k3s

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

func seedUninstallConnection(t *testing.T, f fixture, source *Plan) model.KubernetesCluster {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	cluster := model.KubernetesCluster{ID: uuid.NewString(), Name: "待卸载连接", Mode: model.KubernetesKubeconfig, APIServer: "https://192.0.2.10:6443", KubeconfigCiphertext: "fixture-only", ProjectID: f.host.ProjectID, DepartmentID: f.host.DepartmentID, CreatedBy: f.actor.ID, CreatedAt: now, UpdatedAt: now}
	for _, value := range []any{&cluster,
		&model.HostCapability{HostID: f.host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: cluster.ID, CreatedAt: now, UpdatedAt: now},
		&model.HostCapability{HostID: f.host.ID, Kind: model.HostCapabilityDocker, RuntimeID: uuid.NewString(), CreatedAt: now, UpdatedAt: now},
		&model.KubernetesRBACPolicy{ID: uuid.NewString(), ClusterID: cluster.ID, ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID, UpdatedAt: now},
		&model.KubernetesRoleGrant{ID: uuid.NewString(), ClusterID: cluster.ID, ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID, RoleID: uuid.NewString(), Permissions: datatypes.JSON(`[]`), CreatedAt: now, UpdatedAt: now},
		&model.KubernetesRBACSubject{ID: uuid.NewString(), ClusterID: cluster.ID, ProjectID: cluster.ProjectID, DepartmentID: cluster.DepartmentID, UserID: f.actor.ID, UpdatedAt: now},
	} {
		if err := f.db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", source.ID).Update("cluster_id", cluster.ID).Error; err != nil {
		t.Fatal(err)
	}
	return cluster
}

func runUninstallTask(t *testing.T, f fixture, plan *Plan) *Installation {
	t.Helper()
	if _, err := f.service.ExecuteUninstall(f.ctx, f.actor.ID, plan.ID, UninstallConfirmationText); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatalf("claim=%+v err=%v", record, err)
	}
	f.service.runInstallation(f.ctx, *record)
	task, err := f.service.Get(f.ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func assertUninstallCount(t *testing.T, f fixture, value any, where string, arg any, want int64) {
	t.Helper()
	var count int64
	if err := f.db.Model(value).Where(where, arg).Count(&count).Error; err != nil || count != want {
		t.Fatalf("%T count=%d want=%d err=%v", value, count, want, err)
	}
}

func TestUninstallSuccessRemovesConnectionButPreservesHistoryHostAndOtherCluster(t *testing.T) {
	f, source, transport := uninstallFixture(t)
	cluster := seedUninstallConnection(t, f, source)
	other := cluster
	other.RowID, other.ID, other.Name = 0, uuid.NewString(), "保留的连接"
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	cache := &fakeClusters{}
	f.service.kubernetes = cache
	task := runUninstallTask(t, f, uninstallPlan(t, f, source))
	if task.Status != "succeeded" || task.Cluster != nil || task.ClusterID != cluster.ID || len(transport.runs) != 1 || !strings.Contains(task.Logs, "集群关联及集群连接") {
		t.Fatalf("unexpected result: %+v", task)
	}
	for _, value := range []any{&model.KubernetesCluster{}, &model.KubernetesRBACPolicy{}, &model.KubernetesRoleGrant{}, &model.KubernetesRBACSubject{}} {
		key := "cluster_id = ?"
		if _, ok := value.(*model.KubernetesCluster); ok {
			key = "uuid = ?"
		}
		assertUninstallCount(t, f, value, key, cluster.ID, 0)
	}
	assertUninstallCount(t, f, &model.HostCapability{}, "host_id = ?", f.host.ID, 2)
	assertUninstallCount(t, f, &model.Host{}, "uuid = ?", f.host.ID, 1)
	assertUninstallCount(t, f, &model.KubernetesCluster{}, "uuid = ?", other.ID, 1)
	assertUninstallCount(t, f, &model.K3sInstallation{}, "uuid = ?", source.ID, 1)
	assertUninstallCount(t, f, &model.AuditLog{}, "resource_id = ?", task.ID, 1)
	if len(cache.invalidated) != 1 || cache.invalidated[0] != cluster.ID {
		t.Fatal("deleted connection cache not invalidated")
	}
}

func TestUninstallMultipleHostsOnlyDetachesSelectedHost(t *testing.T) {
	f, source, _ := uninstallFixture(t)
	cluster := seedUninstallConnection(t, f, source)
	otherHost := uuid.NewString()
	if err := f.db.Create(&model.HostCapability{HostID: otherHost, Kind: model.HostCapabilityKubernetes, RuntimeID: cluster.ID}).Error; err != nil {
		t.Fatal(err)
	}
	task := runUninstallTask(t, f, uninstallPlan(t, f, source))
	if task.Status != "succeeded" {
		t.Fatalf("unexpected result: %+v", task)
	}
	assertUninstallCount(t, f, &model.KubernetesCluster{}, "uuid = ?", cluster.ID, 1)
	assertUninstallCount(t, f, &model.HostCapability{}, "runtime_id = ?", cluster.ID, 1)
	assertUninstallCount(t, f, &model.HostCapability{}, "host_id = ?", otherHost, 1)
	assertUninstallCount(t, f, &model.KubernetesRBACPolicy{}, "cluster_id = ?", cluster.ID, 1)
}

func TestUninstallFailureKeepsConnectionAndCleanupFailureRollsBack(t *testing.T) {
	for _, scenario := range []string{"official-script", "verification", "metadata"} {
		t.Run(scenario, func(t *testing.T) {
			f, source, transport := uninstallFixture(t)
			cluster := seedUninstallConnection(t, f, source)
			plan := uninstallPlan(t, f, source)
			switch scenario {
			case "official-script":
				transport.runErr = errors.New("private-script-error")
			case "verification":
				transport.verify = ""
			case "metadata":
				name := "test:uninstall_cleanup_failure"
				if err := f.db.Callback().Delete().Before("gorm:delete").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "kubernetes_clusters" {
						tx.AddError(errors.New("private-database-error"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.db.Callback().Delete().Remove(name) })
			}
			task := runUninstallTask(t, f, plan)
			if task.Status != "failed" || strings.Contains(task.Error+task.Logs, "private-") || len(transport.runs) != 1 {
				t.Fatalf("unexpected failure: %+v", task)
			}
			if scenario == "metadata" && (!strings.Contains(task.Error, "官方卸载已完成") || !strings.Contains(task.Error, "无需再次卸载")) {
				t.Fatal("metadata failure misrepresented as remote uninstall failure")
			}
			assertUninstallCount(t, f, &model.KubernetesCluster{}, "uuid = ?", cluster.ID, 1)
			assertUninstallCount(t, f, &model.HostCapability{}, "runtime_id = ?", cluster.ID, 1)
			for _, value := range []any{&model.KubernetesRBACSubject{}, &model.KubernetesRoleGrant{}, &model.KubernetesRBACPolicy{}} {
				assertUninstallCount(t, f, value, "cluster_id = ?", cluster.ID, 1)
			}
		})
	}
}

func completedLegacyUninstall(t *testing.T, f fixture, source *Plan) *Plan {
	t.Helper()
	plan := uninstallPlan(t, f, source)
	var record model.K3sInstallation
	if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.decode(record)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Uninstall.Connection = nil
	encoded, _ := json.Marshal(snapshot)
	ciphertext, err := f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+record.ID))
	if err != nil {
		t.Fatal(err)
	}
	hosts, _ := json.Marshal([]Host{{ID: f.host.ID, Role: "server", Status: "succeeded", Stage: "uninstall_verify"}})
	if err := f.db.Model(&record).Updates(map[string]any{"status": "succeeded", "finished_at": time.Now().UTC(), "hosts": datatypes.JSON(hosts), "input_ciphertext": ciphertext}).Error; err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestUninstallCompletedLegacyCleanupIsIdempotentAndNeverContactsHost(t *testing.T) {
	f, source, transport := uninstallFixture(t)
	cluster := seedUninstallConnection(t, f, source)
	plan := completedLegacyUninstall(t, f, source)
	reads := transport.reads
	for index := 0; index < 2; index++ {
		if err := f.service.CleanupCompletedUninstall(f.ctx, f.actor.ID, plan.ID); err != nil {
			t.Fatal(err)
		}
	}
	if transport.reads != reads || len(transport.runs) != 0 {
		t.Fatal("metadata cleanup contacted host")
	}
	assertUninstallCount(t, f, &model.KubernetesCluster{}, "uuid = ?", cluster.ID, 0)
	assertUninstallCount(t, f, &model.K3sInstallation{}, "uuid = ?", plan.ID, 1)
	assertUninstallCount(t, f, &model.K3sInstallation{}, "uuid = ?", source.ID, 1)
}

func TestUninstallCompletedCleanupRejectsChangedIdentityScopeAndPermission(t *testing.T) {
	for _, scenario := range []string{"connection", "host-association", "permission", "project", "cluster-scope", "busy-host", "not-succeeded"} {
		t.Run(scenario, func(t *testing.T) {
			f, source, transport := uninstallFixture(t)
			cluster := seedUninstallConnection(t, f, source)
			plan := completedLegacyUninstall(t, f, source)
			ctx := f.ctx
			switch scenario {
			case "connection":
				f.db.Model(&cluster).Update("api_server", "https://192.0.2.20:6443")
			case "host-association":
				f.db.Model(&model.HostCapability{}).Where("host_id = ? AND kind = ?", f.host.ID, model.HostCapabilityKubernetes).Update("runtime_id", uuid.NewString())
			case "permission":
				f.auth.denied = true
			case "project":
				ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: f.actor.ID, DepartmentID: f.host.DepartmentID, ProjectID: uuid.NewString()})
			case "cluster-scope":
				f.db.Model(&cluster).Update("project_id", uuid.NewString())
			case "busy-host":
				f.db.Create(&model.K3sInstallationHostLock{HostID: f.host.ID, InstallationID: uuid.NewString(), ProjectID: f.host.ProjectID, DepartmentID: f.host.DepartmentID, ExpiresAt: time.Now().Add(time.Hour)})
			case "not-succeeded":
				f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", plan.ID).Update("status", "failed")
			}
			if err := f.service.CleanupCompletedUninstall(ctx, f.actor.ID, plan.ID); err == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			assertUninstallCount(t, f, &model.KubernetesCluster{}, "uuid = ?", cluster.ID, 1)
			if len(transport.runs) != 0 {
				t.Fatal("cleanup ran uninstall")
			}
		})
	}
}

func TestUninstallConnectionChangeAfterConfirmationNeverRemovesNewConnection(t *testing.T) {
	f, source, transport := uninstallFixture(t)
	cluster := seedUninstallConnection(t, f, source)
	plan := uninstallPlan(t, f, source)
	transport.afterRun = func() {
		if err := f.db.Model(&cluster).Update("api_server", "https://192.0.2.30:6443").Error; err != nil {
			t.Fatal(err)
		}
	}
	task := runUninstallTask(t, f, plan)
	if task.Status != "failed" || len(transport.runs) != 1 {
		t.Fatalf("unexpected result: %+v", task)
	}
	assertUninstallCount(t, f, &model.KubernetesCluster{}, "uuid = ?", cluster.ID, 1)
	assertUninstallCount(t, f, &model.HostCapability{}, "runtime_id = ?", cluster.ID, 1)
}
