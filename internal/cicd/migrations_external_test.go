package cicd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestApplicationDatabaseMigrationExternal(t *testing.T) {
	driver := os.Getenv("EDO_APP_DB_TEST_DRIVER")
	if driver == "" {
		t.Skip("未选择应用数据库集成测试")
	}
	if driver != "mysql" && driver != "postgres" {
		t.Fatal("应用数据库测试驱动无效")
	}
	port, err := strconv.Atoi(os.Getenv("EDO_APP_DB_TEST_PORT"))
	if err != nil {
		t.Fatal("缺少应用数据库测试端口")
	}
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	repo := model.GitRepository{ID: "database-repository", Name: "database", ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID}
	app := model.Application{ID: "database-application", Name: "database_app", RepositoryID: repo.ID, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID}
	env := model.Environment{ID: "database-environment", Name: "数据库测试", ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID}
	for _, item := range []any{&repo, &app, &env} {
		if err = s.db.WithContext(ctx).Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	password := os.Getenv("EDO_APP_DB_TEST_PASSWORD")
	input := DatabaseInput{Name: "应用数据库", ApplicationID: app.ID, EnvironmentID: env.ID, Driver: driver, Host: "127.0.0.1", Port: port, Database: os.Getenv("EDO_APP_DB_TEST_DATABASE"), Username: os.Getenv("EDO_APP_DB_TEST_USER"), Password: &password, TLSMode: "disable", IsActive: true}
	target, err := s.SaveDatabase(ctx, "owner", "", input, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(target)
	if strings.Contains(string(raw), password) || target.Ciphertext == "" {
		t.Fatal("数据库密码存储或回显不安全")
	}
	directory := t.TempDir()
	first := filepath.Join(directory, "00001_create.sql")
	if err = os.WriteFile(first, []byte("-- +goose Up\nCREATE TABLE edo_application_check (id BIGINT PRIMARY KEY, value VARCHAR(50) NOT NULL);\nINSERT INTO edo_application_check (id,value) VALUES (1,'initial');\n-- +goose Down\nDROP TABLE edo_application_check;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := model.WorkflowMigrationConfig{DatabaseID: target.ID, Version: target.Version, Fingerprint: target.Fingerprint, Directory: "migrations"}
	run := model.PipelineRun{ID: "database-run-1", ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, ApplicationID: app.ID, CommitSHA: strings.Repeat("a", 40)}
	if err = s.ExecuteMigration(ctx, run, "migration", config, directory); err != nil {
		t.Fatal(err)
	}
	// 重投同一套迁移只确认已完成状态，不能再执行 INSERT。
	run.ID = "database-run-2"
	if err = s.ExecuteMigration(ctx, run, "migration", config, directory); err != nil {
		t.Fatal(err)
	}
	var rows []model.ApplicationDatabaseMigration
	if err = s.db.WithContext(ctx).Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].Status != "succeeded" {
		t.Fatalf("迁移历史不正确: %+v %v", rows, err)
	}
	second := filepath.Join(directory, "00002_update.sql")
	if err = os.WriteFile(second, []byte("-- +goose Up\nUPDATE edo_application_check SET value='updated' WHERE id=1;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run.ID = "database-run-3"
	if err = s.ExecuteMigration(ctx, run, "migration", config, directory); err != nil {
		t.Fatal(err)
	}
	db, err := OpenApplicationDatabase(ctx, *target, password)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err = db.QueryRowContext(ctx, "SELECT value FROM edo_application_check WHERE id=1").Scan(&value); err != nil || value != "updated" {
		t.Fatal("数据库没有执行新增迁移")
	}
	if err = os.WriteFile(first, []byte("-- +goose Up\nDROP TABLE edo_application_check;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run.ID = "database-run-4"
	if err = s.ExecuteMigration(ctx, run, "migration", config, directory); !errors.Is(err, ErrMigrationChanged) {
		t.Fatalf("已执行脚本变化没有被拦截: %v", err)
	}
	// 文件恢复后新增失败变更，确认失败状态和重投保护。
	if err = os.WriteFile(first, []byte("-- +goose Up\nCREATE TABLE edo_application_check (id BIGINT PRIMARY KEY, value VARCHAR(50) NOT NULL);\nINSERT INTO edo_application_check (id,value) VALUES (1,'initial');\n-- +goose Down\nDROP TABLE edo_application_check;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "00003_fail.sql"), []byte("-- +goose Up\nINSERT INTO edo_missing_table (id) VALUES (1);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run.ID = "database-run-5"
	if err = s.ExecuteMigration(ctx, run, "migration", config, directory); !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("SQL 失败未正确标记: %v", err)
	}
	run.ID = "database-run-6"
	if err = s.ExecuteMigration(ctx, run, "migration", config, directory); !errors.Is(err, ErrMigrationState) {
		t.Fatal("失败变更被自动重放")
	}
	input.ExpectedVersion = target.Version
	input.Database = "different_database"
	if _, err = s.SaveDatabase(ctx, "owner", target.ID, input, false); !errors.Is(err, ErrInvalidDatabase) {
		t.Fatal("修改实际目标沿用了其它数据库的历史")
	}

}
