package sqlchange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"edo/internal/config"
	"edo/internal/model"
)

const boundarySource = "INSERT INTO edo_boundary_probe (value) VALUES ('private-reviewed-value');"
const boundaryPassword = "private-boundary-password"

// 只记录调用类别；所有结果、参数转换、上下文与事务行为仍由 sqlmock 提供。
type boundaryTrace struct {
	mu    sync.Mutex
	calls []string
}

func (s *boundaryTrace) add(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *boundaryTrace) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

type boundarySQLMockConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.Execer
	driver.ExecerContext
	driver.Queryer
	driver.QueryerContext
	driver.Pinger
	driver.NamedValueChecker
}

type boundaryConnector struct {
	driver driver.Driver
	name   string
	trace  *boundaryTrace
}

func (c boundaryConnector) Driver() driver.Driver { return c.driver }
func (c boundaryConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.driver.Open(c.name)
	if err != nil {
		return nil, err
	}
	wrapped, ok := conn.(boundarySQLMockConn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("测试驱动缺少所需上下文接口")
	}
	return &boundaryConn{boundarySQLMockConn: wrapped, trace: c.trace}, nil
}

type boundaryConn struct {
	boundarySQLMockConn
	trace *boundaryTrace
}

func (c *boundaryConn) recordExec(query string) {
	call := "exec"
	if strings.TrimSpace(query) == boundarySource {
		call = "business"
	} else if strings.HasPrefix(query, "UPDATE edo_sql_change_receipts SET state='succeeded'") {
		call = "receipt_complete"
	}
	c.trace.add(call)
}

func (c *boundaryConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.recordExec(query)
	return c.boundarySQLMockConn.ExecContext(ctx, query, args)
}

func (c *boundaryConn) Exec(query string, args []driver.Value) (driver.Result, error) {
	c.recordExec(query)
	return c.boundarySQLMockConn.Exec(query, args)
}

func (c *boundaryConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.trace.add("query")
	return c.boundarySQLMockConn.QueryContext(ctx, query, args)
}

func (c *boundaryConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	c.trace.add("query")
	return c.boundarySQLMockConn.Query(query, args)
}

func (c *boundaryConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.trace.add("begin")
	tx, err := c.boundarySQLMockConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return boundaryTx{Tx: tx, trace: c.trace}, nil
}

func (c *boundaryConn) Begin() (driver.Tx, error) {
	c.trace.add("begin")
	tx, err := c.boundarySQLMockConn.Begin()
	if err != nil {
		return nil, err
	}
	return boundaryTx{Tx: tx, trace: c.trace}, nil
}

func (c *boundaryConn) Prepare(query string) (driver.Stmt, error) {
	c.trace.add("prepare")
	return c.boundarySQLMockConn.Prepare(query)
}

func (c *boundaryConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.trace.add("prepare")
	return c.boundarySQLMockConn.PrepareContext(ctx, query)
}

func (c *boundaryConn) Close() error {
	c.trace.add("close")
	return c.boundarySQLMockConn.Close()
}

type boundaryTx struct {
	driver.Tx
	trace *boundaryTrace
}

func (t boundaryTx) Commit() error {
	t.trace.add("commit")
	return t.Tx.Commit()
}

func (t boundaryTx) Rollback() error {
	t.trace.add("rollback")
	return t.Tx.Rollback()
}

type runtimeBoundaryFixture struct {
	runtime     *Runtime
	mock        sqlmock.Sqlmock
	base        *sql.DB
	trace       *boundaryTrace
	logs        *bytes.Buffer
	target      model.ApplicationDatabase
	executionID string
	digest      string
	version     int64
	openCalls   int
}

