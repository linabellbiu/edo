package database

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/model"
)

type legacyRepositoryObservationEnvironment struct {
	Environment string `gorm:"type:varchar(16);not null;default:''"`
}

func (legacyRepositoryObservationEnvironment) TableName() string {
	return "application_repository_observations"
}

func TestSQLiteMigrationIsIdempotent(t *testing.T) {
	cfg := config.Database{
		Driver:          "sqlite",
		DSN:             "file::memory:?cache=shared",
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
	}
	db, err := Open(context.Background(), cfg, slog.Default())
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	defer Close(db)

	for range 2 {
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatalf("执行迁移失败: %v", err)
		}
	}
	if !db.Migrator().HasTable(&model.Job{}) || !db.Migrator().HasTable(&model.OutboxEvent{}) ||
		!db.Migrator().HasTable(&model.User{}) || !db.Migrator().HasTable(&model.Role{}) ||
		!db.Migrator().HasTable(&model.AuditLog{}) || !db.Migrator().HasTable(&model.GitRepository{}) ||
		!db.Migrator().HasTable(&model.DockerEndpoint{}) || !db.Migrator().HasTable(&model.KubernetesCluster{}) ||
		!db.Migrator().HasTable(&model.Environment{}) || !db.Migrator().HasTable(&model.Host{}) ||
		!db.Migrator().HasTable(&model.EnvironmentHost{}) ||
		!db.Migrator().HasTable(&model.HostCapability{}) ||
		!db.Migrator().HasTable(&model.DeploymentTarget{}) || !db.Migrator().HasTable(&model.DeploymentRecord{}) ||
		!db.Migrator().HasTable(&model.Configuration{}) || !db.Migrator().HasTable(&model.ConfigurationRevision{}) ||
		!db.Migrator().HasTable(&model.NotificationChannel{}) || !db.Migrator().HasTable(&model.Notification{}) ||
		!db.Migrator().HasTable(&model.ScheduledTask{}) || !db.Migrator().HasTable(&model.MonitorRule{}) ||
		!db.Migrator().HasTable(&model.MonitorCheck{}) || !db.Migrator().HasTable(&model.Application{}) ||
		!db.Migrator().HasTable(&model.BuildPlan{}) || !db.Migrator().HasTable(&model.ImageRegistry{}) ||
		!db.Migrator().HasTable(&model.DeploymentPlan{}) || !db.Migrator().HasTable(&model.ReleasePlan{}) ||
		!db.Migrator().HasTable(&model.ReleaseGroup{}) || !db.Migrator().HasTable(&model.PipelineRun{}) ||
		!db.Migrator().HasTable(&model.PipelineRunLog{}) ||
		!db.Migrator().HasTable(&model.BuildRun{}) || !db.Migrator().HasTable(&model.Artifact{}) ||
		!db.Migrator().HasTable(&model.ApplicationRepository{}) || !db.Migrator().HasTable(&model.ApplicationRepositoryObservation{}) ||
		!db.Migrator().HasTable(&model.PipelineRunRepository{}) ||
		!db.Migrator().HasTable(&model.UserPermission{}) || !db.Migrator().HasTable(&model.GitCredential{}) ||
		!db.Migrator().HasTable(&model.DNSProviderAccount{}) || !db.Migrator().HasTable(&model.DNSDomain{}) {
		t.Fatal("核心任务表未创建")
	}
	if !db.Migrator().HasColumn(&model.PipelineRun{}, "retry_of_id") {
		t.Fatal("流水线重新执行来源字段未创建")
	}
	if !db.Migrator().HasColumn(&model.DockerEndpoint{}, "host_id") {
		t.Fatal("Docker 连接的主机归属字段未创建")
	}
	if !db.Migrator().HasColumn(&model.ReleaseGroupApplication{}, "manual_deploy") ||
		!db.Migrator().HasColumn(&model.ReleaseGroupApplication{}, "source_type") ||
		!db.Migrator().HasColumn(&model.ReleaseGroupApplication{}, "source_value") {
		t.Fatal("发布计划应用的手动版本来源字段未创建")
	}
	if !db.Migrator().HasColumn(&model.ReleasePlan{}, "is_active") {
		t.Fatal("发布计划停用状态字段未创建")
	}
	if !db.Migrator().HasColumn(&model.ReleasePlan{}, "deleted_at") {
		t.Fatal("发布计划软删除字段未创建")
	}
	if !db.Migrator().HasColumn(&model.BuildPlan{}, "deleted_at") {
		t.Fatal("构建方案软删除字段未创建")
	}
	if !db.Migrator().HasColumn(&model.DeploymentPlan{}, "deleted_at") ||
		!db.Migrator().HasIndex(&model.DeploymentPlan{}, "DeletedAt") {
		t.Fatal("部署方案软删除字段或索引未创建")
	}
	if !db.Migrator().HasColumn(&model.BuildPlan{}, "runtime_image") {
		t.Fatal("脚本构建运行镜像字段未创建")
	}
	if !db.Migrator().HasColumn(&model.DeploymentRecord{}, "idempotency_key") ||
		!db.Migrator().HasIndex(&model.DeploymentRecord{}, "IdempotencyKey") {
		t.Fatal("流水线发布幂等字段或唯一索引未创建")
	}
	if !db.Migrator().HasColumn(&model.DeploymentRecord{}, "rollback_source_id") ||
		!db.Migrator().HasColumn(&model.DeploymentRecord{}, "rollback_attempt") ||
		!db.Migrator().HasIndex(&model.DeploymentRecord{}, "idx_deployment_records_rollback_source_attempt") {
		t.Fatal("回滚来源、尝试次数字段或查询索引未创建")
	}
	if !db.Migrator().HasColumn(&model.ApplicationRepositoryObservation{}, "action") {
		t.Fatal("PR 监听动作游标字段未创建")
	}
	if db.Migrator().HasColumn(&model.ApplicationRepositoryObservation{}, "environment") {
		t.Fatal("仓库监听表仍保留旧环境字段")
	}
	if !db.Migrator().HasColumn(&model.GitRepository{}, "api_credential_id") ||
		!db.Migrator().HasIndex(&model.GitRepository{}, "APICredentialID") {
		t.Fatal("代码仓库平台 API 令牌引用字段或索引未创建")
	}
	assertStructuredWorkflowSchema(t, db, "SQLite")
}

