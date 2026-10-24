package cicd

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing/fstest"
	"time"

	"edo/internal/model"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
)

type migrationLogger struct{}

func (migrationLogger) Printf(string, ...any) {}
func (migrationLogger) Fatalf(string, ...any) {}

// 固定 Commit 工作区内的 SQL 内容先全部读取、核对摘要，再交给 Goose。
func MigrationFiles(directory string) (fstest.MapFS, map[string]string, string, error) {
	files := fstest.MapFS{}
	manifest := map[string]string{}
	size := 0
	err := filepath.WalkDir(directory, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return ErrMigrationChanged
		}
		if entry.IsDir() {
			if name != directory {
				return ErrInvalidDatabase
			}
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".sql") {
			return ErrInvalidDatabase
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 1024*1024 || len(files) >= 1000 {
			return ErrInvalidDatabase
		}
		data, err := fs.ReadFile(os.DirFS(directory), entry.Name())
		if err != nil {
			return err
		}
		size += len(data)
		if size > 10*1024*1024 {
			return ErrInvalidDatabase
		}
		files[entry.Name()] = &fstest.MapFile{Data: data, Mode: 0400}
		manifest[entry.Name()] = digest(string(data))
		return nil
	})
	if err != nil || len(files) == 0 {
		return nil, nil, "", errors.Join(ErrInvalidDatabase, err)
	}
	names := make([]string, 0, len(manifest))
	for name := range manifest {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := []string{}
	for _, name := range names {
		parts = append(parts, name, manifest[name])
	}
	return files, manifest, digest(parts...), nil
}
func (s *Service) ExecuteMigration(ctx context.Context, run model.PipelineRun, nodeID string, config model.WorkflowMigrationConfig, directory string) error {
	target, err := s.FindDatabase(ctx, run.ProjectID, run.ApplicationID, config.DatabaseID)
	if err != nil {
		return err
	}
	if target.Version != config.Version || target.Fingerprint != config.Fingerprint {
		return s.failure(ctx, "database_migration_target_changed", ErrVariableConflict, ErrVariableConflict)
	}
	files, manifest, checksum, err := MigrationFiles(directory)
	if err != nil {
		return s.failure(ctx, "database_migration_files", err, ErrMigrationChanged)
	}
	var previous []model.ApplicationDatabaseMigration
	if err = s.db.WithContext(ctx).Where("database_id = ?", target.ID).Order("id ASC").Find(&previous).Error; err != nil {
		return s.failure(ctx, "database_migration_history", err, ErrStorage)
	}
	for _, old := range previous {
		if old.Status != "succeeded" {
			return s.failure(ctx, "database_migration_uncertain", ErrMigrationState, ErrMigrationState)
		}
		var applied map[string]string
		if json.Unmarshal([]byte(old.Manifest), &applied) != nil {
			return s.failure(ctx, "database_migration_manifest", ErrMigrationState, ErrMigrationState)
		}
		for name, hash := range applied {
			if manifest[name] != hash {
				return s.failure(ctx, "database_migration_modified", ErrMigrationChanged, ErrMigrationChanged)
			}
		}
		if old.Digest == checksum {
			return nil
		}
	}
	// 相同实际数据库的其它配置也不能用不同脚本覆盖已经执行的迁移。
	var aliases []model.ApplicationDatabase
	if err = s.db.WithContext(ctx).Unscoped().Where("fingerprint = ?", target.Fingerprint).Find(&aliases).Error; err != nil {
		return s.failure(ctx, "database_migration_aliases", err, ErrStorage)
	}
	for _, alias := range aliases {
		if alias.ID != target.ID {
			return s.failure(ctx, "database_migration_duplicate_target", ErrMigrationState, ErrMigrationState)
		}
	}
	password, err := s.secrets.Decrypt(target.Ciphertext, []byte("application-database:"+target.ID))
	if err != nil {
		return s.failure(ctx, "database_migration_credentials", err, ErrStorage)
	}
	db, err := OpenApplicationDatabase(ctx, *target, password)
	if err != nil {
		return s.failure(ctx, "database_migration_connect", err, ErrDatabaseConnect)
	}
	defer db.Close()
	dialect := goose.DialectMySQL
	if target.Driver == "postgres" {
		dialect = goose.DialectPostgres
	}
	provider, err := goose.NewProvider(dialect, db, files, goose.WithDisableGlobalRegistry(true), goose.WithTableName("edo_schema_version"), goose.WithLogger(migrationLogger{}))
	if err != nil {
		return s.failure(ctx, "database_migration_prepare", err, ErrMigrationChanged)
	}
	if len(previous) == 0 {
		statuses, statusErr := provider.Status(ctx)
		if statusErr != nil {
			return s.failure(ctx, "database_migration_status", statusErr, ErrMigrationState)
		}
		for _, status := range statuses {
			if status.State == goose.StateApplied {
				return s.failure(ctx, "database_migration_untracked_history", ErrMigrationState, ErrMigrationState)
			}
		}
	}
	item := model.ApplicationDatabaseMigration{ID: uuid.NewString(), ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, DatabaseID: target.ID, PipelineRunID: run.ID, NodeID: nodeID, CommitSHA: run.CommitSHA, Directory: config.Directory, Digest: checksum, Manifest: jsonValue(manifest), ExecutionKey: digest(target.Fingerprint, checksum), Status: "running", Message: "正在执行数据库变更", StartedAt: time.Now().UTC()}
	if err = s.db.WithContext(ctx).Create(&item).Error; err != nil {
		return s.failure(ctx, "database_migration_claim", err, ErrMigrationState)
	}
	_, executeErr := provider.Up(ctx)
	status, message := "succeeded", "数据库变更已完成"
	if executeErr != nil {
		status, message = "failed", ErrMigrationFailed.Error()
		s.logger.ErrorContext(ctx, "执行数据库变更失败", "operation", "database_migration_execute", "database_id", target.ID, "run_id", run.ID, "err", redactedMigrationError(executeErr, password))
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err = s.db.WithContext(finishCtx).Model(&model.ApplicationDatabaseMigration{}).Where("uuid = ? AND status = ?", item.ID, "running").Updates(map[string]any{"status": status, "message": message, "finished_at": time.Now().UTC()}).Error; err != nil {
		return s.failure(ctx, "database_migration_finish", err, ErrMigrationState)
	}
	if executeErr != nil {
		return ErrMigrationFailed
	}
	return nil
}
func redactedMigrationError(err error, password string) string {
	v := err.Error()
	if password != "" {
		v = strings.ReplaceAll(v, password, "[已隐藏]")
	}
	return v
}
