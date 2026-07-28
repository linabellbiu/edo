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
	Branches      []repository.GitRef `json:"branches"`
	Tags          []repository.GitRef `json:"tags"`
	ManualSources []ManualRunSource   `json:"manual_sources"`
}

func (s *Service) ListApplicationRefs(ctx context.Context, applicationID string) (ManualRunOptions, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return ManualRunOptions{}, err
	}
	options := ManualRunOptions{ManualSources: make([]ManualRunSource, 0)}
	refs, err := s.repositories.TestConnection(ctx, application.RepositoryID)
	if err != nil {
		return ManualRunOptions{}, err
	}
	options.Branches, options.Tags = refs.Branches, refs.Tags
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
	if run.Status != model.PipelineRunBlocked {
		if ref != "" || commitSHA != "" {
			return nil, ErrInvalidWorkflowTransition
		}
		return s.AdvanceRun(ctx, run.ID, actorID, "")
	}
	application, err := s.FindApplication(ctx, run.ApplicationID)
	if err != nil {
		return nil, err
	}
	if run.CommitSHA == "" {
		ref, commitSHA, err = s.validateManualSelection(ctx, application, ref, commitSHA)
		if err != nil {
			return nil, err
		}
	} else {
		if ref != "" || commitSHA != "" {
			return nil, ErrInvalidWorkflowTransition
		}
		ref, commitSHA = run.Ref, run.CommitSHA
		if sourceNodeID == "" {
			sourceNodeID = run.CurrentNodeID
		}
	}
	sourceNodeID = strings.TrimSpace(sourceNodeID)
	if sourceNodeID != "" && (application.Workflow == nil || manualWorkflowSource(application.Workflow, ref, sourceNodeID) == nil) {
		return nil, ErrInvalidWorkflow
	}
	commitMessage := run.CommitMessage
	if commitMessage == "" {
		links := applicationRepositoryLinks(application)
		if len(links) > 0 {
			commitMessage = s.resolveCommitMessage(ctx, links[0].RepositoryID, ref, commitSHA)
		}
	}
	if !pipelineExecutionConfigured(application) {
		if run.CommitSHA != "" {
			return nil, ErrPipelineIncomplete
		}
		return s.saveBlockedManualSelection(ctx, &run, application, ref, commitSHA, commitMessage, sourceNodeID, pipelineExecutionIncompleteMessage(application))
	}
	if application.Workflow == nil || !application.Workflow.IsActive {
		if run.CommitSHA == "" {
			return s.saveBlockedManualSelection(ctx, &run, application, ref, commitSHA, commitMessage, sourceNodeID, "已选择代码版本；应用流水线尚未启用")
		}
		return nil, ErrWorkflowNotActive
	}
	source := manualWorkflowSource(application.Workflow, ref, sourceNodeID)
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
		createdBy, "已选择代码版本，开始执行流水线", now,
	)
	if err != nil {
		return nil, err
	}
	prepared.ID = run.ID
	prepared.CommitMessage = commitMessage
	components := pipelineRunRepositories(application, run.ID, ref, commitSHA, now)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.PipelineRun{}).
			Where("id = ? AND status = ? AND commit_sha = ?", run.ID, model.PipelineRunBlocked, run.CommitSHA).
			Updates(map[string]any{
				"trigger": prepared.Trigger, "ref": prepared.Ref, "commit_sha": prepared.CommitSHA, "commit_message": prepared.CommitMessage,
				"status": prepared.Status, "stage": prepared.Stage, "environment": prepared.Environment,
				"workflow_id": prepared.WorkflowID, "workflow_revision": prepared.WorkflowRevision,
				"current_node_id": prepared.CurrentNodeID, "workflow_snapshot": prepared.WorkflowSnapshot,
				"message": prepared.Message, "updated_at": now,
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

// saveBlockedManualSelection 将版本选择与执行解耦；应用配置未完成时只保存代码版本，不投递任何构建或发布任务。
func (s *Service) saveBlockedManualSelection(
	ctx context.Context,
	run *model.PipelineRun,
	application *model.Application,
	ref, commitSHA, commitMessage, sourceNodeID, message string,
) (*model.PipelineRun, error) {
	now := time.Now().UTC()
	components := pipelineRunRepositories(application, run.ID, ref, commitSHA, now)
	for i := range components {
		components[i].Status = model.PipelineRunRepositoryPending
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.PipelineRun{}).
			Where("id = ? AND status = ? AND commit_sha = ?", run.ID, model.PipelineRunBlocked, "").
			Updates(map[string]any{
				"trigger": "manual", "ref": ref, "commit_sha": commitSHA, "commit_message": commitMessage,
				"status": model.PipelineRunBlocked, "stage": "configured", "environment": "",
				"workflow_id": "", "workflow_revision": 0, "current_node_id": sourceNodeID,
				"workflow_snapshot": "", "message": message, "updated_at": now,
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
	run.Trigger = "manual"
	run.Ref = ref
	run.CommitSHA = commitSHA
	run.CommitMessage = commitMessage
	run.Status = model.PipelineRunBlocked
	run.Stage = "configured"
	run.Environment = ""
	run.WorkflowID = ""
	run.WorkflowRevision = 0
	run.CurrentNodeID = sourceNodeID
	run.WorkflowSnapshot = ""
	run.Message = message
	run.Repositories = components
	run.UpdatedAt = now
	return run, nil
}

func (s *Service) validateManualSelection(ctx context.Context, application *model.Application, ref, commitSHA string) (string, string, error) {
	ref, commitSHA = strings.TrimSpace(ref), strings.TrimSpace(commitSHA)
	if ref == "" || commitSHA == "" {
		return "", "", ErrManualCommitRequired
	}
	refs, err := s.repositories.TestConnection(ctx, application.RepositoryID)
	if err != nil {
		return "", "", err
	}
	if !remoteRefContainsCommit(refs, ref, commitSHA) {
		return "", "", ErrManualCommitNotFound
	}
	return ref, commitSHA, nil
}

func applicationRepositoryLinks(application *model.Application) []model.ApplicationRepository {
	for i := range application.Repositories {
		if application.Repositories[i].RepositoryID == application.RepositoryID {
			link := application.Repositories[i]
			link.SortOrder = 0
			return []model.ApplicationRepository{link}
		}
	}
	if application.RepositoryID == "" {
		return nil
	}
	return []model.ApplicationRepository{{RepositoryID: application.RepositoryID, Repository: application.Repository}}
}

func pipelineRunRepositories(application *model.Application, runID, ref, commitSHA string, now time.Time) []model.PipelineRunRepository {
	links := applicationRepositoryLinks(application)
	result := make([]model.PipelineRunRepository, 0, len(links))
	for i := range links {
		result = append(result, model.PipelineRunRepository{
			ID: uuid.NewString(), PipelineRunID: runID, RepositoryID: links[i].RepositoryID,
			SortOrder: 0, Ref: ref, CommitSHA: commitSHA,
			BuildPlanID: application.BuildPlanID, ImageRegistryID: application.ImageRegistryID,
			DeploymentPlanID: application.DeploymentPlanID,
			Status:           model.PipelineRunRepositoryReady, CreatedAt: now, UpdatedAt: now,
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

func retryWorkflowSource(workflow *model.ReleaseWorkflow, run *model.PipelineRun) *model.WorkflowNode {
	if workflow == nil || run == nil {
		return nil
	}
	for i := range workflow.Nodes {
		node := &workflow.Nodes[i]
		if node.Type != model.WorkflowNodeTrigger {
			continue
		}
		if strings.HasPrefix(run.Ref, "refs/tags/") {
			if containsEvent(node.Config.Events, "tag") && matchTag(node.Config.TagPattern, strings.TrimPrefix(run.Ref, "refs/tags/")) {
				return node
			}
			continue
		}
		branch := strings.TrimPrefix(run.Ref, "refs/heads/")
		if matched, err := path.Match(node.Config.Branch, branch); err == nil && matched {
			return node
		}
	}
	return manualWorkflowSource(workflow, run.Ref, "")
}
