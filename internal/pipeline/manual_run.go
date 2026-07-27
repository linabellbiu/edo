package pipeline

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/model"
	"zrt/internal/repository"
)

var (
	ErrManualCommitRequired = errors.New("请选择要发布的 Commit")
	ErrManualCommitNotFound = errors.New("所选 Commit 不存在或对应分支、Tag 已经变化，请重新选择")
)

type ManualRunSource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Environment string `json:"environment,omitempty"`
}

type ManualRunOptions struct {
	Branches      []repository.GitRef          `json:"branches"`
	Tags          []repository.GitRef          `json:"tags"`
	ManualSources []ManualRunSource            `json:"manual_sources"`
	Repositories  []ManualRunRepositoryOptions `json:"repositories"`
}

type ManualRunRepositoryOptions struct {
	RepositoryID string              `json:"repository_id"`
	Name         string              `json:"name"`
	SortOrder    int                 `json:"sort_order"`
	Branches     []repository.GitRef `json:"branches"`
	Tags         []repository.GitRef `json:"tags"`
}

type ManualCommitSelection struct {
	RepositoryID string `json:"repository_id"`
	Ref          string `json:"ref"`
	CommitSHA    string `json:"commit_sha"`
}

func (s *Service) ListApplicationRefs(ctx context.Context, applicationID string) (ManualRunOptions, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return ManualRunOptions{}, err
	}
	options := ManualRunOptions{ManualSources: make([]ManualRunSource, 0), Repositories: make([]ManualRunRepositoryOptions, 0)}
	links := applicationRepositoryLinks(application)
	for i := range links {
		refs, err := s.repositories.TestConnection(ctx, links[i].RepositoryID)
		if err != nil {
			return ManualRunOptions{}, err
		}
		options.Repositories = append(options.Repositories, ManualRunRepositoryOptions{
			RepositoryID: links[i].RepositoryID, Name: links[i].Repository.Name,
			SortOrder: links[i].SortOrder, Branches: refs.Branches, Tags: refs.Tags,
		})
		if i == 0 {
			options.Branches, options.Tags = refs.Branches, refs.Tags
		}
	}
	if application.Workflow != nil {
		for i := range application.Workflow.Nodes {
			node := application.Workflow.Nodes[i]
			if node.Type == model.WorkflowNodeManualRelease {
				options.ManualSources = append(options.ManualSources, ManualRunSource{ID: node.ID, Name: node.Name, Environment: node.Config.Environment})
			}
		}
	}
	return options, nil
}

func (s *Service) ExecuteRun(ctx context.Context, runID, actorID, ref, commitSHA, sourceNodeID string, requested []ManualCommitSelection) (*model.PipelineRun, error) {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPipelineRunNotFound
		}
		return nil, err
	}
	ref, commitSHA = strings.TrimSpace(ref), strings.TrimSpace(commitSHA)
	if run.Status != model.PipelineRunBlocked || run.CommitSHA != "" {
		if ref != "" || commitSHA != "" || len(requested) > 0 {
			return nil, ErrInvalidWorkflowTransition
		}
		return s.AdvanceRun(ctx, run.ID, actorID, "")
	}
	application, err := s.FindApplication(ctx, run.ApplicationID)
	if err != nil {
		return nil, err
	}
	selections, err := s.validateManualSelections(ctx, application, ref, commitSHA, requested)
	if err != nil {
		return nil, err
	}
	ref, commitSHA = selections[0].Ref, selections[0].CommitSHA
	if application.Workflow == nil || !application.Workflow.IsActive {
		return nil, ErrWorkflowNotActive
	}
	source := manualWorkflowSource(application.Workflow, ref, strings.TrimSpace(sourceNodeID))
	if source == nil {
		return nil, ErrInvalidWorkflow
	}

	now := time.Now().UTC()
	createdBy := run.CreatedBy
	if createdBy == "" {
		createdBy = actorID
	}
	prepared, err := newWorkflowRun(
		application, application.Workflow, *source, "manual", ref, commitSHA,
		createdBy, "已选择代码版本，开始执行发布计划", now,
	)
	if err != nil {
		return nil, err
	}
	components := pipelineRunRepositories(application, run.ID, selections, now)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.PipelineRun{}).
			Where("id = ? AND status = ? AND commit_sha = ?", run.ID, model.PipelineRunBlocked, "").
			Updates(map[string]any{
				"trigger": prepared.Trigger, "ref": prepared.Ref, "commit_sha": prepared.CommitSHA,
				"status": prepared.Status, "stage": prepared.Stage, "environment": prepared.Environment,
				"workflow_id": prepared.WorkflowID, "workflow_revision": prepared.WorkflowRevision,
				"current_node_id": prepared.CurrentNodeID, "workflow_snapshot": prepared.WorkflowSnapshot,
				"repository_ordered": application.RepositoryOrdered,
				"message":            prepared.Message, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		if err := tx.Where("pipeline_run_id = ?", run.ID).Delete(&model.PipelineRunRepository{}).Error; err != nil {
			return err
		}
		return tx.Create(&components).Error
	})
	if err != nil {
		return nil, err
	}
	return s.AdvanceRun(ctx, run.ID, actorID, "")
}