func TestSQLiteUsesPureGoDriver(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver:          "sqlite",
		DSN:             ":memory:",
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	defer Close(db)

	var foreignKeys int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil {
		t.Fatalf("读取 foreign_keys 失败: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	type uniqueRecord struct {
		ID   uint
		Name string `gorm:"uniqueIndex"`
	}
	if err := db.AutoMigrate(&uniqueRecord{}); err != nil {
		t.Fatalf("创建测试表失败: %v", err)
	}
	if err := db.Create(&uniqueRecord{Name: "same"}).Error; err != nil {
		t.Fatalf("写入测试数据失败: %v", err)
	}
	if err := db.Create(&uniqueRecord{Name: "same"}).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("重复键错误未转换: %v", err)
	}
}

func TestSQLiteRepositoryObservationUpgradeRebuildsWorkflowScopedCursor(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver:          "sqlite",
		DSN:             "file:repository_observation_upgrade?mode=memory&cache=shared",
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	defer Close(db)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("初始化迁移失败: %v", err)
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE version IN ?", []string{"202607300052", "202607300053"}).Error; err != nil {
		t.Fatalf("重置仓库监听迁移版本失败: %v", err)
	}
	if err := db.Exec("ALTER TABLE applications ADD workflow_template_id varchar(36) NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatalf("恢复应用唯一流水线方案旧字段失败: %v", err)
	}
	if err := db.Migrator().DropTable(&model.ApplicationRepositoryObservation{}); err != nil {
		t.Fatalf("删除当前仓库监听表失败: %v", err)
	}
	if err := db.Exec(`CREATE TABLE application_repository_observations (
		id varchar(36) PRIMARY KEY,
		application_repository_id varchar(36) NOT NULL,
		"environment" varchar(16) NOT NULL,
		ref varchar(512) NOT NULL DEFAULT '',
		commit_sha varchar(64) NOT NULL DEFAULT '',
		last_checked_at datetime,
		created_at datetime NOT NULL,
		updated_at datetime NOT NULL,
		watch_key varchar(64) NOT NULL DEFAULT '',
		source_node_id varchar(64) NOT NULL DEFAULT '',
		event varchar(16) NOT NULL DEFAULT '',
		action varchar(16) NOT NULL DEFAULT ''
	)`).Error; err != nil {
		t.Fatalf("构造旧仓库监听表失败: %v", err)
	}
	if err := db.Exec("CREATE INDEX idx_application_repository_observations_environment ON application_repository_observations(environment)").Error; err != nil {
		t.Fatalf("构造旧仓库监听环境索引失败: %v", err)
	}
	now := time.Now().UTC()
	repository := model.GitRepository{
		ID: "repository", Name: "迁移测试仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://example.invalid/repository.git", AuthType: model.GitAuthNone,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	application := model.Application{
		ID: "application", Name: "迁移测试应用", RepositoryID: repository.ID,
		PollIntervalSeconds: 3, SyncStatus: model.ApplicationSyncIdle, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	repositoryLink := model.ApplicationRepository{
		ID: "repository-link", ApplicationID: application.ID, RepositoryID: repository.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&repository, &application, &repositoryLink} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("写入仓库监听关联数据失败: %v", err)
		}
	}
	if err := db.Exec(`INSERT INTO application_repository_observations
		(id, application_repository_id, environment, ref, commit_sha, last_checked_at, created_at, updated_at, watch_key, source_node_id, event, action)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"observation-legacy", "repository-link", "test", "refs/heads/main", "0123456789abcdef", now, now, now,
		"watch-main", "source-main", "push", "",
	).Error; err != nil {
		t.Fatalf("写入旧仓库监听游标失败: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("升级旧仓库监听表失败: %v", err)
	}
	if db.Migrator().HasColumn(&model.ApplicationRepositoryObservation{}, "environment") {
		var ddl string
		_ = db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", "application_repository_observations").Scan(&ddl).Error
		t.Fatalf("升级后仍保留旧环境字段: %s", ddl)
	}
	if db.Migrator().HasColumn(&legacyApplicationWorkflowTemplateColumn{}, "WorkflowTemplateID") {
		var ddl string
		_ = db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", "applications").Scan(&ddl).Error
		t.Fatalf("升级后 applications 仍保留唯一流水线方案字段: %s", ddl)
	}
	var observation model.ApplicationRepositoryObservation
	if err := db.First(&observation, "id = ?", "observation-legacy").Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("旧的应用级监听游标不应进入多流水线模型: %v", err)
	}
	observation = model.ApplicationRepositoryObservation{
		ID: "observation-workflow", ApplicationRepositoryID: repositoryLink.ID,
		WorkflowID: "workflow", WatchKey: "watch-workflow-main", SourceNodeID: "source-main",
		Event: "push", Ref: "refs/heads/main", CommitSHA: "0123456789abcdef",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&observation).Error; err != nil {
		t.Fatalf("升级后无法保存按流水线隔离的监听游标: %v", err)
	}
}

func TestRepositoryAPICredentialMigrationPreservesLegacyRepositories(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:repository_api_credential_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.Exec(`CREATE TABLE git_repositories (id varchar(36) PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO git_repositories (id) VALUES (?)`, "legacy-repository").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateRepositoryAPICredential(db); err != nil {
		t.Fatalf("升级历史仓库 API 令牌字段失败: %v", err)
	}
	if !db.Migrator().HasColumn(&model.GitRepository{}, "api_credential_id") ||
		!db.Migrator().HasIndex(&model.GitRepository{}, "APICredentialID") {
		t.Fatal("仓库 API 令牌字段或索引没有创建")
	}
	var nullCount int64
	if err := db.Table("git_repositories").Where("api_credential_id IS NULL").Count(&nullCount).Error; err != nil || nullCount != 1 {
		t.Fatalf("历史仓库不应被绑定伪造的 API 令牌: count=%d err=%v", nullCount, err)
	}
}

func TestDeploymentIdempotencyMigrationPreservesLegacyRecords(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:deployment_idempotency_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.Exec(`CREATE TABLE deployment_records (id varchar(36) PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO deployment_records (id) VALUES (?), (?)`, "legacy-a", "legacy-b").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateDeploymentIdempotency(db); err != nil {
		t.Fatalf("升级历史发布记录失败: %v", err)
	}
	if !db.Migrator().HasColumn(&model.DeploymentRecord{}, "idempotency_key") ||
		!db.Migrator().HasIndex(&model.DeploymentRecord{}, "IdempotencyKey") {
		t.Fatal("幂等字段或唯一索引没有创建")
	}
	var nullCount int64
	if err := db.Table("deployment_records").Where("idempotency_key IS NULL").Count(&nullCount).Error; err != nil || nullCount != 2 {
		t.Fatalf("历史发布记录不应被伪造幂等标识: count=%d err=%v", nullCount, err)
	}
	if err := db.Exec(`INSERT INTO deployment_records (id) VALUES (?)`, "legacy-c").Error; err != nil {
		t.Fatalf("唯一索引应允许新的非流水线空幂等键记录: %v", err)
	}
	if err := db.Exec(`UPDATE deployment_records SET idempotency_key = ? WHERE id = ?`, "same-key", "legacy-a").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE deployment_records SET idempotency_key = ? WHERE id = ?`, "same-key", "legacy-b").Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("幂等键唯一约束没有生效: %v", err)
	}
}

