package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
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
				&model.BuildPlan{}, &model.ImageRegistry{}, &model.DeploymentPlan{},
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
	{
		version: "202607270020",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(
				&model.GitRepository{}, &model.Application{}, &model.ApplicationRepository{}, &model.ApplicationRepositoryObservation{},
				&model.PipelineRun{}, &model.PipelineRunRepository{},
			); err != nil {
				return err
			}
			return backfillApplicationRepositories(tx)
		},
	},
	{
		version: "202607270021",
		up:      migrateReleasePlanning,
	},
	{
		version: "202607270022",
		up:      migrateDeploymentEnvironmentFields,
	},
	{
		version: "202607270023",
		up:      migratePipelineExecutionFields,
	},
	{
		version: "202607270024",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.PipelineRun{})
		},
	},
}

func migratePipelineExecutionFields(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.PipelineRun{}, &model.PipelineRunRepository{}, &model.DeploymentRecord{}); err != nil {
		return err
	}
	if err := backfillPipelineRunRegistries(tx); err != nil {
		return err
	}
	if err := backfillApplicationWorkflowTargets(tx); err != nil {
		return err
	}
	// 旧实现只推进节点状态，从未创建发布记录；不能继续把这类历史记录展示为真实发布成功。
	return tx.Model(&model.PipelineRun{}).
		Where("status = ? AND current_node_id <> '' AND deployment_id = ''", model.PipelineRunSucceeded).
		Updates(map[string]any{
			"status": model.PipelineRunFailed, "stage": "execution_missing",
			"message": "历史流水线未执行真实构建和发布，请重新运行", "updated_at": time.Now().UTC(),
		}).Error
}

func backfillPipelineRunRegistries(tx *gorm.DB) error {
	var rows []struct {
		ID              string
		ImageRegistryID string
	}
	if err := tx.Table("pipeline_run_repositories AS component").
		Select("component.id, application.image_registry_id").
		Joins("JOIN pipeline_runs AS run ON run.id = component.pipeline_run_id").
		Joins("JOIN applications AS application ON application.id = run.application_id").
		Where("component.image_registry_id = '' AND application.image_registry_id <> ''").
		Scan(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		if err := tx.Model(&model.PipelineRunRepository{}).Where("id = ?", rows[i].ID).
			Update("image_registry_id", rows[i].ImageRegistryID).Error; err != nil {
			return err
		}
	}
	return nil
}

