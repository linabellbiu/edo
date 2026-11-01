package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestWorkflowNameLockNoopZeroRowsUsesCurrentRead(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "applications" SET "updated_at"=updated_at WHERE uuid = \$1`).WithArgs("app").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT "uuid" FROM "applications".*FOR UPDATE`).WithArgs("app", 1).
		WillReturnRows(sqlmock.NewRows([]string{"uuid"}).AddRow("app"))
	mock.ExpectCommit()
	if err := db.Transaction(func(tx *gorm.DB) error { return lockWorkflowApplication(tx, "app") }); err != nil {
		t.Fatalf("MySQL changed rows语义不应误拒绝零行no-op: %v", err)
	}
}

func TestWorkflowNameLockCurrentNameAcrossDialects(t *testing.T) {
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
			query := mock.ExpectQuery("current-name")
			if dialect == "sqlite" {
				query.WithArgs("app", "name", "self")
			} else {
				query.WithArgs("app", "name", "self", 1)
			}
			query.WillReturnRows(sqlmock.NewRows([]string{"uuid"}))
			err = workflowNameAvailable(db, "app", "self", "name")
			upper := strings.ToUpper(actual)
			if err != nil || strings.Contains(upper, "COUNT(") || !strings.Contains(upper, "LIMIT") || strings.Contains(upper, "FOR UPDATE") != (dialect != "sqlite") {
				t.Fatalf("不是方言当前行读取: dialect=%s sql=%q err=%v", dialect, actual, err)
			}
			for _, predicate := range []string{"application_id =", "name =", "uuid <>", "deleted_at"} {
				if !strings.Contains(actual, predicate) {
					t.Fatalf("名称检查缺少约束: predicate=%s sql=%q", predicate, actual)
				}
			}
		})
	}
}

func TestWorkflowNameLockFailureIsSafeAndLogs(t *testing.T) {
	service, mock := newPipelineMockService(t)
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	marker := errors.New("mock-storage-canary")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "applications"`).WithArgs("app").WillReturnError(marker)
	mock.ExpectRollback()
	err := service.db.Transaction(func(tx *gorm.DB) error { return lockWorkflowApplication(tx, "app") })
	if !errors.Is(err, marker) {
		t.Fatalf("内部错误丢失: %v", err)
	}
	public := service.workflowMutationError(context.Background(), "application_workflow_save", "app", "wf", err)
	if !errors.Is(public, errWorkflowMutationFailed) || public.Error() != "保存应用流水线失败，请稍后重试" || strings.Contains(public.Error(), marker.Error()) {
		t.Fatalf("公开错误泄漏或被误判业务重名: %v", public)
	}
	for _, value := range []string{marker.Error(), "application_workflow_save", "app", "wf"} {
		if !strings.Contains(logs.String(), value) {
			t.Fatalf("安全诊断缺少必要上下文: %s", value)
		}
	}
}

func secondNameTestService(t *testing.T, service *Service, db *gorm.DB) *Service {
	t.Helper()
	// 仅重开本用例独立SQLite临时文件；两服务持有独立连接池，不读取外部DSN。
	var entries []struct {
		Seq        int
		Name, File string
	}
	if err := db.Raw("PRAGMA database_list").Scan(&entries).Error; err != nil {
		t.Fatal(err)
	}
	var file string
	for _, entry := range entries {
		if entry.Name == "main" {
			file = entry.File
		}
	}
	if file == "" {
		t.Fatal("本用例SQLite路径未取得")
	}
	dsn := (&url.URL{Scheme: "file", Path: file, RawQuery: "_pragma=journal_mode(WAL)"}).String()
	otherDB, err := database.Open(context.Background(), config.Database{Driver: "sqlite", DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(otherDB); err != nil {
			t.Error(err)
		}
	})
	return service.deliveryTransaction(otherDB)
}

func TestWorkflowNameConcurrentIndependentPools(t *testing.T) {
	for _, kind := range []string{"create_create", "save_save", "create_save", "default_names", "ensure_default"} {
		t.Run(kind, func(t *testing.T) {
			service, db, _, repositoryID := newPipelineTestService(t)
			app := nameTestApplication(t, service, repositoryID, kind)
			var workflows [2]*model.ReleaseWorkflow
			if kind == "save_save" || kind == "create_save" {
				workflows[0] = nameTestWorkflow(t, service, app.ID, "first-name")
				workflows[1] = nameTestWorkflow(t, service, app.ID, "second-name")
			}
			services := []*Service{service, secondNameTestService(t, service, db)}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			start := make(chan struct{})
			type outcome struct {
				err      error
				id, name string
			}
			results := make(chan outcome, 2)
			var wg sync.WaitGroup
			for i, current := range services {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if kind == "ensure_default" {
						result, err := current.GetWorkflow(ctx, app.ID)
						o := outcome{err: err}
						if result != nil {
							o.id, o.name = result.Workflow.ID, result.Workflow.Name
						}
						results <- o
						return
					}
					name := "collision"
					if kind == "default_names" {
						name = ""
					}
					var result *WorkflowResult
					var err error
					if kind == "save_save" || kind == "create_save" && i == 1 {
						result, err = current.SaveApplicationWorkflow(ctx, app.ID, workflows[i].ID, "admin", nameTestInput(workflows[i], name))
					} else {
						result, err = current.CreateApplicationWorkflow(ctx, app.ID, "admin", WorkflowCreateInput{Name: name})
					}
					o := outcome{err: err}
					if result != nil {
						o.id, o.name = result.Workflow.ID, result.Workflow.Name
					}
					results <- o
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			var success, conflict int
			var completed []outcome
			for result := range results {
				completed = append(completed, result)
				if result.err == nil {
					success++
				} else if errors.Is(result.err, ErrWorkflowExists) {
					conflict++
				} else {
					t.Errorf("并发非业务错误不能算通过: %v", result.err)
				}
			}
			if kind == "default_names" || kind == "ensure_default" {
				if success != 2 || conflict != 0 {
					t.Fatalf("默认名并发失败: %+v", completed)
				}
				if kind == "default_names" && (completed[0].id == completed[1].id || completed[0].name == completed[1].name) {
					t.Fatal("默认名分配没有串行")
				}
				if kind == "ensure_default" && completed[0].id != completed[1].id {
					t.Fatal("默认补建产生两条记录")
				}
			} else {
				if success != 1 || conflict != 1 {
					t.Fatalf("必须一成功一业务冲突: success=%d conflict=%d", success, conflict)
				}
				var count int64
				if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ? AND name = ?", app.ID, "collision").Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("并发后名称不唯一: count=%d err=%v", count, err)
				}
			}
		})
	}
}
