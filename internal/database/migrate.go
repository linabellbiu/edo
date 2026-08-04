package database

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"edo/internal/model"
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
			return tx.AutoMigrate(
				&model.Application{},
				&model.ReleaseWorkflow{}, &model.PipelineRun{}, &model.PipelineRunApproval{},
			)
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
			return tx.AutoMigrate(
				&model.GitRepository{}, &model.Application{}, &model.ApplicationRepository{}, &model.ApplicationRepositoryObservation{},
				&model.PipelineRun{}, &model.PipelineRunRepository{},
			)
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
	{
		version: "202607270025",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.PipelineRunLog{})
		},
	},
	{
		version: "202607270026",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.ReleaseGroupApplication{})
		},
	},
	{
		version: "202607280028",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.PipelineRun{})
		},
	},
	{
		version: "202607280030",
		up:      migrateHostsAndEnvironments,
	},
	{
		version: "202607280031",
		up:      migrateSSHDeploymentTargets,
	},
	{
		version: "202607290032",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.PipelineRun{}, &model.ReleasePlanExecution{}, &model.ReleasePlanExecutionItem{},
			)
		},
	},
	{
		version: "202607290033",
		up:      migrateDeploymentPlanTargets,
	},
	{
		version: "202607290034",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.ReleasePlan{})
		},
	},
	{
		version: "202607290036",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.ReleasePlan{})
		},
	},
	{
		version: "202607290038",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.BuildPlan{})
		},
	},
	{
		version: "202607290039",
		up:      migrateEnvironmentHosts,
	},
	{
		version: "202607290040",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.BuildRun{}, &model.Artifact{})
		},
	},
	{
		version: "202607300041",
		up:      migrateDeploymentExecutionConfig,
	},
	{
		version: "202607300042",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.DeploymentPlan{}, &model.DeploymentRecord{}, &model.Artifact{})
		},
	},
	{
		version: "202607300043",
		up:      migrateStructuredWorkflows,
	},
	{
		version: "202607300044",
		up:      migrateDeploymentIdempotency,
	},
	{
		version: "202607300045",
		up:      migrateBuildExecutionConfig,
	},
	{
		version: "202607300046",
		up:      migrateDeploymentPlanLifecycle,
	},
	{
		version: "202607300047",
		up:      migrateDeploymentImageIdentity,
	},
	{
		version: "202607300048",
		up:      migrateRepositoryObservationActions,
	},
	{
		version: "202607300049",
		up: func(tx *gorm.DB) error {
			return addColumns(tx, &model.PipelineRun{}, []string{"LogBytes", "LogTruncated"})
		},
	},
	{
		version: "202607300050",
		up:      migrateRepositoryAPICredential,
	},
	{
		version: "202607300051",
		up:      migrateDeploymentRollbackAttempts,
	},
	{
		version: "202607300052",
		up:      migrateRepositoryObservationWatchSchema,
	},
	{
		version: "202607300053",
		up:      migrateApplicationWorkflowsOneToMany,
	},
	{
		version: "202607310054",
		up:      migrateLegacyDeploymentEnvironmentColumns,
	},
	{
		version: "202607310055",
		up:      migrateImageRegistryProviderSemantics,
	},
	{
		version: "202607310056",
		up: func(tx *gorm.DB) error {
			return addColumns(tx, &model.DeploymentRecord{}, []string{"ImageDisplay"})
		},
	},
	{
		version: "202608020057",
		up: func(tx *gorm.DB) error {
			return addColumns(tx, &model.Host{}, []string{"Architecture"})
		},
	},
	{
		version: "202608030059",
		up:      migrateLinkedWorkflowNames,
	},
	{
		version: "202608030060",
		up:      migrateApplicationRuntimeControls,
	},
	{
		version: "202608030061",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.DeploymentInstanceControl{})
		},
	},
	{
		version: "202608030062",
		up: func(tx *gorm.DB) error {
			return addColumns(tx, &model.DeploymentRecord{}, []string{"RuntimeDeletedAt"})
		},
	},
	{
		version: "202608030063",
		up:      migrateDepartmentDataScope,
	},
	{
		version: "202608030064",
		up:      migrateDepartmentScopedUniqueIndexes,
	},
	{
		version: "202608040065",
		up:      migrateRepeatableReleasePlanExecutions,
	},
}

// migrateRepeatableReleasePlanExecutions 取消“一个发布计划只能执行一次”的旧约束，
// 保留计划内 request_id 幂等，并把旧的 completed 生命周期状态恢复为可再次执行的 active 状态；
// is_active 仍独立决定计划当前是否可用。
func migrateRepeatableReleasePlanExecutions(tx *gorm.DB) error {
	if tx.Migrator().HasIndex(&model.ReleasePlanExecution{}, "idx_release_plan_execution_plan") {
		if err := tx.Migrator().DropIndex(&model.ReleasePlanExecution{}, "idx_release_plan_execution_plan"); err != nil {
			return err
		}
	}
	if !tx.Migrator().HasIndex(&model.ReleasePlanExecution{}, "idx_release_plan_execution_request") {
		if err := tx.Migrator().CreateIndex(&model.ReleasePlanExecution{}, "idx_release_plan_execution_request"); err != nil {
			return err
		}
	}
	if !tx.Migrator().HasIndex(&model.ReleasePlanExecution{}, "ReleasePlanID") {
		if err := tx.Migrator().CreateIndex(&model.ReleasePlanExecution{}, "ReleasePlanID"); err != nil {
			return err
		}
	}
	return tx.Model(&model.ReleasePlan{}).
		Where("status = ?", model.ReleasePlanCompleted).
		Update("status", model.ReleasePlanActive).Error
}

type departmentScopedUniqueIndex struct {
	model       any
	table       string
	field       string
	oldIndex    string
	newIndex    string
	description string
}

