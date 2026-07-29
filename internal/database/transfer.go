package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/model"
)

var (
	ErrTransferUnsupported = errors.New("只能将 SQLite 数据库迁移到 MySQL 或 PostgreSQL")
	ErrInvalidTarget       = errors.New("目标数据库配置无效")
	ErrTargetNotEmpty      = errors.New("目标数据库必须为空库")
	ErrTransferRunning     = errors.New("已有数据库迁移任务正在执行")
	ErrTargetTestRequired  = errors.New("请先测试目标数据库连接")
	ErrActiveJobs          = errors.New("当前仍有待执行或运行中的任务，暂不能迁移")
)

const targetTestTTL = 10 * time.Minute

type TransferTarget struct {
	Driver string `json:"driver"`
	DSN    string `json:"-"`
}

type TargetTestResult struct {
	Token     string    `json:"test_token"`
	Driver    string    `json:"driver"`
	ExpiresAt time.Time `json:"expires_at"`
}

type TransferStatus struct {
	ID              string     `json:"id,omitempty"`
	SourceDriver    string     `json:"source_driver"`
	TargetDriver    string     `json:"target_driver,omitempty"`
	State           string     `json:"state"`
	Message         string     `json:"message"`
	TotalTables     int        `json:"total_tables"`
	CompletedTables int        `json:"completed_tables"`
	CopiedRows      int64      `json:"copied_rows"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	RequiresRestart bool       `json:"requires_restart"`
}

type testedTarget struct {
	digest    [32]byte
	expiresAt time.Time
}

type TransferService struct {
	ctx          context.Context
	source       *gorm.DB
	sourceDriver string
	logger       *slog.Logger

	mu     sync.RWMutex
	tests  map[string]testedTarget
	status TransferStatus
}

func NewTransferService(ctx context.Context, source *gorm.DB, sourceDriver string, logger *slog.Logger) *TransferService {
	sourceDriver = strings.ToLower(strings.TrimSpace(sourceDriver))
	return &TransferService{
		ctx: ctx, source: source, sourceDriver: sourceDriver, logger: logger,
		tests:  make(map[string]testedTarget),
		status: TransferStatus{SourceDriver: sourceDriver, State: "idle", Message: "尚未执行数据库迁移"},
	}
}

func (s *TransferService) Status() TransferStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *TransferService) InProgress() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status.State == "preparing" || s.status.State == "migrating"
}

func (s *TransferService) TestTarget(ctx context.Context, input TransferTarget) (TargetTestResult, error) {
	target, err := s.normalizeTarget(input)
	if err != nil {
		return TargetTestResult{}, err
	}
	db, err := Open(ctx, target, s.logger)
	if err != nil {
		return TargetTestResult{}, fmt.Errorf("连接目标数据库失败: %w", err)
	}
	defer func() {
		if closeErr := Close(db); closeErr != nil {
			s.logger.Error("测试后关闭目标数据库失败", "operation", "database_transfer_test_close", "driver", target.Driver, "err", closeErr)
		}
	}()
	if err := ensureEmptyTransferTarget(db); err != nil {
		return TargetTestResult{}, err
	}

	now := time.Now().UTC()
	result := TargetTestResult{Token: uuid.NewString(), Driver: target.Driver, ExpiresAt: now.Add(targetTestTTL)}
	s.mu.Lock()
	for token, tested := range s.tests {
		if tested.expiresAt.Before(now) {
			delete(s.tests, token)
		}
	}
	s.tests[result.Token] = testedTarget{digest: targetDigest(input), expiresAt: result.ExpiresAt}
	s.mu.Unlock()
	return result, nil
}

func (s *TransferService) Start(input TransferTarget, testToken, confirmation string) (TransferStatus, error) {
	target, err := s.normalizeTarget(input)
	if err != nil {
		return TransferStatus{}, err
	}
	if confirmation != "MIGRATE" {
		return TransferStatus{}, ErrInvalidTarget
	}
	now := time.Now().UTC()
	digest := targetDigest(input)

	s.mu.Lock()
	if s.status.State == "preparing" || s.status.State == "migrating" {
		s.mu.Unlock()
		return TransferStatus{}, ErrTransferRunning
	}
	tested, ok := s.tests[strings.TrimSpace(testToken)]
	if !ok || tested.expiresAt.Before(now) || tested.digest != digest {
		s.mu.Unlock()
		return TransferStatus{}, ErrTargetTestRequired
	}
	delete(s.tests, strings.TrimSpace(testToken))
	startedAt := time.Now().UTC()
	status := TransferStatus{
		ID: uuid.NewString(), SourceDriver: s.sourceDriver, TargetDriver: target.Driver,
		State: "preparing", Message: "正在检查待执行任务", StartedAt: &startedAt,
	}
	s.status = status
	s.mu.Unlock()

	var activeJobs int64
	if err := s.source.WithContext(s.ctx).Model(&model.Job{}).
		Where("status IN ?", []model.JobStatus{model.JobPending, model.JobRunning}).Count(&activeJobs).Error; err != nil {
		s.resetPreparing(status.ID)
		return TransferStatus{}, fmt.Errorf("检查运行中任务失败: %w", err)
	}
	if activeJobs > 0 {
		s.resetPreparing(status.ID)
		return TransferStatus{}, ErrActiveJobs
	}

	s.updateStatus(status.ID, func(current *TransferStatus) { current.Message = "正在准备目标数据库" })

	go s.run(target)
	return status, nil
}

func (s *TransferService) resetPreparing(transferID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.ID == transferID && s.status.State == "preparing" {
		s.status = TransferStatus{SourceDriver: s.sourceDriver, State: "idle", Message: "尚未执行数据库迁移"}
	}
}

func (s *TransferService) run(target config.Database) {
	transferID := s.Status().ID
	targetDB, err := Open(s.ctx, target, s.logger)
	if err != nil {
		s.fail(transferID, "无法连接目标数据库", err)
		return
	}
	defer func() {
		if closeErr := Close(targetDB); closeErr != nil {
			s.logger.Error("迁移后关闭目标数据库失败", "operation", "database_transfer_close", "transfer_id", transferID, "driver", target.Driver, "err", closeErr)
		}
	}()
	if err := ensureEmptyTransferTarget(targetDB); err != nil {
		s.fail(transferID, err.Error(), err)
		return
	}
	if err := Migrate(s.ctx, targetDB); err != nil {
		s.fail(transferID, "初始化目标数据库结构失败", err)
		return
	}
	s.updateStatus(transferID, func(status *TransferStatus) {
		status.State = "migrating"
		status.Message = "正在复制 SQLite 数据快照"
	})
	if err := copyDatabaseSnapshot(s.ctx, s.source, targetDB, target.Driver, func(total, completed int, rows int64) {
		s.updateStatus(transferID, func(status *TransferStatus) {
			status.TotalTables, status.CompletedTables, status.CopiedRows = total, completed, rows
		})
	}); err != nil {
		s.fail(transferID, "复制数据失败，目标库不会自动覆盖或清空", err)
		return
	}
	completedAt := time.Now().UTC()
	s.updateStatus(transferID, func(status *TransferStatus) {
		status.State = "succeeded"
		status.Message = "数据快照已迁移；请停止当前服务，切换数据库环境变量后重启 ZRT"
		status.CompletedAt = &completedAt
		status.RequiresRestart = true
	})
	s.logger.Info("数据库迁移完成", "operation", "database_transfer", "transfer_id", transferID, "source_driver", s.sourceDriver, "target_driver", target.Driver)
}

func (s *TransferService) fail(transferID, message string, err error) {
	completedAt := time.Now().UTC()
	s.updateStatus(transferID, func(status *TransferStatus) {
		status.State = "failed"
		status.Message = message
		status.CompletedAt = &completedAt
	})
	s.logger.Error("数据库迁移失败", "operation", "database_transfer", "transfer_id", transferID, "source_driver", s.sourceDriver, "target_driver", s.Status().TargetDriver, "err", err)
}

func (s *TransferService) updateStatus(transferID string, update func(*TransferStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.ID != transferID {
		return
	}
	update(&s.status)
}

func (s *TransferService) normalizeTarget(input TransferTarget) (config.Database, error) {
	if s.sourceDriver != "sqlite" {
		return config.Database{}, ErrTransferUnsupported
	}
	driver := strings.ToLower(strings.TrimSpace(input.Driver))
	dsn := strings.TrimSpace(input.DSN)
	if (driver != "mysql" && driver != "postgres") || dsn == "" || len(dsn) > 4096 {
		return config.Database{}, ErrInvalidTarget
	}
	return config.Database{
		Driver: driver, DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Hour,
	}, nil
}

func targetDigest(input TransferTarget) [32]byte {
	return sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(input.Driver)) + "\x00" + strings.TrimSpace(input.DSN)))
}

func ensureEmptyTransferTarget(db *gorm.DB) error {
	tables, err := db.Migrator().GetTables()
	if err != nil {
		return fmt.Errorf("检查目标数据库失败: %w", err)
	}
	known := knownTransferTableSet()
	for _, table := range tables {
		if strings.HasPrefix(table, "sqlite_") {
			continue
		}
		if _, ok := known[table]; !ok {
			return ErrTargetNotEmpty
		}
		if table == "schema_migrations" {
			continue
		}
		var count int64
		query := db.Table(table)
		switch table {
		case model.Host{}.TableName():
			query = query.Where("id <> ?", model.BuiltinLocalHostID)
		case model.HostCapability{}.TableName():
			query = query.Where(
				"host_id <> ? OR kind <> ? OR runtime_id <> ?",
				model.BuiltinLocalHostID,
				model.HostCapabilityDocker,
				"zrt-local-docker",
			)
		}
		if err := query.Limit(1).Count(&count).Error; err != nil {
			return fmt.Errorf("检查目标数据表 %s 失败: %w", table, err)
		}
		if count > 0 {
			return ErrTargetNotEmpty
		}
	}
	return nil
}

func copyDatabaseSnapshot(
	ctx context.Context,
	source, target *gorm.DB,
	targetDriver string,
	progress func(total, completed int, rows int64),
) error {
	sourceTables, err := source.Migrator().GetTables()
	if err != nil {
		return fmt.Errorf("读取 SQLite 数据表失败: %w", err)
	}
	available := make(map[string]struct{}, len(sourceTables))
	known := knownTransferTableSet()
	for _, table := range sourceTables {
		if strings.HasPrefix(table, "sqlite_") {
			continue
		}
		if table == "schema_migrations" {
			continue
		}
		if _, ok := known[table]; !ok {
			return fmt.Errorf("发现不受支持的数据表 %s", table)
		}
		available[table] = struct{}{}
	}
	tables := make([]string, 0, len(available))
	for _, table := range orderedTransferTables {
		if _, ok := available[table]; ok {
			tables = append(tables, table)
			delete(available, table)
		}
	}
	if len(available) > 0 {
		remaining := make([]string, 0, len(available))
		for table := range available {
			remaining = append(remaining, table)
		}
		sort.Strings(remaining)
		tables = append(tables, remaining...)
	}
	progress(len(tables), 0, 0)

	return source.WithContext(ctx).Transaction(func(snapshot *gorm.DB) error {
		return target.WithContext(ctx).Transaction(func(destination *gorm.DB) error {
			// 新库迁移会创建内置本地主机。复制前先移除这两条确定性的种子记录，
			// 由源库快照恢复，避免主键冲突，也不会把真实业务数据误判为空库。
			if err := destination.Where("host_id = ?", model.BuiltinLocalHostID).
				Delete(&model.HostCapability{}).Error; err != nil {
				return fmt.Errorf("清理目标库内置主机能力失败: %w", err)
			}
			if err := destination.Delete(&model.Host{}, "id = ?", model.BuiltinLocalHostID).Error; err != nil {
				return fmt.Errorf("清理目标库内置主机失败: %w", err)
			}
			var copiedRows int64
			for index, table := range tables {
				rows, err := copyTable(snapshot, destination, table, targetDriver)
				if err != nil {
					return err
				}
				copiedRows += rows
				progress(len(tables), index+1, copiedRows)
			}
			if targetDriver == "postgres" {
				if err := resetPostgresSequences(destination); err != nil {
					return err
				}
			}
			return nil
		})
	}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelSerializable})
}

func copyTable(source, target *gorm.DB, table, targetDriver string) (int64, error) {
	rows, err := source.Table(table).Rows()
	if err != nil {
		return 0, fmt.Errorf("读取数据表 %s 失败: %w", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("读取数据表 %s 列信息失败: %w", table, err)
	}
	targetTypes := make(map[string]string, len(columns))
	columnTypes, err := target.Migrator().ColumnTypes(table)
	if err != nil {
		return 0, fmt.Errorf("读取目标数据表 %s 列类型失败: %w", table, err)
	}
	for _, columnType := range columnTypes {
		targetTypes[columnType.Name()] = strings.ToLower(columnType.DatabaseTypeName())
	}
	batch := make([]map[string]any, 0, 200)
	var copied int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := target.Table(table).Create(&batch).Error; err != nil {
			return fmt.Errorf("写入目标数据表 %s 失败: %w", table, err)
		}
		copied += int64(len(batch))
		batch = make([]map[string]any, 0, 200)
		return nil
	}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return copied, fmt.Errorf("读取数据表 %s 行失败: %w", table, err)
		}
		item := make(map[string]any, len(columns))
		for i, column := range columns {
			item[column] = normalizeTransferValue(values[i], targetDriver, targetTypes[column])
		}
		batch = append(batch, item)
		if len(batch) == cap(batch) {
			if err := flush(); err != nil {
				return copied, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return copied, fmt.Errorf("遍历数据表 %s 失败: %w", table, err)
	}
	if err := flush(); err != nil {
		return copied, err
	}
	return copied, nil
}

func normalizeTransferValue(value any, targetDriver, databaseType string) any {
	if value == nil || targetDriver != "postgres" {
		return value
	}
	if databaseType == "bool" || databaseType == "boolean" {
		switch current := value.(type) {
		case bool:
			return current
		case int64:
			return current != 0
		case []byte:
			return string(current) == "1" || strings.EqualFold(string(current), "true")
		case string:
			return current == "1" || strings.EqualFold(current, "true")
		}
	}
	if strings.Contains(databaseType, "timestamp") || databaseType == "date" {
		if parsed, ok := parseTransferTime(value); ok {
			return parsed
		}
	}
	if bytes, ok := value.([]byte); ok && databaseType != "bytea" {
		return string(bytes)
	}
	return value
}

func parseTransferTime(value any) (time.Time, bool) {
	if current, ok := value.(time.Time); ok {
		return current, true
	}
	text := ""
	switch current := value.(type) {
	case string:
		text = current
	case []byte:
		text = string(current)
	default:
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func resetPostgresSequences(db *gorm.DB) error {
	for _, table := range []string{"pipeline_run_logs", "outbox_events"} {
		statement := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE(MAX(id), 1), COUNT(*) > 0) FROM "%s"`,
			table, table,
		)
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("重置 PostgreSQL 数据表 %s 自增序列失败: %w", table, err)
		}
	}
	return nil
}

