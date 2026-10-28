package cicd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/variablecatalog"
)

var (
	ErrScope                  = errors.New("请先选择有权访问的项目")
	ErrInvalidVariable        = errors.New("变量配置无效，请检查名称、用途和适用范围")
	ErrVariableConflict       = errors.New("变量已存在或已被修改，请刷新后重试")
	ErrVariableUnavailable    = errors.New("引用的变量不存在、已停用或不允许用于当前版本")
	ErrExecutionAuthorization = errors.New("当前执行授权已失效，请检查项目成员和任务权限")
	ErrStorage                = errors.New("交付配置暂时无法保存或读取，请稍后重试")
)

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type Service struct {
	db      *gorm.DB
	secrets *secret.Manager
	logger  *slog.Logger
}

func New(db *gorm.DB, secrets *secret.Manager, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: db, secrets: secrets, logger: logger}
}

func (s *Service) failure(ctx context.Context, operation string, cause, public error) error {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	s.logger.ErrorContext(ctx, "交付配置操作失败", "operation", operation, "project_id", scope.ProjectID, "department_id", scope.DepartmentID, "err", cause)
	return public
}

func scopeFrom(ctx context.Context) (database.DepartmentScope, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.ProjectID == "" || scope.DepartmentID == "" {
		return scope, ErrScope
	}
	return scope, nil
}

type VariableInput struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	ApplicationID   string   `json:"application_id"`
	EnvironmentID   string   `json:"environment_id"`
	Kind            string   `json:"kind"`
	Usage           string   `json:"usage"`
	Value           *string  `json:"value"`
	IsSecret        bool     `json:"is_secret"`
	IsActive        bool     `json:"is_active"`
	ProtectedRefs   []string `json:"protected_refs"`
	ExpectedVersion uint64   `json:"expected_version"`
}

type VariableView struct {
	model.DeliveryVariable
	Value *string `json:"value,omitempty"`
}

func (s *Service) ListVariables(ctx context.Context) ([]VariableView, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, s.failure(ctx, "delivery_variable_scope", err, err)
	}
	var variables []model.DeliveryVariable
	if err = s.db.WithContext(ctx).Where("project_id = ?", scope.ProjectID).Order("id DESC").Find(&variables).Error; err != nil {
		return nil, s.failure(ctx, "delivery_variable_list", err, ErrStorage)
	}
	result := make([]VariableView, 0, len(variables))
	for _, v := range variables {
		view := VariableView{DeliveryVariable: v}
		if !v.IsSecret {
			var revision model.DeliveryVariableRevision
			if err = s.db.WithContext(ctx).Where("project_id = ? AND variable_id = ? AND version = ?", scope.ProjectID, v.ID, v.Version).First(&revision).Error; err != nil {
				return nil, s.failure(ctx, "delivery_variable_revision_read", err, ErrStorage)
			}
			view.Value = &revision.Value
		}
		result = append(result, view)
	}
	return result, nil
}