var departmentScopedUniqueIndexes = []departmentScopedUniqueIndex{
	{&model.GitRepository{}, "git_repositories", "name", "idx_git_repositories_name", "ux_git_repositories_department_name", "代码仓库名称"},
	{&model.BuildPlan{}, "build_plans", "name", "idx_build_plans_name", "ux_build_plans_department_name", "构建方案名称"},
	{&model.ImageRegistry{}, "image_registries", "name", "idx_image_registries_name", "ux_image_registries_department_name", "镜像仓库名称"},
	{&model.DeploymentPlan{}, "deployment_plans", "name", "idx_deployment_plans_name", "ux_deployment_plans_department_name", "部署方案名称"},
	{&model.Application{}, "applications", "name", "idx_applications_name", "ux_applications_department_name", "应用名称"},
	{&model.DockerEndpoint{}, "docker_endpoints", "name", "idx_docker_endpoints_name", "ux_docker_endpoints_department_name", "Docker 连接名称"},
	{&model.KubernetesCluster{}, "kubernetes_clusters", "name", "idx_kubernetes_clusters_name", "ux_kubernetes_clusters_department_name", "Kubernetes 集群名称"},
	{&model.DeploymentTarget{}, "deployment_targets", "name", "idx_deployment_targets_name", "ux_deployment_targets_department_name", "部署目标名称"},
	{&model.DNSProviderAccount{}, "dns_provider_accounts", "name", "idx_dns_provider_accounts_name", "ux_dns_provider_accounts_department_name", "DNS 厂商账户名称"},
	{&model.Environment{}, "environments", "name", "idx_environments_name", "ux_environments_department_name", "环境名称"},
	{&model.Host{}, "hosts", "name", "idx_hosts_name", "ux_hosts_department_name", "主机名称"},
	{&model.NotificationChannel{}, "notification_channels", "name", "idx_notification_channels_name", "ux_notification_channels_department_name", "通知渠道名称"},
	{&model.ScheduledTask{}, "scheduled_tasks", "name", "idx_scheduled_tasks_name", "ux_scheduled_tasks_department_name", "定时任务名称"},
	{&model.MonitorRule{}, "monitor_rules", "name", "idx_monitor_rules_name", "ux_monitor_rules_department_name", "监控规则名称"},
	{&model.ReleaseWorkflowTemplate{}, "release_workflow_templates", "name", "idx_release_workflow_templates_name", "ux_release_workflow_templates_department_name", "流水线方案名称"},
	{&model.ReleasePlan{}, "release_plans", "version", "idx_release_plans_version", "ux_release_plans_department_version", "发布计划版本"},
}

// migrateDepartmentScopedUniqueIndexes 把部门资源的业务唯一标识从全局唯一收紧为部门内唯一。
// 三个阶段不能调换：先验证全部数据，避免 MySQL 的非事务 DDL 留下半套结构；再创建新索引；
// 最后才删除旧全局索引，确保升级过程中始终至少有一层唯一约束。
func migrateDepartmentScopedUniqueIndexes(tx *gorm.DB) error {
	for _, item := range departmentScopedUniqueIndexes {
		if !tx.Migrator().HasTable(item.model) || tx.Migrator().HasIndex(item.model, item.newIndex) {
			continue
		}
		if !tx.Migrator().HasColumn(item.model, "DepartmentID") || !tx.Migrator().HasColumn(item.model, item.field) {
			return fmt.Errorf("%s缺少部门唯一索引所需字段", item.description)
		}
		var duplicate struct {
			Count int64 `gorm:"column:duplicate_count"`
		}
		result := tx.Table(item.table).
			Select("COUNT(*) AS duplicate_count").
			Group("department_id, " + item.field).
			Having("COUNT(*) > 1").
			Limit(1).
			Scan(&duplicate)
		if result.Error != nil {
			return fmt.Errorf("检查%s部门内重复数据失败: %w", item.description, result.Error)
		}
		if result.RowsAffected > 0 {
			return fmt.Errorf("%s存在部门内重复数据，无法创建唯一索引", item.description)
		}
	}

	for _, item := range departmentScopedUniqueIndexes {
		if !tx.Migrator().HasTable(item.model) || tx.Migrator().HasIndex(item.model, item.newIndex) {
			continue
		}
		// 使用模型中显式命名的组合索引，让 GORM 为 SQLite、PostgreSQL 和 MySQL
		// 分别生成正确的标识符引用与建索引语句。
		if err := tx.Migrator().CreateIndex(item.model, item.newIndex); err != nil {
			return fmt.Errorf("创建%s部门唯一索引失败: %w", item.description, err)
		}
	}

	for _, item := range departmentScopedUniqueIndexes {
		if !tx.Migrator().HasTable(item.model) || !tx.Migrator().HasIndex(item.model, item.oldIndex) {
			continue
		}
		// 必须传显式旧索引名。模型 tag 已经不再包含旧索引，按字段名删除会误删新组合索引。
		if err := tx.Migrator().DropIndex(item.model, item.oldIndex); err != nil {
			return fmt.Errorf("删除%s旧全局唯一索引失败: %w", item.description, err)
		}
	}
	return nil
}

