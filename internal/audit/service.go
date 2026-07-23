package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"zrt/internal/model"
)

type RecordInput struct {
	ActorUserID  string
	Action       string
	ResourceType string
	ResourceID   string
	Result       model.AuditResult
	RequestID    string
	ClientIP     string
	UserAgent    string
	Metadata     map[string]any
}

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) Record(ctx context.Context, input RecordInput) error {
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return fmt.Errorf("序列化审计元数据失败: %w", err)
	}
	var actorUserID *string
	if input.ActorUserID != "" {
		actorUserID = &input.ActorUserID
	}
	entry := model.AuditLog{
		ID: uuid.NewString(), ActorUserID: actorUserID, Action: input.Action,
		ResourceType: input.ResourceType, ResourceID: input.ResourceID,
		Result: input.Result, RequestID: input.RequestID, ClientIP: input.ClientIP,
		UserAgent: input.UserAgent, Metadata: datatypes.JSON(metadata), CreatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(&entry).Error; err != nil {
		return fmt.Errorf("写入审计日志失败: %w", err)
	}
	return nil
}

func (s *Service) List(ctx context.Context, limit int, before time.Time) ([]model.AuditLog, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := s.db.WithContext(ctx).Order("created_at DESC, id DESC").Limit(limit)
	if !before.IsZero() {
		query = query.Where("created_at < ?", before.UTC())
	}
	var entries []model.AuditLog
	if err := query.Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("查询审计日志失败: %w", err)
	}
	return entries, nil
}