func (s *Service) SaveVariable(ctx context.Context, actorID, id string, in VariableInput) (*VariableView, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, s.failure(ctx, "delivery_variable_scope", err, err)
	}
	in.Name = strings.TrimSpace(in.Name)
	if !variableName.MatchString(in.Name) || len(in.Description) > 500 || (in.Kind != "env" && in.Kind != "file") ||
		(in.Usage != "build" && in.Usage != "deploy") || (in.Usage == "deploy" && in.Kind == "file") || len(in.ProtectedRefs) > 32 ||
		(in.Value != nil && (len(*in.Value) > 65536 || strings.ContainsRune(*in.Value, '\x00'))) {
		return nil, s.failure(ctx, "delivery_variable_validate", ErrInvalidVariable, ErrInvalidVariable)
	}
	for _, p := range in.ProtectedRefs {
		if len(p) > 255 || (!strings.HasPrefix(p, "refs/heads/") && !strings.HasPrefix(p, "refs/tags/")) {
			return nil, s.failure(ctx, "delivery_variable_ref_validate", ErrInvalidVariable, ErrInvalidVariable)
		}
		if _, e := path.Match(p, "refs/heads/main"); e != nil {
			return nil, s.failure(ctx, "delivery_variable_ref_validate", e, ErrInvalidVariable)
		}
	}
	// 系统变量不能由用户配置覆盖。
	if _, reserved := variablecatalog.ReservedScriptEnvironmentNames()[in.Name]; reserved {
		return nil, s.failure(ctx, "delivery_variable_reserved", ErrInvalidVariable, ErrInvalidVariable)
	}
	var item model.DeliveryVariable
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if in.ApplicationID != "" {
			var application model.Application
			if e := tx.Where("uuid = ? AND project_id = ?", in.ApplicationID, scope.ProjectID).First(&application).Error; e != nil {
				return ErrInvalidVariable
			}
		}
		if in.EnvironmentID != "" {
			var environment model.Environment
			if e := tx.Where("uuid = ? AND project_id = ?", in.EnvironmentID, scope.ProjectID).First(&environment).Error; e != nil {
				return ErrInvalidVariable
			}
		}
		now := time.Now().UTC()
		if id == "" {
			if in.Value == nil || in.ExpectedVersion != 0 {
				return ErrInvalidVariable
			}
			item = model.DeliveryVariable{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID,
				ApplicationID: in.ApplicationID, EnvironmentID: in.EnvironmentID, Name: in.Name, Kind: in.Kind, Usage: in.Usage,
				Version: 1, CreatedBy: actorID, CreatedAt: now}
			item.ScopeKey = digest(scope.ProjectID, in.ApplicationID, in.EnvironmentID, in.Usage, in.Name)
		} else {
			if e := tx.Where("uuid = ? AND project_id = ?", id, scope.ProjectID).First(&item).Error; e != nil {
				return ErrVariableUnavailable
			}
			if item.Version != in.ExpectedVersion {
				return ErrVariableConflict
			}
			if item.Name != in.Name || item.ApplicationID != in.ApplicationID || item.EnvironmentID != in.EnvironmentID || item.Kind != in.Kind || item.Usage != in.Usage {
				return ErrInvalidVariable
			}
			item.Version++
		}
		value := ""
		if in.Value != nil {
			value = *in.Value
		} else {
			var previous model.DeliveryVariableRevision
			if e := tx.Where("project_id = ? AND variable_id = ? AND version = ?", scope.ProjectID, item.ID, item.Version-1).First(&previous).Error; e != nil {
				return e
			}
			value = previous.Value
			if previous.IsSecret {
				var e error
				value, e = s.secrets.Decrypt(previous.Ciphertext, []byte(variableAAD(item.ID, previous.Version)))
				if e != nil {
					return e
				}
			}
		}
		if item.IsSecret && !in.IsSecret {
			return ErrInvalidVariable
		}
		item.Description, item.IsSecret, item.IsActive = in.Description, in.IsSecret, in.IsActive
		item.ProtectedRefs = append([]string{}, in.ProtectedRefs...)
		item.UpdatedBy, item.UpdatedAt = actorID, now
		revision := model.DeliveryVariableRevision{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID,
			VariableID: item.ID, Version: item.Version, IsSecret: in.IsSecret, Value: value, CreatedBy: actorID, CreatedAt: now}
		if in.IsSecret {
			var e error
			revision.Ciphertext, e = s.secrets.Encrypt(value, []byte(variableAAD(item.ID, item.Version)))
			if e != nil {
				return e
			}
			revision.Value = ""
		}
		if id == "" {
			if e := tx.Create(&item).Error; e != nil {
				return e
			}
		} else {
			result := tx.Model(&model.DeliveryVariable{}).Where("uuid = ? AND project_id = ? AND version = ?", item.ID, scope.ProjectID, in.ExpectedVersion).
				Updates(map[string]any{"description": item.Description, "is_secret": item.IsSecret, "is_active": item.IsActive,
					"protected_refs": jsonValue(item.ProtectedRefs), "version": item.Version, "updated_by": actorID, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrVariableConflict
			}
		}
		return tx.Create(&revision).Error
	})
	if err != nil {
		public := ErrStorage
		switch {
		case errors.Is(err, ErrInvalidVariable):
			public = ErrInvalidVariable
		case errors.Is(err, ErrVariableUnavailable):
			public = ErrVariableUnavailable
		case errors.Is(err, ErrVariableConflict), errors.Is(err, gorm.ErrDuplicatedKey):
			public = ErrVariableConflict
		case errors.Is(err, secret.ErrUnavailable):
			public = secret.ErrUnavailable
		}
		return nil, s.failure(ctx, "delivery_variable_save", err, public)
	}
	view := &VariableView{DeliveryVariable: item}
	if !item.IsSecret && in.Value != nil {
		v := *in.Value
		view.Value = &v
	}
	return view, nil
}

func (s *Service) DeleteVariable(ctx context.Context, id string, version uint64) error {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return s.failure(ctx, "delivery_variable_scope", err, err)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item model.DeliveryVariable
		if e := tx.Where("uuid = ? AND project_id = ? AND version = ?", id, scope.ProjectID, version).First(&item).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return ErrVariableConflict
			}
			return e
		}
		result := tx.Model(&model.DeliveryVariable{}).Where("uuid = ? AND project_id = ? AND version = ?", item.ID, scope.ProjectID, version).
			Updates(map[string]any{"scope_key": digest("deleted", item.ID), "is_active": false})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVariableConflict
		}
		result = tx.Where("uuid = ? AND project_id = ? AND version = ?", item.ID, scope.ProjectID, version).Delete(&model.DeliveryVariable{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVariableConflict
		}
		return nil
	})
	if err != nil {
		public := ErrStorage
		if errors.Is(err, ErrVariableConflict) {
			public = ErrVariableConflict
		}
		return s.failure(ctx, "delivery_variable_delete", err, public)
	}
	return nil
}

