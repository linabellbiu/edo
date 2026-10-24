package sqlchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/task"
	"edo/internal/worker"
	"github.com/bsm/redislock"
	"gorm.io/gorm"
)

func (s *Service) taskContext(ctx context.Context, message task.Message, kind string) (context.Context, taskPayload, error) {
	var payload, stored taskPayload
	if message.Version != 1 || message.JobID == "" || message.Kind != kind || json.Unmarshal(message.Payload, &payload) != nil || payload.ChangeID == "" || payload.RevisionID == "" {
		return ctx, payload, ErrInvalid
	}
	var job model.Job
	if err := s.db.WithContext(ctx).First(&job, "uuid = ? AND kind = ?", message.JobID, kind).Error; err != nil {
		return ctx, payload, ErrNotFound
	}
	if json.Unmarshal(job.Payload, &stored) != nil || payload != stored || job.ProjectID == "" || job.DepartmentID == "" || job.Status == model.JobCanceled || job.Status == model.JobFailed {
		return ctx, payload, ErrState
	}
	return database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: job.ProjectID, DepartmentID: job.DepartmentID}), payload, nil
}

func (s *Service) HandleCheck(ctx context.Context, message task.Message) error {
	ctx, payload, err := s.taskContext(ctx, message, "sql_change.check")
	if err != nil {
		return s.failure(ctx, "sql_change_check_task", err, safeError(err))
	}
	item, err := s.load(ctx, s.db, payload.ChangeID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return s.failure(ctx, "sql_change_check_load", err, ErrStorage)
	}
	if item.RevisionID != payload.RevisionID || item.Status != "checking" {
		return nil
	}
	var revision model.SQLChangeRevision
	if err = s.db.WithContext(ctx).First(&revision, "uuid = ? AND change_id = ? AND check_job_id = ?", payload.RevisionID, item.ID, message.JobID).Error; err != nil {
		return s.failure(ctx, "sql_change_check_revision", err, ErrStorage)
	}
	if revision.CheckStatus != "checking" {
		return nil
	}
	ctx = withActor(ctx, revision.CreatedBy)
	if revision.CheckPermission != "sql_change.create" && revision.CheckPermission != "sql_change.update" && revision.CheckPermission != "sql_change.check" {
		return s.finishCheck(ctx, item, revision, CheckResult{}, ErrForbidden)
	}
	if err = s.authorize(ctx, revision.CreatedBy, revision.CheckPermission); err != nil {
		return s.finishCheck(ctx, item, revision, CheckResult{}, err)
	}
	if s.runner == nil {
		return s.finishCheck(ctx, item, revision, CheckResult{}, ErrUnavailable)
	}
	claim := s.db.WithContext(ctx).Model(&model.SQLChangeRevision{}).Where("uuid = ? AND check_job_id = ? AND check_status = ?", revision.ID, message.JobID, "checking").Update("check_status", "running")
	if claim.Error != nil {
		return s.failure(ctx, "sql_change_check_claim", claim.Error, ErrStorage)
	}
	if claim.RowsAffected != 1 {
		return nil
	}
	sql, err := s.secrets.Decrypt(revision.Ciphertext, revisionAAD(revision.ID))
	if err != nil {
		return s.finishCheck(ctx, item, revision, CheckResult{}, ErrStorage)
	}
	var target model.ApplicationDatabase
	if err = s.db.WithContext(ctx).First(&target, "uuid = ? AND project_id = ?", item.DatabaseID, item.ProjectID).Error; err != nil {
		return s.finishCheck(ctx, item, revision, CheckResult{}, ErrNotFound)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result, runErr := s.runner.Check(checkCtx, target.Driver, sql)
	return s.finishCheck(ctx, item, revision, result, runErr)
}
func withActor(ctx context.Context, actorID string) context.Context {
	scope, _ := scopeFrom(ctx)
	scope.UserID = actorID
	return database.WithDepartmentScope(ctx, scope)
}

func (s *Service) finishCheck(ctx context.Context, item *model.SQLChange, revision model.SQLChangeRevision, result CheckResult, runErr error) error {
	status, checkStatus, summary := "pending_review", "passed", "SQL 静态检查通过，等待指定审核人审核"
	if len(result.Findings) == 0 {
		result.Findings = json.RawMessage("[]")
	}
	if runErr != nil || !result.Passed || len(result.Findings) > maxCheckReportBytes || !json.Valid(result.Findings) {
		status, checkStatus, summary = "check_failed", "failed", "SQL 静态检查未通过，请查看检查结果"
		if !json.Valid(result.Findings) || len(result.Findings) > maxCheckReportBytes {
			result.Findings = json.RawMessage("[]")
		}
	}
	if runErr != nil {
		summary = "SQL 检查未完成，请检查执行服务后重新检查"
	}
	// 检查器错误可能带 SQL，只记录类型；完整报告加密后仅在工单详情读取。
	if runErr != nil {
		s.logger.ErrorContext(ctx, "SQL 静态检查失败", "operation", "sql_change_check_run", "change_id", item.ID, "revision_id", revision.ID, "error_type", fmt.Sprintf("%T", runErr), "err", ErrCheckFailed)
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	report, err := s.secrets.Encrypt(string(result.Findings), []byte("sql-change-check:"+revision.ID))
	if err != nil {
		return s.failure(finish, "sql_change_check_report", err, ErrStorage)
	}
	err = s.db.WithContext(finish).Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&model.SQLChangeRevision{}).Where("uuid = ? AND check_job_id = ? AND check_status IN ?", revision.ID, revision.CheckJobID, []string{"checking", "running"}).Updates(map[string]any{"check_status": checkStatus, "check_summary": summary, "check_report": report})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			return nil
		}
		return tx.Model(&model.SQLChange{}).Where("uuid = ? AND revision_id = ? AND status = ?", item.ID, revision.ID, "checking").Updates(map[string]any{"status": status, "version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC()}).Error
	})
	if err != nil {
		return s.failure(finish, "sql_change_check_finish", err, ErrStorage)
	}
	if status == "check_failed" {
		return ErrCheckFailed
	}
	return nil
}