func (s *Service) validateManualSelections(ctx context.Context, application *model.Application, ref, commitSHA string, requested []ManualCommitSelection) ([]ManualCommitSelection, error) {
	links := applicationRepositoryLinks(application)
	if len(requested) == 0 && len(links) == 1 {
		requested = []ManualCommitSelection{{RepositoryID: links[0].RepositoryID, Ref: ref, CommitSHA: commitSHA}}
	}
	if len(requested) != len(links) {
		return nil, ErrManualCommitRequired
	}
	byRepository := make(map[string]ManualCommitSelection, len(requested))
	for i := range requested {
		selection := requested[i]
		selection.RepositoryID = strings.TrimSpace(selection.RepositoryID)
		selection.Ref = strings.TrimSpace(selection.Ref)
		selection.CommitSHA = strings.TrimSpace(selection.CommitSHA)
		if selection.RepositoryID == "" || selection.Ref == "" || selection.CommitSHA == "" {
			return nil, ErrManualCommitRequired
		}
		if _, exists := byRepository[selection.RepositoryID]; exists {
			return nil, ErrManualCommitRequired
		}
		byRepository[selection.RepositoryID] = selection
	}
	result := make([]ManualCommitSelection, 0, len(links))
	for i := range links {
		selection, ok := byRepository[links[i].RepositoryID]
		if !ok {
			return nil, ErrManualCommitRequired
		}
		refs, err := s.repositories.TestConnection(ctx, links[i].RepositoryID)
		if err != nil {
			return nil, err
		}
		if !remoteRefContainsCommit(refs, selection.Ref, selection.CommitSHA) {
			return nil, ErrManualCommitNotFound
		}
		result = append(result, selection)
	}
	return result, nil
}

func applicationRepositoryLinks(application *model.Application) []model.ApplicationRepository {
	if len(application.Repositories) > 0 {
		return application.Repositories
	}
	if application.RepositoryID == "" {
		return nil
	}
	return []model.ApplicationRepository{{RepositoryID: application.RepositoryID, Repository: application.Repository}}
}

func pipelineRunRepositories(application *model.Application, runID string, selections []ManualCommitSelection, now time.Time) []model.PipelineRunRepository {
	links := applicationRepositoryLinks(application)
	selected := make(map[string]ManualCommitSelection, len(selections))
	for i := range selections {
		selected[selections[i].RepositoryID] = selections[i]
	}
	result := make([]model.PipelineRunRepository, 0, len(links))
	for i := range links {
		selection := selected[links[i].RepositoryID]
		buildPlanID, releasePlanID := links[i].Repository.BuildPlanID, links[i].Repository.ReleasePlanID
		if len(links) == 1 {
			if buildPlanID == "" {
				buildPlanID = application.BuildPlanID
			}
			if releasePlanID == "" {
				releasePlanID = application.ReleasePlanID
			}
		}
		result = append(result, model.PipelineRunRepository{
			ID: uuid.NewString(), PipelineRunID: runID, RepositoryID: links[i].RepositoryID,
			SortOrder: i, Ref: selection.Ref, CommitSHA: selection.CommitSHA,
			BuildPlanID: buildPlanID, ReleasePlanID: releasePlanID,
			Status: model.PipelineRunRepositoryReady, CreatedAt: now, UpdatedAt: now,
		})
	}
	return result
}

func remoteRefContainsCommit(refs repository.RefResult, ref, commitSHA string) bool {
	var candidates []repository.GitRef
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		candidates = refs.Branches
		ref = strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		candidates = refs.Tags
		ref = strings.TrimPrefix(ref, "refs/tags/")
	default:
		return false
	}
	for i := range candidates {
		if candidates[i].Name == ref && candidates[i].SHA == commitSHA {
			return true
		}
	}
	return false
}

func manualWorkflowSource(workflow *model.ReleaseWorkflow, ref, sourceNodeID string) *model.WorkflowNode {
	if sourceNodeID != "" {
		for i := range workflow.Nodes {
			node := &workflow.Nodes[i]
			if node.ID == sourceNodeID && node.Type == model.WorkflowNodeManualRelease {
				return node
			}
		}
		return nil
	}
	for i := range workflow.Nodes {
		node := &workflow.Nodes[i]
		if node.Type == model.WorkflowNodeManualRelease {
			return node
		}
	}
	var fallback *model.WorkflowNode
	for i := range workflow.Nodes {
		node := &workflow.Nodes[i]
		if node.Type != model.WorkflowNodeTrigger {
			continue
		}
		if fallback == nil {
			fallback = node
		}
		if strings.HasPrefix(ref, "refs/tags/") {
			if containsEvent(node.Config.Events, "tag") && matchTag(node.Config.TagPattern, strings.TrimPrefix(ref, "refs/tags/")) {
				return node
			}
			continue
		}
		branch := strings.TrimPrefix(ref, "refs/heads/")
		if matched, err := path.Match(node.Config.Branch, branch); err == nil && matched {
			return node
		}
	}
	return fallback
}
