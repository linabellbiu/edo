package sqlchange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/task"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db                     *gorm.DB
	secrets                *secret.Manager
	permissions            PermissionChecker
	logger                 *slog.Logger
	runner                 Runner
	locks                  LockManager
	lockPrefix             string
	executionCheckInterval time.Duration
}

func New(db *gorm.DB, secrets *secret.Manager, permissions PermissionChecker, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: db, secrets: secrets, permissions: permissions, logger: logger}
}
func (s *Service) ConfigureRunner(runner Runner) { s.runner = runner }
func (s *Service) ConfigureLocks(locks LockManager, prefix string) {
	s.locks = locks
	s.lockPrefix = prefix
}

func (s *Service) failure(ctx context.Context, operation string, cause, public error) error {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	s.logger.ErrorContext(ctx, "SQL 工单操作失败", "operation", operation, "project_id", scope.ProjectID, "department_id", scope.DepartmentID, "actor_id", scope.UserID, "err", cause)
	return public
}
func scopeFrom(ctx context.Context) (database.DepartmentScope, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.ProjectID == "" || scope.DepartmentID == "" {
		return scope, ErrScope
	}
	return scope, nil
}
func (s *Service) authorize(ctx context.Context, actorID, permission string) error {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return s.failure(ctx, "sql_change_scope", err, err)
	}
	if actorID == "" || scope.UserID != "" && scope.UserID != actorID {
		return s.failure(ctx, "sql_change_actor", ErrForbidden, ErrForbidden)
	}
	var user model.User
	if err = s.db.WithContext(ctx).First(&user, "uuid = ? AND is_active = ?", actorID, true).Error; err != nil {
		return s.failure(ctx, "sql_change_actor", err, ErrForbidden)
	}
	if !user.IsSuperuser && user.DepartmentID != scope.DepartmentID {
		return s.failure(ctx, "sql_change_department", ErrForbidden, ErrForbidden)
	}
	var project model.Project
	if err = s.db.WithContext(ctx).First(&project, "uuid = ? AND department_id = ? AND is_active = ?", scope.ProjectID, scope.DepartmentID, true).Error; err != nil {
		return s.failure(ctx, "sql_change_project", err, ErrForbidden)
	}
	if !user.IsSuperuser {
		var members int64
		if err = s.db.WithContext(ctx).Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", scope.ProjectID, actorID).Count(&members).Error; err != nil {
			return s.failure(ctx, "sql_change_membership", err, ErrForbidden)
		}
		if members == 0 {
			return s.failure(ctx, "sql_change_membership", ErrForbidden, ErrForbidden)
		}
	}
	if s.permissions == nil {
		return s.failure(ctx, "sql_change_permission_unavailable", ErrUnavailable, ErrUnavailable)
	}
	allowed, err := s.permissions.HasPermission(ctx, &user, permission)
	if err != nil || !allowed {
		return s.failure(ctx, "sql_change_permission", err, ErrForbidden)
	}
	if permission != "sql_change.read" {
		allowed, err = s.permissions.HasPermission(ctx, &user, "sql_change.read")
		if err != nil || !allowed {
			return s.failure(ctx, "sql_change_read_permission", err, ErrForbidden)
		}
	}
	return nil
}
func (s *Service) requestActor(ctx context.Context, permission string) error {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return s.failure(ctx, "sql_change_scope", err, err)
	}
	return s.authorize(ctx, scope.UserID, permission)
}
func (s *Service) load(ctx context.Context, db *gorm.DB, id string) (*model.SQLChange, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	var item model.SQLChange
	if err = db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ?", id, scope.ProjectID, scope.DepartmentID).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &item, nil
}
func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
func revisionAAD(id string) []byte { return []byte("sql-change-revision:" + id) }
func editable(item *model.SQLChange) error {
	if item.SuccessAt != nil || item.Status == "succeeded" {
		return ErrImmutable
	}
	if item.Status == "queued" || item.Status == "running" {
		return ErrState
	}
	return nil
}
func safeError(err error) error {
	for _, known := range []error{ErrScope, ErrForbidden, ErrNotFound, ErrInvalid, ErrConflict, ErrImmutable, ErrState, ErrUnavailable, ErrCheckFailed, ErrExecutionFailed, ErrExecutionUncertain} {
		if errors.Is(err, known) {
			return known
		}
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrConflict
	}
	return ErrStorage
}
func (s *Service) Detail(ctx context.Context, id string) (*DetailResult, error) {
	if err := s.requestActor(ctx, "sql_change.read"); err != nil {
		return nil, err
	}
	return s.detail(ctx, id)
}
func (s *Service) detail(ctx context.Context, id string) (*DetailResult, error) {
	item, err := s.load(ctx, s.db, id)
	if err != nil {
		return nil, s.failure(ctx, "sql_change_detail", err, safeError(err))
	}
	var revision model.SQLChangeRevision
	if err = s.db.WithContext(ctx).First(&revision, "uuid = ? AND change_id = ?", item.RevisionID, item.ID).Error; err != nil {
		return nil, s.failure(ctx, "sql_change_revision", err, ErrStorage)
	}
	sql, err := s.secrets.Decrypt(revision.Ciphertext, revisionAAD(revision.ID))
	if err != nil {
		return nil, s.failure(ctx, "sql_change_decrypt", err, ErrStorage)
	}
	result := &DetailResult{Change: View{SQLChange: *item, Digest: revision.Digest, AuthorID: revision.AuthorID}, SQL: sql, Revision: RevisionView{SQLChangeRevision: revision, CheckReport: json.RawMessage("[]")}, Reviews: []model.SQLChangeReview{}, Executions: []model.SQLChangeExecution{}}
	if revision.CheckReport != "" {
		raw, e := s.secrets.Decrypt(revision.CheckReport, []byte("sql-change-check:"+revision.ID))
		if e != nil {
			return nil, s.failure(ctx, "sql_change_check_decrypt", e, ErrStorage)
		}
		result.Revision.CheckReport = json.RawMessage(raw)
	}
	if err = s.db.WithContext(ctx).Where("change_id = ?", id).Order("id ASC").Find(&result.Reviews).Error; err == nil {
		err = s.db.WithContext(ctx).Where("change_id = ?", id).Order("id ASC").Find(&result.Executions).Error
	}
	if err != nil {
		return nil, s.failure(ctx, "sql_change_history", err, ErrStorage)
	}
	return result, nil
}
func (s *Service) List(ctx context.Context, input ListInput) (*ListResult, error) {
	if err := s.requestActor(ctx, "sql_change.read"); err != nil {
		return nil, err
	}
	scope, _ := scopeFrom(ctx)
	if input.Page < 1 {
		input.Page = 1
	}
	if input.PageSize < 1 {
		input.PageSize = 10
	}
	if input.PageSize > 100 {
		input.PageSize = 100
	}
	if input.Page > 1000000 {
		return nil, s.failure(ctx, "sql_change_page", ErrInvalid, ErrInvalid)
	}
	query := s.db.WithContext(ctx).Model(&model.SQLChange{}).Where("project_id = ? AND department_id = ?", scope.ProjectID, scope.DepartmentID)
	if input.Search != "" {
		search := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(input.Search)
		query = query.Where("name LIKE ? ESCAPE '!'", "%"+search+"%")
	}
	if input.Status != "" {
		query = query.Where("status = ?", input.Status)
	}
	if input.ApplicationID != "" {
		query = query.Where("application_id = ?", input.ApplicationID)
	}
	if input.PipelineRunID != "" {
		query = query.Where("pipeline_run_id = ?", input.PipelineRunID)
	}
	result := &ListResult{Items: []View{}}
	if err := query.Count(&result.Total).Error; err != nil {
		return nil, s.failure(ctx, "sql_change_count", err, ErrStorage)
	}
	var items []model.SQLChange
	if err := query.Order("id DESC").Offset((input.Page - 1) * input.PageSize).Limit(input.PageSize).Find(&items).Error; err != nil {
		return nil, s.failure(ctx, "sql_change_list", err, ErrStorage)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.RevisionID)
	}
	var revisions []model.SQLChangeRevision
	if len(ids) > 0 {
		if err := s.db.WithContext(ctx).Select("uuid", "digest", "author_id").Where("uuid IN ?", ids).Find(&revisions).Error; err != nil {
			return nil, s.failure(ctx, "sql_change_list_revisions", err, ErrStorage)
		}
	}
	digests := map[string]string{}
	authors := map[string]string{}
	for _, revision := range revisions {
		digests[revision.ID] = revision.Digest
		authors[revision.ID] = revision.AuthorID
	}
	for _, item := range items {
		result.Items = append(result.Items, View{SQLChange: item, Digest: digests[item.RevisionID], AuthorID: authors[item.RevisionID]})
	}
	return result, nil
}
func validateSQL(sql string) bool {
	if len(sql) == 0 || len(sql) > MaxSQLBytes || !utf8.ValidString(sql) || strings.ContainsRune(sql, 0) || strings.TrimSpace(sql) == "" {
		return false
	}
	for _, line := range strings.Split(sql, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(line, "--") && (strings.Contains(line, "sqlfluff:") || strings.Contains(line, "+goose") || strings.Contains(line, "noqa")) {
			return false
		}
	}
	return true
}
func (s *Service) validateReviewer(ctx context.Context, db *gorm.DB, id, authorID string) error {
	scope, _ := scopeFrom(ctx)
	if id == "" || id == authorID {
		return ErrInvalid
	}
	var user model.User
	if err := db.WithContext(ctx).First(&user, "uuid = ? AND is_active = ?", id, true).Error; err != nil {
		return ErrInvalid
	}
	if !user.IsSuperuser && user.DepartmentID != scope.DepartmentID {
		return ErrForbidden
	}
	var count int64
	if err := db.WithContext(ctx).Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", scope.ProjectID, id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrInvalid
	}
	allowed, err := s.permissions.HasPermission(ctx, &user, "sql_change.review")
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	allowed, err = s.permissions.HasPermission(ctx, &user, "sql_change.read")
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
func (s *Service) Save(ctx context.Context, actorID, id string, input Input) (*DetailResult, error) {
	permission := "sql_change.create"
	if id != "" {
		permission = "sql_change.update"
	}
	return s.save(ctx, actorID, id, input, permission, actorID)
}
func (s *Service) save(ctx context.Context, actorID, id string, input Input, permission, authorID string) (*DetailResult, error) {
	if err := s.authorize(ctx, actorID, permission); err != nil {
		return nil, err
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Phase == "" {
		input.Phase = "before"
	}
	input.Branch = strings.TrimSpace(input.Branch)
	if !strings.HasPrefix(input.Branch, "refs/heads/") {
		input.Branch = "refs/heads/" + input.Branch
	}
	if input.Name == "" || len(input.Name) > 128 || !utf8.ValidString(input.Name) || !validateSQL(input.SQL) || (input.Phase != "before" && input.Phase != "after") || input.Branch == "refs/heads/" || len(input.Branch) > 512 {
		return nil, s.failure(ctx, "sql_change_validate", ErrInvalid, ErrInvalid)
	}
	scope, _ := scopeFrom(ctx)
	now := time.Now().UTC()
	var item *model.SQLChange
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if id == "" {
			if input.ExpectedVersion != 0 {
				return ErrConflict
			}
			item = &model.SQLChange{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, CreatedBy: actorID, CreatedAt: now}
		}
		if id != "" {
			var e error
			item, e = s.load(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"}), id)
			if e != nil {
				return e
			}
			if item.Version != input.ExpectedVersion {
				return ErrConflict
			}
			if e = editable(item); e != nil {
				return e
			}
		}
		var app model.Application
		if e := tx.First(&app, "uuid = ? AND project_id = ? AND department_id = ? AND is_active = ?", input.ApplicationID, scope.ProjectID, scope.DepartmentID, true).Error; e != nil {
			return ErrNotFound
		}
		if input.RepositoryID != "" && input.RepositoryID != app.RepositoryID {
			return ErrInvalid
		}
		var run model.PipelineRun
		if e := tx.First(&run, "uuid = ? AND project_id = ? AND application_id = ? AND ref = ?", input.PipelineRunID, scope.ProjectID, app.ID, input.Branch).Error; e != nil {
			return ErrNotFound
		}
		var target model.ApplicationDatabase
		if e := tx.First(&target, "uuid = ? AND project_id = ? AND application_id = ? AND is_active = ?", input.DatabaseID, scope.ProjectID, app.ID, true).Error; e != nil {
			return ErrNotFound
		}
		if input.EnvironmentID != "" && input.EnvironmentID != target.EnvironmentID {
			return ErrInvalid
		}
		if input.ReviewerID == item.CreatedBy {
			return ErrInvalid
		}
		if e := s.validateReviewer(ctx, tx, input.ReviewerID, authorID); e != nil {
			return e
		}
		if id != "" {
			var executions int64
			if e := tx.Model(&model.SQLChangeExecution{}).Where("change_id = ?", item.ID).Count(&executions).Error; e != nil {
				return e
			}
			if executions > 0 && (item.PipelineRunID != run.ID || item.DatabaseID != target.ID || item.DatabaseFingerprint != target.Fingerprint) {
				return ErrState
			}
		}
		revision := model.SQLChangeRevision{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, ChangeID: item.ID, Version: item.Version + 1, Digest: digest(input.SQL), CheckStatus: "checking", CheckPermission: permission, CheckSummary: "正在检查 SQL", CreatedBy: actorID, AuthorID: authorID, CreatedAt: now}
		var e error
		revision.Ciphertext, e = s.secrets.Encrypt(input.SQL, revisionAAD(revision.ID))
		if e != nil {
			return e
		}
		// 重查必须走工单入口重新冻结修订，不能由任务中心换 Job ID 重放旧检查。
		job, e := task.NewService(tx, 4).Create(ctx, task.CreateInput{ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Kind: "sql_change.check", Subject: task.LogicalSubjectPrefix + ".sql_change.check", Payload: taskPayload{ChangeID: item.ID, RevisionID: revision.ID}, IdempotencyKey: "sql-change-check:" + revision.ID, MaxAttempts: 1, Idempotent: false})
		if e != nil {
			return e
		}
		revision.CheckJobID = job.ID
		if e = tx.Create(&revision).Error; e != nil {
			return e
		}
		item.Name, item.ApplicationID, item.RepositoryID, item.Branch, item.PipelineRunID = input.Name, app.ID, app.RepositoryID, input.Branch, run.ID
		item.DatabaseID, item.EnvironmentID, item.DatabaseVersion, item.DatabaseFingerprint = target.ID, target.EnvironmentID, target.Version, target.Fingerprint
		item.Phase, item.ReviewerID, item.Version, item.RevisionID, item.Status, item.UpdatedAt = input.Phase, input.ReviewerID, revision.Version, revision.ID, "checking", now
		if id == "" {
			return tx.Create(item).Error
		}
		result := tx.Model(&model.SQLChange{}).Where("uuid = ? AND version = ? AND success_at IS NULL AND status NOT IN ?", id, input.ExpectedVersion, []string{"queued", "running", "succeeded"}).Select("*").Omit("id", "uuid", "project_id", "department_id", "created_at", "created_by", "deleted_at").Updates(item)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, s.failure(ctx, "sql_change_save", err, safeError(err))
	}
	return s.detail(ctx, item.ID)
}
func (s *Service) Recheck(ctx context.Context, actorID, id string, input ActionInput) (*DetailResult, error) {
	if err := s.authorize(ctx, actorID, "sql_change.check"); err != nil {
		return nil, err
	}
	detail, err := s.detail(ctx, id)
	if err != nil {
		return nil, err
	}
	c := detail.Change
	return s.save(ctx, actorID, id, Input{Name: c.Name, ApplicationID: c.ApplicationID, RepositoryID: c.RepositoryID, Branch: c.Branch, PipelineRunID: c.PipelineRunID, DatabaseID: c.DatabaseID, EnvironmentID: c.EnvironmentID, Phase: c.Phase, ReviewerID: c.ReviewerID, SQL: detail.SQL, ExpectedVersion: input.ExpectedVersion}, "sql_change.check", detail.Revision.AuthorID)
}
func (s *Service) Delete(ctx context.Context, actorID, id string, version uint64) error {
	if err := s.authorize(ctx, actorID, "sql_change.delete"); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item, e := s.load(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"}), id)
		if e != nil {
			return e
		}
		if e = editable(item); e != nil {
			return e
		}
		if item.Version != version {
			return ErrConflict
		}
		r := tx.Where("uuid = ? AND version = ? AND success_at IS NULL AND status NOT IN ?", id, version, []string{"queued", "running", "succeeded"}).Delete(&model.SQLChange{})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return s.failure(ctx, "sql_change_delete", err, safeError(err))
	}
	return nil
}