// FreezeVariables 固定引用与修订号，不把解密值写入运行快照。
func (s *Service) FreezeVariables(ctx context.Context, projectID, applicationID, environmentID, usage, ref string, names []string) ([]model.DeliveryVariableBinding, error) {
	if len(names) == 0 {
		return []model.DeliveryVariableBinding{}, nil
	}
	// 后台任务也必须携带已验证的项目范围，不能只信任调用方传入的项目 UUID。
	scope, scopeErr := scopeFrom(ctx)
	if scopeErr != nil || projectID != scope.ProjectID || len(names) > 128 {
		return nil, s.failure(ctx, "delivery_variable_freeze_scope", ErrScope, ErrScope)
	}
	var items []model.DeliveryVariable
	err := s.db.WithContext(ctx).Where(map[string]any{"project_id": projectID, "usage": usage, "is_active": true}).
		Where("application_id IN ? AND environment_id IN ?", []string{"", applicationID}, []string{"", environmentID}).Order("id ASC").Find(&items).Error
	if err != nil {
		return nil, s.failure(ctx, "delivery_variable_freeze", err, ErrStorage)
	}
	selected := map[string]model.DeliveryVariable{}
	for _, item := range items {
		if old, ok := selected[item.Name]; !ok || variablePriority(item) > variablePriority(old) {
			selected[item.Name] = item
		}
	}
	result := make([]model.DeliveryVariableBinding, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		item, ok := selected[name]
		if !ok || seen[name] || !refAllowed(item.ProtectedRefs, ref) {
			return nil, s.failure(ctx, "delivery_variable_freeze_reference", ErrVariableUnavailable, ErrVariableUnavailable)
		}
		seen[name] = true
		result = append(result, model.DeliveryVariableBinding{Name: name, VariableID: item.ID, Version: item.Version, Kind: item.Kind, IsSecret: item.IsSecret})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *Service) ResolveVariables(ctx context.Context, projectID, ref string, bindings []model.DeliveryVariableBinding) (map[string]string, map[string]string, []string, error) {
	values, files := map[string]string{}, map[string]string{}
	redactions := []string{}
	if len(bindings) > 0 {
		scope, err := scopeFrom(ctx)
		if err != nil {
			return nil, nil, nil, s.failure(ctx, "delivery_variable_resolve_scope", err, ErrScope)
		}
		if projectID != scope.ProjectID {
			return nil, nil, nil, s.failure(ctx, "delivery_variable_resolve_scope", ErrScope, ErrVariableUnavailable)
		}
	}
	for _, binding := range bindings {
		var item model.DeliveryVariable
		if e := s.db.WithContext(ctx).Where("project_id = ? AND uuid = ? AND is_active = ?", projectID, binding.VariableID, true).First(&item).Error; e != nil {
			return nil, nil, nil, s.failure(ctx, "delivery_variable_resolve_scope", e, ErrVariableUnavailable)
		}
		if !refAllowed(item.ProtectedRefs, ref) {
			return nil, nil, nil, s.failure(ctx, "delivery_variable_resolve_ref", ErrVariableUnavailable, ErrVariableUnavailable)
		}
		var revision model.DeliveryVariableRevision
		if e := s.db.WithContext(ctx).Where("project_id = ? AND variable_id = ? AND version = ?", projectID, binding.VariableID, binding.Version).First(&revision).Error; e != nil {
			return nil, nil, nil, s.failure(ctx, "delivery_variable_resolve_revision", e, ErrVariableUnavailable)
		}
		if binding.Name != item.Name || binding.Kind != item.Kind || binding.IsSecret != revision.IsSecret {
			return nil, nil, nil, s.failure(ctx, "delivery_variable_resolve_identity", ErrVariableUnavailable, ErrVariableUnavailable)
		}
		value := revision.Value
		if revision.IsSecret {
			var e error
			value, e = s.secrets.Decrypt(revision.Ciphertext, []byte(variableAAD(item.ID, revision.Version)))
			if e != nil {
				return nil, nil, nil, s.failure(ctx, "delivery_variable_decrypt", e, ErrVariableUnavailable)
			}
			if value != "" {
				redactions = append(redactions, value)
			}
		}
		if binding.Kind == "file" {
			files[binding.Name] = value
		} else {
			values[binding.Name] = value
		}
	}
	return values, files, redactions, nil
}

func variablePriority(v model.DeliveryVariable) int {
	score := 0
	if v.ApplicationID != "" {
		score += 2
	}
	if v.EnvironmentID != "" {
		score += 4
	}
	return score
}
func refAllowed(patterns []string, ref string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, ref); ok {
			return true
		}
	}
	return false
}
func variableAAD(id string, version uint64) string {
	return fmt.Sprintf("delivery-variable:%s:%d", id, version)
}
func digest(parts ...string) string {
	v := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(v[:])
}