func TestDeploymentRollbackAttemptMigrationPreservesLegacyRecords(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:deployment_rollback_attempt_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.Exec(`CREATE TABLE deployment_records (id varchar(36) PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO deployment_records (id) VALUES (?)`, "legacy-rollback").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateDeploymentRollbackAttempts(db); err != nil {
		t.Fatalf("升级历史回滚尝试字段失败: %v", err)
	}
	if !db.Migrator().HasColumn(&model.DeploymentRecord{}, "rollback_source_id") ||
		!db.Migrator().HasColumn(&model.DeploymentRecord{}, "rollback_attempt") ||
		!db.Migrator().HasIndex(&model.DeploymentRecord{}, "idx_deployment_records_rollback_source_attempt") {
		t.Fatal("回滚来源、尝试次数字段或查询索引没有创建")
	}
	var sourceID string
	var attempt int
	if err := db.Table("deployment_records").Select("rollback_source_id", "rollback_attempt").
		Where("id = ?", "legacy-rollback").Row().Scan(&sourceID, &attempt); err != nil {
		t.Fatalf("读取迁移后的历史回滚记录失败: %v", err)
	}
	if sourceID != "" || attempt != 0 {
		t.Fatalf("迁移不应伪造历史回滚关系: source=%q attempt=%d", sourceID, attempt)
	}
}

func TestDeploymentPlanLifecycleMigrationPreservesLegacyPlans(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:deployment_plan_lifecycle_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.Exec(`CREATE TABLE deployment_plans (id varchar(36) PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO deployment_plans (id) VALUES (?), (?)`, "legacy-a", "legacy-b").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateDeploymentPlanLifecycle(db); err != nil {
		t.Fatalf("升级历史部署方案失败: %v", err)
	}
	if !db.Migrator().HasColumn(&model.DeploymentPlan{}, "deleted_at") ||
		!db.Migrator().HasIndex(&model.DeploymentPlan{}, "DeletedAt") {
		t.Fatal("部署方案软删除字段或索引没有创建")
	}
	var nullCount int64
	if err := db.Table("deployment_plans").Where("deleted_at IS NULL").Count(&nullCount).Error; err != nil || nullCount != 2 {
		t.Fatalf("历史部署方案不应被标记为已删除: count=%d err=%v", nullCount, err)
	}
	if err := db.Delete(&model.DeploymentPlan{ID: "legacy-a"}).Error; err != nil {
		t.Fatalf("新软删除字段无法使用: %v", err)
	}
	var visible, all int64
	if err := db.Model(&model.DeploymentPlan{}).Count(&visible).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Model(&model.DeploymentPlan{}).Count(&all).Error; err != nil {
		t.Fatal(err)
	}
	if visible != 1 || all != 2 {
		t.Fatalf("软删除默认范围错误: visible=%d all=%d", visible, all)
	}
}

