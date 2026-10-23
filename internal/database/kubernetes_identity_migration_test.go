package database

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/model"
)

type legacyKubernetesIdentityRecord struct {
	ID            string `gorm:"type:varchar(36);primaryKey"`
	Image         string `gorm:"type:varchar(1024);not null"`
	PreviousImage string `gorm:"type:varchar(1024);not null;default:''"`
	Status        string `gorm:"type:varchar(32);not null"`
}

func (legacyKubernetesIdentityRecord) TableName() string { return "deployment_records" }

func TestKubernetesDeploymentIdentityUpgradePreservesHistory(t *testing.T) {
	db, err := Open(context.Background(), config.Database{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "identity.db"), MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.AutoMigrate(&legacyKubernetesIdentityRecord{}); err != nil {
		t.Fatal(err)
	}
	exerciseKubernetesIdentityColumnUpgrade(t, db, "SQLite")
}

// 外部数据库矩阵同时调用本断言，验证真实旧表升级且不回填历史身份。
func exerciseKubernetesIdentityColumnUpgrade(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	if db.Migrator().HasColumn(&model.DeploymentRecord{}, "KubernetesUID") {
		if err := db.Migrator().DropColumn(&model.DeploymentRecord{}, "KubernetesUID"); err != nil {
			t.Fatalf("%s 准备旧表失败: %v", name, err)
		}
	}
	legacy := legacyKubernetesIdentityRecord{ID: "uid-upgrade-history", Image: "registry.example.test/api@sha256:historical", PreviousImage: "registry.example.test/api:legacy", Status: "failed"}
	// 完整外部表还有其他非空列，使用模型的显式零值写入后再删除新列。
	if db.Migrator().HasColumn(&model.DeploymentRecord{}, "TargetID") {
		row := model.DeploymentRecord{ID: legacy.ID, Image: legacy.Image, PreviousImage: legacy.PreviousImage, Status: model.DeploymentFailed}
		if err := db.Omit("KubernetesUID").Create(&row).Error; err != nil {
			t.Fatalf("%s 创建历史数据失败: %v", name, err)
		}
	} else if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("%s 创建旧表数据失败: %v", name, err)
	}
	if err := migrateRowIdentities(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateKubernetesDeploymentIdentity(db); err != nil {
			t.Fatalf("%s 升级对象身份列失败: %v", name, err)
		}
	}
	var stored struct {
		Image         string
		PreviousImage string
		Status        string
		KubernetesUID string
	}
	if err := db.Table("deployment_records").Select("image, previous_image, status, kubernetes_uid").Where("uuid = ?", legacy.ID).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.KubernetesUID != "" || stored.Image != legacy.Image || stored.PreviousImage != legacy.PreviousImage || stored.Status != legacy.Status {
		t.Fatalf("%s 迁移伪造了 UID 或改变了历史: %+v", name, stored)
	}
	if err := db.Where("uuid = ?", legacy.ID).Delete(&legacyKubernetesIdentityRecord{}).Error; err != nil {
		t.Fatal(err)
	}
}
