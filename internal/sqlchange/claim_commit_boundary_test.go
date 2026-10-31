package sqlchange

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/model"
	"edo/internal/task"
	"edo/internal/worker"
	"gorm.io/gorm"
)

var errClaimCommitReplyLost = errors.New("合成认领事务提交回执丢失")

// 测试只包装真实 SQLite 事务；数据提交和查询仍由原 database/sql 连接完成。
type claimCommitFault struct {
	db                    *sql.DB
	changeID, executionID string
	injected              atomic.Int32
	confirmed             bool
	proofErr              error
	rollback              bool
}

type claimCommitPool struct {
	*sql.DB
	fault *claimCommitFault
}

func (p *claimCommitPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &claimCommitTx{Tx: tx, fault: p.fault}, nil
}

type claimCommitTx struct {
	*sql.Tx
	fault *claimCommitFault
	claim bool
}

func (tx *claimCommitTx) Commit() error {
	if !tx.claim || !tx.fault.injected.CompareAndSwap(0, 1) {
		return tx.Tx.Commit()
	}
	var err error
	want := "running"
	if tx.fault.rollback {
		err = tx.Tx.Rollback()
		want = "queued"
	} else {
		err = tx.Tx.Commit()
	}
	if err != nil {
		return err
	}
	// 先独立证实真实提交或回滚结果，再模拟客户端只收到事务失败。
	var execution, change string
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = tx.fault.db.QueryRowContext(ctx, "SELECT status FROM sql_change_executions WHERE uuid = ?", tx.fault.executionID).Scan(&execution)
	if err == nil {
		err = tx.fault.db.QueryRowContext(ctx, "SELECT status FROM sql_changes WHERE uuid = ?", tx.fault.changeID).Scan(&change)
	}
	tx.fault.proofErr = err
	tx.fault.confirmed = err == nil && execution == want && change == want
	return errClaimCommitReplyLost
}

func TestSQLChangeClaimCommitReplyLossCannotReportTaskSuccess(t *testing.T) {
	for _, mode := range []string{"direct_handler", "registered_handler"} {
		t.Run(mode, func(t *testing.T) {
			testSQLChangeClaimFailure(t, mode == "registered_handler", false)
		})
	}
}

func TestSQLChangeClaimRollbackCannotReportTaskSuccess(t *testing.T) {
	testSQLChangeClaimFailure(t, true, true)
}

func testSQLChangeClaimFailure(t *testing.T, registered, rollback bool) {
	t.Helper()
	f := fixture(t)
	if f.s.db.Dialector.Name() != "sqlite" {
		t.Skip("此用例只包装独立 SQLite 的真实提交边界")
	}
	d := f.approved(t)
	d, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest})
	if err != nil {
		t.Fatal(err)
	}
	message := f.message(t, "sql_change.execute")
	var record model.SQLChangeExecution
	if err := f.s.db.WithContext(f.ctx).First(&record, "job_id = ?", message.JobID).Error; err != nil || record.Status != "queued" {
		t.Fatal("认领前置状态未确认")
	}
	base := f.s.db
	db, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	fault := &claimCommitFault{db: db, changeID: d.Change.ID, executionID: record.ID, rollback: rollback}
	pool := &claimCommitPool{DB: db, fault: fault}
	wrapped := base.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	wrapped.Config.ConnPool, wrapped.Statement.ConnPool = pool, pool
	const callback = "test:sql_change_claim_commit_boundary"
	if err := wrapped.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "sql_change_executions" {
			return
		}
		updates, ok := tx.Statement.Dest.(map[string]any)
		transaction, wrapped := tx.Statement.ConnPool.(*claimCommitTx)
		if ok && wrapped && updates["status"] == "running" {
			transaction.claim = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Callback().Update().Remove(callback) })
	f.s.db = wrapped
	registry := worker.NewRegistry()
	if err := f.s.Register(registry); err != nil {
		t.Fatal(err)
	}
	handler, ok := registry.Handler("sql_change.execute")
	if !ok {
		t.Fatal("执行任务未注册")
	}
	var executionErr error
	if registered {
		executionErr = handler(context.Background(), message)
		var failure *worker.ExecutionError
		if executionErr != nil && (!errors.As(executionErr, &failure) || failure.Retryable) {
			t.Error("提交回执丢失不得转成可自动重试任务")
		}
	} else {
		executionErr = f.s.HandleExecute(context.Background(), message)
	}
	if fault.injected.Load() != 1 || !fault.confirmed || fault.proofErr != nil {
		t.Fatal("未在真实事务结束并独立回读成功后注入一次失败")
	}
	if f.runner.executions.Load() != 0 {
		t.Fatal("认领结果不明确时调用了业务 SQL 执行器")
	}
	if !errors.Is(executionErr, ErrStorage) {
		t.Fatalf("认领失败必须返回稳定存储错误: error_type=%T", executionErr)
	}
	// 这里走真实注册的终止钩子与数据库事务，不冒充 NATS 消费验收。
	hook, ok := registry.TerminalFailureHook(message.Kind)
	if !ok {
		t.Fatal("执行任务终止钩子未注册")
	}
	if err := base.WithContext(f.ctx).Transaction(func(tx *gorm.DB) error {
		var job model.Job
		if err := tx.First(&job, "uuid = ?", message.JobID).Error; err != nil {
			return err
		}
		if job.MaxAttempts != 1 || job.IsIdempotent {
			return errors.New("SQL 执行任务允许自动重放")
		}
		if err := tx.Model(&model.Job{}).Where("uuid = ?", job.ID).Updates(map[string]any{"status": model.JobFailed, "attempt": 1}).Error; err != nil {
			return err
		}
		return hook(f.ctx, tx, job, "sql_change_failed", ErrStorage.Error())
	}); err != nil {
		t.Fatal(err)
	}
	want := "uncertain"
	if rollback {
		want = "failed"
	}
	stored, err := f.s.Detail(f.ctx, d.Change.ID)
	if err != nil || stored.Change.Status != want || stored.Change.SuccessAt != nil || len(stored.Executions) != 1 || stored.Executions[0].Status != want || stored.Executions[0].FinishedAt == nil {
		t.Fatalf("终止钩子未按真实事务结果收口: rollback=%t error_type=%T", rollback, err)
	}
	if err := handler(context.Background(), message); err == nil {
		t.Fatal("失败的同一任务被重新接受")
	}
	if _, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: stored.Change.Version, ConfirmDigest: stored.Change.Digest}); !errors.Is(err, ErrState) {
		t.Fatal("失败后允许原工单再次执行")
	}
	if _, err := task.NewService(base, 4).Retry(f.ctx, message.JobID); !errors.Is(err, task.ErrJobNotRetryable) {
		t.Fatal("任务中心允许非幂等 SQL 重试")
	}
	var count int64
	if err := base.WithContext(f.ctx).Model(&model.SQLChangeExecution{}).Where("change_id = ?", d.Change.ID).Count(&count).Error; err != nil || count != 1 || f.runner.executions.Load() != 0 || fault.injected.Load() != 1 {
		t.Fatal("收口或重复消息产生了第二次执行")
	}
}

var _ gorm.ConnPoolBeginner = (*claimCommitPool)(nil)
var _ gorm.ConnPool = (*claimCommitTx)(nil)
var _ gorm.TxCommitter = (*claimCommitTx)(nil)
