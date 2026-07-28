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
		!db.Migrator().HasTable(&model.DeploymentTarget{}) || !db.Migrator().HasTable(&model.DeploymentRecord{}) ||
		!db.Migrator().HasTable(&model.Configuration{}) || !db.Migrator().HasTable(&model.ConfigurationRevision{}) ||
		!db.Migrator().HasTable(&model.NotificationChannel{}) || !db.Migrator().HasTable(&model.Notification{}) ||
		!db.Migrator().HasTable(&model.ScheduledTask{}) || !db.Migrator().HasTable(&model.MonitorRule{}) ||
		!db.Migrator().HasTable(&model.MonitorCheck{}) || !db.Migrator().HasTable(&model.Application{}) ||
		!db.Migrator().HasTable(&model.BuildPlan{}) || !db.Migrator().HasTable(&model.ImageRegistry{}) ||
		!db.Migrator().HasTable(&model.DeploymentPlan{}) || !db.Migrator().HasTable(&model.ReleasePlan{}) ||
		!db.Migrator().HasTable(&model.ReleaseGroup{}) || !db.Migrator().HasTable(&model.PipelineRun{}) ||
		!db.Migrator().HasTable(&model.PipelineRunLog{}) ||
		!db.Migrator().HasTable(&model.ApplicationRepository{}) || !db.Migrator().HasTable(&model.ApplicationRepositoryObservation{}) ||
		!db.Migrator().HasTable(&model.PipelineRunRepository{}) ||
		!db.Migrator().HasTable(&model.UserPermission{}) || !db.Migrator().HasTable(&model.GitCredential{}) ||
		!db.Migrator().HasTable(&model.DNSProviderAccount{}) || !db.Migrator().HasTable(&model.DNSDomain{}) {
		t.Fatal("核心任务表未创建")
	}
	if !db.Migrator().HasColumn(&model.PipelineRun{}, "retry_of_id") {
		t.Fatal("流水线重新执行来源字段未创建")
	}
	if !db.Migrator().HasColumn(&model.ReleaseGroupApplication{}, "manual_deploy") ||
		!db.Migrator().HasColumn(&model.ReleaseGroupApplication{}, "source_type") ||
		!db.Migrator().HasColumn(&model.ReleaseGroupApplication{}, "source_value") {
		t.Fatal("发布计划应用的手动版本来源字段未创建")
	}
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