func migrateDepartmentDataScope(tx *gorm.DB) error {
	now := time.Now().UTC()
	if err := tx.AutoMigrate(&model.Department{}); err != nil {
		return fmt.Errorf("创建部门表失败: %w", err)
	}
	defaultDepartment := model.Department{
		ID: DefaultDepartmentID, Name: "默认部门", Description: "升级前已有用户和资源的默认归属",
		IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Where("id = ?", DefaultDepartmentID).FirstOrCreate(&defaultDepartment).Error; err != nil {
		return fmt.Errorf("初始化默认部门失败: %w", err)
	}
	departmentModels := []any{
		&model.User{}, &model.AuditLog{}, &model.Job{}, &model.OutboxEvent{},
		&model.GitRepository{}, &model.DockerEndpoint{}, &model.KubernetesCluster{},
		&model.DeploymentTarget{}, &model.DeploymentRecord{}, &model.NotificationChannel{}, &model.Notification{},
		&model.ScheduledTask{}, &model.MonitorRule{}, &model.BuildRun{}, &model.Artifact{},
		&model.ReleasePlan{}, &model.ReleasePlanExecution{}, &model.ReleaseWorkflow{}, &model.ReleaseWorkflowTemplate{},
		&model.BuildPlan{}, &model.ImageRegistry{}, &model.DeploymentPlan{}, &model.Application{}, &model.PipelineRun{},
		&model.DNSProviderAccount{}, &model.DNSDomain{}, &model.Environment{}, &model.Host{},
	}
	for _, value := range departmentModels {
		if err := addColumns(tx, value, []string{"DepartmentID"}); err != nil {
			return fmt.Errorf("增加部门归属字段失败: %w", err)
		}
		statement := &gorm.Statement{DB: tx}
		if err := statement.Parse(value); err != nil {
			return fmt.Errorf("解析部门资源表失败: %w", err)
		}
		table := statement.Schema.Table
		if err := tx.Table(table).Where("department_id IS NULL OR department_id = ?", "").
			Update("department_id", DefaultDepartmentID).Error; err != nil {
			return fmt.Errorf("回填%s部门归属失败: %w", table, err)
		}
		if err := addIndexes(tx, value, []string{"DepartmentID"}); err != nil {
			return fmt.Errorf("创建%s部门索引失败: %w", table, err)
		}
	}
	return migrateLegacyPermissions(tx)
}

func migrateLegacyPermissions(tx *gorm.DB) error {
	expansions := map[string][]string{
		"user.manage":         {"user.create", "user.update", "user.delete"},
		"role.manage":         {"role.create", "role.update", "role.delete"},
		"identity.manage":     {"identity.create", "identity.update"},
		"repository.manage":   {"repository.create", "repository.update", "repository.delete", "repository.execute"},
		"credential.manage":   {"credential.create", "credential.update", "credential.delete"},
		"dns.manage":          {"dns.create", "dns.update", "dns.delete", "dns.execute"},
		"delivery.manage":     {"delivery.create", "delivery.update", "delivery.delete"},
		"delivery.run":        {"delivery.execute"},
		"deployment.manage":   {"deployment.create", "deployment.update", "deployment.delete"},
		"deployment.run":      {"deployment.execute"},
		"cluster.manage":      {"cluster.create", "cluster.update", "cluster.delete", "cluster.execute"},
		"task.manage":         {"task.execute"},
		"config.manage":       {"config.create", "config.update", "config.execute"},
		"notification.manage": {"notification.create", "notification.update", "notification.execute"},
		"monitor.manage":      {"monitor.create", "monitor.update", "monitor.execute"},
		"scheduler.manage":    {"scheduler.create", "scheduler.update"},
	}
	legacyCodes := make([]string, 0, len(expansions))
	for code := range expansions {
		legacyCodes = append(legacyCodes, code)
	}
	if tx.Migrator().HasTable(&model.RolePermission{}) {
		var roleRules []model.RolePermission
		if err := tx.Where("permission IN ?", legacyCodes).Find(&roleRules).Error; err != nil {
			return fmt.Errorf("读取旧角色权限失败: %w", err)
		}
		for _, rule := range roleRules {
			for _, permission := range expansions[rule.Permission] {
				item := model.RolePermission{RoleID: rule.RoleID, Permission: permission, CreatedAt: rule.CreatedAt}
				if err := tx.Where("role_id = ? AND permission = ?", item.RoleID, item.Permission).FirstOrCreate(&item).Error; err != nil {
					return fmt.Errorf("迁移角色权限失败: %w", err)
				}
			}
		}
		if err := tx.Where("permission IN ?", legacyCodes).Delete(&model.RolePermission{}).Error; err != nil {
			return fmt.Errorf("清理旧角色权限失败: %w", err)
		}
	}
	if tx.Migrator().HasTable(&model.UserPermission{}) {
		var userRules []model.UserPermission
		if err := tx.Where("permission IN ?", legacyCodes).Find(&userRules).Error; err != nil {
			return fmt.Errorf("读取旧用户权限覆盖失败: %w", err)
		}
		for _, rule := range userRules {
			for _, permission := range expansions[rule.Permission] {
				item := model.UserPermission{
					UserID: rule.UserID, Permission: permission, Effect: rule.Effect,
					CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
				}
				if err := tx.Where("user_id = ? AND permission = ?", item.UserID, item.Permission).FirstOrCreate(&item).Error; err != nil {
					return fmt.Errorf("迁移用户权限覆盖失败: %w", err)
				}
			}
		}
		if err := tx.Where("permission IN ?", legacyCodes).Delete(&model.UserPermission{}).Error; err != nil {
			return fmt.Errorf("清理旧用户权限覆盖失败: %w", err)
		}
	}
	return nil
}

func migrateApplicationRuntimeControls(tx *gorm.DB) error {
	if err := addColumns(tx, &model.DeploymentRecord{}, []string{"ApplicationID"}); err != nil {
		return err
	}
	if err := tx.AutoMigrate(&model.DeploymentInstanceControl{}); err != nil {
		return err
	}
	// 已有发布记录通过不可变流水线运行补齐应用关联；无法关联的历史记录保持空值，
	// 后续运行控制会明确拒绝，不能按应用名猜测归属。
	return tx.Exec(`UPDATE deployment_records
		SET application_id = (
			SELECT pipeline_runs.application_id FROM pipeline_runs
			WHERE pipeline_runs.id = deployment_records.pipeline_run_id
		)
		WHERE application_id = '' AND pipeline_run_id <> ''
		AND EXISTS (
			SELECT 1 FROM pipeline_runs WHERE pipeline_runs.id = deployment_records.pipeline_run_id
		)`).Error
}

// migrateLinkedWorkflowNames 清理早期把应用名和公共流水线方案名拼接后保存的名称。
// 自定义流水线没有方案关联，名称由用户维护，不参与本次修正。
func migrateLinkedWorkflowNames(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.ReleaseWorkflow{}) ||
		!tx.Migrator().HasTable(&model.ReleaseWorkflowTemplate{}) {
		return nil
	}

	var workflows []model.ReleaseWorkflow
	if err := tx.Select("id", "workflow_template_id", "name").
		Where("workflow_template_id <> ?", "").Find(&workflows).Error; err != nil {
		return fmt.Errorf("查询关联公共方案的流水线失败: %w", err)
	}
	if len(workflows) == 0 {
		return nil
	}

	templateIDs := make([]string, 0, len(workflows))
	seen := make(map[string]struct{}, len(workflows))
	for i := range workflows {
		if _, ok := seen[workflows[i].WorkflowTemplateID]; ok {
			continue
		}
		seen[workflows[i].WorkflowTemplateID] = struct{}{}
		templateIDs = append(templateIDs, workflows[i].WorkflowTemplateID)
	}
	var templates []model.ReleaseWorkflowTemplate
	if err := tx.Select("id", "name").Where("id IN ?", templateIDs).Find(&templates).Error; err != nil {
		return fmt.Errorf("查询流水线公共方案名称失败: %w", err)
	}
	templateNames := make(map[string]string, len(templates))
	for i := range templates {
		templateNames[templates[i].ID] = templates[i].Name
	}
	for i := range workflows {
		name, ok := templateNames[workflows[i].WorkflowTemplateID]
		if !ok || workflows[i].Name == name {
			continue
		}
		if err := tx.Model(&model.ReleaseWorkflow{}).
			Where("id = ? AND workflow_template_id = ?", workflows[i].ID, workflows[i].WorkflowTemplateID).
			Update("name", name).Error; err != nil {
			return fmt.Errorf("修正关联流水线名称失败: %w", err)
		}
	}
	return nil
}

