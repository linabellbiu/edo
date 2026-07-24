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
	ErrWorkflowTemplateExists           = errors.New("公共发布计划名称已存在")
	ErrWorkflowTemplateRevisionConflict = errors.New("公共发布计划已被其他人修改，请刷新后再保存")
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
		return nil, fmt.Errorf("查询公共发布计划失败: %w", err)
	}
	return templates, nil
}

func (s *Service) GetWorkflowTemplate(ctx context.Context, id string) (*WorkflowTemplateResult, error) {
	var template model.ReleaseWorkflowTemplate
	if err := s.db.WithContext(ctx).First(&template, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrWorkflowTemplateNotFound
		}
		return nil, fmt.Errorf("读取公共发布计划失败: %w", err)
	}
	issues := s.validateWorkflowTemplate(ctx, template.Nodes, template.Edges)
	return &WorkflowTemplateResult{WorkflowTemplate: &template, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) ValidateWorkflowTemplate(ctx context.Context, input WorkflowTemplateInput) (*WorkflowTemplateResult, error) {
	if err := sanitizeWorkflowTemplateInput(&input); err != nil {
		return nil, err
	}
	issues := s.validateWorkflowTemplate(ctx, input.Nodes, input.Edges)
	return &WorkflowTemplateResult{WorkflowTemplate: &model.ReleaseWorkflowTemplate{
		Name: input.Name, Description: input.Description, Revision: input.Revision,
		Nodes: input.Nodes, Edges: input.Edges, Viewport: input.Viewport,
	}, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) CreateWorkflowTemplate(ctx context.Context, actorID string, input WorkflowTemplateInput) (*WorkflowTemplateResult, error) {
	if err := sanitizeWorkflowTemplateInput(&input); err != nil {
		return nil, err
	}
	issues := s.validateWorkflowTemplate(ctx, input.Nodes, input.Edges)
	if input.Activate && len(issues) > 0 {
		return &WorkflowTemplateResult{WorkflowTemplate: &model.ReleaseWorkflowTemplate{
			Name: input.Name, Description: input.Description, Nodes: input.Nodes,
			Edges: input.Edges, Viewport: input.Viewport,
		}, Valid: false, Issues: issues}, ErrInvalidWorkflow
	}
	now := time.Now().UTC()
	template := &model.ReleaseWorkflowTemplate{
		ID: uuid.NewString(), Name: input.Name, Description: input.Description,
		Revision: 1, IsActive: input.Activate, Nodes: input.Nodes, Edges: input.Edges,
		Viewport: input.Viewport, CreatedBy: actorID, UpdatedBy: actorID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(template).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrWorkflowTemplateExists
		}
		return nil, fmt.Errorf("创建公共发布计划失败: %w", err)
	}
	return &WorkflowTemplateResult{WorkflowTemplate: template, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) SaveWorkflowTemplate(ctx context.Context, id, actorID string, input WorkflowTemplateInput) (*WorkflowTemplateResult, error) {
	if err := sanitizeWorkflowTemplateInput(&input); err != nil {
		return nil, err
	}
	issues := s.validateWorkflowTemplate(ctx, input.Nodes, input.Edges)
	if input.Activate && len(issues) > 0 {
		return &WorkflowTemplateResult{WorkflowTemplate: &model.ReleaseWorkflowTemplate{
			ID: id, Name: input.Name, Description: input.Description, Revision: input.Revision,
			Nodes: input.Nodes, Edges: input.Edges, Viewport: input.Viewport,
		}, Valid: false, Issues: issues}, ErrInvalidWorkflow
	}
	nodesJSON, _ := json.Marshal(input.Nodes)
	edgesJSON, _ := json.Marshal(input.Edges)
	viewportJSON, _ := json.Marshal(input.Viewport)
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
				"name": input.Name, "description": input.Description,
				"nodes": string(nodesJSON), "edges": string(edgesJSON), "viewport": string(viewportJSON),
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
		return tx.First(&saved, "id = ?", id).Error
	})
	if err != nil {
		return nil, err
	}
	return &WorkflowTemplateResult{WorkflowTemplate: &saved, Valid: len(issues) == 0, Issues: issues}, nil
}

func sanitizeWorkflowTemplateInput(input *WorkflowTemplateInput) error {
	input.Description = strings.TrimSpace(input.Description)
	if len([]rune(input.Description)) > 500 {
		return ErrInvalidWorkflow
	}
	return sanitizeWorkflowInput(&input.WorkflowInput)
}

func (s *Service) validateWorkflowTemplate(ctx context.Context, nodes []model.WorkflowNode, edges []model.WorkflowEdge) []WorkflowIssue {
	application := &model.Application{Environments: workflowTemplateEnvironments(nodes)}
	return s.validateWorkflow(ctx, application, nodes, edges)
}

