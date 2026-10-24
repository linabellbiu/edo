package database

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/model"
)

func TestSQLChangeSQLiteMigration(t *testing.T) {
	exerciseSQLChangeMigration(t, "sqlite", filepath.Join(t.TempDir(), "sql-changes.db"))
}

func TestSQLChangeExternalMigration(t *testing.T) {
	selected := os.Getenv("EDO_TEST_DATABASE_DRIVER")
	if selected != "" && selected != "sqlite" && selected != "mysql" && selected != "postgres" {
		t.Fatal("数据库测试驱动无效")
	}
	for _, target := range []struct{ driver, env string }{{"postgres", "EDO_TEST_POSTGRES_DSN"}, {"mysql", "EDO_TEST_MYSQL_DSN"}} {
		t.Run(target.driver, func(t *testing.T) {
			if selected != "" && selected != target.driver {
				t.Skip("当前任务验证其他数据库")
			}
			dsn := os.Getenv(target.env)
			if dsn == "" {
				if selected == target.driver {
					t.Fatal("已选择真实数据库测试，但连接配置缺失")
				}
				t.Skip("未配置独立迁移测试数据库")
			}
			exerciseSQLChangeMigration(t, target.driver, dsn)
		})
	}
}

func exerciseSQLChangeMigration(t *testing.T, driver, dsn string) {
	t.Helper()
	db, err := Open(context.Background(), config.Database{Driver: driver, DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close(db) })
	ctx := context.Background()
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	models := []any{&model.SQLChange{}, &model.SQLChangeRevision{}, &model.SQLChangeReview{}, &model.SQLChangeExecution{}}
	assertSQLChangeSchema(t, db, models)
	retained := model.User{ID: uuid.NewString(), Username: "sql-upgrade-" + uuid.NewString()[:16], Nickname: "保留账户", IsActive: true, AuthVersion: 9}
	if err = db.Create(&retained).Error; err != nil {
		t.Fatal(err)
	}
	// 此处仅回退本次新增表，专用测试数据库不承载其他业务。
	if err = db.Migrator().DropTable(&model.SQLChangeExecution{}, &model.SQLChangeReview{}, &model.SQLChangeRevision{}, &model.SQLChange{}); err != nil {
		t.Fatal(err)
	}
	if err = db.Where("version = ?", "202610240073").Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	assertSQLChangeSchema(t, db, models)
	var existing model.User
	if err = db.First(&existing, "uuid = ?", retained.ID).Error; err != nil || existing.AuthVersion != 9 || existing.Nickname != retained.Nickname {
		t.Fatalf("升级改写了原有用户: %v", err)
	}
	change := model.SQLChange{ID: uuid.NewString(), Name: "升级验证", Version: 1, ProjectID: uuid.NewString(), DepartmentID: DefaultDepartmentID, Status: "pending_review"}
	revision := model.SQLChangeRevision{ID: uuid.NewString(), ChangeID: change.ID, Version: 1, Ciphertext: "encrypted-test-sql", Digest: "test-digest"}
	change.RevisionID = revision.ID
	review := model.SQLChangeReview{ID: uuid.NewString(), ChangeID: change.ID, RevisionID: revision.ID, Decision: "approve"}
	execution := model.SQLChangeExecution{ID: uuid.NewString(), ChangeID: change.ID, RevisionID: revision.ID, ExecutionKey: uuid.NewString(), Status: "succeeded"}
	for _, value := range []any{&change, &revision, &review, &execution} {
		if err = db.Create(value).Error; err != nil {
			t.Fatalf("新表无法写入 %T: %v", value, err)
		}
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = VerifyMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var stored model.SQLChangeRevision
	if err = db.First(&stored, "uuid = ?", revision.ID).Error; err != nil || stored.Ciphertext != revision.Ciphertext || stored.ChangeID != change.ID || stored.RowID != revision.RowID {
		t.Fatalf("重复迁移未保留 SQL 修订: %v", err)
	}
	second := model.SQLChangeRevision{ID: uuid.NewString(), ChangeID: change.ID, Version: 2, Ciphertext: "next-revision"}
	if err = db.Create(&second).Error; err != nil || second.RowID <= revision.RowID {
		t.Fatalf("升级后不能继续自增写入: %v", err)
	}
	duplicate := execution
	duplicate.RowID, duplicate.ID = 0, uuid.NewString()
	if err = db.Create(&duplicate).Error; err == nil {
		t.Fatal("执行去重约束未生效")
	}
}

func assertSQLChangeSchema(t *testing.T, db *gorm.DB, models []any) {
	t.Helper()
	for _, value := range models {
		if !db.Migrator().HasTable(value) || !db.Migrator().HasColumn(value, "uuid") || !db.Migrator().HasColumn(value, "id") {
			t.Fatalf("SQL 结构不完整: %T", value)
		}
		columns, err := db.Migrator().ColumnTypes(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, column := range columns {
			if column.Name() == "ciphertext" || column.Name() == "check_report" {
				if _, exists := column.DefaultValue(); exists {
					t.Fatalf("大字段不能有字面量默认值: %s", column.Name())
				}
			}
		}
	}
	if !db.Migrator().HasIndex(&model.SQLChangeRevision{}, "ux_sql_change_revision") || !db.Migrator().HasIndex(&model.SQLChangeExecution{}, "ExecutionKey") {
		t.Fatal("SQL 修订或执行唯一约束缺失")
	}
}
