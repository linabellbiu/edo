package database

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"zrt/internal/model"
)

type migration struct {
	version string
	up      func(*gorm.DB) error
}

var migrations = []migration{
	{
		version: "202607230001",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Job{}, &model.OutboxEvent{})
		},
	},
	{
		version: "202607230002",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.User{})
		},
	},
	{
		version: "202607230003",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.Role{},
				&model.RolePermission{},
				&model.UserRole{},
				&model.AuditLog{},
			)
		},
	},
	{
		version: "202607230004",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Job{})
		},
	},
	{
		version: "202607230005",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.GitRepository{}, &model.GitWebhookDelivery{})
		},
	},
	{
		version: "202607230006",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.DockerEndpoint{}, &model.KubernetesCluster{})
		},
	},
	{
		version: "202607230007",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.DeploymentTarget{}, &model.DeploymentRecord{})
		},
	},
	{
		version: "202607230008",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Configuration{}, &model.ConfigurationRevision{})
		},
	},
	{
		version: "202607230009",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.NotificationChannel{}, &model.Notification{})
		},
	},
	{
		version: "202607230010",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.ScheduledTask{}, &model.MonitorRule{}, &model.MonitorCheck{})
		},
	},
	{
		version: "202607230011",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Job{})
		},
	},
	{
		version: "202607230012",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.User{})
		},
	},
}

func Migrate(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).AutoMigrate(&model.SchemaMigration{}); err != nil {
		return fmt.Errorf("初始化迁移记录失败: %w", err)
	}

	for _, item := range migrations {
		var count int64
		if err := db.WithContext(ctx).Model(&model.SchemaMigration{}).
			Where("version = ?", item.version).Count(&count).Error; err != nil {
			return fmt.Errorf("读取迁移版本 %s 失败: %w", item.version, err)
		}
		if count > 0 {
			continue
		}
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := item.up(tx); err != nil {
				return err
			}
			return tx.Create(&model.SchemaMigration{Version: item.version, AppliedAt: time.Now().UTC()}).Error
		}); err != nil {
			return fmt.Errorf("执行迁移版本 %s 失败: %w", item.version, err)
		}
	}
	return nil
}

func VerifyMigrations(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.SchemaMigration{}) {
		return fmt.Errorf("数据库尚未初始化，请先执行 zrt migrate")
	}
	for _, item := range migrations {
		var count int64
		if err := db.WithContext(ctx).Model(&model.SchemaMigration{}).
			Where("version = ?", item.version).Count(&count).Error; err != nil {
			return fmt.Errorf("检查数据库迁移版本 %s 失败: %w", item.version, err)
		}
		if count == 0 {
			return fmt.Errorf("数据库迁移版本 %s 尚未执行，请先执行 zrt migrate", item.version)
		}
	}
	return nil
}