func (s *Service) HandleExecute(ctx context.Context, message task.Message) error {
	ctx, payload, err := s.taskContext(ctx, message, "sql_change.execute")
	if err != nil {
		return s.failure(ctx, "sql_change_execute_task", err, safeError(err))
	}
	var record model.SQLChangeExecution
	if payload.ExecutionID == "" {
		return s.failure(ctx, "sql_change_execute_payload", ErrInvalid, ErrInvalid)
	}
	if err = s.db.WithContext(ctx).First(&record, "uuid = ? AND change_id = ? AND revision_id = ? AND job_id = ?", payload.ExecutionID, payload.ChangeID, payload.RevisionID, message.JobID).Error; err != nil {
		return s.failure(ctx, "sql_change_execute_record", err, ErrStorage)
	}
	if record.Status != "queued" {
		return nil
	}
	ctx = withActor(ctx, record.ActorID)
	item, err := s.load(ctx, s.db, record.ChangeID)
	if err != nil {
		return s.finishExecution(ctx, record, "failed", ErrExecutionFailed)
	}
	if item.Status != "queued" || item.RevisionID != record.RevisionID || executionKey(item) != record.ExecutionKey {
		return s.finishExecution(ctx, record, "failed", ErrExecutionFailed)
	}
	if err = s.authorize(ctx, record.ActorID, "sql_change.execute"); err != nil {
		return s.finishExecution(ctx, record, "failed", err)
	}
	if err = s.authorize(ctx, record.ActorID, "application_database.execute"); err != nil {
		return s.finishExecution(ctx, record, "failed", err)
	}
	if err = s.validateReviewer(ctx, s.db, item.ReviewerID, item.CreatedBy); err != nil {
		return s.finishExecution(ctx, record, "failed", ErrForbidden)
	}
	var approvedRevision model.SQLChangeRevision
	if err = s.db.WithContext(ctx).First(&approvedRevision, "uuid = ? AND change_id = ?", record.RevisionID, item.ID).Error; err != nil {
		return s.finishExecution(ctx, record, "failed", ErrStorage)
	}
	if err = s.validateReviewer(ctx, s.db, item.ReviewerID, approvedRevision.AuthorID); err != nil {
		return s.finishExecution(ctx, record, "failed", ErrForbidden)
	}
	target, err := s.currentTarget(ctx, s.db, item)
	if err != nil {
		return s.finishExecution(ctx, record, "failed", err)
	}
	if s.runner == nil || s.locks == nil {
		return s.finishExecution(ctx, record, "failed", ErrUnavailable)
	}
	lock, err := s.locks.Obtain(ctx, s.lockPrefix+":"+target.Fingerprint, 30*time.Second, &redislock.Options{RetryStrategy: redislock.NoRetry()})
	if err != nil {
		s.logger.ErrorContext(ctx, "获取 SQL 目标锁失败", "operation", "sql_change_lock", "execution_id", record.ID, "err", err)
		return s.finishExecution(ctx, record, "failed", ErrExecutionFailed)
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := lock.Release(release); err != nil {
			s.logger.WarnContext(release, "释放 SQL 目标锁失败", "operation", "sql_change_unlock", "execution_id", record.ID, "err", err)
		}
	}()
	// 等锁期间可能撤权或调整连接，取得锁后再核对一次，不能仅信任入队时授权。
	if err = s.authorize(ctx, record.ActorID, "sql_change.execute"); err != nil {
		return s.finishExecution(ctx, record, "failed", err)
	}
	if err = s.authorize(ctx, record.ActorID, "application_database.execute"); err != nil {
		return s.finishExecution(ctx, record, "failed", err)
	}
	if err = s.validateReviewer(ctx, s.db, item.ReviewerID, approvedRevision.AuthorID); err != nil {
		return s.finishExecution(ctx, record, "failed", ErrForbidden)
	}
	target, err = s.currentTarget(ctx, s.db, item)
	if err != nil {
		return s.finishExecution(ctx, record, "failed", err)
	}
	claimed := false
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&model.SQLChangeExecution{}).Where("uuid = ? AND status = ? AND job_id = ?", record.ID, "queued", message.JobID).Updates(map[string]any{"status": "running", "started_at": now})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			return nil
		}
		r = tx.Model(&model.SQLChange{}).Where("uuid = ? AND revision_id = ? AND status = ? AND version = ? AND success_at IS NULL", item.ID, record.RevisionID, "queued", item.Version).Updates(map[string]any{"status": "running", "version": item.Version + 1, "updated_at": now})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		claimed = true
		return nil
	})
	if err != nil {
		return s.finishExecution(ctx, record, "failed", safeError(err))
	}
	if !claimed {
		return nil
	}
	record.Status = "running"
	var revision model.SQLChangeRevision
	if err = s.db.WithContext(ctx).First(&revision, "uuid = ? AND change_id = ?", record.RevisionID, item.ID).Error; err != nil {
		return s.finishExecution(ctx, record, "failed", ErrStorage)
	}
	sql, err := s.secrets.Decrypt(revision.Ciphertext, revisionAAD(revision.ID))
	if err != nil {
		return s.finishExecution(ctx, record, "failed", ErrStorage)
	}
	if digest(sql) != record.Digest {
		return s.finishExecution(ctx, record, "failed", ErrConflict)
	}
	password, err := s.secrets.Decrypt(target.Ciphertext, []byte("application-database:"+target.ID))
	if err != nil {
		return s.finishExecution(ctx, record, "failed", ErrStorage)
	}
	executeCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		interval := s.executionCheckInterval
		if interval <= 0 {
			interval = 10 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-executeCtx.Done():
				return
			case <-ticker.C:
				if e := s.checkExecutionAuthorization(executeCtx, record, item, approvedRevision.AuthorID); e != nil {
					s.logger.WarnContext(executeCtx, "SQL 执行授权或目标已变化，正在停止执行", "operation", "sql_change_execute_authorization", "execution_id", record.ID, "err", safeError(e))
					cancel()
					return
				}
				if e := lock.Refresh(executeCtx, 30*time.Second, nil); e != nil {
					s.logger.ErrorContext(executeCtx, "续期 SQL 目标锁失败", "operation", "sql_change_lock_refresh", "execution_id", record.ID, "err", e)
					cancel()
					return
				}
			}
		}
	}()
	err = s.runner.Execute(executeCtx, *target, password, sql, record.ID)
	if executeCtx.Err() != nil {
		return s.finishExecution(ctx, record, "uncertain", ErrExecutionUncertain)
	}
	if err != nil {
		// Goose/驱动可能复述 SQL 和密码，跨领域只传递稳定错误。
		s.logger.ErrorContext(ctx, "SQL 执行失败", "operation", "sql_change_execute_run", "execution_id", record.ID, "error_type", fmt.Sprintf("%T", err), "err", ErrExecutionFailed)
		if errors.Is(err, ErrExecutionUncertain) || executeCtx.Err() != nil {
			return s.finishExecution(ctx, record, "uncertain", ErrExecutionUncertain)
		}
		return s.finishExecution(ctx, record, "failed", ErrExecutionFailed)
	}
	return s.finishExecution(ctx, record, "succeeded", nil)
}