func newRuntimeBoundaryFixture(t *testing.T, dialect, executionID string) *runtimeBoundaryFixture {
	t.Helper()
	name := "edo-runtime-boundary-" + uuid.NewString()
	base, mock, err := sqlmock.NewWithDSN(name, sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	trace := &boundaryTrace{}
	db := sql.OpenDB(boundaryConnector{driver: base.Driver(), name: name, trace: trace})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close(); _ = base.Close() })
	logs := &bytes.Buffer{}
	r := NewRuntime(config.Runtime{}, slog.New(slog.NewJSONHandler(logs, nil)))
	checksum := sha256.Sum256([]byte(boundarySource))
	identity := sha256.Sum256([]byte("edo-sql-change:" + executionID))
	version := int64(binary.BigEndian.Uint64(identity[:8]) & (1<<63 - 1))
	if version == 0 {
		version = 1
	}
	f := &runtimeBoundaryFixture{runtime: r, mock: mock, base: base, trace: trace, logs: logs, target: model.ApplicationDatabase{Driver: dialect}, executionID: executionID, digest: hex.EncodeToString(checksum[:]), version: version}
	r.openDatabase = func(ctx context.Context, target model.ApplicationDatabase, password string) (*sql.DB, error) {
		f.openCalls++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if target.Driver != dialect || password != boundaryPassword {
			return nil, errors.New("测试执行器的连接参数不匹配")
		}
		return db, nil
	}
	return f
}

func (f *runtimeBoundaryFixture) receipt(state string, claimErr error) {
	rowIdentity, markers := "id BIGSERIAL PRIMARY KEY", []string{"$1", "$2", "$3"}
	if f.target.Driver == "mysql" {
		rowIdentity, markers = "id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY", []string{"?", "?", "?"}
	}
	f.mock.ExpectExec("CREATE TABLE IF NOT EXISTS edo_sql_change_receipts (" + rowIdentity + ", execution_id VARCHAR(36) NOT NULL UNIQUE, digest VARCHAR(64) NOT NULL, migration_version BIGINT NOT NULL UNIQUE, state VARCHAR(16) NOT NULL)").WillReturnResult(sqlmock.NewResult(0, 0))
	claim := f.mock.ExpectExec("INSERT INTO edo_sql_change_receipts (execution_id,digest,migration_version,state) VALUES ("+strings.Join(markers, ",")+",'running')").WithArgs(f.executionID, f.digest, f.version)
	if claimErr != nil {
		claim.WillReturnError(claimErr)
	} else {
		claim.WillReturnResult(sqlmock.NewResult(1, 1))
	}
	f.mock.ExpectQuery("SELECT digest,migration_version,state FROM edo_sql_change_receipts WHERE execution_id = " + markers[0]).WithArgs(f.executionID).WillReturnRows(sqlmock.NewRows([]string{"digest", "migration_version", "state"}).AddRow(f.digest, f.version, state)).RowsWillBeClosed()
}

func (f *runtimeBoundaryFixture) migration(commitErr error) {
	marker, tableQuery := "$1", "SELECT EXISTS ( SELECT 1 FROM pg_tables WHERE (current_schema() IS NULL OR schemaname = current_schema()) AND tablename = 'edo_sql_change_versions' )"
	versionArgs := "$1, $2"
	if f.target.Driver == "mysql" {
		marker, versionArgs = "?", "?, ?"
		tableQuery = "SELECT EXISTS ( SELECT 1 FROM information_schema.tables WHERE (database() IS NULL OR table_schema = database()) AND table_name = 'edo_sql_change_versions' )"
	}
	// 版本表已存在，故障只能落在业务事务提交，不能误测建表重试。
	f.mock.ExpectQuery(tableQuery).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true)).RowsWillBeClosed()
	f.mock.ExpectQuery("SELECT tstamp, is_applied FROM edo_sql_change_versions WHERE version_id=" + marker + " ORDER BY tstamp DESC LIMIT 1").WithArgs(f.version).WillReturnRows(sqlmock.NewRows([]string{"tstamp", "is_applied"})).RowsWillBeClosed()
	f.mock.ExpectBegin()
	f.mock.ExpectExec(boundarySource).WillReturnResult(sqlmock.NewResult(0, 1))
	f.mock.ExpectExec("INSERT INTO edo_sql_change_versions (version_id, is_applied) VALUES ("+versionArgs+")").WithArgs(f.version, true).WillReturnResult(sqlmock.NewResult(1, 1))
	commit := f.mock.ExpectCommit()
	if commitErr != nil {
		commit.WillReturnError(commitErr)
	}
}

