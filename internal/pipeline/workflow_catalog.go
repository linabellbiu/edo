package pipeline

import (
	"context"
	"errors"
	"sort"
	"time"

	"edo/internal/model"
)

var ErrWorkflowCatalogUnavailable = errors.New("读取流水线列表失败，请稍后重试")

// WorkflowCatalogEntry 统一呈现当前项目内可查看的共用方案和应用流水线。
type WorkflowCatalogEntry struct {
	ID                 string                `json:"id"`
	Kind               string                `json:"kind"`
	ProjectID          string                `json:"project_id"`
	ApplicationID      string                `json:"application_id,omitempty"`
	ApplicationName    string                `json:"application_name,omitempty"`
	WorkflowTemplateID string                `json:"workflow_template_id,omitempty"`
	SchemaVersion      uint16                `json:"schema_version"`
	Name               string                `json:"name"`
	Description        string                `json:"description"`
	Revision           uint64                `json:"revision"`
	IsActive           bool                  `json:"is_active"`
	Source             model.WorkflowNode    `json:"source"`
	Stages             []model.WorkflowStage `json:"stages"`
	ApplicationCount   int                   `json:"application_count"`
	UpdatedAt          time.Time             `json:"updated_at"`
	rowID              uint64
}

func (s *Service) ListWorkflowCatalog(ctx context.Context) ([]WorkflowCatalogEntry, error) {
	scope, err := s.deliveryScope(ctx)
	fail := func(err error) ([]WorkflowCatalogEntry, error) {
		if s.logger != nil {
			s.logger.Error("读取流水线目录失败", "operation", "workflow_catalog_list", "project_id", scope.ProjectID, "err", err)
		}
		return nil, ErrWorkflowCatalogUnavailable
	}
	if err != nil {
		return fail(err)
	}
	var templates []model.ReleaseWorkflowTemplate
	if err := s.db.WithContext(ctx).Order("id DESC").Find(&templates).Error; err != nil {
		return fail(err)
	}
	var workflows []model.ReleaseWorkflow
	if err := s.db.WithContext(ctx).Order("id DESC").Find(&workflows).Error; err != nil {
		return fail(err)
	}
	var applications []model.Application
	if err := s.db.WithContext(ctx).Select("uuid", "name").Find(&applications).Error; err != nil {
		return fail(err)
	}
	names := map[string]string{}
	for _, app := range applications {
		names[app.ID] = app.Name
	}
	usage := map[string]map[string]bool{}
	for _, flow := range workflows {
		if flow.WorkflowTemplateID != "" {
			if usage[flow.WorkflowTemplateID] == nil {
				usage[flow.WorkflowTemplateID] = map[string]bool{}
			}
			usage[flow.WorkflowTemplateID][flow.ApplicationID] = true
		}
	}
	result := make([]WorkflowCatalogEntry, 0, len(templates)+len(workflows))
	for _, item := range templates {
		result = append(result, WorkflowCatalogEntry{
			ID: item.ID, Kind: "template", ProjectID: item.ProjectID,
			SchemaVersion: item.SchemaVersion, Name: item.Name, Description: item.Description,
			Revision: item.Revision, IsActive: item.IsActive, Source: item.Source, Stages: item.Stages,
			ApplicationCount: len(usage[item.ID]), UpdatedAt: item.UpdatedAt, rowID: item.RowID,
		})
	}
	for _, item := range workflows {
		result = append(result, WorkflowCatalogEntry{
			ID: item.ID, Kind: "application", ProjectID: item.ProjectID,
			ApplicationID: item.ApplicationID, ApplicationName: names[item.ApplicationID],
			WorkflowTemplateID: item.WorkflowTemplateID, SchemaVersion: item.SchemaVersion,
			Name: item.Name, Revision: item.Revision, IsActive: item.IsActive,
			Source: item.Source, Stages: item.Stages, ApplicationCount: 1,
			UpdatedAt: item.UpdatedAt, rowID: item.RowID,
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.After(result[j].UpdatedAt)
		}
		if result[i].rowID != result[j].rowID {
			return result[i].rowID > result[j].rowID
		}
		return result[i].Kind < result[j].Kind
	})
	return result, nil
}
