package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/repository"
)

var (
	ErrManualCommitRequired   = errors.New("请选择要发布的 Commit")
	ErrManualCommitNotFound   = errors.New("所选 Commit 不存在或对应分支、Tag 已经变化，请重新选择")
	ErrManualSelectionInvalid = errors.New("请选择代码版本或已有制品，不能同时选择")
	ErrManualArtifactInvalid  = errors.New("所选制品不可用或与当前流水线不匹配，请重新选择")
	ErrManualReleaseDisabled  = errors.New("应用流水线没有启用手动发布，请在代码源中勾选手动发布")
)

type ManualRunSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ManualRunOptions struct {
	Branches       []repository.GitRef `json:"branches"`
	Tags           []repository.GitRef `json:"tags"`
	ManualSources  []ManualRunSource   `json:"manual_sources"`
	Artifacts      []ManualRunArtifact `json:"artifacts"`
	ReferenceError string              `json:"reference_error,omitempty"`
}

type ManualRunArtifact struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Kind        model.ArtifactKind `json:"kind"`
	Digest      string             `json:"digest"`
	BuildPlanID string             `json:"build_plan_id"`
	Ref         string             `json:"ref,omitempty"`
	CommitSHA   string             `json:"commit_sha,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
}

type manualArtifactSelection struct {
	artifact   model.Artifact
	build      model.BuildRun
	resumeNode model.WorkflowNode
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
	options := ManualRunOptions{ManualSources: make([]ManualRunSource, 0), Artifacts: make([]ManualRunArtifact, 0)}
	if workflow != nil {
		node := workflow.Source
		if workflowNodeSupportsManualRelease(node) {
			options.ManualSources = append(options.ManualSources, ManualRunSource{ID: node.ID, Name: node.Name})
		}
	}
	artifacts, err := s.listManualWorkflowArtifacts(ctx, application, workflow)
	if err != nil {
		return ManualRunOptions{}, err
	}
	options.Artifacts = artifacts
	refs, err := s.repositories.TestConnection(ctx, application.RepositoryID)
	if err != nil {
		if len(options.Artifacts) == 0 {
			return ManualRunOptions{}, err
		}
		if s.logger != nil {
			workflowID := ""
			if workflow != nil {
				workflowID = workflow.ID
			}
			s.logger.Error("读取手动执行代码版本失败，仍可选择已有制品", "operation", "pipeline_manual_refs", "application_id", application.ID, "workflow_id", workflowID, "err", err)
		}
		options.ReferenceError = "代码仓库暂时不可用，仍可选择已有制品执行"
		return options, nil
	}
	options.Branches, options.Tags = refs.Branches, refs.Tags
	return options, nil
}

func (s *Service) ExecuteRun(ctx context.Context, runID, actorID, ref, commitSHA, sourceNodeID string) (*model.PipelineRun, error) {
	return s.ExecuteRunSelection(ctx, runID, actorID, ref, commitSHA, sourceNodeID, "")
}

func (s *Service) ExecuteRunSelection(ctx context.Context, runID, actorID, ref, commitSHA, sourceNodeID, artifactID string) (*model.PipelineRun, error) {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "uuid = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPipelineRunNotFound
		}
		return nil, err
	}
	if run.ReleasePlanExecutionID != "" || run.ReleasePlanExecutionItemID != "" {
		return nil, ErrPipelineRunAwaitingReleasePlan
	}
	ref, commitSHA, artifactID = strings.TrimSpace(ref), strings.TrimSpace(commitSHA), strings.TrimSpace(artifactID)
	if run.Status != model.PipelineRunBlocked {
		if ref != "" || commitSHA != "" || artifactID != "" {
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
	var selectedArtifact *manualArtifactSelection
	if artifactID != "" {
		if ref != "" || commitSHA != "" || run.CommitSHA != "" {
			return nil, ErrManualSelectionInvalid
		}
		selectedArtifact, err = s.validateManualArtifactSelection(ctx, application, workflow, artifactID)
		if err != nil {
			return nil, err
		}
		ref, commitSHA = selectedArtifact.build.Ref, selectedArtifact.build.CommitSHA
	} else if run.CommitSHA == "" {
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
	if commitMessage == "" && ref != "" && commitSHA != "" {
		links := applicationRepositoryLinks(application)
		if len(links) > 0 {
			commitMessage = s.resolveCommitMessage(ctx, links[0].RepositoryID, ref, commitSHA)
		}
	}
	if !pipelineExecutionConfiguredForWorkflow(application, workflow) {
		if selectedArtifact != nil {
			return nil, ErrPipelineIncomplete
		}
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
	if selectedArtifact != nil {
		prepared.CurrentNodeID = selectedArtifact.resumeNode.ID
		prepared.Status = model.PipelineRunReady
		prepared.Stage = "task_succeeded"
		prepared.ArtifactID = selectedArtifact.artifact.ID
		prepared.Image = selectedArtifact.artifact.ImageRef
		prepared.Message = "已选择已有制品，跳过构建和构建后的脚本检查并继续执行"
	}
	components, err := pipelineRunRepositories(application, run.ID, ref, commitSHA, now)
	if err != nil {
		return nil, err
	}
	if selectedArtifact != nil {
		for i := range components {
			components[i].Status = model.PipelineRunRepositoryReady
		}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.PipelineRun{}).
			Where("uuid = ? AND status = ? AND commit_sha = ?", run.ID, model.PipelineRunBlocked, run.CommitSHA).
			Updates(map[string]any{
				"trigger": prepared.Trigger, "ref": prepared.Ref, "commit_sha": prepared.CommitSHA, "commit_message": prepared.CommitMessage,
				"status": prepared.Status, "stage": prepared.Stage, "environment": prepared.Environment,
				"workflow_id": prepared.WorkflowID, "workflow_revision": prepared.WorkflowRevision,
				"current_node_id": prepared.CurrentNodeID, "workflow_snapshot": prepared.WorkflowSnapshot,
				"artifact_id": prepared.ArtifactID, "image": prepared.Image,
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
	if selectedArtifact != nil {
		s.appendRunLog(ctx, run.ID, "configured", "info", "已固定已有制品 "+selectedArtifact.artifact.Name+"（"+selectedArtifact.artifact.Digest+"）")
	}
	return s.advanceManualWorkflowEntry(ctx, run.ID, actorID, workflow)
}

func (s *Service) listManualWorkflowArtifacts(ctx context.Context, application *model.Application, workflow *model.ReleaseWorkflow) ([]ManualRunArtifact, error) {
	if application == nil || workflow == nil {
		return []ManualRunArtifact{}, nil
	}
	var artifacts []model.Artifact
	if err := s.db.WithContext(ctx).
		Where("application_id = ? AND status = ?", application.ID, model.ArtifactStatusAvailable).
		Order("id DESC").Limit(100).Find(&artifacts).Error; err != nil {
		if s.logger != nil {
			s.logger.Error("查询手动执行可选制品失败", "operation", "pipeline_manual_artifacts", "application_id", application.ID, "workflow_id", workflow.ID, "err", err)
		}
		return nil, err
	}
	if len(artifacts) == 0 {
		return []ManualRunArtifact{}, nil
	}
	buildIDs := make([]string, 0, len(artifacts))
	for i := range artifacts {
		buildIDs = append(buildIDs, artifacts[i].BuildRunID)
	}
	var builds []model.BuildRun
	if err := s.db.WithContext(ctx).Where("uuid IN ? AND status = ?", buildIDs, model.BuildRunStatusSucceeded).Find(&builds).Error; err != nil {
		if s.logger != nil {
			s.logger.Error("查询手动执行制品生产记录失败", "operation", "pipeline_manual_artifact_builds", "application_id", application.ID, "workflow_id", workflow.ID, "err", err)
		}
		return nil, err
	}
	buildByID := make(map[string]model.BuildRun, len(builds))
	planIDs := make([]string, 0, len(builds))
	for i := range builds {
		buildByID[builds[i].ID] = builds[i]
		if builds[i].BuildPlanID != "" {
			planIDs = append(planIDs, builds[i].BuildPlanID)
		}
	}
	var plans []model.BuildPlan
	if len(planIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", planIDs, true).Find(&plans).Error; err != nil {
			if s.logger != nil {
				s.logger.Error("查询手动执行制品构建方案失败", "operation", "pipeline_manual_artifact_plans", "application_id", application.ID, "workflow_id", workflow.ID, "err", err)
			}
			return nil, err
		}
	}
	planByID := inlineBuildPlansForWorkflow(workflow)
	for i := range plans {
		planByID[plans[i].ID] = plans[i]
	}
	result := make([]ManualRunArtifact, 0, len(artifacts))
	for i := range artifacts {
		build, buildOK := buildByID[artifacts[i].BuildRunID]
		plan, planOK := planByID[build.BuildPlanID]
		if !buildOK || !planOK || build.ApplicationID != application.ID {
			continue
		}
		if _, _, ok := manualArtifactBuildNodes(workflow, build, plan, artifacts[i]); !ok {
			continue
		}
		result = append(result, manualRunArtifactSummary(artifacts[i], build))
	}
	return result, nil
}

// runReusableBuildArtifact 是一次重新执行可以沿用的构建制品及其在当前流水线中的构建节点。
type runReusableBuildArtifact struct {
	artifact model.Artifact
	build    model.BuildRun
	node     model.WorkflowNode
}

// runReuseSkipNode 是沿用制品时需要预先标记为跳过的任务。
type runReuseSkipNode struct {
	node       model.WorkflowNode
	artifactID string
	name       string
	digest     string
	imageRef   string
}

// runReusableArtifactGroup 是同一个构建节点上可沿用的制品，最新在前。
type runReusableArtifactGroup struct {
	node    model.WorkflowNode
	options []runReusableBuildArtifact
}

// listRunReusableArtifactGroups 返回该运行固定版本上已经构建、且能对应当前构建节点的可用制品，
// 按构建节点分组，组内最新在前。制品的生产记录必须使用同一个 Ref 和 Commit。
func (s *Service) listRunReusableArtifactGroups(ctx context.Context, application *model.Application, workflow *model.ReleaseWorkflow, run *model.PipelineRun) ([]runReusableArtifactGroup, error) {
	if application == nil || workflow == nil || run == nil ||
		strings.TrimSpace(run.Ref) == "" || strings.TrimSpace(run.CommitSHA) == "" {
		return nil, nil
	}
	tasks := workflowTasks(enabledWorkflowStages(workflow.Stages))
	if len(tasks) == 0 {
		return nil, nil
	}
	planIDByNode := make(map[string]string, len(tasks))
	plans := inlineBuildPlansForWorkflow(workflow)
	libraryPlanIDs := make([]string, 0, 4)
	for i := range tasks {
		if tasks[i].Type != model.WorkflowNodeBuild {
			continue
		}
		if tasks[i].Config.InlineBuild != nil {
			planIDByNode[tasks[i].ID] = inlineBuildIdentity(workflow.ID, tasks[i].ID)
			continue
		}
		planID := strings.TrimSpace(tasks[i].Config.BuildPlanID)
		planIDByNode[tasks[i].ID] = planID
		if planID != "" {
			libraryPlanIDs = append(libraryPlanIDs, planID)
		}
	}
	if len(libraryPlanIDs) > 0 {
		var stored []model.BuildPlan
		if err := s.db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", libraryPlanIDs, true).Find(&stored).Error; err != nil {
			if s.logger != nil {
				s.logger.Error("查询可沿用制品的构建方案失败", "operation", "pipeline_reuse_artifact_plans", "application_id", application.ID, "workflow_id", workflow.ID, "err", err)
			}
			return nil, err
		}
		for i := range stored {
			plans[stored[i].ID] = stored[i]
		}
	}
	if len(plans) == 0 {
		return nil, nil
	}
	var builds []model.BuildRun
	if err := s.db.WithContext(ctx).
		Where("application_id = ? AND ref = ? AND commit_sha = ? AND status = ?", application.ID, run.Ref, run.CommitSHA, model.BuildRunStatusSucceeded).
		Order("id DESC").Limit(100).Find(&builds).Error; err != nil {
		if s.logger != nil {
			s.logger.Error("查询可沿用制品的构建记录失败", "operation", "pipeline_reuse_artifact_builds", "application_id", application.ID, "workflow_id", workflow.ID, "err", err)
		}
		return nil, err
	}
	if len(builds) == 0 {
		return nil, nil
	}
	buildIDs := make([]string, 0, len(builds))
	for i := range builds {
		buildIDs = append(buildIDs, builds[i].ID)
	}
	var artifacts []model.Artifact
	if err := s.db.WithContext(ctx).
		Where("build_run_id IN ? AND application_id = ? AND status = ?", buildIDs, application.ID, model.ArtifactStatusAvailable).
		Order("id DESC").Find(&artifacts).Error; err != nil {
		if s.logger != nil {
			s.logger.Error("查询可沿用制品失败", "operation", "pipeline_reuse_artifacts", "application_id", application.ID, "workflow_id", workflow.ID, "err", err)
		}
		return nil, err
	}
	if len(artifacts) == 0 {
		return nil, nil
	}
	artifactByBuild := make(map[string]model.Artifact, len(artifacts))
	for i := range artifacts {
		if _, exists := artifactByBuild[artifacts[i].BuildRunID]; exists {
			continue
		}
		artifactByBuild[artifacts[i].BuildRunID] = artifacts[i]
	}
	grouped := make(map[string][]runReusableBuildArtifact, len(tasks))
	for i := range builds {
		plan, planOK := plans[builds[i].BuildPlanID]
		if !planOK {
			continue
		}
		artifact, artifactOK := artifactByBuild[builds[i].ID]
		if !artifactOK || !manualArtifactKindMatches(artifact, plan) {
			continue
		}
		node, nodeOK := reusableBuildNode(tasks, planIDByNode, builds[i], plan)
		if !nodeOK {
			continue
		}
		grouped[node.ID] = append(grouped[node.ID], runReusableBuildArtifact{artifact: artifact, build: builds[i], node: node})
	}
	result := make([]runReusableArtifactGroup, 0, len(grouped))
	for i := range tasks {
		if options := grouped[tasks[i].ID]; len(options) > 0 {
			result = append(result, runReusableArtifactGroup{node: tasks[i], options: options})
		}
	}
	return result, nil
}

// selectRunReusableArtifacts 解析实际沿用的制品：未指定时每个构建节点取最新一份，指定时只接受
// 该固定版本上的可用制品，并且同一构建节点只能选一个。
func selectRunReusableArtifacts(groups []runReusableArtifactGroup, artifactIDs []string) ([]runReusableBuildArtifact, error) {
	if len(artifactIDs) == 0 {
		selected := make([]runReusableBuildArtifact, 0, len(groups))
		for i := range groups {
			if len(groups[i].options) > 0 {
				selected = append(selected, groups[i].options[0])
			}
		}
		return selected, nil
	}
	index := make(map[string]runReusableBuildArtifact, len(groups))
	for i := range groups {
		for j := range groups[i].options {
			index[groups[i].options[j].artifact.ID] = groups[i].options[j]
		}
	}
	chosen := make(map[string]string, len(artifactIDs))
	for _, raw := range artifactIDs {
		artifactID := strings.TrimSpace(raw)
		if artifactID == "" {
			continue
		}
		option, ok := index[artifactID]
		if !ok {
			return nil, ErrRetryArtifactInvalid
		}
		if _, exists := chosen[option.node.ID]; exists {
			return nil, ErrRetryArtifactInvalid
		}
		chosen[option.node.ID] = artifactID
	}
	if len(chosen) == 0 {
		return nil, ErrRetryArtifactsUnavailable
	}
	selected := make([]runReusableBuildArtifact, 0, len(chosen))
	for i := range groups {
		artifactID, ok := chosen[groups[i].node.ID]
		if !ok {
			continue
		}
		for j := range groups[i].options {
			if groups[i].options[j].artifact.ID == artifactID {
				selected = append(selected, groups[i].options[j])
				break
			}
		}
	}
	return selected, nil
}

// reusableBuildNode 在流水线中定位生产该制品的构建节点：有节点的构建记录必须与节点一致，
// 否则只有在构建方案唯一对应一个构建节点时才接受。
func reusableBuildNode(tasks []model.WorkflowNode, planIDByNode map[string]string, build model.BuildRun, plan model.BuildPlan) (model.WorkflowNode, bool) {
	var match model.WorkflowNode
	matched := 0
	for i := range tasks {
		if tasks[i].Type != model.WorkflowNodeBuild || planIDByNode[tasks[i].ID] != plan.ID {
			continue
		}
		if build.WorkflowNodeID != "" && tasks[i].ID != build.WorkflowNodeID {
			continue
		}
		match = tasks[i]
		matched++
	}
	if matched != 1 {
		return model.WorkflowNode{}, false
	}
	return match, true
}

// runReuseSkipNodes 计算沿用这些制品时需要预先标记为跳过的任务：构建节点，以及紧随其后、
// 不涉及数据库迁移的 Shell 检查。跳过之后必须还有部署可以继续，否则不提供沿用。
func runReuseSkipNodes(workflow *model.ReleaseWorkflow, artifacts []runReusableBuildArtifact) []runReuseSkipNode {
	if workflow == nil || len(artifacts) == 0 {
		return nil
	}
	tasks := workflowTasks(enabledWorkflowStages(workflow.Stages))
	byNode := make(map[string]runReusableBuildArtifact, len(artifacts))
	for i := range artifacts {
		byNode[artifacts[i].node.ID] = artifacts[i]
	}
	skipped := make([]runReuseSkipNode, 0, len(artifacts))
	for index := 0; index < len(tasks); index++ {
		item, exists := byNode[tasks[index].ID]
		if !exists || tasks[index].Type != model.WorkflowNodeBuild {
			continue
		}
		skipped = append(skipped, runReuseSkipNode{
			node: tasks[index], artifactID: item.artifact.ID,
			name: item.artifact.Name, digest: item.artifact.Digest, imageRef: item.artifact.ImageRef,
		})
		for next := index + 1; next < len(tasks); next++ {
			if tasks[next].Type != model.WorkflowNodeShell || tasks[next].Config.Migration != nil {
				break
			}
			skipped = append(skipped, runReuseSkipNode{node: tasks[next]})
			index = next
		}
	}
	if len(skipped) == 0 {
		return nil
	}
	resume := skipped[len(skipped)-1].node.ID
	for index := range tasks {
		if tasks[index].ID != resume {
			continue
		}
		for later := index + 1; later < len(tasks); later++ {
			if tasks[later].Type == model.WorkflowNodeDeploy {
				return skipped
			}
		}
		return nil
	}
	return nil
}

// manualArtifactKindMatches 判断制品类型与构建方案一致：Dockerfile 产出镜像，脚本产出文件制品。
func manualArtifactKindMatches(stored model.Artifact, plan model.BuildPlan) bool {
	switch plan.Kind {
	case model.BuildPlanDockerfile:
		return stored.Kind == model.ArtifactKindOCIImage
	case model.BuildPlanScript:
		return stored.Kind == model.ArtifactKindFileBundle
	}
	return false
}

func manualRunArtifactSummary(stored model.Artifact, build model.BuildRun) ManualRunArtifact {
	return ManualRunArtifact{
		ID: stored.ID, Name: stored.Name, Kind: stored.Kind, Digest: stored.Digest,
		BuildPlanID: build.BuildPlanID, Ref: build.Ref, CommitSHA: build.CommitSHA, CreatedAt: stored.CreatedAt,
	}
}

func (s *Service) validateManualArtifactSelection(ctx context.Context, application *model.Application, workflow *model.ReleaseWorkflow, artifactID string) (*manualArtifactSelection, error) {
	artifactID = strings.TrimSpace(artifactID)
	if application == nil || workflow == nil || artifactID == "" {
		return nil, ErrManualArtifactInvalid
	}
	var stored model.Artifact
	if err := s.db.WithContext(ctx).First(&stored, "uuid = ?", artifactID).Error; err != nil {
		if s.logger != nil {
			s.logger.Warn("手动执行选择的制品不存在", "operation", "pipeline_manual_artifact_validate", "application_id", application.ID, "artifact_id", artifactID, "err", err)
		}
		return nil, ErrManualArtifactInvalid
	}
	var build model.BuildRun
	if err := s.db.WithContext(ctx).First(&build, "uuid = ?", stored.BuildRunID).Error; err != nil {
		if s.logger != nil {
			s.logger.Error("手动执行选择的制品缺少生产记录", "operation", "pipeline_manual_artifact_validate", "application_id", application.ID, "artifact_id", artifactID, "build_run_id", stored.BuildRunID, "err", err)
		}
		return nil, ErrManualArtifactInvalid
	}
	var plan model.BuildPlan
	if build.BuildPlanID == "" {
		if s.logger != nil {
			s.logger.Warn("手动执行选择的制品没有关联构建方案", "operation", "pipeline_manual_artifact_validate", "application_id", application.ID, "artifact_id", artifactID, "build_run_id", build.ID)
		}
		return nil, ErrManualArtifactInvalid
	}
	inline, hasInline := inlineBuildPlansForWorkflow(workflow)[build.BuildPlanID]
	if hasInline {
		plan = inline
	}
	if err := func() error {
		if hasInline {
			return nil
		}
		return s.db.WithContext(ctx).First(&plan, "uuid = ? AND is_active = ?", build.BuildPlanID, true).Error
	}(); err != nil {
		if s.logger != nil {
			s.logger.Warn("手动执行选择的制品构建方案不可用", "operation", "pipeline_manual_artifact_validate", "application_id", application.ID, "artifact_id", artifactID, "build_plan_id", build.BuildPlanID, "err", err)
		}
		return nil, ErrManualArtifactInvalid
	}
	_, resumeNode, ok := manualArtifactBuildNodes(workflow, build, plan, stored)
	if !ok || stored.ApplicationID != application.ID || stored.Status != model.ArtifactStatusAvailable ||
		build.ApplicationID != application.ID || build.Status != model.BuildRunStatusSucceeded {
		if s.logger != nil {
			s.logger.Warn("手动执行选择的制品与当前流水线不匹配", "operation", "pipeline_manual_artifact_validate", "application_id", application.ID, "workflow_id", workflow.ID, "artifact_id", artifactID, "build_plan_id", build.BuildPlanID, "artifact_status", stored.Status, "build_status", build.Status)
		}
		return nil, ErrManualArtifactInvalid
	}
	return &manualArtifactSelection{artifact: stored, build: build, resumeNode: resumeNode}, nil
}

func manualArtifactBuildNodes(workflow *model.ReleaseWorkflow, build model.BuildRun, plan model.BuildPlan, stored model.Artifact) (model.WorkflowNode, model.WorkflowNode, bool) {
	if workflow == nil || plan.ID == "" || build.BuildPlanID != plan.ID {
		return model.WorkflowNode{}, model.WorkflowNode{}, false
	}
	if !manualArtifactKindMatches(stored, plan) {
		return model.WorkflowNode{}, model.WorkflowNode{}, false
	}
	tasks := workflowTasks(enabledWorkflowStages(workflow.Stages))
	type candidate struct {
		build  model.WorkflowNode
		resume model.WorkflowNode
	}
	candidates := make([]candidate, 0, 1)
	for index := range tasks {
		resumeNode, feedsDeployment := workflowArtifactResumeNode(tasks, index)
		if tasks[index].Type != model.WorkflowNodeBuild || func() string {
			if tasks[index].Config.InlineBuild != nil {
				return inlineBuildIdentity(workflow.ID, tasks[index].ID)
			}
			return tasks[index].Config.BuildPlanID
		}() != plan.ID || !feedsDeployment {
			continue
		}
		if build.WorkflowNodeID != "" && tasks[index].ID == build.WorkflowNodeID {
			return tasks[index], resumeNode, true
		}
		candidates = append(candidates, candidate{build: tasks[index], resume: resumeNode})
	}
	if len(candidates) != 1 {
		return model.WorkflowNode{}, model.WorkflowNode{}, false
	}
	return candidates[0].build, candidates[0].resume, true
}

func workflowArtifactResumeNode(tasks []model.WorkflowNode, buildIndex int) (model.WorkflowNode, bool) {
	if buildIndex < 0 || buildIndex >= len(tasks) || tasks[buildIndex].Type != model.WorkflowNodeBuild {
		return model.WorkflowNode{}, false
	}
	resume := tasks[buildIndex]
	for index := buildIndex + 1; index < len(tasks); index++ {
		if tasks[index].Type == model.WorkflowNodeBuild {
			return model.WorkflowNode{}, false
		}
		if tasks[index].Type == model.WorkflowNodeShell {
			resume = tasks[index]
			continue
		}
		if tasks[index].Type == model.WorkflowNodeDeploy {
			return resume, true
		}
		for later := index + 1; later < len(tasks); later++ {
			if tasks[later].Type == model.WorkflowNodeBuild {
				return model.WorkflowNode{}, false
			}
			if tasks[later].Type == model.WorkflowNodeDeploy {
				return resume, true
			}
		}
		return model.WorkflowNode{}, false
	}
	return model.WorkflowNode{}, false
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
	advanced, err := s.AdvanceRun(ctx, runID, actorID, "")
	if err != nil {
		return nil, err
	}
	return s.FindRun(ctx, advanced.ID)
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
			Where("uuid = ? AND status = ? AND commit_sha = ?", run.ID, model.PipelineRunBlocked, "").
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
	// 重试使用原运行已经固定的 Ref 和 Commit，不再按当前远程分支、Tag、PR 状态或触发规则重新匹配版本。
	return node
}