// migrateImageRegistryProviderSemantics 修复早期表单允许“Docker Hub”配任意
// 地址造成的类型歧义。真正的 Docker Hub 固定为标准地址；指向其他厂商的记录
// 保留原地址和凭据，仅改为通用 OCI Registry。
func migrateImageRegistryProviderSemantics(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.ImageRegistry{}) {
		return nil
	}
	var registries []model.ImageRegistry
	if err := tx.Where("provider = ?", model.RegistryDockerHub).Find(&registries).Error; err != nil {
		return fmt.Errorf("查询 Docker Hub 镜像仓库失败: %w", err)
	}
	for i := range registries {
		updates := map[string]any{}
		if registryEndpointIsDockerHub(registries[i].Endpoint) {
			updates["endpoint"] = model.DockerHubEndpoint
			updates["allow_insecure_http"] = false
		} else {
			updates["provider"] = model.RegistryGeneric
		}
		if err := tx.Model(&model.ImageRegistry{}).Where("id = ?", registries[i].ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("修正镜像仓库类型失败: %w", err)
		}
	}
	return nil
}

func registryEndpointIsDockerHub(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	host, endpointPath := strings.ToLower(parsed.Host), strings.Trim(parsed.Path, "/")
	switch host {
	case "docker.io", "registry-1.docker.io":
		return endpointPath == ""
	case "index.docker.io":
		return endpointPath == "" || endpointPath == "v1"
	default:
		return false
	}
}

// migrateApplicationWorkflowsOneToMany 删除旧的“应用唯一流水线”结构。
// 流水线定义和仓库监听游标都不是历史执行审计数据；本次模型重构明确不兼容
// 旧定义，因此直接重建这两张表，已完成运行仍由 pipeline_runs 中的快照保留。
func migrateApplicationWorkflowsOneToMany(tx *gorm.DB) error {
	if tx.Migrator().HasTable(&model.ApplicationRepositoryObservation{}) {
		if err := tx.Migrator().DropTable(&model.ApplicationRepositoryObservation{}); err != nil {
			return fmt.Errorf("重建应用流水线监听游标失败: %w", err)
		}
	}
	if tx.Migrator().HasTable(&model.ReleaseWorkflow{}) {
		if err := tx.Migrator().DropTable(&model.ReleaseWorkflow{}); err != nil {
			return fmt.Errorf("重建应用流水线失败: %w", err)
		}
	}
	legacyApplication := &legacyApplicationWorkflowTemplateColumn{}
	if tx.Migrator().HasTable(legacyApplication) && tx.Migrator().HasColumn(legacyApplication, "WorkflowTemplateID") {
		var err error
		if tx.Dialector.Name() == "sqlite" {
			// GORM 的 SQLite DropColumn 会重建 applications，入站外键会阻止删除
			// 原表。当前 SQLite 已原生支持 DROP COLUMN，先移除旧字段索引即可原位完成。
			if err = tx.Exec(`DROP INDEX IF EXISTS "idx_applications_workflow_template_id"`).Error; err == nil {
				err = tx.Exec(`ALTER TABLE "applications" DROP COLUMN "workflow_template_id"`).Error
			}
		} else {
			err = tx.Migrator().DropColumn(legacyApplication, "WorkflowTemplateID")
		}
		if err != nil {
			return fmt.Errorf("移除应用唯一流水线方案字段失败: %w", err)
		}
	}
	if tx.Migrator().HasTable(&model.ReleasePlanExecutionItem{}) {
		if err := addBackfilledNotNullColumn(
			tx, &model.ReleasePlanExecutionItem{}, &nullableReleasePlanExecutionItemWorkflowColumn{},
			"WorkflowID", "workflow_id", "",
		); err != nil {
			return err
		}
	}
	// MySQL 的 DDL 会隐式提交事务，DropTable 后 AutoMigrate 仍可能根据迁移事务
	// 的旧元数据快照判断表存在，继而探测一个已经删除的表。两张表本就需要完整
	// 重建，因此直接按依赖顺序 CreateTable，避免再次执行存在性判断。
	if err := tx.Migrator().CreateTable(&model.ReleaseWorkflow{}); err != nil {
		return fmt.Errorf("创建应用流水线表失败: %w", err)
	}
	if err := tx.Migrator().CreateTable(&model.ApplicationRepositoryObservation{}); err != nil {
		return fmt.Errorf("创建流水线监听游标表失败: %w", err)
	}
	return tx.AutoMigrate(&model.ReleasePlanExecutionItem{})
}

type nullableReleasePlanExecutionItemWorkflowColumn struct {
	WorkflowID *string `gorm:"type:varchar(36)"`
}

func (nullableReleasePlanExecutionItemWorkflowColumn) TableName() string {
	return "release_plan_execution_items"
}

// GORM 的 SQLite Migrator 删除列时需要从模型 Schema 中解析字段；直接传表名会
// 触发空 Schema。这个最小旧模型只用于安全移除已经退出正式 Application 模型的列。
type legacyApplicationWorkflowTemplateColumn struct {
	WorkflowTemplateID string `gorm:"column:workflow_template_id;type:varchar(36);not null;default:''"`
}

func (legacyApplicationWorkflowTemplateColumn) TableName() string {
	return "applications"
}

type legacyDeploymentTargetEnvironmentColumn struct {
	Environment model.EnvironmentType `gorm:"column:environment;type:varchar(16);not null;index"`
}

func (legacyDeploymentTargetEnvironmentColumn) TableName() string {
	return "deployment_targets"
}

type legacyDeploymentRecordEnvironmentColumn struct {
	Environment model.EnvironmentType `gorm:"column:environment;type:varchar(16);not null;index"`
}

func (legacyDeploymentRecordEnvironmentColumn) TableName() string {
	return "deployment_records"
}