func TestExecutionConfigMigrationsUpgradeNonemptySQLiteTables(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:execution_config_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.AutoMigrate(&model.BuildPlan{}, &model.DeploymentPlan{}, &model.DeploymentRecord{}); err != nil {
		t.Fatalf("创建升级前测试表失败: %v", err)
	}

	now := time.Now().UTC()
	buildPlan := model.BuildPlan{
		ID: "legacy-build", Name: "旧构建方案", Kind: model.BuildPlanDockerfile,
		Pull: true, CacheEnabled: true, BuildArgs: map[string]string{}, EnvironmentVariables: map[string]string{},
		TimeoutSeconds: 1800, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	deploymentPlan := model.DeploymentPlan{
		ID: "legacy-deployment", Name: "旧部署方案", Kind: model.DeploymentPlanScript,
		Script: "echo deploy", DockerConfig: model.DockerContainerConfig{},
		TimeoutSeconds: 600, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	deploymentRecord := model.DeploymentRecord{
		ID: "legacy-record", TargetID: "target", TargetName: "旧目标", Platform: model.DeploymentSSH,
		Operation: model.DeploymentRelease, Status: model.DeploymentSucceeded,
		DockerConfig: model.DockerContainerConfig{}, RequestedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&buildPlan, &deploymentPlan, &deploymentRecord} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("写入升级前测试数据失败: %v", err)
		}
	}

	removed := []struct {
		model any
		field string
	}{
		{&model.DeploymentPlan{}, "DeletedAt"},
		{&model.DeploymentPlan{}, "ComposeYAML"},
		{&model.DeploymentPlan{}, "DockerConfig"},
		{&model.DeploymentRecord{}, "ComposeYAML"},
		{&model.DeploymentRecord{}, "DockerConfig"},
		{&model.BuildPlan{}, "Pull"},
		{&model.BuildPlan{}, "CacheEnabled"},
		{&model.BuildPlan{}, "BuildArgs"},
		{&model.BuildPlan{}, "EnvironmentVariables"},
	}
	for _, column := range removed {
		if err := db.Migrator().DropColumn(column.model, column.field); err != nil {
			t.Fatalf("构造升级前字段缺失状态失败: %s: %v", column.field, err)
		}
	}

	if err := migrateDeploymentExecutionConfig(db); err != nil {
		t.Fatalf("升级部署执行配置失败: %v", err)
	}
	if err := migrateBuildExecutionConfig(db); err != nil {
		t.Fatalf("升级构建执行配置失败: %v", err)
	}

	var migratedPlan model.DeploymentPlan
	if err := db.First(&migratedPlan, "id = ?", deploymentPlan.ID).Error; err != nil {
		t.Fatalf("读取升级后的部署方案失败: %v", err)
	}
	if migratedPlan.ComposeYAML != "" || migratedPlan.DockerConfig.Network != "" {
		t.Fatalf("旧部署方案执行配置回填错误: %+v", migratedPlan)
	}
	var migratedRecord model.DeploymentRecord
	if err := db.First(&migratedRecord, "id = ?", deploymentRecord.ID).Error; err != nil {
		t.Fatalf("读取升级后的部署记录失败: %v", err)
	}
	if migratedRecord.ComposeYAML != "" || migratedRecord.DockerConfig.Network != "" {
		t.Fatalf("旧部署记录执行快照回填错误: %+v", migratedRecord)
	}
	var migratedBuild model.BuildPlan
	if err := db.First(&migratedBuild, "id = ?", buildPlan.ID).Error; err != nil {
		t.Fatalf("读取升级后的构建方案失败: %v", err)
	}
	if !migratedBuild.Pull || !migratedBuild.CacheEnabled || len(migratedBuild.BuildArgs) != 0 || len(migratedBuild.EnvironmentVariables) != 0 {
		t.Fatalf("旧构建方案执行配置回填错误: %+v", migratedBuild)
	}

	for _, column := range []struct {
		model any
		name  string
	}{
		{&model.DeploymentPlan{}, "compose_yaml"},
		{&model.DeploymentPlan{}, "docker_config"},
		{&model.DeploymentRecord{}, "compose_yaml"},
		{&model.DeploymentRecord{}, "docker_config"},
		{&model.BuildPlan{}, "pull"},
		{&model.BuildPlan{}, "cache_enabled"},
		{&model.BuildPlan{}, "build_args"},
		{&model.BuildPlan{}, "environment_variables"},
	} {
		assertNotNullWithoutDefault(t, db, column.model, column.name)
	}
}

func assertNotNullWithoutDefault(t *testing.T, db *gorm.DB, value any, column string) {
	t.Helper()
	columns, err := db.Migrator().ColumnTypes(value)
	if err != nil {
		t.Fatalf("读取字段 %s 结构失败: %v", column, err)
	}
	for _, current := range columns {
		if current.Name() != column {
			continue
		}
		if nullable, ok := current.Nullable(); !ok || nullable {
			t.Fatalf("字段 %s 没有收紧为 NOT NULL", column)
		}
		if defaultValue, ok := current.DefaultValue(); ok {
			t.Fatalf("字段 %s 不应保留数据库默认值，实际为 %q", column, defaultValue)
		}
		return
	}
	t.Fatalf("没有找到字段 %s", column)
}