var orderedTransferTables = []string{
	"users", "roles", "role_permissions", "user_roles", "user_permissions",
	"git_credentials", "identity_providers", "external_identities",
	"configurations", "configuration_revisions", "audit_logs",
	"dns_provider_accounts", "dns_domains",
	"build_plans", "image_registries", "deployment_plans", "release_workflow_templates",
	"git_repositories", "git_webhook_deliveries",
	"environments", "hosts", "environment_hosts", "docker_endpoints", "kubernetes_clusters", "host_capabilities", "deployment_targets",
	"applications", "application_environments", "application_repositories", "application_repository_observations", "release_workflows",
	"release_plans", "release_groups", "release_group_applications", "release_group_dependencies", "release_plan_executions", "release_plan_execution_items",
	"pipeline_runs", "pipeline_run_repositories", "pipeline_run_approvals", "pipeline_run_logs", "deployment_records",
	"notification_channels", "notifications", "scheduled_tasks", "monitor_rules", "monitor_checks",
	"jobs", "outbox_events",
}

func knownTransferTableSet() map[string]struct{} {
	result := make(map[string]struct{}, len(orderedTransferTables)+1)
	result["schema_migrations"] = struct{}{}
	for _, table := range orderedTransferTables {
		result[table] = struct{}{}
	}
	return result
}