// 这些仅用于先创建可回填的 NULL 列。回填完成后，迁移会依据正式模型收紧为
// NOT NULL；不能直接用正式模型 AddColumn，否则非空 SQLite 表会拒绝新增列。
type nullableDeploymentPlanExecutionColumns struct {
	ComposeYAML  *string `gorm:"type:text"`
	DockerConfig *string `gorm:"type:text"`
}

func (nullableDeploymentPlanExecutionColumns) TableName() string { return "deployment_plans" }

type nullableDeploymentRecordExecutionColumns struct {
	ComposeYAML  *string `gorm:"type:text"`
	DockerConfig *string `gorm:"type:text"`
}

func (nullableDeploymentRecordExecutionColumns) TableName() string { return "deployment_records" }

type nullableBuildPlanExecutionColumns struct {
	Pull                 *bool   `gorm:"type:boolean"`
	CacheEnabled         *bool   `gorm:"type:boolean"`
	BuildArgs            *string `gorm:"type:text"`
	EnvironmentVariables *string `gorm:"type:text"`
}

func (nullableBuildPlanExecutionColumns) TableName() string { return "build_plans" }

func migrateDeploymentExecutionConfig(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.DeploymentPlan{}) {
		if err := tx.AutoMigrate(&model.DeploymentPlan{}); err != nil {
			return err
		}
	}
	if !tx.Migrator().HasTable(&model.DeploymentRecord{}) {
		if err := tx.AutoMigrate(&model.DeploymentRecord{}); err != nil {
			return err
		}
	}
	columns := []struct {
		model       any
		nullable    any
		field       string
		column      string
		legacyValue string
	}{
		{&model.DeploymentPlan{}, &nullableDeploymentPlanExecutionColumns{}, "ComposeYAML", "compose_yaml", ""},
		{&model.DeploymentPlan{}, &nullableDeploymentPlanExecutionColumns{}, "DockerConfig", "docker_config", "{}"},
		{&model.DeploymentRecord{}, &nullableDeploymentRecordExecutionColumns{}, "ComposeYAML", "compose_yaml", ""},
		{&model.DeploymentRecord{}, &nullableDeploymentRecordExecutionColumns{}, "DockerConfig", "docker_config", "{}"},
	}
	for _, column := range columns {
		if err := addBackfilledNotNullColumn(tx, column.model, column.nullable, column.field, column.column, column.legacyValue); err != nil {
			return err
		}
	}
	return tx.AutoMigrate(&model.DeploymentPlan{}, &model.DeploymentRecord{})
}

func migrateBuildExecutionConfig(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.BuildPlan{}) {
		return tx.AutoMigrate(&model.BuildPlan{})
	}
	columns := []struct {
		field       string
		column      string
		legacyValue any
	}{
		{"Pull", "pull", true},
		{"CacheEnabled", "cache_enabled", true},
		{"BuildArgs", "build_args", "{}"},
		{"EnvironmentVariables", "environment_variables", "{}"},
	}
	for _, column := range columns {
		if err := addBackfilledNotNullColumn(
			tx, &model.BuildPlan{}, &nullableBuildPlanExecutionColumns{},
			column.field, column.column, column.legacyValue,
		); err != nil {
			return err
		}
	}
	return tx.AutoMigrate(&model.BuildPlan{})
}

// addBackfilledNotNullColumn 使用“先允许 NULL、回填历史行、再收紧约束”的顺序，
// 同时兼容 SQLite 重建表、PostgreSQL ALTER COLUMN 和 MySQL MODIFY COLUMN。
// 正式结构不保留数据库默认值，后续零值仍由 Go 显式写入。
func addBackfilledNotNullColumn(
	tx *gorm.DB,
	value any,
	nullableValue any,
	field string,
	column string,
	legacyValue any,
) error {
	if !tx.Migrator().HasColumn(value, column) {
		if err := tx.Migrator().AddColumn(nullableValue, field); err != nil {
			return fmt.Errorf("添加可回填字段 %s 失败: %w", column, err)
		}
	}
	// 某些正式模型已经包含软删除字段，但旧表要在更晚的迁移才新增该列。
	// Unscoped 避免 GORM 在回填 SQL 中提前附加不存在的 deleted_at 条件。
	if err := tx.Unscoped().Model(value).Where(column+" IS NULL").UpdateColumn(column, legacyValue).Error; err != nil {
		return fmt.Errorf("回填字段 %s 失败: %w", column, err)
	}
	nullable, err := columnNullable(tx, value, column)
	if err != nil {
		return err
	}
	if !nullable {
		return nil
	}
	if err := tx.Migrator().AlterColumn(value, field); err != nil {
		return fmt.Errorf("收紧字段 %s 非空约束失败: %w", column, err)
	}
	return nil
}

func columnNullable(tx *gorm.DB, value any, column string) (bool, error) {
	columns, err := tx.Migrator().ColumnTypes(value)
	if err != nil {
		return false, fmt.Errorf("读取字段 %s 结构失败: %w", column, err)
	}
	for _, current := range columns {
		if current.Name() != column {
			continue
		}
		nullable, ok := current.Nullable()
		if !ok {
			return false, fmt.Errorf("数据库未返回字段 %s 的非空约束", column)
		}
		return nullable, nil
	}
	return false, fmt.Errorf("迁移后未找到字段 %s", column)
}

func physicalColumnExists(tx *gorm.DB, value any, column string) (bool, error) {
	columns, err := tx.Migrator().ColumnTypes(value)
	if err != nil {
		return false, fmt.Errorf("读取字段 %s 结构失败: %w", column, err)
	}
	for _, current := range columns {
		if strings.EqualFold(current.Name(), column) {
			return true, nil
		}
	}
	return false, nil
}

func migrateRepositoryAPICredential(tx *gorm.DB) error {
	// 历史仓库默认不绑定平台 API Token；NULL 同时表达“公开 API 匿名访问”和
	// “Token 克隆复用自身 Token”，不需要跨数据库不一致的字符串默认值。
	if err := addColumns(tx, &model.GitRepository{}, []string{"APICredentialID"}); err != nil {
		return err
	}
	return addIndexes(tx, &model.GitRepository{}, []string{"APICredentialID"})
}

func migrateDeploymentPlanLifecycle(tx *gorm.DB) error {
	// deleted_at 保持 NULL 表示可见方案；不为历史行制造删除时间，也不为
	// SQLite、PostgreSQL 或 MySQL 的时间列配置字面量默认值。
	if err := addColumns(tx, &model.DeploymentPlan{}, []string{"DeletedAt"}); err != nil {
		return err
	}
	return addIndexes(tx, &model.DeploymentPlan{}, []string{"DeletedAt"})
}