func TestDockerSSHMigrationUpgradesExistingDatabase(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:docker_ssh_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)

	if err := db.AutoMigrate(&model.SchemaMigration{}); err != nil {
		t.Fatalf("创建迁移版本表失败: %v", err)
	}
	legacyStatements := []string{
		`CREATE TABLE docker_endpoints (
			id varchar(36) PRIMARY KEY, name varchar(128) NOT NULL, host varchar(1024) NOT NULL,
			tls_ciphertext text NOT NULL, is_active numeric NOT NULL DEFAULT 1,
			created_by varchar(36) NOT NULL, created_at datetime NOT NULL, updated_at datetime NOT NULL
		)`,
		`CREATE UNIQUE INDEX idx_docker_endpoints_name ON docker_endpoints(name)`,
		`CREATE TABLE deployment_targets (
			id varchar(36) PRIMARY KEY, name varchar(128) NOT NULL, platform varchar(16) NOT NULL,
			environment varchar(16) NOT NULL, runtime_id varchar(36) NOT NULL,
			namespace varchar(253) NOT NULL DEFAULT '', workload_name varchar(253) NOT NULL,
			container_name varchar(253) NOT NULL DEFAULT '', rollout_timeout integer NOT NULL DEFAULT 300,
			is_active numeric NOT NULL DEFAULT 1, created_by varchar(36) NOT NULL,
			created_at datetime NOT NULL, updated_at datetime NOT NULL
		)`,
		`CREATE UNIQUE INDEX idx_deployment_targets_name ON deployment_targets(name)`,
	}
	for _, statement := range legacyStatements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("创建旧数据库结构失败: %v", err)
		}
	}
	now := time.Now().UTC()
	if err := db.Exec(
		"INSERT INTO docker_endpoints (id,name,host,tls_ciphertext,is_active,created_by,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)",
		"docker-old", "旧 Docker", "unix:///var/run/docker.sock", "", true, "admin", now, now,
	).Error; err != nil {
		t.Fatalf("写入旧 Docker 连接失败: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO deployment_targets (id,name,platform,environment,runtime_id,workload_name,is_active,created_by,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)",
		"target-old", "旧环境", "docker", "development", "docker-old", "demo", true, "admin", now, now,
	).Error; err != nil {
		t.Fatalf("写入旧发布环境失败: %v", err)
	}
	for _, item := range migrations {
		if item.version == "202607270022" {
			break
		}
		if err := db.Create(&model.SchemaMigration{Version: item.version, AppliedAt: now}).Error; err != nil {
			t.Fatalf("写入旧迁移版本失败: %v", err)
		}
	}

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("升级旧数据库失败: %v", err)
	}
	for _, column := range []string{"ssh_credential_ciphertext", "ssh_host_key_fingerprint"} {
		if !db.Migrator().HasColumn(&model.DockerEndpoint{}, column) {
			t.Fatalf("Docker SSH 字段没有迁移: %s", column)
		}
	}
	if !db.Migrator().HasColumn(&model.DeploymentTarget{}, "description") {
		t.Fatal("发布环境说明字段没有迁移")
	}
	var endpoint model.DockerEndpoint
	if err := db.First(&endpoint, "id = ?", "docker-old").Error; err != nil {
		t.Fatalf("迁移后读取旧 Docker 连接失败: %v", err)
	}
	if endpoint.SSHCredentialCiphertext != "" || endpoint.SSHHostKeyFingerprint != "" {
		t.Fatalf("旧 Docker 连接迁移默认值错误: %+v", endpoint)
	}
}

