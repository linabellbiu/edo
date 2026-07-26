package pipeline

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

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
	Branches      []repository.GitRef `json:"branches"`
	Tags          []repository.GitRef `json:"tags"`
	ManualSources []ManualRunSource   `json:"manual_sources"`
}

func (s *Service) ListApplicationRefs(ctx context.Context, applicationID string) (ManualRunOptions, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return ManualRunOptions{}, err
	}
	refs, err := s.repositories.TestConnection(ctx, application.RepositoryID)
	if err != nil {
		return ManualRunOptions{}, err
	}
	options := ManualRunOptions{Branches: refs.Branches, Tags: refs.Tags}
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

func (s *Service) ExecuteRun(ctx context.Context, runID, actorID, ref, commitSHA, sourceNodeID string) (*model.PipelineRun, error) {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPipelineRunNotFound
		}
		return nil, err
	}
	ref, commitSHA = strings.TrimSpace(ref), strings.TrimSpace(commitSHA)
	if run.Status != model.PipelineRunBlocked || run.CommitSHA != "" {
		if ref != "" || commitSHA != "" {
			return nil, ErrInvalidWorkflowTransition
		}
		return s.AdvanceRun(ctx, run.ID, actorID, "")
	}
	if ref == "" || commitSHA == "" {
		return nil, ErrManualCommitRequired
	}

	application, err := s.FindApplication(ctx, run.ApplicationID)
	if err != nil {
		return nil, err
	}
	refs, err := s.repositories.TestConnection(ctx, application.RepositoryID)
	if err != nil {
		return nil, err
	}
	if !remoteRefContainsCommit(refs, ref, commitSHA) {
		return nil, ErrManualCommitNotFound
	}
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
	result := s.db.WithContext(ctx).Model(&model.PipelineRun{}).
		Where("id = ? AND status = ? AND commit_sha = ?", run.ID, model.PipelineRunBlocked, "").
		Updates(map[string]any{
			"trigger": prepared.Trigger, "ref": prepared.Ref, "commit_sha": prepared.CommitSHA,
			"status": prepared.Status, "stage": prepared.Stage, "environment": prepared.Environment,
			"workflow_id": prepared.WorkflowID, "workflow_revision": prepared.WorkflowRevision,
			"current_node_id": prepared.CurrentNodeID, "workflow_snapshot": prepared.WorkflowSnapshot,
			"message": prepared.Message, "updated_at": now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrInvalidWorkflowTransition
	}
	return s.AdvanceRun(ctx, run.ID, actorID, "")
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