func backfillApplicationWorkflowTargets(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.ReleaseWorkflow{}) || !tx.Migrator().HasTable(&model.ReleaseWorkflowTemplate{}) {
		return nil
	}
	var workflows []model.ReleaseWorkflow
	if err := tx.Where("workflow_template_id <> ''").Find(&workflows).Error; err != nil {
		return err
	}
	for i := range workflows {
		workflow := &workflows[i]
		var template model.ReleaseWorkflowTemplate
		if err := tx.First(&template, "id = ?", workflow.WorkflowTemplateID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return err
		}
		templateNodes := make(map[string]model.WorkflowNode, len(template.Nodes))
		for j := range template.Nodes {
			templateNodes[template.Nodes[j].ID] = template.Nodes[j]
		}
		changed := false
		for j := range workflow.Nodes {
			node := &workflow.Nodes[j]
			if node.Type != model.WorkflowNodeDeploy || node.Config.DeploymentTargetID != "" {
				continue
			}
			templateNode, ok := templateNodes[node.ID]
			if !ok || templateNode.Config.DeploymentTargetID == "" {
				continue
			}
			node.Config.DeploymentTargetID = templateNode.Config.DeploymentTargetID
			changed = true
			if err := tx.Model(&model.ApplicationEnvironment{}).
				Where("application_id = ? AND key = ? AND deployment_target_id = ''", workflow.ApplicationID, node.Config.Environment).
				Update("deployment_target_id", node.Config.DeploymentTargetID).Error; err != nil {
				return err
			}
		}
		if !changed {
			continue
		}
		workflow.Revision++
		workflow.WorkflowTemplateRevision = template.Revision
		workflow.UpdatedAt = time.Now().UTC()
		nodes, err := json.Marshal(workflow.Nodes)
		if err != nil {
			return fmt.Errorf("序列化应用流水线迁移数据失败: %w", err)
		}
		if err := tx.Model(workflow).Updates(map[string]any{
			"nodes": string(nodes), "revision": workflow.Revision,
			"workflow_template_revision": workflow.WorkflowTemplateRevision,
			"updated_at":                 workflow.UpdatedAt,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateDeploymentEnvironmentFields(tx *gorm.DB) error {
	// 仅补新增字段，避免 SQLite 因历史字段类型差异重建整表。
	columns := []struct {
		model any
		field string
	}{
		{&model.DockerEndpoint{}, "SSHCredentialCiphertext"},
		{&model.DockerEndpoint{}, "SSHHostKeyFingerprint"},
		{&model.DeploymentTarget{}, "Description"},
	}
	for _, column := range columns {
		if tx.Migrator().HasColumn(column.model, column.field) {
			continue
		}
		if err := tx.Migrator().AddColumn(column.model, column.field); err != nil {
			return err
		}
	}
	return nil
}

func migrateReleasePlanning(tx *gorm.DB) error {
	if tx.Migrator().HasTable("release_plans") && !tx.Migrator().HasTable("deployment_plans") {
		if err := tx.Migrator().RenameTable("release_plans", "deployment_plans"); err != nil {
			return err
		}
		// PostgreSQL 和 SQLite 改表名后会保留旧索引名；先同步索引名，避免新发布计划表创建同名索引失败。
		for _, suffix := range []string{"name", "kind", "is_active", "created_by"} {
			oldName, newName := "idx_release_plans_"+suffix, "idx_deployment_plans_"+suffix
			if !tx.Migrator().HasIndex("deployment_plans", oldName) {
				continue
			}
			if tx.Migrator().HasIndex("deployment_plans", newName) {
				if err := tx.Migrator().DropIndex("deployment_plans", oldName); err != nil {
					return err
				}
				continue
			}
			if err := tx.Migrator().RenameIndex("deployment_plans", oldName, newName); err != nil {
				return err
			}
		}
	}
	if err := tx.AutoMigrate(
		&model.DeploymentPlan{}, &model.Application{}, &model.ApplicationEnvironment{},
		&model.PipelineRunRepository{}, &model.ReleasePlan{}, &model.ReleaseGroup{},
		&model.ReleaseGroupApplication{}, &model.ReleaseGroupDependency{},
	); err != nil {
		return err
	}
	return moveRepositoryPlansToApplications(tx)
}

func backfillApplicationRepositories(tx *gorm.DB) error {
	var applications []model.Application
	if err := tx.Find(&applications).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range applications {
		application := applications[i]
		if application.RepositoryID == "" {
			continue
		}
		var count int64
		if err := tx.Model(&model.ApplicationRepository{}).
			Where("application_id = ? AND repository_id = ?", application.ID, application.RepositoryID).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			link := model.ApplicationRepository{
				ID: uuid.NewString(), ApplicationID: application.ID, RepositoryID: application.RepositoryID,
				SortOrder: 0, LastObservedRef: application.LastObservedRef,
				LastObservedCommit: application.LastObservedCommit, LastCheckedAt: application.LastCheckedAt,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&link).Error; err != nil {
				return err
			}
		}
	}

	var runs []model.PipelineRun
	if err := tx.Find(&runs).Error; err != nil {
		return err
	}
	for i := range runs {
		run := runs[i]
		var count int64
		if err := tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", run.ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		var application model.Application
		if err := tx.First(&application, "id = ?", run.ApplicationID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return err
		}
		if application.RepositoryID == "" {
			continue
		}
		status := model.PipelineRunRepositoryPending
		if run.CommitSHA != "" {
			status = model.PipelineRunRepositoryReady
		}
		component := model.PipelineRunRepository{
			ID: uuid.NewString(), PipelineRunID: run.ID, RepositoryID: application.RepositoryID,
			SortOrder: 0, Ref: run.Ref, CommitSHA: run.CommitSHA,
			BuildPlanID: application.BuildPlanID, ImageRegistryID: application.ImageRegistryID,
			DeploymentPlanID: application.DeploymentPlanID,
			Status:           status, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		}
		if err := tx.Create(&component).Error; err != nil {
			return err
		}
	}
	return nil
}

func dropOptionalDeliveryConstraints(tx *gorm.DB) error {
	constraints := []struct {
		model any
		names []string
	}{
		{&model.Application{}, []string{"BuildPlan", "ImageRegistry", "DeploymentPlan", "DeploymentTarget"}},
		{&model.ApplicationEnvironment{}, []string{"DeploymentPlan", "DeploymentTarget"}},
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
			TagPattern: applications[i].TagPattern, DeploymentPlanID: applications[i].DeploymentPlanID,
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

func moveRepositoryPlansToApplications(tx *gorm.DB) error {
	if !tx.Migrator().HasColumn("git_repositories", "build_plan_id") &&
		!tx.Migrator().HasColumn("git_repositories", "release_plan_id") {
		return nil
	}
	type legacyRepositoryPlan struct {
		ID               string `gorm:"column:id"`
		BuildPlanID      string `gorm:"column:build_plan_id"`
		DeploymentPlanID string `gorm:"column:release_plan_id"`
	}
	var legacy []legacyRepositoryPlan
	if err := tx.Table("git_repositories").Select("id", "build_plan_id", "release_plan_id").Scan(&legacy).Error; err != nil {
		return err
	}
	for _, repository := range legacy {
		updates := map[string]any{}
		if repository.BuildPlanID != "" {
			updates["build_plan_id"] = gorm.Expr("CASE WHEN build_plan_id = '' THEN ? ELSE build_plan_id END", repository.BuildPlanID)
		}
		if repository.DeploymentPlanID != "" {
			updates["release_plan_id"] = gorm.Expr("CASE WHEN release_plan_id = '' THEN ? ELSE release_plan_id END", repository.DeploymentPlanID)
		}
		if len(updates) == 0 {
			continue
		}
		if err := tx.Model(&model.Application{}).Where("repository_id = ?", repository.ID).Updates(updates).Error; err != nil {
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