func TestHostMigrationBackfillsLegacySSHDockerEndpoints(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:host_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)

	if err := db.Exec(`CREATE TABLE docker_endpoints (
		id varchar(36) PRIMARY KEY, name varchar(128) NOT NULL, host varchar(1024) NOT NULL,
		tls_ciphertext text NOT NULL, ssh_credential_ciphertext text NOT NULL,
		ssh_host_key_fingerprint varchar(128) NOT NULL DEFAULT '',
		is_active numeric NOT NULL DEFAULT 1, created_by varchar(36) NOT NULL,
		created_at datetime NOT NULL, updated_at datetime NOT NULL
	)`).Error; err != nil {
		t.Fatalf("创建旧 Docker 连接表失败: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_docker_endpoints_name ON docker_endpoints(name)`).Error; err != nil {
		t.Fatalf("创建旧 Docker 连接名称索引失败: %v", err)
	}
	now := time.Now().UTC()
	fingerprint := "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	legacy := []struct {
		id, name, host, ciphertext, fingerprint string
	}{
		{"docker-ssh-one", "开发 Docker", "ssh://deploy@docker.example.com:2202", "ciphertext-one", fingerprint},
		{"docker-ssh-two", "测试 Docker", "ssh://release@docker.example.com:2202", "ciphertext-two", fingerprint},
		{"docker-tcp", "兼容 Docker API", "tcp://docker.example.com:2376", "", ""},
	}
	for _, endpoint := range legacy {
		if err := db.Exec(
			`INSERT INTO docker_endpoints (
				id,name,host,tls_ciphertext,ssh_credential_ciphertext,ssh_host_key_fingerprint,
				is_active,created_by,created_at,updated_at
			) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			endpoint.id, endpoint.name, endpoint.host, "", endpoint.ciphertext, endpoint.fingerprint,
			true, "admin", now, now,
		).Error; err != nil {
			t.Fatalf("写入旧 Docker 连接失败: %v", err)
		}
	}

	for range 2 {
		if err := migrateHostsAndEnvironments(db); err != nil {
			t.Fatalf("执行主机模型迁移失败: %v", err)
		}
	}
	if !db.Migrator().HasTable(&model.Environment{}) || !db.Migrator().HasTable(&model.Host{}) ||
		!db.Migrator().HasTable(&model.HostCapability{}) ||
		!db.Migrator().HasColumn(&model.DockerEndpoint{}, "host_id") {
		t.Fatal("主机与环境模型结构没有完整创建")
	}

	var hosts []model.Host
	if err := db.Order("id ASC").Find(&hosts).Error; err != nil {
		t.Fatalf("读取迁移后的主机失败: %v", err)
	}
	if len(hosts) != 3 {
		t.Fatalf("SSH Docker 连接没有逐条迁移为主机: %+v", hosts)
	}
	for _, host := range hosts {
		if host.ID == model.BuiltinLocalHostID {
			if host.Name != "本地" || host.Mode != model.HostModeLocal || !host.IsBuiltin || !host.IsActive {
				t.Fatalf("内置本地主机字段错误: %+v", host)
			}
			continue
		}
		if host.ID != "docker-ssh-one" && host.ID != "docker-ssh-two" {
			t.Fatalf("迁移后的主机没有复用 Endpoint ID: %+v", host)
		}
		if host.Mode != model.HostModeSSH || host.Address != "docker.example.com" ||
			host.SSHPort != 2202 || host.SSHUsername == "" {
			t.Fatalf("迁移后的 SSH 主机字段错误: %+v", host)
		}
		if host.SSHCredentialCiphertext != "" || host.SSHAuthType != "" || host.EnvironmentID != "" {
			t.Fatalf("迁移不应复制旧密文、猜测认证方式或环境: %+v", host)
		}
		if host.SSHHostKeyFingerprint != fingerprint {
			t.Fatalf("迁移后的 SSH 主机指纹没有保留: %+v", host)
		}
	}

	var endpoints []model.DockerEndpoint
	if err := db.Order("id ASC").Find(&endpoints).Error; err != nil {
		t.Fatalf("读取迁移后的 Docker 连接失败: %v", err)
	}
	endpointByID := make(map[string]model.DockerEndpoint, len(endpoints))
	for _, endpoint := range endpoints {
		endpointByID[endpoint.ID] = endpoint
	}
	for _, expected := range legacy[:2] {
		endpoint := endpointByID[expected.id]
		if endpoint.HostID != endpoint.ID || endpoint.SSHCredentialCiphertext != expected.ciphertext {
			t.Fatalf("迁移改变了旧 Endpoint ID、归属或密文: %+v", endpoint)
		}
	}
	if endpointByID["docker-tcp"].HostID != "" {
		t.Fatalf("非 SSH Docker 连接不应自动映射主机: %+v", endpointByID["docker-tcp"])
	}

	var capabilities []model.HostCapability
	if err := db.Order("host_id ASC").Find(&capabilities).Error; err != nil {
		t.Fatalf("读取迁移后的主机能力失败: %v", err)
	}
	if len(capabilities) != 3 {
		t.Fatalf("Docker 主机能力没有逐条回填: %+v", capabilities)
	}
	for _, capability := range capabilities {
		if capability.HostID == model.BuiltinLocalHostID {
			if capability.Kind != model.HostCapabilityDocker ||
				capability.RuntimeID != "zrt-local-docker" ||
				capability.Status != model.HostCapabilityReady {
				t.Fatalf("内置本地主机能力错误: %+v", capability)
			}
			continue
		}
		if capability.Kind != model.HostCapabilityDocker || capability.RuntimeID != capability.HostID ||
			capability.Status != model.HostCapabilityUnchecked || capability.UseSudo {
			t.Fatalf("迁移后的 Docker 主机能力错误: %+v", capability)
		}
	}
}

func TestHostMigrationKeepsDisabledBuiltinCapabilities(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:host_builtin_capability_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.AutoMigrate(&model.DockerEndpoint{}); err != nil {
		t.Fatalf("创建 Docker 连接测试表失败: %v", err)
	}

	if err := migrateHostsAndEnvironments(db); err != nil {
		t.Fatalf("首次执行主机模型迁移失败: %v", err)
	}
	if err := db.Model(&model.Host{}).Where("id = ?", model.BuiltinLocalHostID).
		Update("name", "ZRT 本机").Error; err != nil {
		t.Fatalf("准备历史默认名失败: %v", err)
	}
	if err := db.Delete(&model.HostCapability{}, "host_id = ? AND kind = ?",
		model.BuiltinLocalHostID, model.HostCapabilityDocker).Error; err != nil {
		t.Fatalf("关闭本地 Docker 能力失败: %v", err)
	}

	if err := migrateHostsAndEnvironments(db); err != nil {
		t.Fatalf("重复执行主机模型迁移失败: %v", err)
	}
	var host model.Host
	if err := db.First(&host, "id = ?", model.BuiltinLocalHostID).Error; err != nil {
		t.Fatalf("读取内置本地主机失败: %v", err)
	}
	if host.Name != "本地" {
		t.Fatalf("历史默认名没有迁移为本地: %+v", host)
	}
	var capabilityCount int64
	if err := db.Model(&model.HostCapability{}).
		Where("host_id = ? AND kind = ?", model.BuiltinLocalHostID, model.HostCapabilityDocker).
		Count(&capabilityCount).Error; err != nil {
		t.Fatalf("统计本地 Docker 能力失败: %v", err)
	}
	if capabilityCount != 0 {
		t.Fatalf("重跑迁移恢复了已关闭的本地 Docker 能力: %d", capabilityCount)
	}

	if err := db.Model(&host).Update("name", "开发机").Error; err != nil {
		t.Fatalf("修改本地主机自定义名称失败: %v", err)
	}
	if err := migrateHostsAndEnvironments(db); err != nil {
		t.Fatalf("自定义名称后重跑迁移失败: %v", err)
	}
	if err := db.First(&host, "id = ?", model.BuiltinLocalHostID).Error; err != nil {
		t.Fatalf("读取自定义本地主机失败: %v", err)
	}
	if host.Name != "开发机" {
		t.Fatalf("迁移覆盖了自定义本地主机名称: %+v", host)
	}
}

func TestEnvironmentHostMigrationBackfillsLegacyMembership(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:environment_host_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.AutoMigrate(&model.Environment{}, &model.Host{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	environment := model.Environment{
		ID: "environment-legacy", Name: "历史环境", IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	host := model.Host{
		ID: "host-legacy", Name: "历史主机", Mode: model.HostModeSSH,
		EnvironmentID: environment.ID, IsActive: true, CreatedBy: "admin",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&environment).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateEnvironmentHosts(db); err != nil {
			t.Fatalf("迁移环境主机关联失败: %v", err)
		}
	}
	var memberships []model.EnvironmentHost
	if err := db.Find(&memberships).Error; err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 1 || memberships[0].EnvironmentID != environment.ID || memberships[0].HostID != host.ID {
		t.Fatalf("旧主机环境归属未准确回填: %+v", memberships)
	}
	if err := db.First(&host, "id = ?", host.ID).Error; err != nil {
		t.Fatal(err)
	}
	if host.EnvironmentID != "" {
		t.Fatalf("旧单值环境列回填后未清空: %+v", host)
	}
}

func TestExternalDatabaseMigration(t *testing.T) {
	tests := []struct {
		name   string
		driver string
		env    string
	}{
		{name: "PostgreSQL", driver: "postgres", env: "ZRT_TEST_POSTGRES_DSN"},
		{name: "MySQL", driver: "mysql", env: "ZRT_TEST_MYSQL_DSN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.env)
			if dsn == "" {
				t.Skipf("未设置 %s", test.env)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			db, err := Open(ctx, config.Database{
				Driver: test.driver, DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
			}, slog.Default())
			if err != nil {
				t.Fatalf("打开 %s 失败: %v", test.name, err)
			}
			defer func() { _ = Close(db) }()
			for range 2 {
				if err := Migrate(ctx, db); err != nil {
					t.Fatalf("执行 %s 迁移失败: %v", test.name, err)
				}
			}
			if err := VerifyMigrations(ctx, db); err != nil {
				t.Fatalf("验证 %s 迁移失败: %v", test.name, err)
			}
			if !db.Migrator().HasTable(&model.User{}) || !db.Migrator().HasTable(&model.DeploymentRecord{}) ||
				!db.Migrator().HasTable(&model.MonitorCheck{}) || !db.Migrator().HasTable(&model.Application{}) ||
				!db.Migrator().HasTable(&model.PipelineRun{}) || !db.Migrator().HasTable(&model.BuildRun{}) ||
				!db.Migrator().HasTable(&model.Artifact{}) {
				t.Fatalf("%s 核心表不完整", test.name)
			}
			assertStructuredWorkflowSchema(t, db, test.name)
			if !db.Migrator().HasColumn(&model.DeploymentRecord{}, "idempotency_key") ||
				!db.Migrator().HasIndex(&model.DeploymentRecord{}, "IdempotencyKey") {
				t.Fatalf("%s 流水线发布幂等字段或唯一索引未创建", test.name)
			}
			if !db.Migrator().HasColumn(&model.DeploymentRecord{}, "rollback_source_id") ||
				!db.Migrator().HasColumn(&model.DeploymentRecord{}, "rollback_attempt") ||
				!db.Migrator().HasIndex(&model.DeploymentRecord{}, "idx_deployment_records_rollback_source_attempt") {
				t.Fatalf("%s 回滚来源、尝试次数字段或查询索引未创建", test.name)
			}
			if !db.Migrator().HasColumn(&model.DeploymentPlan{}, "deleted_at") ||
				!db.Migrator().HasIndex(&model.DeploymentPlan{}, "DeletedAt") {
				t.Fatalf("%s 部署方案软删除字段或索引未创建", test.name)
			}
			if !db.Migrator().HasColumn(&model.ApplicationRepositoryObservation{}, "action") {
				t.Fatalf("%s PR 监听动作游标字段未创建", test.name)
			}
			exerciseExternalExecutionConfigUpgrade(t, db, test.driver)
			exerciseRepositoryObservationEnvironmentUpgrade(t, ctx, db, test.driver)
		})
	}
}

func exerciseRepositoryObservationEnvironmentUpgrade(t *testing.T, ctx context.Context, db *gorm.DB, databaseName string) {
	t.Helper()
	if err := db.Migrator().AddColumn(&legacyRepositoryObservationEnvironment{}, "Environment"); err != nil {
		t.Fatalf("构造 %s 仓库监听旧环境字段失败: %v", databaseName, err)
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE version = ?", "202607300052").Error; err != nil {
		t.Fatalf("重置 %s 仓库监听迁移版本失败: %v", databaseName, err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("升级 %s 仓库监听表失败: %v", databaseName, err)
	}
	hasEnvironment, err := physicalColumnExists(db, &model.ApplicationRepositoryObservation{}, "environment")
	if err != nil {
		t.Fatalf("检查 %s 仓库监听字段失败: %v", databaseName, err)
	}
	if hasEnvironment {
		t.Fatalf("%s 仓库监听表仍保留旧环境字段", databaseName)
	}
}

func exerciseExternalExecutionConfigUpgrade(t *testing.T, db *gorm.DB, prefix string) {
	t.Helper()
	now := time.Now().UTC()
	buildPlan := model.BuildPlan{
		ID: prefix + "-upgrade-build", Name: prefix + "-旧构建方案", Kind: model.BuildPlanDockerfile,
		Pull: true, CacheEnabled: true, BuildArgs: map[string]string{}, EnvironmentVariables: map[string]string{},
		TimeoutSeconds: 1800, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	deploymentPlan := model.DeploymentPlan{
		ID: prefix + "-upgrade-deployment", Name: prefix + "-旧部署方案", Kind: model.DeploymentPlanScript,
		Script: "echo deploy", DockerConfig: model.DockerContainerConfig{},
		TimeoutSeconds: 600, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	deploymentRecord := model.DeploymentRecord{
		ID: prefix + "-upgrade-record", TargetID: "target", TargetName: "旧目标", Platform: model.DeploymentSSH,
		Operation: model.DeploymentRelease, Status: model.DeploymentSucceeded,
		DockerConfig: model.DockerContainerConfig{}, RequestedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&deploymentRecord, &deploymentPlan, &buildPlan} {
		if err := db.Unscoped().Delete(value).Error; err != nil {
			t.Fatalf("清理 %s 升级测试数据失败: %v", prefix, err)
		}
	}
	for _, value := range []any{&buildPlan, &deploymentPlan, &deploymentRecord} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("写入 %s 升级前测试数据失败: %v", prefix, err)
		}
	}

	removed := []struct {
		model any
		field string
	}{
		{&model.DeploymentPlan{}, "DeletedAt"},
		{&model.DeploymentPlan{}, "ComposeYAML"},
		{&model.DeploymentPlan{}, "DockerConfig"},
		{&model.DeploymentRecord{}, "ComposeYAML"},
		{&model.DeploymentRecord{}, "DockerConfig"},
		{&model.BuildPlan{}, "Pull"},
		{&model.BuildPlan{}, "CacheEnabled"},
		{&model.BuildPlan{}, "BuildArgs"},
		{&model.BuildPlan{}, "EnvironmentVariables"},
	}
	for _, column := range removed {
		if err := db.Migrator().DropColumn(column.model, column.field); err != nil {
			t.Fatalf("构造 %s 升级前字段缺失状态失败: %s: %v", prefix, column.field, err)
		}
	}
	if err := migrateDeploymentExecutionConfig(db); err != nil {
		t.Fatalf("升级 %s 部署执行配置失败: %v", prefix, err)
	}
	if err := migrateBuildExecutionConfig(db); err != nil {
		t.Fatalf("升级 %s 构建执行配置失败: %v", prefix, err)
	}

	var migratedBuild model.BuildPlan
	if err := db.First(&migratedBuild, "id = ?", buildPlan.ID).Error; err != nil {
		t.Fatalf("读取 %s 升级后的构建方案失败: %v", prefix, err)
	}
	if !migratedBuild.Pull || !migratedBuild.CacheEnabled || len(migratedBuild.BuildArgs) != 0 || len(migratedBuild.EnvironmentVariables) != 0 {
		t.Fatalf("%s 旧构建方案执行配置回填错误: %+v", prefix, migratedBuild)
	}
	for _, column := range []struct {
		model any
		name  string
	}{
		{&model.DeploymentPlan{}, "compose_yaml"},
		{&model.DeploymentPlan{}, "docker_config"},
		{&model.DeploymentRecord{}, "compose_yaml"},
		{&model.DeploymentRecord{}, "docker_config"},
		{&model.BuildPlan{}, "pull"},
		{&model.BuildPlan{}, "cache_enabled"},
		{&model.BuildPlan{}, "build_args"},
		{&model.BuildPlan{}, "environment_variables"},
	} {
		assertNotNullWithoutDefault(t, db, column.model, column.name)
	}
}

func assertStructuredWorkflowSchema(t *testing.T, db *gorm.DB, databaseName string) {
	t.Helper()
	for _, table := range []any{&model.ReleaseWorkflow{}, &model.ReleaseWorkflowTemplate{}} {
		if !db.Migrator().HasTable(table) ||
			!db.Migrator().HasColumn(table, "schema_version") ||
			!db.Migrator().HasColumn(table, "source") ||
			!db.Migrator().HasColumn(table, "stages") {
			t.Fatalf("%s 阶段式流水线表结构不完整: %T", databaseName, table)
		}
		for _, legacyColumn := range []string{"nodes", "edges", "viewport"} {
			if db.Migrator().HasColumn(table, legacyColumn) {
				t.Fatalf("%s 仍保留旧流水线字段 %s: %T", databaseName, legacyColumn, table)
			}
		}
	}
	for _, field := range []string{"trigger_action", "source_branch", "target_branch", "event_dedup_key"} {
		if !db.Migrator().HasColumn(&model.PipelineRun{}, field) {
			t.Fatalf("%s 流水线运行缺少事件去重字段 %s", databaseName, field)
		}
	}
}
