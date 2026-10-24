package sqlchange

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"edo/internal/model"
	"edo/internal/task"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) Review(ctx context.Context, actorID, id string, input ActionInput) (*DetailResult, error) {
	if err := s.authorize(ctx, actorID, "sql_change.review"); err != nil {
		return nil, err
	}
	if input.Decision != "approve" && input.Decision != "reject" || len(input.Reason) > 1000 || !utf8.ValidString(input.Reason) {
		return nil, s.failure(ctx, "sql_change_review_validate", ErrInvalid, ErrInvalid)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item, e := s.load(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"}), id)
		if e != nil {
			return e
		}
		if item.Version != input.ExpectedVersion {
			return ErrConflict
		}
		if item.Status != "pending_review" {
			return ErrState
		}
		if item.ReviewerID != actorID || item.CreatedBy == actorID {
			return ErrForbidden
		}
		if e = s.validateReviewer(ctx, tx, actorID, item.CreatedBy); e != nil {
			return e
		}
		var revision model.SQLChangeRevision
		if e = tx.First(&revision, "uuid = ? AND change_id = ? AND check_status = ?", item.RevisionID, item.ID, "passed").Error; e != nil {
			return ErrCheckFailed
		}
		if revision.AuthorID == actorID {
			return ErrForbidden
		}
		now := time.Now().UTC()
		status := "approved"
		if input.Decision == "reject" {
			status = "rejected"
		}
		r := tx.Model(&model.SQLChange{}).Where("uuid = ? AND version = ? AND status = ?", id, input.ExpectedVersion, "pending_review").Updates(map[string]any{"status": status, "version": item.Version + 1, "updated_at": now})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		return tx.Create(&model.SQLChangeReview{ID: uuid.NewString(), ProjectID: item.ProjectID, DepartmentID: item.DepartmentID, ChangeID: id, RevisionID: item.RevisionID, ReviewerID: actorID, Decision: input.Decision, Reason: strings.TrimSpace(input.Reason), CreatedAt: now}).Error
	})
	if err != nil {
		return nil, s.failure(ctx, "sql_change_review", err, safeError(err))
	}
	return s.detail(ctx, id)
}

