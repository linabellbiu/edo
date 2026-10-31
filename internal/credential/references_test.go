package credential

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"edo/internal/testutil"
)

func TestReferenceLocksSortDeduplicateAndAcceptNoopZeroRows(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	mock.ExpectBegin()
	for _, id := range []string{"a", "b"} {
		mock.ExpectExec(`UPDATE "git_credentials" SET "updated_at"=updated_at WHERE uuid = \$1`).WithArgs(id).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectCommit()
	if err := db.Transaction(func(tx *gorm.DB) error { return LockReferences(tx, []string{"b", "", "a", "b"}) }); err != nil {
		t.Fatalf("有序锁误把 no-op 零行数视为缺失: %v", err)
	}
}

func TestReferenceLockFailureRollsBackWithoutContinuing(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	marker := errors.New("本例锁写入失败")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "git_credentials"`).WithArgs("a").WillReturnError(marker)
	mock.ExpectRollback()
	if err := db.Transaction(func(tx *gorm.DB) error { return LockReferences(tx, []string{"b", "a"}) }); !errors.Is(err, marker) {
		t.Fatalf("锁失败没有保留原错误并回滚: %v", err)
	}
}

func TestReferenceChecksUseCurrentRowReadAcrossDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			var actual string
			conn, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, query string) error { actual = query; return nil })))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Error(err)
				}
				mock.ExpectClose()
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			})
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				mock.ExpectQuery("sqlite_version").WillReturnRows(sqlmock.NewRows([]string{"sqlite_version()"}).AddRow("3.45.0"))
				driver = sqlite.New(sqlite.Config{Conn: conn})
			case "postgres":
				driver = postgres.New(postgres.Config{Conn: conn})
			case "mysql":
				driver = mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			query := mock.ExpectQuery("current-reference-read")
			if dialect == "sqlite" {
				// SQLite 方言将 LIMIT 1 写为字面量，另两种方言绑定为第四个参数。
				query.WithArgs("selected", "selected", "department")
			} else {
				query.WithArgs("selected", "selected", "department", 1)
			}
			query.WillReturnRows(sqlmock.NewRows([]string{"uuid"}).AddRow("repo"))
			used, err := ReferencesInUse(db, []string{"selected"}, "department")
			upper := strings.ToUpper(actual)
			if err != nil || !used || strings.Contains(upper, "COUNT(") || !strings.Contains(upper, "LIMIT") || strings.Contains(upper, "PROJECT_ID") {
				t.Fatalf("引用查询不是有界当前行读取: dialect=%s used=%t sql=%q err=%v", dialect, used, actual, err)
			}
			if strings.Contains(upper, "FOR UPDATE") != (dialect != "sqlite") {
				t.Fatalf("方言锁语句错误: dialect=%s sql=%q", dialect, actual)
			}
			if dialect == "sqlite" && !regexp.MustCompile(`\bLIMIT\s+1\b`).MatchString(upper) {
				t.Fatalf("SQLite 当前行查询没有准确限制一条: sql=%q", actual)
			}
			for _, predicate := range []string{`(?i)\bcredential_id\s+IN\s*\(`, `(?i)\bOR\s+api_credential_id\s+IN\s*\(`, `(?i)\bAND\s+department_id\s*=`} {
				if !regexp.MustCompile(predicate).MatchString(actual) {
					t.Fatalf("引用当前读缺少准确过滤条件: dialect=%s predicate=%q sql=%q", dialect, predicate, actual)
				}
			}
		})
	}
}