func (s *Service) checkExecutionAuthorization(ctx context.Context, record model.SQLChangeExecution, item *model.SQLChange, authorID string) error {
	if err := s.authorize(ctx, record.ActorID, "sql_change.execute"); err != nil {
		return err
	}
	if err := s.authorize(ctx, record.ActorID, "application_database.execute"); err != nil {
		return err
	}
	if err := s.validateReviewer(ctx, s.db, item.ReviewerID, authorID); err != nil {
		return err
	}
	_, err := s.currentTarget(ctx, s.db, item)
	return err
}

func (s *Service) finishExecution(ctx context.Context, record model.SQLChangeExecution, status string, public error) error {
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	reason := "SQL 已执行成功"
	if public != nil {
		reason = public.Error()
	}
	changed := false
	err := s.db.WithContext(finish).Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&model.SQLChangeExecution{}).Where("uuid = ? AND job_id = ? AND status = ?", record.ID, record.JobID, record.Status).Updates(map[string]any{"status": status, "reason": reason, "finished_at": now})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			return nil
		}
		updates := map[string]any{"status": status, "version": gorm.Expr("version + 1"), "updated_at": now}
		if status == "succeeded" {
			updates["success_at"] = now
		}
		r = tx.Model(&model.SQLChange{}).Where("uuid = ? AND revision_id = ? AND status = ?", record.ChangeID, record.RevisionID, record.Status).Updates(updates)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		changed = true
		return nil
	})
	if err != nil {
		return s.failure(finish, "sql_change_execute_finish", err, ErrStorage)
	}
	if !changed {
		return nil
	}
	return public
}