func TestReleasePlanningMigrationPreservesLegacyDeploymentPlans(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: ":memory:", MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)

	if err := db.Table("release_plans").AutoMigrate(&model.DeploymentPlan{}); err != nil {
		t.Fatalf("创建旧部署方案表失败: %v", err)
	}
	now := time.Now().UTC()
	legacyPlan := model.DeploymentPlan{
		ID: "deploy-legacy", Name: "旧 Helm 方案", Kind: model.DeploymentPlanHelm,
		HelmChart: "deploy/chart", TimeoutSeconds: 600, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Table("release_plans").Create(&legacyPlan).Error; err != nil {
		t.Fatalf("写入旧部署方案失败: %v", err)
	}
	if err := db.AutoMigrate(&model.GitRepository{}, &model.Application{}); err != nil {
		t.Fatalf("创建旧应用表失败: %v", err)
	}
	if err := db.Exec("ALTER TABLE git_repositories ADD COLUMN build_plan_id varchar(36) NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatalf("添加旧构建方案字段失败: %v", err)
	}
	if err := db.Exec("ALTER TABLE git_repositories ADD COLUMN release_plan_id varchar(36) NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatalf("添加旧部署方案字段失败: %v", err)
	}
	repository := model.GitRepository{
		ID: "repo-legacy", Name: "旧仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/app.git", AuthType: model.GitAuthNone,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatalf("写入旧仓库失败: %v", err)
	}
	if err := db.Model(&model.GitRepository{}).Where("id = ?", repository.ID).
		Updates(map[string]any{"build_plan_id": "build-legacy", "release_plan_id": legacyPlan.ID}).Error; err != nil {
		t.Fatalf("写入旧仓库方案失败: %v", err)
	}
	application := model.Application{
		ID: "app-legacy", Name: "旧应用", RepositoryID: repository.ID, Branch: "main",
		PollIntervalSeconds: 60, WatchPush: true, SyncStatus: model.ApplicationSyncIdle,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&application).Error; err != nil {
		t.Fatalf("写入旧应用失败: %v", err)
	}

	if err := migrateReleasePlanning(db); err != nil {
		t.Fatalf("执行发布模型迁移失败: %v", err)
	}
	var migratedPlan model.DeploymentPlan
	if err := db.First(&migratedPlan, "id = ?", legacyPlan.ID).Error; err != nil || migratedPlan.HelmChart != legacyPlan.HelmChart {
		t.Fatalf("旧部署方案没有完整保留: plan=%+v err=%v", migratedPlan, err)
	}
	if !db.Migrator().HasTable(&model.ReleasePlan{}) || !db.Migrator().HasTable(&model.ReleaseGroup{}) {
		t.Fatal("新的发布计划和发布组表未创建")
	}
	if err := db.First(&application, "id = ?", application.ID).Error; err != nil {
		t.Fatal(err)
	}
	if application.BuildPlanID != "build-legacy" || application.DeploymentPlanID != legacyPlan.ID {
		t.Fatalf("旧仓库方案没有迁移到应用: %+v", application)
	}
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

func TestPipelineExecutionMigrationRepairsLegacyRuns(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:pipeline_execution_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.AutoMigrate(
		&model.GitRepository{}, &model.Application{}, &model.ApplicationEnvironment{},
		&model.ReleaseWorkflowTemplate{}, &model.ReleaseWorkflow{},
		&model.PipelineRun{}, &model.PipelineRunRepository{}, &model.DeploymentRecord{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	repository := model.GitRepository{
		ID: "repo-execution", Name: "执行迁移仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/app.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	application := model.Application{
		ID: "app-execution", Name: "执行迁移应用", RepositoryID: repository.ID, Branch: "main",
		WatchPush: true, PollIntervalSeconds: 3, ImageRegistryID: "registry-execution",
		SyncStatus: model.ApplicationSyncIdle, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&application).Error; err != nil {
		t.Fatal(err)
	}
	template := model.ReleaseWorkflowTemplate{
		ID: "template-execution", Name: "执行迁移模板", Revision: 2, IsActive: false,
		Nodes: []model.WorkflowNode{{
			ID: "deploy-prod", Type: model.WorkflowNodeDeploy, Name: "部署生产",
			Config: model.WorkflowNodeConfig{Environment: "prod", DeploymentTargetID: "target-execution"},
		}},
		Edges: []model.WorkflowEdge{}, Viewport: model.WorkflowViewport{Zoom: 1},
		CreatedBy: "admin", UpdatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}
	workflow := model.ReleaseWorkflow{
		ID: "workflow-execution", ApplicationID: application.ID, WorkflowTemplateID: template.ID,
		Name: "旧应用流水线", Revision: 1, IsActive: true,
		Nodes: []model.WorkflowNode{{
			ID: "deploy-prod", Type: model.WorkflowNodeDeploy, Name: "部署生产",
			Config: model.WorkflowNodeConfig{Environment: "prod"},
		}},
		Edges: []model.WorkflowEdge{}, Viewport: model.WorkflowViewport{Zoom: 1},
		CreatedBy: "admin", UpdatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&workflow).Error; err != nil {
		t.Fatal(err)
	}
	environment := model.ApplicationEnvironment{
		ID: "environment-execution", ApplicationID: application.ID, Key: "prod", Name: "生产",
		Branch: "main", WatchPush: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&environment).Error; err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{
		ID: "run-execution", ApplicationID: application.ID, Trigger: "manual", Ref: "refs/heads/main",
		CommitSHA: "0123456789012345678901234567890123456789", Status: model.PipelineRunSucceeded,
		Stage: "completed", CurrentNodeID: "deploy-prod", WorkflowSnapshot: "{}",
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	component := model.PipelineRunRepository{
		ID: "component-execution", PipelineRunID: run.ID, RepositoryID: repository.ID,
		Status: model.PipelineRunRepositorySucceeded, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&component).Error; err != nil {
		t.Fatal(err)
	}

	if err := migratePipelineExecutionFields(db); err != nil {
		t.Fatalf("执行流水线迁移失败: %v", err)
	}
	if err := db.First(&component, "id = ?", component.ID).Error; err != nil || component.ImageRegistryID != application.ImageRegistryID {
		t.Fatalf("运行没有补齐镜像仓库快照: component=%+v err=%v", component, err)
	}
	if err := db.First(&workflow, "id = ?", workflow.ID).Error; err != nil || workflow.Nodes[0].Config.DeploymentTargetID != "target-execution" {
		t.Fatalf("应用流水线没有补齐发布环境: workflow=%+v err=%v", workflow, err)
	}
	if err := db.First(&environment, "id = ?", environment.ID).Error; err != nil || environment.DeploymentTargetID != "target-execution" {
		t.Fatalf("应用环境没有补齐发布环境: environment=%+v err=%v", environment, err)
	}
	if err := db.First(&run, "id = ?", run.ID).Error; err != nil || run.Status != model.PipelineRunFailed || run.Stage != "execution_missing" {
		t.Fatalf("历史伪成功运行没有改为失败: run=%+v err=%v", run, err)
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
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
				!db.Migrator().HasTable(&model.PipelineRun{}) {
				t.Fatalf("%s 核心表不完整", test.name)
			}
		})
	}
}

type legacyRepositoryObservation struct {
	ID                      string `gorm:"type:varchar(36);primaryKey"`
	ApplicationRepositoryID string `gorm:"type:varchar(36);not null;uniqueIndex:idx_repository_environment,priority:1"`
	Environment             string `gorm:"type:varchar(16);not null;uniqueIndex:idx_repository_environment,priority:2"`
	Ref                     string `gorm:"type:varchar(512);not null;default:''"`
	CommitSHA               string `gorm:"type:varchar(64);not null;default:''"`
	LastCheckedAt           *time.Time
	CreatedAt               time.Time `gorm:"not null"`
	UpdatedAt               time.Time `gorm:"not null"`
}

func (legacyRepositoryObservation) TableName() string {
	return "application_repository_observations"
}

func TestRepositoryObservationMigrationSupportsMultipleCustomRefs(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:repository_observation_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.AutoMigrate(&legacyRepositoryObservation{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, observation := range []legacyRepositoryObservation{
		{ID: "legacy-test", ApplicationRepositoryID: "app-repo", Environment: "test", Ref: "refs/heads/test", CommitSHA: "test-1", CreatedAt: now, UpdatedAt: now},
		{ID: "legacy-prod", ApplicationRepositoryID: "app-repo", Environment: "prod", Ref: "refs/heads/main", CommitSHA: "main-1", CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&observation).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateRepositoryObservationWatchKeys(db); err != nil {
		t.Fatalf("迁移仓库监听游标失败: %v", err)
	}
	if db.Migrator().HasIndex(&model.ApplicationRepositoryObservation{}, "idx_repository_environment") ||
		!db.Migrator().HasIndex(&model.ApplicationRepositoryObservation{}, "idx_repository_watch") {
		t.Fatal("仓库监听游标唯一索引没有完成迁移")
	}
	var migrated []model.ApplicationRepositoryObservation
	if err := db.Order("id ASC").Find(&migrated).Error; err != nil || len(migrated) != 2 {
		t.Fatalf("读取迁移后的监听游标失败: observations=%+v err=%v", migrated, err)
	}
	for i := range migrated {
		if migrated[i].WatchKey != "legacy:"+migrated[i].ID {
			t.Fatalf("旧监听游标没有保留为兼容基线: %+v", migrated[i])
		}
	}
	additional := model.ApplicationRepositoryObservation{
		ID: "custom-test-pr", ApplicationRepositoryID: "app-repo", WatchKey: "custom-pr-key",
		SourceNodeID: "trigger-custom", Event: "pr", Environment: "test", Ref: "refs/pull/8/head",
		CommitSHA: "pr-1", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&additional).Error; err != nil {
		t.Fatalf("同一环境不能保存独立 PR 游标: %v", err)
	}
}

func TestDeploymentApprovalMigrationCancelsLegacyPendingRecords(t *testing.T) {
	db, err := Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:deployment_approval_upgrade?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.AutoMigrate(&model.DeploymentRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := model.DeploymentRecord{
		ID: "legacy-awaiting-approval", TargetID: "production-target", TargetName: "生产环境",
		Platform: model.DeploymentDocker, Environment: model.EnvironmentProduction,
		RuntimeID: "docker-1", WorkloadName: "api", RolloutTimeout: 300,
		Operation: model.DeploymentRelease, Image: "registry.example.com/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status: model.DeploymentAwaitingApproval, RequestedBy: "operator", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateDeploymentApprovalsToWorkflow(db); err != nil {
		t.Fatalf("迁移历史待审批发布失败: %v", err)
	}
	if err := db.First(&record, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.Status != model.DeploymentCanceled || record.ErrorCode != "legacy_environment_approval_removed" || record.FinishedAt == nil {
		t.Fatalf("历史待审批记录没有安全取消: %+v", record)
	}
}
