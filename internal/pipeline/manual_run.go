package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/model"
	"zrt/internal/repository"
)

var (
	ErrManualCommitRequired  = errors.New("请选择要发布的 Commit")
	ErrManualCommitNotFound  = errors.New("所选 Commit 不存在或对应分支、Tag 已经变化，请重新选择")
	ErrManualReleaseDisabled = errors.New("应用流水线没有启用手动发布，请在代码源中勾选手动发布")
)

type ManualRunSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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
	if application.Workflow == nil {
		return ManualRunOptions{}, ErrWorkflowNotFound
	}
	return s.listWorkflowRefs(ctx, application, application.Workflow)
}

func (s *Service) ListWorkflowRefs(ctx context.Context, applicationID, workflowID string) (ManualRunOptions, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return ManualRunOptions{}, err
	}
	workflow, err := s.FindApplicationWorkflow(ctx, applicationID, workflowID)
	if err != nil {
		return ManualRunOptions{}, err
	}
	return s.listWorkflowRefs(ctx, application, workflow)
}

func (s *Service) listWorkflowRefs(ctx context.Context, application *model.Application, workflow *model.ReleaseWorkflow) (ManualRunOptions, error) {
	options := ManualRunOptions{ManualSources: make([]ManualRunSource, 0)}
	refs, err := s.repositories.TestConnection(ctx, application.RepositoryID)
	if err != nil {
		return ManualRunOptions{}, err
	}
	options.Branches, options.Tags = refs.Branches, refs.Tags
	if workflow != nil {
		node := workflow.Source
		if workflowNodeSupportsManualRelease(node) {
			options.ManualSources = append(options.ManualSources, ManualRunSource{ID: node.ID, Name: node.Name})
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
	if run.ReleasePlanExecutionID != "" || run.ReleasePlanExecutionItemID != "" {
		return nil, ErrPipelineRunAwaitingReleasePlan
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
	if run.WorkflowID == "" {
		return nil, ErrWorkflowNotFound
	}
	workflow, err := s.FindApplicationWorkflow(ctx, application.ID, run.WorkflowID)
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
	if sourceNodeID != "" && (workflow == nil || manualWorkflowSource(workflow, ref, sourceNodeID) == nil) {
		return nil, ErrInvalidWorkflow
	}
	commitMessage := run.CommitMessage
	if commitMessage == "" {
		links := applicationRepositoryLinks(application)
		if len(links) > 0 {
			commitMessage = s.resolveCommitMessage(ctx, links[0].RepositoryID, ref, commitSHA)
		}
	}
	if !pipelineExecutionConfiguredForWorkflow(application, workflow) {
		if run.CommitSHA != "" {
			return nil, ErrPipelineIncomplete
		}
		return s.saveBlockedManualSelection(ctx, &run, application, workflow, ref, commitSHA, commitMessage, sourceNodeID, pipelineExecutionIncompleteMessageForWorkflow(application, workflow))
	}
	if workflow == nil || !workflow.IsActive {
		if run.CommitSHA == "" {
			return s.saveBlockedManualSelection(ctx, &run, application, workflow, ref, commitSHA, commitMessage, sourceNodeID, "已选择代码版本；应用流水线尚未启用")
		}
		return nil, ErrWorkflowNotActive
	}
	source := manualWorkflowSource(workflow, ref, sourceNodeID)
	if source == nil {
		return nil, ErrInvalidWorkflow
	}

	now := time.Now().UTC()
	createdBy := run.CreatedBy
	if createdBy == "" {
		createdBy = actorID
	}
	prepared, err := s.newResolvedWorkflowRun(
		ctx,
		application, workflow, *source, "manual", ref, commitSHA,
		createdBy, "已选择代码版本，开始执行流水线", now,
	)
	if err != nil {
		return nil, err
	}
	prepared.ID = run.ID
	prepared.CommitMessage = commitMessage
	components, err := pipelineRunRepositories(application, run.ID, ref, commitSHA, now)
	if err != nil {
		return nil, err
	}
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
	return s.advanceManualWorkflowEntry(ctx, run.ID, actorID, workflow)
}

// advanceManualWorkflowEntry 从启用手动启动的唯一代码源进入第一个任务。
func (s *Service) advanceManualWorkflowEntry(
	ctx context.Context,
	runID, actorID string,
	workflow *model.ReleaseWorkflow,
) (*model.PipelineRun, error) {
	if workflow == nil || workflowTaskCount(workflow.Stages) == 0 {
		return nil, ErrInvalidWorkflowTransition
	}
	return s.AdvanceRun(ctx, runID, actorID, "")
}

// saveBlockedManualSelection 将版本选择与执行解耦；应用配置未完成时只保存代码版本，不投递任何构建或发布任务。
func (s *Service) saveBlockedManualSelection(
	ctx context.Context,
	run *model.PipelineRun,
	application *model.Application,
	workflow *model.ReleaseWorkflow,
	ref, commitSHA, commitMessage, sourceNodeID, message string,
) (*model.PipelineRun, error) {
	now := time.Now().UTC()
	workflowID := ""
	var workflowRevision uint64
	if workflow != nil {
		workflowID, workflowRevision = workflow.ID, workflow.Revision
	}
	components, err := pipelineRunRepositories(application, run.ID, ref, commitSHA, now)
	if err != nil {
		return nil, err
	}
	for i := range components {
		components[i].Status = model.PipelineRunRepositoryPending
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.PipelineRun{}).
			Where("id = ? AND status = ? AND commit_sha = ?", run.ID, model.PipelineRunBlocked, "").
			Updates(map[string]any{
				"trigger": "manual", "ref": ref, "commit_sha": commitSHA, "commit_message": commitMessage,
				"status": model.PipelineRunBlocked, "stage": "configured", "environment": "",
				"workflow_id": workflowID, "workflow_revision": workflowRevision, "current_node_id": sourceNodeID,
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
	run.WorkflowID = workflowID
	run.WorkflowRevision = workflowRevision
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

func applicationRepositoryLink(application *model.Application) (*model.ApplicationRepository, error) {
	if application == nil || application.ID == "" || application.RepositoryID == "" || len(application.Repositories) != 1 {
		return nil, ErrApplicationRepositoryInvariant
	}
	link := &application.Repositories[0]
	if link.ApplicationID != application.ID || link.RepositoryID != application.RepositoryID {
		return nil, ErrApplicationRepositoryInvariant
	}
	return link, nil
}

func applicationRepositoryLinks(application *model.Application) []model.ApplicationRepository {
	link, err := applicationRepositoryLink(application)
	if err != nil {
		return nil
	}
	return []model.ApplicationRepository{*link}
}

func pipelineRunRepositories(application *model.Application, runID, ref, commitSHA string, now time.Time) ([]model.PipelineRunRepository, error) {
	link, err := applicationRepositoryLink(application)
	if err != nil {
		return nil, err
	}
	return []model.PipelineRunRepository{{
		ID: uuid.NewString(), PipelineRunID: runID, RepositoryID: link.RepositoryID,
		SortOrder: 0, Ref: ref, CommitSHA: commitSHA,
		Status: model.PipelineRunRepositoryReady, CreatedAt: now, UpdatedAt: now,
	}}, nil
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

func manualWorkflowSource(workflow *model.ReleaseWorkflow, _ string, sourceNodeID string) *model.WorkflowNode {
	if workflow == nil {
		return nil
	}
	source := &workflow.Source
	if sourceNodeID != "" && source.ID != sourceNodeID {
		return nil
	}
	if !workflowNodeSupportsManualRelease(*source) {
		return nil
	}
	return source
}

func retryWorkflowSource(workflow *model.ReleaseWorkflow, run *model.PipelineRun) *model.WorkflowNode {
	if workflow == nil || run == nil {
		return nil
	}
	node := &workflow.Source
	if node.Type != model.WorkflowNodeTrigger {
		return nil
	}
	if strings.HasPrefix(run.Ref, "refs/tags/") {
		if containsEvent(node.Config.Events, "tag") && matchTag(node.Config.TagPattern, strings.TrimPrefix(run.Ref, "refs/tags/")) {
			return node
		}
		return nil
	}
	if strings.HasPrefix(run.Ref, "refs/pull/") || strings.HasPrefix(run.Ref, "refs/merge-requests/") {
		matched := matchingWorkflowTriggers(
			workflow, "pr", run.Ref, run.SourceBranch, run.TargetBranch, run.TriggerAction,
		)
		if len(matched) == 1 {
			return &workflow.Source
		}
		return nil
	}
	branch := strings.TrimPrefix(run.Ref, "refs/heads/")
	if matchWorkflowPattern(node.Config.Branch, branch) {
		return node
	}
	return manualWorkflowSource(workflow, run.Ref, "")
}