func workflowTemplateEnvironments(nodes []model.WorkflowNode) []model.ApplicationEnvironment {
	type environmentConfig struct {
		branch, tagPattern, releasePlanID, deploymentTargetID string
		poll, push, pullRequest, tags                         bool
	}
	configs := make(map[string]*environmentConfig, 4)
	for i := range nodes {
		key := strings.ToLower(strings.TrimSpace(nodes[i].Config.Environment))
		if !validEnvironmentKey(key) {
			continue
		}
		config := configs[key]
		if config == nil {
			config = &environmentConfig{}
			configs[key] = config
		}
		switch nodes[i].Type {
		case model.WorkflowNodeTrigger:
			if config.branch == "" {
				config.branch = nodes[i].Config.Branch
			}
			if config.tagPattern == "" {
				config.tagPattern = nodes[i].Config.TagPattern
			}
			config.poll = config.poll || containsEvent(nodes[i].Config.Events, "pull")
			config.push = config.push || containsEvent(nodes[i].Config.Events, "push")
			config.pullRequest = config.pullRequest || containsEvent(nodes[i].Config.Events, "pr")
			config.tags = config.tags || containsEvent(nodes[i].Config.Events, "tag")
		case model.WorkflowNodeDeploy:
			if config.releasePlanID == "" {
				config.releasePlanID = nodes[i].Config.ReleasePlanID
			}
			if config.deploymentTargetID == "" {
				config.deploymentTargetID = nodes[i].Config.DeploymentTargetID
			}
		}
	}
	keys := []string{"dev", "test", "pre", "prod"}
	result := make([]model.ApplicationEnvironment, 0, len(configs))
	for order, key := range keys {
		config := configs[key]
		if config == nil {
			continue
		}
		branch := strings.TrimSpace(config.branch)
		if branch == "" {
			branch = defaultEnvironmentBranch(key, "main")
		}
		result = append(result, model.ApplicationEnvironment{
			Key: key, Name: environmentName(key), Branch: branch,
			PollEnabled: config.poll, WatchPush: config.push, WatchPullRequest: config.pullRequest,
			WatchTags: config.tags, TagPattern: config.tagPattern,
			ReleasePlanID: config.releasePlanID, DeploymentTargetID: config.deploymentTargetID,
			SortOrder: order,
		})
	}
	return result
}

func workflowTemplateEnvironmentInputs(nodes []model.WorkflowNode) []EnvironmentInput {
	environments := workflowTemplateEnvironments(nodes)
	result := make([]EnvironmentInput, 0, len(environments))
	for i := range environments {
		result = append(result, EnvironmentInput{
			Key: environments[i].Key, Name: environments[i].Name, Branch: environments[i].Branch,
			PollEnabled: environments[i].PollEnabled, WatchPush: environments[i].WatchPush,
			WatchPullRequest: environments[i].WatchPullRequest, WatchTags: environments[i].WatchTags,
			TagPattern: environments[i].TagPattern, ReleasePlanID: environments[i].ReleasePlanID,
			DeploymentTargetID: environments[i].DeploymentTargetID, SortOrder: environments[i].SortOrder,
		})
	}
	return result
}

func (s *Service) newApplicationWorkflow(ctx context.Context, application *model.Application, actorID string, now time.Time) (*model.ReleaseWorkflow, error) {
	if application.WorkflowTemplateID == "" {
		return defaultWorkflow(application, application.Environments, actorID, now), nil
	}
	var template model.ReleaseWorkflowTemplate
	if err := s.db.WithContext(ctx).First(&template, "id = ? AND is_active = ?", application.WorkflowTemplateID, true).Error; err != nil {
		return nil, ErrWorkflowTemplateNotFound
	}
	nodes, edges := cloneWorkflowGraph(template.Nodes, template.Edges)
	issues := s.validateWorkflow(ctx, application, nodes, edges)
	return &model.ReleaseWorkflow{
		ID: uuid.NewString(), ApplicationID: application.ID,
		WorkflowTemplateID: template.ID, WorkflowTemplateRevision: template.Revision,
		Name: application.Name + " · " + template.Name, Revision: 1,
		IsActive: len(issues) == 0, Nodes: nodes, Edges: edges, Viewport: template.Viewport,
		CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func cloneWorkflowGraph(nodes []model.WorkflowNode, edges []model.WorkflowEdge) ([]model.WorkflowNode, []model.WorkflowEdge) {
	data, _ := json.Marshal(struct {
		Nodes []model.WorkflowNode `json:"nodes"`
		Edges []model.WorkflowEdge `json:"edges"`
	}{Nodes: nodes, Edges: edges})
	var clone struct {
		Nodes []model.WorkflowNode `json:"nodes"`
		Edges []model.WorkflowEdge `json:"edges"`
	}
	_ = json.Unmarshal(data, &clone)
	return clone.Nodes, clone.Edges
}