func (s *Service) Register(registry *worker.Registry) error {
	for _, entry := range []struct {
		kind    string
		handler worker.Handler
	}{{"sql_change.check", s.HandleCheck}, {"sql_change.execute", s.HandleExecute}} {
		entry := entry
		if err := registry.Register(entry.kind, func(ctx context.Context, message task.Message) error {
			if err := entry.handler(ctx, message); err != nil {
				return worker.NewPermanentError("sql_change_failed", safeError(err).Error(), safeError(err))
			}
			return nil
		}); err != nil {
			return s.failure(context.Background(), "sql_change_register", err, ErrUnavailable)
		}
		if err := registry.RegisterTerminalFailureHook(entry.kind, s.HandleTerminalTaskFailure); err != nil {
			return s.failure(context.Background(), "sql_change_register_failure", err, ErrUnavailable)
		}
	}
	return nil
}

func (s *Service) HandleTerminalTaskFailure(ctx context.Context, tx *gorm.DB, job model.Job, _ string, _ string) (result error) {
	defer func() {
		if result != nil {
			result = s.failure(ctx, "sql_change_terminal_failure", result, ErrStorage)
		}
	}()
	var payload taskPayload
	if json.Unmarshal(job.Payload, &payload) != nil {
		return nil
	}
	ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: job.ProjectID, DepartmentID: job.DepartmentID})
	db := tx.WithContext(ctx)
	now := time.Now().UTC()
	if job.Kind == "sql_change.check" {
		if err := db.Model(&model.SQLChangeRevision{}).Where("uuid = ? AND change_id = ? AND check_job_id = ? AND check_status IN ?", payload.RevisionID, payload.ChangeID, job.ID, []string{"checking", "running"}).Updates(map[string]any{"check_status": "failed", "check_summary": "SQL 检查中断，请重新检查"}).Error; err != nil {
			return err
		}
		return db.Model(&model.SQLChange{}).Where("uuid = ? AND revision_id = ? AND status = ?", payload.ChangeID, payload.RevisionID, "checking").Updates(map[string]any{"status": "check_failed", "version": gorm.Expr("version + 1"), "updated_at": now}).Error
	}
	if job.Kind != "sql_change.execute" {
		return nil
	}
	var record model.SQLChangeExecution
	if err := db.First(&record, "uuid = ? AND change_id = ? AND revision_id = ? AND job_id = ?", payload.ExecutionID, payload.ChangeID, payload.RevisionID, job.ID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if record.Status != "queued" && record.Status != "running" {
		return nil
	}
	status, reason := "failed", ErrExecutionFailed.Error()
	if record.Status == "running" {
		status, reason = "uncertain", ErrExecutionUncertain.Error()
	}
	if err := db.Model(&model.SQLChangeExecution{}).Where("uuid = ? AND status = ?", record.ID, record.Status).Updates(map[string]any{"status": status, "reason": reason, "finished_at": now}).Error; err != nil {
		return err
	}
	return db.Model(&model.SQLChange{}).Where("uuid = ? AND revision_id = ? AND status IN ?", payload.ChangeID, payload.RevisionID, []string{"queued", "running"}).Updates(map[string]any{"status": status, "version": gorm.Expr("version + 1"), "updated_at": now}).Error
}
