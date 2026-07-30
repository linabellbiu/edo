package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/model"
)

var (
	ErrWorkflowTemplateExists           = errors.New("流水线方案名称已存在")
	ErrWorkflowTemplateRevisionConflict = errors.New("流水线方案已被其他人修改，请刷新后再保存")
	ErrWorkflowTemplateInUse            = errors.New("流水线方案仍被应用使用，不能删除")
)

type WorkflowTemplateInput struct {
	WorkflowInput
	Description string
}

type WorkflowTemplateResult struct {
	WorkflowTemplate *model.ReleaseWorkflowTemplate `json:"workflow_template"`
	Valid            bool                           `json:"valid"`
	Issues           []WorkflowIssue                `json:"issues"`
}

func (s *Service) ListWorkflowTemplates(ctx context.Context) ([]model.ReleaseWorkflowTemplate, error) {
	var templates []model.ReleaseWorkflowTemplate
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&templates).Error; err != nil {
		return nil, fmt.Errorf("查询流水线方案失败: %w", err)
	}
	return templates, nil
}

func (s *Service) GetWorkflowTemplate(ctx context.Context, id string) (*WorkflowTemplateResult, error) {
	var template model.ReleaseWorkflowTemplate
	if err := s.db.WithContext(ctx).First(&template, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrWorkflowTemplateNotFound
		}
		return nil, fmt.Errorf("读取流水线方案失败: %w", err)
	}
	issues := s.validateWorkflowTemplate(ctx, template.SchemaVersion, template.Source, template.Stages)
	return &WorkflowTemplateResult{WorkflowTemplate: &template, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) ValidateWorkflowTemplate(ctx context.Context, input WorkflowTemplateInput) (*WorkflowTemplateResult, error) {
	if err := sanitizeWorkflowTemplateInput(&input); err != nil {
		return nil, err
	}
	issues := s.validateWorkflowTemplate(ctx, input.SchemaVersion, input.Source, input.Stages)
	return &WorkflowTemplateResult{WorkflowTemplate: &model.ReleaseWorkflowTemplate{
		SchemaVersion: input.SchemaVersion, Name: input.Name, Description: input.Description,
		Revision: input.Revision, Source: input.Source, Stages: input.Stages,
	}, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) CreateWorkflowTemplate(ctx context.Context, actorID string, input WorkflowTemplateInput) (*WorkflowTemplateResult, error) {
	if err := sanitizeWorkflowTemplateInput(&input); err != nil {
		return nil, err
	}
	issues := s.validateWorkflowTemplate(ctx, input.SchemaVersion, input.Source, input.Stages)
	if len(issues) > 0 && (input.Activate || hasWorkflowSaveBlockingIssue(issues)) {
		return &WorkflowTemplateResult{WorkflowTemplate: &model.ReleaseWorkflowTemplate{
			SchemaVersion: input.SchemaVersion, Name: input.Name, Description: input.Description,
			Source: input.Source, Stages: input.Stages,
		}, Valid: false, Issues: issues}, ErrInvalidWorkflow
	}
	now := time.Now().UTC()
	template := &model.ReleaseWorkflowTemplate{
		ID: uuid.NewString(), SchemaVersion: input.SchemaVersion, Name: input.Name, Description: input.Description,
		Revision: 1, IsActive: input.Activate, Source: input.Source, Stages: input.Stages,
		CreatedBy: actorID, UpdatedBy: actorID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(template).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrWorkflowTemplateExists
		}
		return nil, fmt.Errorf("创建流水线方案失败: %w", err)
	}
	return &WorkflowTemplateResult{WorkflowTemplate: template, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) SaveWorkflowTemplate(ctx context.Context, id, actorID string, input WorkflowTemplateInput) (*WorkflowTemplateResult, error) {
	if err := sanitizeWorkflowTemplateInput(&input); err != nil {
		return nil, err
	}
	var existingForValidation model.ReleaseWorkflowTemplate
	if err := s.db.WithContext(ctx).First(&existingForValidation, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrWorkflowTemplateNotFound
		}
		return nil, fmt.Errorf("读取流水线方案失败: %w", err)
	}
	if existingForValidation.Revision != input.Revision {
		return nil, ErrWorkflowTemplateRevisionConflict
	}
	issues := s.validateWorkflowTemplate(ctx, input.SchemaVersion, input.Source, input.Stages)
	if len(issues) > 0 && (input.Activate || hasWorkflowSaveBlockingIssue(issues)) {
		return &WorkflowTemplateResult{WorkflowTemplate: &model.ReleaseWorkflowTemplate{
			ID: id, SchemaVersion: input.SchemaVersion, Name: input.Name,
			Description: input.Description, Revision: input.Revision,
			Source: input.Source, Stages: input.Stages,
		}, Valid: false, Issues: issues}, ErrInvalidWorkflow
	}
	sourceJSON, _ := json.Marshal(input.Source)
	stagesJSON, _ := json.Marshal(input.Stages)
	now := time.Now().UTC()
	var saved model.ReleaseWorkflowTemplate
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.ReleaseWorkflowTemplate
		if err := tx.First(&existing, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWorkflowTemplateNotFound
			}
			return err
		}
		if existing.Revision != input.Revision {
			return ErrWorkflowTemplateRevisionConflict
		}
		result := tx.Model(&model.ReleaseWorkflowTemplate{}).
			Where("id = ? AND revision = ?", id, input.Revision).
			Updates(map[string]any{
				"schema_version": input.SchemaVersion, "name": input.Name, "description": input.Description,
				"source": string(sourceJSON), "stages": string(stagesJSON),
				"is_active": input.Activate, "revision": input.Revision + 1,
				"updated_by": actorID, "updated_at": now,
			})
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
				return ErrWorkflowTemplateExists
			}
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkflowTemplateRevisionConflict
		}
		if err := tx.First(&saved, "id = ?", id).Error; err != nil {
			return err
		}
		if saved.IsActive {
			return syncLinkedApplicationWorkflows(tx, &saved, actorID, now)
		}
		return nil
	})
	if err != nil {
		if s.logger != nil && !errors.Is(err, ErrWorkflowTemplateRevisionConflict) && !errors.Is(err, ErrWorkflowTemplateExists) {
			s.logger.Error("同步流水线方案失败", "operation", "workflow_template_sync", "workflow_template_id", id, "err", err)
		}
		return nil, err
	}
	return &WorkflowTemplateResult{WorkflowTemplate: &saved, Valid: len(issues) == 0, Issues: issues}, nil
}