func (f *runtimeBoundaryFixture) closeExpectations() {
	f.mock.ExpectClose() // Runtime 关闭观察连接。
	f.mock.ExpectClose() // 测试随后关闭 sqlmock 的原始连接。
}

func (f *runtimeBoundaryFixture) verify(t *testing.T, calls []string, phase string) {
	t.Helper()
	if err := f.base.Close(); err != nil {
		t.Fatal("关闭测试驱动失败")
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal("数据库边界没有依序完成预期调用")
	}
	if f.openCalls != 1 || !slices.Equal(f.trace.snapshot(), calls) {
		t.Fatalf("发生额外执行或预期阶段未到达: opens=%d calls=%v", f.openCalls, f.trace.snapshot())
	}
	for _, private := range []string{boundarySource, boundaryPassword, "private-reviewed-value", "private-transport-detail"} {
		if strings.Contains(f.logs.String(), private) {
			t.Fatal("执行边界日志包含敏感内容")
		}
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(f.logs.Bytes()), &entry); err != nil {
		t.Fatal("执行边界未保留单条安全日志")
	}
	errorType, _ := entry["error_type"].(string)
	if entry["operation"] != "sql_change_execute_runtime" || entry["execution_id"] != f.executionID || entry["phase"] != phase || errorType == "" {
		t.Fatal("执行边界未保留准确且安全的单条失败日志")
	}
}

func TestSQLChangeRuntimeTransactionAndReceiptFailuresStayUncertain(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql"} {
		for _, phase := range []string{"apply", "receipt_complete"} {
			t.Run(dialect+"/"+phase, func(t *testing.T) {
				f := newRuntimeBoundaryFixture(t, dialect, uuid.NewString())
				fault := fmt.Errorf("private-transport-detail %s %s: %w", boundarySource, boundaryPassword, io.ErrUnexpectedEOF)
				f.receipt("running", nil)
				calls := []string{"exec", "exec", "query", "query", "query", "begin", "business", "exec", "commit"}
				if phase == "apply" {
					f.migration(fault)
				} else {
					f.migration(nil)
					markers := []string{"$1", "$2"}
					if dialect == "mysql" {
						markers = []string{"?", "?"}
					}
					f.mock.ExpectExec("UPDATE edo_sql_change_receipts SET state='succeeded' WHERE execution_id = "+markers[0]+" AND digest = "+markers[1]).WithArgs(f.executionID, f.digest).WillReturnError(fault)
					calls = append(calls, "receipt_complete")
				}
				f.closeExpectations()
				if err := f.runtime.Execute(context.Background(), f.target, boundaryPassword, boundarySource, f.executionID); !errors.Is(err, ErrExecutionUncertain) || err != ErrExecutionUncertain {
					t.Fatal("事务或完成回执错误必须保留稳定的不确定结果")
				}
				f.verify(t, append(calls, "close"), phase)
			})
		}
	}
}

func TestSQLChangeRuntimeExistingRunningReceiptDoesNotReplay(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			f := newRuntimeBoundaryFixture(t, dialect, uuid.NewString())
			f.receipt("running", errors.New("private-transport-detail: 已有执行回执"))
			f.closeExpectations()
			if err := f.runtime.Execute(context.Background(), f.target, boundaryPassword, boundarySource, f.executionID); err != ErrExecutionUncertain {
				t.Fatal("已有未完成回执不能进入迁移器")
			}
			f.verify(t, []string{"exec", "exec", "query", "close"}, "receipt_exists")
		})
	}
}