func executionKey(item *model.SQLChange) string {
	return digest(item.ID, item.PipelineRunID, item.DatabaseFingerprint)
}
func executionRecord(item *model.SQLChange, revision *model.SQLChangeRevision, actorID, status, reason string, now time.Time) model.SQLChangeExecution {
	return model.SQLChangeExecution{ID: uuid.NewString(), ProjectID: item.ProjectID, DepartmentID: item.DepartmentID, ChangeID: item.ID, RevisionID: item.RevisionID, PipelineRunID: item.PipelineRunID, DatabaseID: item.DatabaseID, DatabaseFingerprint: item.DatabaseFingerprint, EnvironmentID: item.EnvironmentID, Phase: item.Phase, Digest: revision.Digest, ExecutionKey: executionKey(item), Status: status, ActorID: actorID, Reason: reason, CreatedAt: now}
}
func (s *Service) currentTarget(ctx context.Context, db *gorm.DB, item *model.SQLChange) (*model.ApplicationDatabase, error) {
	var target model.ApplicationDatabase
	if err := db.WithContext(ctx).First(&target, "uuid = ? AND project_id = ? AND department_id = ? AND application_id = ? AND environment_id = ? AND is_active = ?", item.DatabaseID, item.ProjectID, item.DepartmentID, item.ApplicationID, item.EnvironmentID, true).Error; err != nil {
		return nil, ErrNotFound
	}
	if target.Version != item.DatabaseVersion || target.Fingerprint != item.DatabaseFingerprint {
		return nil, ErrConflict
	}
	var run model.PipelineRun
	if err := db.WithContext(ctx).First(&run, "uuid = ? AND project_id = ? AND application_id = ? AND ref = ?", item.PipelineRunID, item.ProjectID, item.ApplicationID, item.Branch).Error; err != nil {
		return nil, ErrNotFound
	}
	return &target, nil
}
func (s *Service) Execute(ctx context.Context, actorID, id string, input ActionInput) (*DetailResult, error) {
	if err := s.authorize(ctx, actorID, "sql_change.execute"); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, actorID, "application_database.execute"); err != nil {
		return nil, err
	}
	if s.runner == nil || s.locks == nil {
		return nil, s.failure(ctx, "sql_change_execute_unavailable", ErrUnavailable, ErrUnavailable)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item, e := s.load(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"}), id)
		if e != nil {
			return e
		}
		if item.Version != input.ExpectedVersion {
			return ErrConflict
		}
		if item.Status != "approved" || item.SuccessAt != nil {
			return ErrState
		}
		if e = s.validateReviewer(ctx, tx, item.ReviewerID, item.CreatedBy); e != nil {
			return e
		}
		if _, e = s.currentTarget(ctx, tx, item); e != nil {
			return e
		}
		var revision model.SQLChangeRevision
		if e = tx.First(&revision, "uuid = ? AND change_id = ? AND check_status = ?", item.RevisionID, id, "passed").Error; e != nil {
			return ErrCheckFailed
		}
		if e = s.validateReviewer(ctx, tx, item.ReviewerID, revision.AuthorID); e != nil {
			return e
		}
		if input.ConfirmDigest == "" || input.ConfirmDigest != revision.Digest {
			return ErrConflict
		}
		var approval int64
		if e = tx.Model(&model.SQLChangeReview{}).Where("change_id = ? AND revision_id = ? AND reviewer_id = ? AND decision = ?", id, revision.ID, item.ReviewerID, "approve").Count(&approval).Error; e != nil {
			return e
		}
		if approval == 0 {
			return ErrState
		}
		var prior int64
		if e = tx.Model(&model.SQLChangeExecution{}).Where("execution_key = ?", executionKey(item)).Count(&prior).Error; e != nil {
			return e
		}
		if prior > 0 {
			return ErrState
		}
		now := time.Now().UTC()
		record := executionRecord(item, &revision, actorID, "queued", "", now)
		if e = tx.Create(&record).Error; e != nil {
			return e
		}
		job, e := task.NewService(tx, 4).Create(ctx, task.CreateInput{ProjectID: item.ProjectID, DepartmentID: item.DepartmentID, Kind: "sql_change.execute", Subject: task.LogicalSubjectPrefix + ".sql_change.execute", Payload: taskPayload{ChangeID: id, RevisionID: revision.ID, ExecutionID: record.ID}, IdempotencyKey: "sql-change-execute:" + record.ExecutionKey, MaxAttempts: 1, Idempotent: false})
		if e != nil {
			return e
		}
		if e = tx.Model(&model.SQLChangeExecution{}).Where("uuid = ?", record.ID).Update("job_id", job.ID).Error; e != nil {
			return e
		}
		r := tx.Model(&model.SQLChange{}).Where("uuid = ? AND version = ? AND revision_id = ? AND status = ? AND success_at IS NULL", id, input.ExpectedVersion, revision.ID, "approved").Updates(map[string]any{"status": "queued", "version": item.Version + 1, "updated_at": now})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, s.failure(ctx, "sql_change_execute", err, safeError(err))
	}
	return s.detail(ctx, id)
}
func (s *Service) Skip(ctx context.Context, actorID, id string, input ActionInput) (*DetailResult, error) {
	if err := s.authorize(ctx, actorID, "sql_change.skip"); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, actorID, "sql_change.review"); err != nil {
		return nil, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" || len(input.Reason) > 1000 || !utf8.ValidString(input.Reason) {
		return nil, s.failure(ctx, "sql_change_skip_validate", ErrInvalid, ErrInvalid)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item, e := s.load(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"}), id)
		if e != nil {
			return e
		}
		if item.Version != input.ExpectedVersion {
			return ErrConflict
		}
		if e = editable(item); e != nil {
			return e
		}
		if item.Status == "skipped" {
			return ErrState
		}
		if item.ReviewerID != actorID || item.CreatedBy == actorID {
			return ErrForbidden
		}
		if e = s.validateReviewer(ctx, tx, actorID, item.CreatedBy); e != nil {
			return e
		}
		var revision model.SQLChangeRevision
		if e = tx.First(&revision, "uuid = ? AND change_id = ?", item.RevisionID, id).Error; e != nil {
			return e
		}
		if revision.AuthorID == actorID {
			return ErrForbidden
		}
		now := time.Now().UTC()
		var prior model.SQLChangeExecution
		e = tx.Where("execution_key = ?", executionKey(item)).First(&prior).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			record := executionRecord(item, &revision, actorID, "skipped", input.Reason, now)
			record.FinishedAt = &now
			if e = tx.Create(&record).Error; e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else if prior.Status == "queued" || prior.Status == "running" || prior.Status == "succeeded" {
			return ErrState
		}
		// 失败后的跳过保留原执行结果，以独立审核记录表达人工决定。
		if e = tx.Create(&model.SQLChangeReview{ID: uuid.NewString(), ProjectID: item.ProjectID, DepartmentID: item.DepartmentID, ChangeID: id, RevisionID: item.RevisionID, ReviewerID: actorID, Decision: "skip", Reason: input.Reason, CreatedAt: now}).Error; e != nil {
			return e
		}
		r := tx.Model(&model.SQLChange{}).Where("uuid = ? AND version = ? AND success_at IS NULL AND status NOT IN ?", id, input.ExpectedVersion, []string{"queued", "running", "succeeded"}).Updates(map[string]any{"status": "skipped", "version": item.Version + 1, "updated_at": now})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, s.failure(ctx, "sql_change_skip", err, safeError(err))
	}
	return s.detail(ctx, id)
}