func syncLinkedApplicationWorkflows(tx *gorm.DB, template *model.ReleaseWorkflowTemplate, actorID string, now time.Time) error {
	var workflows []model.ReleaseWorkflow
	if err := tx.Where("workflow_template_id = ?", template.ID).Find(&workflows).Error; err != nil {
		return err
	}
	if len(workflows) == 0 {
		return nil
	}

	sourceJSON, err := json.Marshal(template.Source)
	if err != nil {
		return err
	}
	stagesJSON, err := json.Marshal(template.Stages)
	if err != nil {
		return err
	}
	for i := range workflows {
		var application model.Application
		if err := tx.First(&application, "id = ?", workflows[i].ApplicationID).Error; err != nil {
			return err
		}
		result := tx.Model(&model.ReleaseWorkflow{}).
			Where("id = ? AND revision = ?", workflows[i].ID, workflows[i].Revision).
			Updates(map[string]any{
				"name":           application.Name + " · " + template.Name,
				"schema_version": template.SchemaVersion,
				"source":         string(sourceJSON), "stages": string(stagesJSON),
				"workflow_template_revision": template.Revision,
				"revision":                   workflows[i].Revision + 1, "updated_by": actorID, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkflowRevisionConflict
		}
	}
	return nil
}

func (s *Service) DeleteWorkflowTemplate(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var template model.ReleaseWorkflowTemplate
		if err := tx.First(&template, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWorkflowTemplateNotFound
			}
			return err
		}
		var workflowCount int64
		if err := tx.Model(&model.ReleaseWorkflow{}).Where("workflow_template_id = ?", id).Count(&workflowCount).Error; err != nil {
			return err
		}
		if workflowCount > 0 {
			return ErrWorkflowTemplateInUse
		}
		if err := tx.Delete(&template).Error; err != nil {
			return fmt.Errorf("删除流水线方案失败: %w", err)
		}
		return nil
	})
}

func sanitizeWorkflowTemplateInput(input *WorkflowTemplateInput) error {
	input.Description = strings.TrimSpace(input.Description)
	if len([]rune(input.Description)) > 500 {
		return ErrInvalidWorkflow
	}
	return sanitizeWorkflowInput(&input.WorkflowInput)
}

func (s *Service) validateWorkflowTemplate(ctx context.Context, schemaVersion uint16, source model.WorkflowNode, stages []model.WorkflowStage) []WorkflowIssue {
	return s.validateWorkflow(ctx, nil, schemaVersion, source, stages)
}

func (s *Service) newApplicationWorkflow(ctx context.Context, application *model.Application, actorID string, now time.Time) (*model.ReleaseWorkflow, error) {
	if application.WorkflowTemplateID == "" {
		if application.Repository.ID == "" {
			repository, err := s.repositories.Find(ctx, application.RepositoryID)
			if err != nil {
				return nil, ErrInvalidApplication
			}
			application.Repository = *repository
		}
		return defaultWorkflow(application, actorID, now), nil
	}
	var template model.ReleaseWorkflowTemplate
	if err := s.db.WithContext(ctx).First(&template, "id = ? AND is_active = ?", application.WorkflowTemplateID, true).Error; err != nil {
		return nil, ErrWorkflowTemplateNotFound
	}
	stages := cloneWorkflowStages(template.Stages)
	issues := s.validateWorkflow(ctx, application, template.SchemaVersion, template.Source, stages)
	return &model.ReleaseWorkflow{
		ID: uuid.NewString(), ApplicationID: application.ID,
		WorkflowTemplateID: template.ID, WorkflowTemplateRevision: template.Revision,
		SchemaVersion: template.SchemaVersion, Name: application.Name + " · " + template.Name, Revision: 1,
		IsActive: len(issues) == 0, Source: template.Source, Stages: stages,
		CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}, nil
}
