package cicd

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/secret"
)

// VariableRevisionView 不包含密文；只有明确读取正文时才填充 Value。
type VariableRevisionView struct {
	Version       uint64    `json:"version"`
	IsSecret      bool      `json:"is_secret"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
	Value         *string   `json:"value,omitempty"`
	Hidden        bool      `json:"hidden"`
}

type VariableRevisionPage struct {
	Revisions      []VariableRevisionView `json:"revisions"`
	Total          int64                  `json:"total"`
	Page           int                    `json:"page"`
	PageSize       int                    `json:"page_size"`
	CurrentVersion uint64                 `json:"current_version"`
}

func (s *Service) revisionVariable(ctx context.Context, id string) (*model.DeliveryVariable, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, s.failure(ctx, "configuration_history_scope", err, err)
	}
	var item model.DeliveryVariable
	if err = s.db.WithContext(ctx).Where("uuid = ? AND project_id = ?", id, scope.ProjectID).First(&item).Error; err != nil {
		public := ErrStorage
		if errors.Is(err, gorm.ErrRecordNotFound) {
			public = ErrVariableUnavailable
		}
		return nil, s.failure(ctx, "configuration_history_variable", err, public)
	}
	return &item, nil
}

func (s *Service) ListVariableRevisions(ctx context.Context, id string, page, pageSize int) (*VariableRevisionPage, error) {
	if page < 1 || page > 100000 || pageSize < 1 || pageSize > 50 {
		return nil, s.failure(ctx, "configuration_history_pagination", ErrInvalidVariable, ErrInvalidVariable)
	}
	item, err := s.revisionVariable(ctx, id)
	if err != nil {
		return nil, err
	}
	query := s.db.WithContext(ctx).Model(&model.DeliveryVariableRevision{}).Where("variable_id = ? AND project_id = ?", item.ID, item.ProjectID)
	result := &VariableRevisionPage{Revisions: []VariableRevisionView{}, Page: page, PageSize: pageSize, CurrentVersion: item.Version}
	if err = query.Count(&result.Total).Error; err != nil {
		return nil, s.failure(ctx, "configuration_history_count", err, ErrStorage)
	}
	var revisions []model.DeliveryVariableRevision
	if err = query.Select("id, version, is_secret, created_by, created_at").Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&revisions).Error; err != nil {
		return nil, s.failure(ctx, "configuration_history_list", err, ErrStorage)
	}
	ids := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		ids = append(ids, revision.CreatedBy)
	}
	names := map[string]string{}
	if len(ids) > 0 {
		var users []model.User
		if err = s.db.WithContext(ctx).Select("uuid, username, nickname").Where("uuid IN ?", ids).Find(&users).Error; err != nil {
			return nil, s.failure(ctx, "configuration_history_authors", err, ErrStorage)
		}
		for _, user := range users {
			name := user.Nickname
			if name == "" {
				name = user.Username
			}
			names[user.ID] = name
		}
	}
	for _, revision := range revisions {
		result.Revisions = append(result.Revisions, VariableRevisionView{Version: revision.Version, IsSecret: item.IsSecret || revision.IsSecret,
			CreatedBy: revision.CreatedBy, CreatedByName: names[revision.CreatedBy], CreatedAt: revision.CreatedAt, Hidden: item.IsSecret || revision.IsSecret})
	}
	return result, nil
}

// ReadVariableRevision 的 reveal 只由已完成独立权限校验和审计的接口传入。
func (s *Service) ReadVariableRevision(ctx context.Context, id string, version uint64, reveal bool) (*VariableRevisionView, error) {
	if version == 0 {
		return nil, s.failure(ctx, "configuration_history_version", ErrInvalidVariable, ErrInvalidVariable)
	}
	item, err := s.revisionVariable(ctx, id)
	if err != nil {
		return nil, err
	}
	var revision model.DeliveryVariableRevision
	if err = s.db.WithContext(ctx).Where("variable_id = ? AND project_id = ? AND version = ?", item.ID, item.ProjectID, version).First(&revision).Error; err != nil {
		public := ErrStorage
		if errors.Is(err, gorm.ErrRecordNotFound) {
			public = ErrVariableUnavailable
		}
		return nil, s.failure(ctx, "configuration_history_content", err, public)
	}
	result := &VariableRevisionView{Version: revision.Version, IsSecret: item.IsSecret || revision.IsSecret,
		CreatedBy: revision.CreatedBy, CreatedAt: revision.CreatedAt, Hidden: item.IsSecret || revision.IsSecret}
	if result.Hidden && !reveal {
		return result, nil
	}
	value := revision.Value
	if revision.IsSecret {
		if s.secrets == nil {
			return nil, s.failure(ctx, "configuration_history_decrypt", secret.ErrUnavailable, ErrStorage)
		}
		value, err = s.secrets.Decrypt(revision.Ciphertext, []byte(variableAAD(item.ID, revision.Version)))
		if err != nil {
			return nil, s.failure(ctx, "configuration_history_decrypt", err, ErrStorage)
		}
	}
	result.Value, result.Hidden = &value, false
	return result, nil
}