func migrateDeploymentIdempotency(tx *gorm.DB) error {
	// 历史发布及非流水线发布保持 NULL；SQLite、PostgreSQL 和 MySQL 的唯一索引
	// 都允许多条 NULL，因此无需制造无法追溯的历史幂等标识。
	if err := addColumns(tx, &model.DeploymentRecord{}, []string{"IdempotencyKey"}); err != nil {
		return err
	}
	return addIndexes(tx, &model.DeploymentRecord{}, []string{"IdempotencyKey"})
}

func migrateDeploymentImageIdentity(tx *gorm.DB) error {
	// 历史记录没有可信的 Docker Image ID，保持空值并由回滚入口拒绝，
	// 不能根据可变标签猜测或补造不可变身份。
	return addColumns(tx, &model.DeploymentRecord{}, []string{"ExpectedImageID", "PreviousImageID"})
}

func migrateDeploymentRollbackAttempts(tx *gorm.DB) error {
	// 旧回滚记录无法从哈希幂等键反推出来源发布，保持空来源和第 0 次尝试；
	// 回滚入口会在再次请求同一来源时按旧幂等键精确识别并补齐第 1 次尝试。
	if err := addColumns(tx, &model.DeploymentRecord{}, []string{"RollbackSourceID", "RollbackAttempt"}); err != nil {
		return err
	}
	const indexName = "idx_deployment_records_rollback_source_attempt"
	if tx.Migrator().HasIndex(&model.DeploymentRecord{}, indexName) {
		return nil
	}
	return tx.Migrator().CreateIndex(&model.DeploymentRecord{}, indexName)
}

func migrateRepositoryObservationActions(tx *gorm.DB) error {
	// 空动作表示升级前只记录过 Commit 的监听游标。下一次轮询会补齐当前动作，
	// 但不会把已有开启 PR 误判为一次新事件。
	return addColumns(tx, &model.ApplicationRepositoryObservation{}, []string{"Action"})
}

func migrateRepositoryObservationWatchSchema(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.ApplicationRepositoryObservation{}) {
		return tx.AutoMigrate(&model.ApplicationRepositoryObservation{})
	}
	// environment 是旧的按环境监听模型遗留列。当前监听游标以代码源节点、事件和 Ref
	// 唯一标识；保留这个 NOT NULL 列会让不再携带环境字段的 Upsert 永久失败。
	const legacyEnvironmentIndex = "idx_application_repository_observations_environment"
	if tx.Migrator().HasIndex(&model.ApplicationRepositoryObservation{}, legacyEnvironmentIndex) {
		if err := tx.Migrator().DropIndex(&model.ApplicationRepositoryObservation{}, legacyEnvironmentIndex); err != nil {
			return fmt.Errorf("删除仓库监听旧环境索引失败: %w", err)
		}
	}
	hasEnvironment, err := physicalColumnExists(tx, &model.ApplicationRepositoryObservation{}, "environment")
	if err != nil {
		return err
	}
	if hasEnvironment {
		if err := tx.Migrator().DropColumn(&model.ApplicationRepositoryObservation{}, "environment"); err != nil {
			return fmt.Errorf("删除仓库监听旧环境字段失败: %w", err)
		}
	}
	// 下一版本会按多流水线维度重建监听游标；这里不能用已经包含 workflow_id
	// 的正式模型 AutoMigrate 非空旧表，否则 SQLite 会在新增 NOT NULL 列时失败。
	return nil
}

// migrateStructuredWorkflows 明确切断旧画布定义。旧 nodes/edges/viewport 列均为
// NOT NULL，仅追加 source/stages 会让升级后的数据库无法写入新结构，因此这里按产品
// 决策删除旧定义并重建两张配置表。流水线运行及其日志不删除，继续作为审计记录保留。
func migrateStructuredWorkflows(tx *gorm.DB) error {
	if err := tx.Migrator().DropTable(&model.ReleaseWorkflow{}); err != nil {
		return fmt.Errorf("删除旧应用流水线定义失败: %w", err)
	}
	if err := tx.Migrator().DropTable(&model.ReleaseWorkflowTemplate{}); err != nil {
		return fmt.Errorf("删除旧流水线方案定义失败: %w", err)
	}
	// MySQL 的 DDL 会隐式提交事务。先完成表替换，再修改应用行，避免在同一连接上
	// 持有 applications 行锁时获取关联表的元数据锁。
	if tx.Migrator().HasTable(&model.Application{}) && tx.Migrator().HasColumn(&model.Application{}, "WorkflowTemplateID") {
		if err := tx.Model(&model.Application{}).Where("workflow_template_id <> ''").Update("workflow_template_id", "").Error; err != nil {
			return fmt.Errorf("清空应用旧流水线方案关联失败: %w", err)
		}
	}
	if err := tx.AutoMigrate(&model.ReleaseWorkflowTemplate{}, &model.ReleaseWorkflow{}, &model.PipelineRun{}); err != nil {
		return fmt.Errorf("创建阶段式流水线结构失败: %w", err)
	}
	return nil
}

func migrateEnvironmentHosts(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.EnvironmentHost{}); err != nil {
		return err
	}
	if !tx.Migrator().HasTable(&model.Host{}) || !tx.Migrator().HasColumn(&model.Host{}, "EnvironmentID") {
		return nil
	}
	var legacy []struct {
		HostID        string    `gorm:"column:host_id"`
		EnvironmentID string    `gorm:"column:environment_id"`
		CreatedAt     time.Time `gorm:"column:created_at"`
	}
	if err := tx.Model(&model.Host{}).
		Select("id AS host_id", "environment_id", "created_at").
		Where("environment_id <> ''").
		Scan(&legacy).Error; err != nil {
		return err
	}
	for i := range legacy {
		createdAt := legacy[i].CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		membership := model.EnvironmentHost{
			EnvironmentID: legacy[i].EnvironmentID,
			HostID:        legacy[i].HostID,
			CreatedAt:     createdAt,
		}
		if err := tx.Where(
			"environment_id = ? AND host_id = ?", membership.EnvironmentID, membership.HostID,
		).FirstOrCreate(&membership).Error; err != nil {
			return err
		}
	}
	// 旧单值列必须清空，避免后续代码或外部工具把它误认为权威关系。
	return tx.Model(&model.Host{}).Where("environment_id <> ''").Update("environment_id", "").Error
}

