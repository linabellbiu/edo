package k3s

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"edo/internal/database"
	"edo/internal/model"
)

func manualGuideFixture(t *testing.T) (fixture, model.KubernetesCluster, model.K3sInstallation, savedInput) {
	t.Helper()
	f := newFixture(t)
	scope, _ := database.DepartmentScopeFromContext(f.ctx)
	cluster := model.KubernetesCluster{ID: uuid.NewString(), Name: "显示名称与 k3d 名称不同", Mode: model.KubernetesKubeconfig, APIServer: "https://192.0.2.10:16443", DefaultNamespace: "default", DepartmentID: scope.DepartmentID, ProjectID: scope.ProjectID, CreatedBy: f.actor.ID}
	if err := f.db.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	input := f.input()
	input.Mode, input.K3d = "k3d", &K3dOptions{}
	hosts, _ := json.Marshal([]Host{{ID: f.host.ID, Name: f.host.Name, Address: f.host.Address, Role: "host"}})
	snapshot := savedInput{Input: input, K3dCleanup: &k3dCleanupProof{InstallationID: id, HostID: f.host.ID, DaemonID: "original-docker-id"}}
	encoded, _ := json.Marshal(snapshot)
	preview, _ := json.Marshal(Plan{ID: id, Mode: "k3d", Commands: []string{"edo_cli cluster create '" + k3dResourceName(id) + "' --servers 1"}})
	ciphertext, err := f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+id))
	if err != nil {
		t.Fatal(err)
	}
	record := model.K3sInstallation{ID: id, ClusterID: cluster.ID, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, CreatedBy: f.actor.ID, Name: cluster.Name, Action: "create", Status: "succeeded", Hosts: datatypes.JSON(hosts), Preview: datatypes.JSON(preview), InputCiphertext: ciphertext, ExpiresAt: time.Now()}
	if err := f.db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	return f, cluster, record, snapshot
}

func TestManualUninstallGuideWorksOfflineAfterHostIPChangesWithoutRemoteOrDatabaseWrites(t *testing.T) {
	f, cluster, record, _ := manualGuideFixture(t)
	if err := f.db.Model(&model.Host{}).Where("uuid = ?", f.host.ID).Updates(map[string]any{"address": "192.0.2.99", "is_active": false, "ssh_credential_ciphertext": "unreadable"}).Error; err != nil {
		t.Fatal(err)
	}
	guide, err := f.service.ManualUninstall(f.ctx, f.actor.ID, cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if guide.Command != "k3d cluster delete '"+k3dResourceName(record.ID)+"'" || guide.ResourceName == cluster.Name || guide.Host.Address != "192.0.2.10" || guide.DockerID != "original-docker-id" || guide.Local {
		t.Fatalf("指引目标与安装快照不一致: %+v", guide)
	}
	if f.transport.reads != 0 || f.transport.privileges != 0 || len(f.transport.writes) != 0 {
		t.Fatal("获取手动指引不应连接或修改主机")
	}
	var saved model.K3sInstallation
	if err := f.db.First(&saved, "uuid = ?", record.ID).Error; err != nil || saved.Status != record.Status || saved.InputCiphertext != record.InputCiphertext {
		t.Fatal("读取指引不应改变安装状态或快照", err)
	}
	var locks int64
	if err := f.db.Model(&model.K3sInstallationHostLock{}).Count(&locks).Error; err != nil || locks != 0 {
		t.Fatal("读取指引不应创建任务锁", err)
	}
}

func TestManualUninstallGuideRejectsRevokedPermissionsAndForeignProject(t *testing.T) {
	f, cluster, _, _ := manualGuideFixture(t)
	f.auth.denied = true
	if _, err := f.service.ManualUninstall(f.ctx, f.actor.ID, cluster.ID); !errors.Is(err, ErrUninstallPermission) {
		t.Fatal("撤权后仍提供卸载指引", err)
	}
	f.auth.denied = false
	if err := f.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", cluster.ID).Update("project_id", uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ManualUninstall(f.ctx, f.actor.ID, cluster.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("不能查看其他项目的卸载目标", err)
	}
}

func TestManualUninstallGuideUsesReviewedNameInsteadOfReconstructingHistoricalNames(t *testing.T) {
	f, cluster, record, _ := manualGuideFixture(t)
	preview, _ := json.Marshal(Plan{ID: record.ID, Mode: "k3d", Commands: []string{"edo_cli cluster create 'historical-resource-name' --servers 1"}})
	if err := f.db.Model(&record).Update("preview", datatypes.JSON(preview)).Error; err != nil {
		t.Fatal(err)
	}
	guide, err := f.service.ManualUninstall(f.ctx, f.actor.ID, cluster.ID)
	if err != nil || guide.Command != "k3d cluster delete 'historical-resource-name'" {
		t.Fatal("历史名称必须以安装时的计划为准", guide, err)
	}
}

func TestManualUninstallGuideWorksWithoutUsableInstallationRecord(t *testing.T) {
	for _, scenario := range []string{"missing", "ambiguous", "native", "damaged", "host-mismatch", "failed", "missing-name", "records-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			f, cluster, record, snapshot := manualGuideFixture(t)
			switch scenario {
			case "records-unavailable":
				if err := f.db.Migrator().DropTable(&model.K3sInstallation{}); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := f.db.Delete(&record).Error; err != nil {
					t.Fatal(err)
				}
			case "ambiguous":
				record.ID, record.RowID = uuid.NewString(), 0
				if err := f.db.Create(&record).Error; err != nil {
					t.Fatal(err)
				}
			case "damaged":
				if err := f.db.Model(&record).Update("input_ciphertext", "private-corrupt-snapshot").Error; err != nil {
					t.Fatal(err)
				}
			case "failed":
				if err := f.db.Model(&record).Update("status", "failed").Error; err != nil {
					t.Fatal(err)
				}
			case "missing-name":
				if err := f.db.Model(&record).Update("preview", datatypes.JSON(`{"mode":"k3d"}`)).Error; err != nil {
					t.Fatal(err)
				}
			default:
				if scenario == "native" {
					snapshot.Input.Mode = "native"
				} else {
					snapshot.Input.ServerHostID = uuid.NewString()
				}
				encoded, _ := json.Marshal(snapshot)
				ciphertext, err := f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+record.ID))
				if err != nil {
					t.Fatal(err)
				}
				if err := f.db.Model(&record).Update("input_ciphertext", ciphertext).Error; err != nil {
					t.Fatal(err)
				}
			}
			guide, err := f.service.ManualUninstall(f.ctx, f.actor.ID, cluster.ID)
			if err != nil || guide == nil || guide.ClusterID != cluster.ID || guide.ListCommand != "k3d cluster list" || guide.Command != "" || guide.ResourceName != "" || guide.Host != nil {
				t.Fatal("没有可预填记录时仍须返回通用指引，不猜测卸载名称", guide, err)
			}
		})
	}
}
