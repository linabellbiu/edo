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
	{
		version: "202607230013",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.BuildPlan{}, &model.ImageRegistry{}, &model.ReleasePlan{},
				&model.Application{}, &model.PipelineRun{},
			)
		},
	},
	{
		version: "202607230014",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.IdentityProvider{}, &model.ExternalIdentity{})
		},
	},
	{
		version: "202607230015",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(
				&model.Application{}, &model.ApplicationEnvironment{},
				&model.ReleaseWorkflow{}, &model.PipelineRun{}, &model.PipelineRunApproval{},
			); err != nil {
				return err
			}
			return backfillApplicationEnvironments(tx)
		},
	},
	{
		version: "202607230016",
		up: func(tx *gorm.DB) error {
			return dropOptionalDeliveryConstraints(tx)
		},
	},
	{
		version: "202607240017",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.ReleaseWorkflowTemplate{}, &model.Application{}, &model.ReleaseWorkflow{},
			)
		},
	},
	{
		version: "202607240018",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.UserPermission{}, &model.GitCredential{}, &model.GitRepository{},
			)
		},
	},
	{
		version: "202607240019",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.DNSProviderAccount{}, &model.DNSDomain{})
		},
	},
}

func dropOptionalDeliveryConstraints(tx *gorm.DB) error {
	constraints := []struct {
		model any
		names []string
	}{
		{&model.Application{}, []string{"BuildPlan", "ImageRegistry", "ReleasePlan", "DeploymentTarget"}},
		{&model.ApplicationEnvironment{}, []string{"ReleasePlan", "DeploymentTarget"}},
	}
	for _, item := range constraints {
		for _, name := range item.names {
			if tx.Migrator().HasConstraint(item.model, name) {
				if err := tx.Migrator().DropConstraint(item.model, name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func backfillApplicationEnvironments(tx *gorm.DB) error {
	var applications []model.Application
	if err := tx.Find(&applications).Error; err != nil {
		return err
	}
	for i := range applications {
		var count int64
		if err := tx.Model(&model.ApplicationEnvironment{}).
			Where("application_id = ?", applications[i].ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		now := time.Now().UTC()
		environment := model.ApplicationEnvironment{
			ID: applications[i].ID + "-dev", ApplicationID: applications[i].ID,
			Key: "dev", Name: "开发环境", Branch: applications[i].Branch,
			PollEnabled: applications[i].PollEnabled, WatchPush: applications[i].WatchPush,
			WatchPullRequest: applications[i].WatchPullRequest, WatchTags: applications[i].WatchTags,
			TagPattern: applications[i].TagPattern, ReleasePlanID: applications[i].ReleasePlanID,
			DeploymentTargetID: applications[i].DeploymentTargetID,
			LastObservedRef:    applications[i].LastObservedRef,
			LastObservedCommit: applications[i].LastObservedCommit,
			LastCheckedAt:      applications[i].LastCheckedAt, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&environment).Error; err != nil {
			return err
		}
	}
	return nil
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