func migrateDeploymentPlanTargets(tx *gorm.DB) error {
	if err := addColumns(tx, &model.DeploymentPlan{}, []string{"DeploymentTargetID"}); err != nil {
		return err
	}
	return addIndexes(tx, &model.DeploymentPlan{}, []string{"DeploymentTargetID"})
}

func migrateSSHDeploymentTargets(tx *gorm.DB) error {
	// 旧版本的 SQLite 表可能保留与当前模型不同的列约束。这里仅添加本次功能需要的列，
	// 避免 AutoMigrate 重建整表时因历史脏数据或旧默认值导致升级失败。
	if err := addColumns(tx, &model.DeploymentTarget{}, []string{
		"EnvironmentID", "HostID", "WorkingDirectory",
	}); err != nil {
		return err
	}
	if err := addColumns(tx, &model.DeploymentRecord{}, []string{
		"EnvironmentID", "HostID", "WorkingDirectory", "DeploymentPlanID", "DeploymentPlanKind",
		"CommandScript", "CommandDigest", "CommandTimeout", "CommandExitCode",
	}); err != nil {
		return err
	}
	if err := addColumns(tx, &model.PipelineRunRepository{}, []string{
		"DeploymentPlanKind", "DeploymentPlanScript", "DeploymentPlanTimeoutSeconds", "DeploymentPlanDigest",
	}); err != nil {
		return err
	}
	if err := addIndexes(tx, &model.DeploymentTarget{}, []string{"EnvironmentID", "HostID"}); err != nil {
		return err
	}
	if err := addIndexes(tx, &model.DeploymentRecord{}, []string{"EnvironmentID", "HostID", "DeploymentPlanID"}); err != nil {
		return err
	}
	var targets []model.DeploymentTarget
	if err := tx.Where("platform = ? AND host_id = '' AND runtime_id <> ''", model.DeploymentDocker).Find(&targets).Error; err != nil {
		return err
	}
	for i := range targets {
		var endpoint model.DockerEndpoint
		if err := tx.Select("host_id").First(&endpoint, "id = ?", targets[i].RuntimeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		if endpoint.HostID != "" {
			if err := tx.Model(&model.DeploymentTarget{}).Where("id = ?", targets[i].ID).Update("host_id", endpoint.HostID).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func addColumns(tx *gorm.DB, value any, fields []string) error {
	if !tx.Migrator().HasTable(value) {
		return tx.AutoMigrate(value)
	}
	for _, field := range fields {
		if tx.Migrator().HasColumn(value, field) {
			continue
		}
		if err := tx.Migrator().AddColumn(value, field); err != nil {
			return err
		}
	}
	return nil
}

func addIndexes(tx *gorm.DB, value any, fields []string) error {
	for _, field := range fields {
		if tx.Migrator().HasIndex(value, field) {
			continue
		}
		if err := tx.Migrator().CreateIndex(value, field); err != nil {
			return err
		}
	}
	return nil
}

func migrateHostsAndEnvironments(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.Environment{}, &model.Host{}, &model.HostCapability{}); err != nil {
		return err
	}
	if !tx.Migrator().HasColumn(&model.DockerEndpoint{}, "HostID") {
		if err := tx.Migrator().AddColumn(&model.DockerEndpoint{}, "HostID"); err != nil {
			return err
		}
	}
	if !tx.Migrator().HasIndex(&model.DockerEndpoint{}, "idx_docker_endpoints_host_id") {
		if err := tx.Migrator().CreateIndex(&model.DockerEndpoint{}, "HostID"); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	localHost := model.Host{
		ID: model.BuiltinLocalHostID, Name: "本地", Mode: model.HostModeLocal,
		SSHPort: 22, IsBuiltin: true, IsActive: true, CreatedBy: "system",
		CreatedAt: now, UpdatedAt: now,
	}
	createLocalHost := tx.Where("id = ?", localHost.ID).FirstOrCreate(&localHost)
	if createLocalHost.Error != nil {
		return createLocalHost.Error
	}
	// 只迁移历史版本写入的精确默认名，不覆盖管理员后续设置的自定义名称。
	if localHost.Name == "EDO 本机" {
		if err := tx.Model(&model.Host{}).Where("id = ? AND name = ?", localHost.ID, "EDO 本机").
			Updates(map[string]any{"name": "本地", "updated_at": now}).Error; err != nil {
			return err
		}
		localHost.Name = "本地"
	}
	// Docker 能力只是新安装的初始选择。之后是否启用由用户管理，
	// 重跑迁移不得把已经关闭的能力静默恢复。
	if createLocalHost.RowsAffected == 0 {
		return migrateLegacySSHDockerHosts(tx, now)
	}
	localCapability := model.HostCapability{
		HostID: localHost.ID, Kind: model.HostCapabilityDocker, RuntimeID: "edo-local-docker",
		Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&localCapability).Error; err != nil {
		return err
	}
	return migrateLegacySSHDockerHosts(tx, now)
}

func migrateLegacySSHDockerHosts(tx *gorm.DB, now time.Time) error {
	var endpoints []model.DockerEndpoint
	if err := tx.Where("host_id = ''").Find(&endpoints).Error; err != nil {
		return err
	}
	for i := range endpoints {
		endpoint := &endpoints[i]
		parsed, err := url.Parse(endpoint.Host)
		if err != nil || parsed.Scheme != "ssh" || parsed.User == nil ||
			parsed.User.Username() == "" || parsed.Hostname() == "" {
			continue
		}
		port := 22
		if parsed.Port() != "" {
			value, err := strconv.ParseUint(parsed.Port(), 10, 16)
			if err != nil || value == 0 {
				continue
			}
			port = int(value)
		}

		// 旧凭据仍由 DockerEndpoint 使用 endpoint ID 作为 AAD 解密。数据库迁移没有密钥上下文，
		// 因此这里只建立主机归属，不复制或重加密密文，也不猜测认证方式与 sudo 配置。
		host := model.Host{
			ID: endpoint.ID, Name: endpoint.Name, Mode: model.HostModeSSH,
			Address: parsed.Hostname(), SSHPort: port, SSHUsername: parsed.User.Username(),
			SSHHostKeyFingerprint: endpoint.SSHHostKeyFingerprint,
			IsActive:              endpoint.IsActive, CreatedBy: endpoint.CreatedBy,
			CreatedAt: endpoint.CreatedAt, UpdatedAt: endpoint.UpdatedAt,
		}
		var count int64
		if err := tx.Model(&model.Host{}).Where("id = ?", host.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := tx.Create(&host).Error; err != nil {
				return err
			}
		}
		capability := model.HostCapability{
			HostID: host.ID, Kind: model.HostCapabilityDocker, RuntimeID: endpoint.ID,
			Status: model.HostCapabilityUnchecked, CreatedAt: endpoint.CreatedAt, UpdatedAt: endpoint.UpdatedAt,
		}
		if err := tx.Where("host_id = ? AND kind = ?", capability.HostID, capability.Kind).
			FirstOrCreate(&capability).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.DockerEndpoint{}).Where("id = ? AND host_id = ''", endpoint.ID).
			Update("host_id", host.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

func migratePipelineExecutionFields(tx *gorm.DB) error {
	return tx.AutoMigrate(&model.PipelineRun{}, &model.PipelineRunRepository{}, &model.DeploymentRecord{})
}

func migrateDeploymentEnvironmentFields(tx *gorm.DB) error {
	if err := migrateDockerSSHCredentialColumn(tx); err != nil {
		return err
	}
	// 仅补新增字段，避免 SQLite 因历史字段类型差异重建整表。
	columns := []struct {
		model any
		field string
	}{
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

func migrateLegacyDeploymentEnvironmentColumns(tx *gorm.DB) error {
	// environment 是旧的固定安全级别字段，当前模型已经改为独立环境资源。
	// 旧列没有默认值，若只从 Go 模型删除它，升级后的数据库会拒绝所有不再写入
	// environment 的发布目标和发布记录，因此必须从物理表结构中一并移除。
	columns := []struct {
		value         any
		index         string
		dropIndexSQL  string
		dropColumnSQL string
		label         string
	}{
		{
			value:         &legacyDeploymentTargetEnvironmentColumn{},
			index:         "idx_deployment_targets_environment",
			dropIndexSQL:  `DROP INDEX IF EXISTS "idx_deployment_targets_environment"`,
			dropColumnSQL: `ALTER TABLE "deployment_targets" DROP COLUMN "environment"`,
			label:         "部署目标",
		},
		{
			value:         &legacyDeploymentRecordEnvironmentColumn{},
			index:         "idx_deployment_records_environment",
			dropIndexSQL:  `DROP INDEX IF EXISTS "idx_deployment_records_environment"`,
			dropColumnSQL: `ALTER TABLE "deployment_records" DROP COLUMN "environment"`,
			label:         "发布记录",
		},
	}
	for _, column := range columns {
		if !tx.Migrator().HasTable(column.value) {
			continue
		}
		exists, err := physicalColumnExists(tx, column.value, "environment")
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if tx.Dialector.Name() == "sqlite" {
			// 当前 SQLite 已支持原生 DROP COLUMN。原位删除可避免 GORM 重建表时
			// 受到入站外键影响，并完整保留现有发布审计记录及其索引。
			if err := tx.Exec(column.dropIndexSQL).Error; err != nil {
				return fmt.Errorf("删除%s旧环境索引失败: %w", column.label, err)
			}
			if err := tx.Exec(column.dropColumnSQL).Error; err != nil {
				return fmt.Errorf("删除%s旧环境字段失败: %w", column.label, err)
			}
			continue
		}
		if tx.Migrator().HasIndex(column.value, column.index) {
			if err := tx.Migrator().DropIndex(column.value, column.index); err != nil {
				return fmt.Errorf("删除%s旧环境索引失败: %w", column.label, err)
			}
		}
		if err := tx.Migrator().DropColumn(column.value, "Environment"); err != nil {
			return fmt.Errorf("删除%s旧环境字段失败: %w", column.label, err)
		}
	}
	return nil
}

func migrateDockerSSHCredentialColumn(tx *gorm.DB) error {
	const column = "ssh_credential_ciphertext"
	if !tx.Migrator().HasColumn(&model.DockerEndpoint{}, column) {
		switch tx.Dialector.Name() {
		case "sqlite":
			// SQLite 给已有表新增非空列时必须提供默认值；该默认值仅属于旧库升级结构。
			return tx.Exec("ALTER TABLE docker_endpoints ADD COLUMN ssh_credential_ciphertext text NOT NULL DEFAULT ''").Error
		case "mysql", "postgres":
			// 先允许 NULL，避免已有连接记录导致新增非空列失败，随后统一回填并收紧约束。
			if err := tx.Exec("ALTER TABLE docker_endpoints ADD COLUMN ssh_credential_ciphertext text NULL").Error; err != nil {
				return err
			}
		default:
			return tx.Migrator().AddColumn(&model.DockerEndpoint{}, "SSHCredentialCiphertext")
		}
	}

	switch tx.Dialector.Name() {
	case "mysql":
		if err := tx.Exec("UPDATE docker_endpoints SET ssh_credential_ciphertext = '' WHERE ssh_credential_ciphertext IS NULL").Error; err != nil {
			return err
		}
		return tx.Exec("ALTER TABLE docker_endpoints MODIFY COLUMN ssh_credential_ciphertext text NOT NULL").Error
	case "postgres":
		if err := tx.Exec("UPDATE docker_endpoints SET ssh_credential_ciphertext = '' WHERE ssh_credential_ciphertext IS NULL").Error; err != nil {
			return err
		}
		return tx.Exec("ALTER TABLE docker_endpoints ALTER COLUMN ssh_credential_ciphertext SET NOT NULL").Error
	default:
		return nil
	}
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
		&model.DeploymentPlan{}, &model.Application{},
		&model.PipelineRunRepository{}, &model.ReleasePlan{}, &model.ReleaseGroup{},
		&model.ReleaseGroupApplication{}, &model.ReleaseGroupDependency{},
	); err != nil {
		return err
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
		return fmt.Errorf("数据库尚未初始化，请先执行 edo migrate")
	}
	for _, item := range migrations {
		var count int64
		if err := db.WithContext(ctx).Model(&model.SchemaMigration{}).
			Where("version = ?", item.version).Count(&count).Error; err != nil {
			return fmt.Errorf("检查数据库迁移版本 %s 失败: %w", item.version, err)
		}
		if count == 0 {
			return fmt.Errorf("数据库迁移版本 %s 尚未执行，请先执行 edo migrate", item.version)
		}
	}
	return nil
}
