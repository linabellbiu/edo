package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

var (
	ErrInvalidReleasePlanExecution           = errors.New("发布计划执行配置无效，请刷新后重试")
	ErrReleasePlanExecutionExists            = errors.New("发布计划已经执行，不能重复执行")
	ErrReleasePlanExecutionNotFound          = errors.New("发布计划执行不存在")
	ErrReleasePlanExecutionPlanChanged       = errors.New("发布计划已经变更，请刷新后重试")
	ErrReleasePlanExecutionWorkflowChanged   = errors.New("应用流水线已经变更，请刷新后重试")
	ErrReleasePlanExecutionVersionChanged    = errors.New("所选代码版本已经变化，请重新选择")
	ErrReleasePlanExecutionTemporarilyFailed = errors.New("发布计划执行暂时不可用，请稍后重试")
)

type ReleasePlanExecutionSelection struct {
	ReleaseGroupApplicationID string
	WorkflowID                string
	ExpectedWorkflowRevision  uint64
	SourceNodeID              string
	Ref                       string
	CommitSHA                 string
}

type ReleasePlanExecutionInput struct {
	RequestID             string
	ExpectedPlanUpdatedAt time.Time
	Selections            []ReleasePlanExecutionSelection
}

type releasePlanExecutionSnapshot struct {
	Groups []releasePlanExecutionGroupSnapshot `json:"groups"`
}

type releasePlanExecutionGroupSnapshot struct {
	ID            string                          `json:"id"`
	Mode          model.ReleaseGroupMode          `json:"mode"`
	FailurePolicy model.ReleaseGroupFailurePolicy `json:"failure_policy"`
	SortOrder     int                             `json:"sort_order"`
	DependsOn     []string                        `json:"depends_on"`
	ItemIDs       []string                        `json:"item_ids"`
}

type preparedReleasePlanExecutionItem struct {
	item                 model.ReleasePlanExecutionItem
	run                  model.PipelineRun
	components           []model.PipelineRunRepository
	applicationUpdatedAt time.Time
	workflowUpdatedAt    time.Time
}

type preparedReleasePlanExecution struct {
	execution     model.ReleasePlanExecution
	items         []preparedReleasePlanExecutionItem
	planSignature string
}

type releasePlanExecutionPlanState struct {
	Groups []releasePlanExecutionPlanGroupState `json:"groups"`
}

type releasePlanExecutionPlanGroupState struct {
	ID            string                                     `json:"id"`
	Mode          model.ReleaseGroupMode                     `json:"mode"`
	FailurePolicy model.ReleaseGroupFailurePolicy            `json:"failure_policy"`
	SortOrder     int                                        `json:"sort_order"`
	Applications  []releasePlanExecutionPlanApplicationState `json:"applications"`
	Dependencies  []string                                   `json:"dependencies"`
}

type releasePlanExecutionPlanApplicationState struct {
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	SortOrder     int    `json:"sort_order"`
}

func (s *Service) FindReleasePlanExecution(ctx context.Context, id string) (*model.ReleasePlanExecution, error) {
	var execution model.ReleasePlanExecution
	err := s.db.WithContext(ctx).
		Preload("Items", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC, created_at ASC") }).
		First(&execution, "id = ?", strings.TrimSpace(id)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrReleasePlanExecutionNotFound
	}
	if err != nil {
		s.logReleasePlanExecutionError("release_plan_execution_find", id, err)
		return nil, ErrReleasePlanExecutionTemporarilyFailed
	}
	return &execution, nil
}

// CreateReleasePlanExecution 在一次远端预检全部通过后，原子固化计划、流水线和仓库快照。
// 事务内不会投递构建或部署任务；任务只会在 ReconcileReleasePlanExecution 领取执行槽后产生。
func (s *Service) CreateReleasePlanExecution(
	ctx context.Context,
	planID, actorID string,
	input ReleasePlanExecutionInput,
) (*model.ReleasePlanExecution, error) {
	planID, actorID = strings.TrimSpace(planID), strings.TrimSpace(actorID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if planID == "" || actorID == "" || input.RequestID == "" || len(input.RequestID) > 128 || input.ExpectedPlanUpdatedAt.IsZero() {
		return nil, ErrInvalidReleasePlanExecution
	}
	if existing, err := s.findReleasePlanExecutionByPlan(ctx, planID); err == nil {
		if existing.RequestID == input.RequestID {
			return s.FindReleasePlanExecution(ctx, existing.ID)
		}
		return nil, ErrReleasePlanExecutionExists
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logReleasePlanExecutionError("release_plan_execution_idempotency_check", planID, err)
		return nil, ErrReleasePlanExecutionTemporarilyFailed
	}

	prepared, err := s.prepareReleasePlanExecution(ctx, planID, actorID, input)
	if err != nil {
		return nil, err
	}

	s.releasePlanExecutionMu.Lock()
	defer s.releasePlanExecutionMu.Unlock()

	var existingID string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.ReleasePlanExecution
		findExisting := tx.Where("release_plan_id = ?", planID).First(&existing).Error
		if findExisting == nil {
			if existing.RequestID == input.RequestID {
				existingID = existing.ID
				return nil
			}
			return ErrReleasePlanExecutionExists
		}
		if !errors.Is(findExisting, gorm.ErrRecordNotFound) {
			return findExisting
		}

		var plan model.ReleasePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", planID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReleasePlanNotFound
			}
			return err
		}
		if !plan.IsActive {
			return ErrReleasePlanDisabled
		}
		if (plan.Status != model.ReleasePlanDraft && plan.Status != model.ReleasePlanActive) ||
			!plan.UpdatedAt.Equal(input.ExpectedPlanUpdatedAt) {
			return ErrReleasePlanExecutionPlanChanged
		}
		var currentPlan model.ReleasePlan
		if err := releasePlanQuery(tx).First(&currentPlan, "id = ?", planID).Error; err != nil {
			return err
		}
		currentSignature, err := releasePlanExecutionPlanSignature(&currentPlan)
		if err != nil {
			return err
		}
		if currentSignature != prepared.planSignature {
			return ErrReleasePlanExecutionPlanChanged
		}
		lockOrder := make([]int, len(prepared.items))
		for i := range prepared.items {
			lockOrder[i] = i
		}
		sort.Slice(lockOrder, func(i, j int) bool {
			return prepared.items[lockOrder[i]].item.ApplicationID < prepared.items[lockOrder[j]].item.ApplicationID
		})
		for _, itemIndex := range lockOrder {
			item := &prepared.items[itemIndex]
			var application model.Application
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "is_active", "updated_at").
				First(&application, "id = ?", item.item.ApplicationID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrInvalidReleasePlanExecution
				}
				return err
			}
			if !application.IsActive || !application.UpdatedAt.Equal(item.applicationUpdatedAt) {
				return ErrReleasePlanExecutionPlanChanged
			}
			var workflow model.ReleaseWorkflow
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "revision", "is_active", "updated_at").
				First(&workflow, "id = ? AND application_id = ?", item.run.WorkflowID, item.item.ApplicationID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrReleasePlanExecutionWorkflowChanged
				}
				return err
			}
			if !workflow.IsActive || workflow.Revision != item.run.WorkflowRevision ||
				!workflow.UpdatedAt.Equal(item.workflowUpdatedAt) {
				return ErrReleasePlanExecutionWorkflowChanged
			}
		}

		if err := tx.Create(&prepared.execution).Error; err != nil {
			return err
		}
		for i := range prepared.items {
			item := &prepared.items[i]
			if err := tx.Create(&item.run).Error; err != nil {
				return err
			}
			if len(item.components) > 0 {
				if err := tx.Create(&item.components).Error; err != nil {
					return err
				}
			}
			if err := tx.Create(&item.item).Error; err != nil {
				return err
			}
		}
		if plan.Status == model.ReleasePlanDraft {
			now := time.Now().UTC()
			result := tx.Model(&model.ReleasePlan{}).
				Where("id = ? AND status = ?", planID, model.ReleasePlanDraft).
				Updates(map[string]any{
					"status": model.ReleasePlanActive, "updated_by": actorID, "updated_at": now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrReleasePlanExecutionPlanChanged
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			if existing, findErr := s.findReleasePlanExecutionByPlan(ctx, planID); findErr == nil {
				if existing.RequestID == input.RequestID {
					return s.FindReleasePlanExecution(ctx, existing.ID)
				}
				return nil, ErrReleasePlanExecutionExists
			}
		}
		if isReleasePlanExecutionPublicError(err) {
			return nil, err
		}
		s.logReleasePlanExecutionError("release_plan_execution_create", planID, err)
		return nil, ErrReleasePlanExecutionTemporarilyFailed
	}
	if existingID != "" {
		return s.FindReleasePlanExecution(ctx, existingID)
	}
	return s.FindReleasePlanExecution(ctx, prepared.execution.ID)
}

func (s *Service) prepareReleasePlanExecution(
	ctx context.Context,
	planID, actorID string,
	input ReleasePlanExecutionInput,
) (*preparedReleasePlanExecution, error) {
	var plan model.ReleasePlan
	if err := releasePlanQuery(s.db.WithContext(ctx)).First(&plan, "id = ?", planID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReleasePlanNotFound
		}
		s.logReleasePlanExecutionError("release_plan_execution_preflight_plan", planID, err)
		return nil, ErrReleasePlanExecutionTemporarilyFailed
	}
	if !plan.IsActive {
		return nil, ErrReleasePlanDisabled
	}
	if (plan.Status != model.ReleasePlanDraft && plan.Status != model.ReleasePlanActive) ||
		!plan.UpdatedAt.Equal(input.ExpectedPlanUpdatedAt) {
		return nil, ErrReleasePlanExecutionPlanChanged
	}
	planSignature, err := releasePlanExecutionPlanSignature(&plan)
	if err != nil {
		s.logReleasePlanExecutionError("release_plan_execution_preflight_signature", planID, err)
		return nil, ErrReleasePlanExecutionTemporarilyFailed
	}

	selectionByApplication := make(map[string]ReleasePlanExecutionSelection, len(input.Selections))
	for i := range input.Selections {
		selection := input.Selections[i]
		selection.ReleaseGroupApplicationID = strings.TrimSpace(selection.ReleaseGroupApplicationID)
		selection.WorkflowID = strings.TrimSpace(selection.WorkflowID)
		selection.SourceNodeID = strings.TrimSpace(selection.SourceNodeID)
		selection.Ref = strings.TrimSpace(selection.Ref)
		selection.CommitSHA = strings.TrimSpace(selection.CommitSHA)
		if selection.ReleaseGroupApplicationID == "" || selection.WorkflowID == "" || selection.SourceNodeID == "" || selection.Ref == "" || selection.CommitSHA == "" {
			return nil, ErrInvalidReleasePlanExecution
		}
		if _, exists := selectionByApplication[selection.ReleaseGroupApplicationID]; exists {
			return nil, ErrInvalidReleasePlanExecution
		}
		selectionByApplication[selection.ReleaseGroupApplicationID] = selection
	}

	groupApplicationCount := 0
	for i := range plan.Groups {
		groupApplicationCount += len(plan.Groups[i].Applications)
	}
	if len(plan.Groups) == 0 || groupApplicationCount == 0 || len(selectionByApplication) != groupApplicationCount {
		return nil, ErrInvalidReleasePlanExecution
	}

	now := time.Now().UTC()
	executionID := uuid.NewString()
	prepared := &preparedReleasePlanExecution{
		execution: model.ReleasePlanExecution{
			ID: executionID, ReleasePlanID: plan.ID, RequestID: input.RequestID,
			Status: model.ReleasePlanExecutionPending, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
		},
		items:         make([]preparedReleasePlanExecutionItem, 0, groupApplicationCount),
		planSignature: planSignature,
	}
	snapshot := releasePlanExecutionSnapshot{Groups: make([]releasePlanExecutionGroupSnapshot, 0, len(plan.Groups))}
	seenGroupApplications := make(map[string]struct{}, groupApplicationCount)
	seenApplications := make(map[string]struct{}, groupApplicationCount)

	sort.SliceStable(plan.Groups, func(i, j int) bool { return plan.Groups[i].SortOrder < plan.Groups[j].SortOrder })
	for groupIndex := range plan.Groups {
		group := &plan.Groups[groupIndex]
		if len(group.Applications) == 0 ||
			(group.Mode != model.ReleaseGroupParallel && group.Mode != model.ReleaseGroupSequential) ||
			(group.FailurePolicy != model.ReleaseGroupStopOnFailure && group.FailurePolicy != model.ReleaseGroupContinue) {
			return nil, ErrInvalidReleasePlanExecution
		}
		groupSnapshot := releasePlanExecutionGroupSnapshot{
			ID: group.ID, Mode: group.Mode, FailurePolicy: group.FailurePolicy, SortOrder: group.SortOrder,
			DependsOn: make([]string, 0, len(group.Dependencies)), ItemIDs: make([]string, 0, len(group.Applications)),
		}
		for i := range group.Dependencies {
			groupSnapshot.DependsOn = append(groupSnapshot.DependsOn, group.Dependencies[i].DependsOnGroupID)
		}
		sort.Strings(groupSnapshot.DependsOn)
		sort.SliceStable(group.Applications, func(i, j int) bool {
			return group.Applications[i].SortOrder < group.Applications[j].SortOrder
		})
		for applicationIndex := range group.Applications {
			groupApplication := &group.Applications[applicationIndex]
			selection, exists := selectionByApplication[groupApplication.ID]
			if !exists {
				return nil, ErrInvalidReleasePlanExecution
			}
			if _, duplicate := seenGroupApplications[groupApplication.ID]; duplicate {
				return nil, ErrInvalidReleasePlanExecution
			}
			seenGroupApplications[groupApplication.ID] = struct{}{}
			if _, duplicate := seenApplications[groupApplication.ApplicationID]; duplicate {
				return nil, ErrInvalidReleasePlanExecution
			}
			seenApplications[groupApplication.ApplicationID] = struct{}{}

			application, err := s.FindApplication(ctx, groupApplication.ApplicationID)
			if err != nil {
				if !errors.Is(err, ErrApplicationNotFound) {
					s.logReleasePlanExecutionError("release_plan_execution_preflight_application", groupApplication.ApplicationID, err)
				}
				return nil, ErrInvalidReleasePlanExecution
			}
			workflow, err := s.FindApplicationWorkflow(ctx, application.ID, selection.WorkflowID)
			if err != nil || !application.IsActive || !pipelineExecutionConfiguredForWorkflow(application, workflow) || !workflow.IsActive {
				return nil, ErrInvalidReleasePlanExecution
			}
			if workflow.Revision != selection.ExpectedWorkflowRevision {
				return nil, ErrReleasePlanExecutionWorkflowChanged
			}
			if issues := s.validateWorkflow(ctx, application, workflow.SchemaVersion, workflow.Source, workflow.Stages); len(issues) != 0 {
				return nil, ErrInvalidReleasePlanExecution
			}
			source := releasePlanManualSource(workflow, selection.SourceNodeID, selection.Ref)
			if source == nil {
				return nil, ErrInvalidReleasePlanExecution
			}
			ref, commitSHA, err := s.validateManualSelection(ctx, application, selection.Ref, selection.CommitSHA)
			if err != nil {
				if errors.Is(err, ErrManualCommitNotFound) || errors.Is(err, ErrManualCommitRequired) {
					return nil, ErrReleasePlanExecutionVersionChanged
				}
				s.logReleasePlanExecutionError("release_plan_execution_preflight_ref", groupApplication.ApplicationID, err)
				return nil, ErrReleasePlanExecutionTemporarilyFailed
			}

			itemID := uuid.NewString()
			run, err := s.newResolvedWorkflowRun(
				ctx,
				application, workflow, *source, "release_plan", ref, commitSHA,
				actorID, "发布计划等待编排启动", now,
			)
			if err != nil {
				s.logReleasePlanExecutionError("release_plan_execution_snapshot_workflow", groupApplication.ApplicationID, err)
				return nil, ErrReleasePlanExecutionTemporarilyFailed
			}
			run.ReleasePlanExecutionID = executionID
			run.ReleasePlanExecutionItemID = itemID
			run.Status = model.PipelineRunBlocked
			run.Stage = string(source.Type)
			run.CommitMessage = s.resolveCommitMessage(ctx, application.RepositoryID, ref, commitSHA)
			run.Message = "发布计划等待编排启动"
			components, err := pipelineRunRepositories(application, run.ID, ref, commitSHA, now)
			if err != nil {
				s.logReleasePlanExecutionError("release_plan_execution_repository_invariant", groupApplication.ApplicationID, err)
				return nil, ErrReleasePlanExecutionTemporarilyFailed
			}
			item := model.ReleasePlanExecutionItem{
				ID: itemID, ReleasePlanExecutionID: executionID,
				ReleaseGroupID: group.ID, ReleaseGroupApplicationID: groupApplication.ID,
				ApplicationID: application.ID, WorkflowID: workflow.ID, PipelineRunID: run.ID,
				Status: model.ReleasePlanExecutionItemPending, Ref: ref, CommitSHA: commitSHA,
				SourceNodeID: source.ID, SortOrder: groupApplication.SortOrder,
				Message: "等待所属发布组开始", CreatedAt: now, UpdatedAt: now,
			}
			prepared.items = append(prepared.items, preparedReleasePlanExecutionItem{
				item: item, run: *run, components: components,
				applicationUpdatedAt: application.UpdatedAt, workflowUpdatedAt: workflow.UpdatedAt,
			})
			groupSnapshot.ItemIDs = append(groupSnapshot.ItemIDs, itemID)
		}
		snapshot.Groups = append(snapshot.Groups, groupSnapshot)
	}
	if len(seenGroupApplications) != len(selectionByApplication) || !validReleasePlanExecutionDAG(snapshot) {
		return nil, ErrInvalidReleasePlanExecution
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		s.logReleasePlanExecutionError("release_plan_execution_snapshot_plan", planID, err)
		return nil, ErrReleasePlanExecutionTemporarilyFailed
	}
	prepared.execution.Snapshot = string(snapshotJSON)
	return prepared, nil
}

func releasePlanExecutionPlanSignature(plan *model.ReleasePlan) (string, error) {
	state := releasePlanExecutionPlanState{Groups: make([]releasePlanExecutionPlanGroupState, 0, len(plan.Groups))}
	for i := range plan.Groups {
		group := plan.Groups[i]
		groupState := releasePlanExecutionPlanGroupState{
			ID: group.ID, Mode: group.Mode, FailurePolicy: group.FailurePolicy, SortOrder: group.SortOrder,
			Applications: make([]releasePlanExecutionPlanApplicationState, 0, len(group.Applications)),
			Dependencies: make([]string, 0, len(group.Dependencies)),
		}
		for j := range group.Applications {
			application := group.Applications[j]
			groupState.Applications = append(groupState.Applications, releasePlanExecutionPlanApplicationState{
				ID: application.ID, ApplicationID: application.ApplicationID, SortOrder: application.SortOrder,
			})
		}
		for j := range group.Dependencies {
			groupState.Dependencies = append(groupState.Dependencies, group.Dependencies[j].DependsOnGroupID)
		}
		sort.Slice(groupState.Applications, func(i, j int) bool {
			if groupState.Applications[i].SortOrder == groupState.Applications[j].SortOrder {
				return groupState.Applications[i].ID < groupState.Applications[j].ID
			}
			return groupState.Applications[i].SortOrder < groupState.Applications[j].SortOrder
		})
		sort.Strings(groupState.Dependencies)
		state.Groups = append(state.Groups, groupState)
	}
	sort.Slice(state.Groups, func(i, j int) bool {
		if state.Groups[i].SortOrder == state.Groups[j].SortOrder {
			return state.Groups[i].ID < state.Groups[j].ID
		}
		return state.Groups[i].SortOrder < state.Groups[j].SortOrder
	})
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func releasePlanManualSource(workflow *model.ReleaseWorkflow, sourceNodeID, ref string) *model.WorkflowNode {
	if sourceNodeID == "" {
		return nil
	}
	return manualWorkflowSource(workflow, ref, sourceNodeID)
}

func validReleasePlanExecutionDAG(snapshot releasePlanExecutionSnapshot) bool {
	groups := make(map[string]releasePlanExecutionGroupSnapshot, len(snapshot.Groups))
	indegree := make(map[string]int, len(snapshot.Groups))
	outgoing := make(map[string][]string, len(snapshot.Groups))
	for i := range snapshot.Groups {
		group := snapshot.Groups[i]
		if group.ID == "" || len(group.ItemIDs) == 0 {
			return false
		}
		if _, exists := groups[group.ID]; exists {
			return false
		}
		groups[group.ID], indegree[group.ID] = group, 0
	}
	for i := range snapshot.Groups {
		group := snapshot.Groups[i]
		seen := make(map[string]struct{}, len(group.DependsOn))
		for _, dependencyID := range group.DependsOn {
			if dependencyID == group.ID {
				return false
			}
			if _, exists := groups[dependencyID]; !exists {
				return false
			}
			if _, duplicate := seen[dependencyID]; duplicate {
				return false
			}
			seen[dependencyID] = struct{}{}
			indegree[group.ID]++
			outgoing[dependencyID] = append(outgoing[dependencyID], group.ID)
		}
	}
	queue := make([]string, 0, len(groups))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, targetID := range outgoing[id] {
			indegree[targetID]--
			if indegree[targetID] == 0 {
				queue = append(queue, targetID)
			}
		}
	}
	return visited == len(groups)
}

func (s *Service) ReconcileReleasePlanExecution(ctx context.Context, executionID string) (*model.ReleasePlanExecution, error) {
	executionID = strings.TrimSpace(executionID)
	if executionID == "" {
		return nil, ErrReleasePlanExecutionNotFound
	}
	s.releasePlanExecutionMu.Lock()
	defer s.releasePlanExecutionMu.Unlock()

	execution, err := s.FindReleasePlanExecution(ctx, executionID)
	if err != nil {
		return nil, err
	}
	if execution.Status == model.ReleasePlanExecutionSucceeded || execution.Status == model.ReleasePlanExecutionFailed {
		return execution, nil
	}
	var snapshot releasePlanExecutionSnapshot
	if json.Unmarshal([]byte(execution.Snapshot), &snapshot) != nil || !validReleasePlanExecutionDAG(snapshot) {
		s.logReleasePlanExecutionError("release_plan_execution_reconcile_snapshot", executionID, errors.New("发布计划执行快照损坏"))
		return nil, ErrReleasePlanExecutionTemporarilyFailed
	}

	if err := s.syncReleasePlanExecutionItems(ctx, execution); err != nil {
		s.logReleasePlanExecutionError("release_plan_execution_reconcile_runs", executionID, err)
		return nil, ErrReleasePlanExecutionTemporarilyFailed
	}
	execution, err = s.FindReleasePlanExecution(ctx, executionID)
	if err != nil {
		return nil, err
	}
	itemByID := make(map[string]*model.ReleasePlanExecutionItem, len(execution.Items))
	for i := range execution.Items {
		itemByID[execution.Items[i].ID] = &execution.Items[i]
	}
	for i := range snapshot.Groups {
		for _, itemID := range snapshot.Groups[i].ItemIDs {
			if itemByID[itemID] == nil {
				s.logReleasePlanExecutionError("release_plan_execution_reconcile_item", executionID, errors.New("发布计划执行项缺失"))
				return nil, ErrReleasePlanExecutionTemporarilyFailed
			}
		}
	}

	blockedGroups, skips := releasePlanExecutionSkips(snapshot, itemByID)
	if len(skips) > 0 {
		now := time.Now().UTC()
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for itemID, message := range skips {
				item := itemByID[itemID]
				runResult := tx.Model(&model.PipelineRun{}).
					Where(
						"id = ? AND release_plan_execution_id = ? AND release_plan_execution_item_id = ? AND status = ?",
						item.PipelineRunID, execution.ID, item.ID, model.PipelineRunBlocked,
					).
					Updates(map[string]any{
						"status": model.PipelineRunCanceled, "stage": "canceled",
						"message": message, "updated_at": now,
					})
				if runResult.Error != nil {
					return runResult.Error
				}
				if runResult.RowsAffected != 1 {
					return ErrInvalidWorkflowTransition
				}
				result := tx.Model(&model.ReleasePlanExecutionItem{}).
					Where("id = ? AND status = ?", itemID, model.ReleasePlanExecutionItemPending).
					Updates(map[string]any{
						"status": model.ReleasePlanExecutionItemSkipped, "message": message,
						"finished_at": now, "updated_at": now,
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrInvalidWorkflowTransition
				}
			}
			return nil
		})
		if err != nil {
			s.logReleasePlanExecutionError("release_plan_execution_reconcile_skip", executionID, err)
			return nil, ErrReleasePlanExecutionTemporarilyFailed
		}
		for itemID, message := range skips {
			item := itemByID[itemID]
			item.Status, item.Message, item.FinishedAt, item.UpdatedAt = model.ReleasePlanExecutionItemSkipped, message, &now, now
		}
		blockedGroups, _ = releasePlanExecutionSkips(snapshot, itemByID)
	}

	if releasePlanExecutionAllTerminal(itemByID) {
		status := model.ReleasePlanExecutionSucceeded
		for _, item := range itemByID {
			if item.Status != model.ReleasePlanExecutionItemSucceeded {
				status = model.ReleasePlanExecutionFailed
				break
			}
		}
		now := time.Now().UTC()
		updates := map[string]any{"status": status, "finished_at": now, "updated_at": now}
		if execution.StartedAt == nil {
			updates["started_at"] = now
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.ReleasePlanExecution{}).Where("id = ? AND status IN ?", execution.ID, []model.ReleasePlanExecutionStatus{
				model.ReleasePlanExecutionPending, model.ReleasePlanExecutionRunning,
			}).Updates(updates).Error; err != nil {
				return err
			}
			return tx.Model(&model.ReleasePlan{}).
				Where("id = ? AND status = ?", execution.ReleasePlanID, model.ReleasePlanActive).
				Updates(map[string]any{"status": model.ReleasePlanCompleted, "updated_at": now}).Error
		}); err != nil {
			s.logReleasePlanExecutionError("release_plan_execution_reconcile_complete", executionID, err)
			return nil, ErrReleasePlanExecutionTemporarilyFailed
		}
		return s.FindReleasePlanExecution(ctx, executionID)
	}

	startItemIDs := releasePlanExecutionStartItems(snapshot, itemByID, blockedGroups)
	if len(startItemIDs) == 0 {
		return s.FindReleasePlanExecution(ctx, executionID)
	}
	now := time.Now().UTC()
	claimed := make([]model.ReleasePlanExecutionItem, 0, len(startItemIDs))
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, itemID := range startItemIDs {
			result := tx.Model(&model.ReleasePlanExecutionItem{}).
				Where("id = ? AND release_plan_execution_id = ? AND status = ?", itemID, execution.ID, model.ReleasePlanExecutionItemPending).
				Updates(map[string]any{
					"status": model.ReleasePlanExecutionItemRunning, "message": "流水线正在执行",
					"started_at": now, "finished_at": nil, "updated_at": now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				item := *itemByID[itemID]
				item.Status, item.Message, item.StartedAt, item.FinishedAt = model.ReleasePlanExecutionItemRunning, "流水线正在执行", &now, nil
				claimed = append(claimed, item)
			}
		}
		if len(claimed) == 0 {
			return nil
		}
		updates := map[string]any{"status": model.ReleasePlanExecutionRunning, "updated_at": now}
		if execution.StartedAt == nil {
			updates["started_at"] = now
		}
		return tx.Model(&model.ReleasePlanExecution{}).Where("id = ?", execution.ID).Updates(updates).Error
	})
	if err != nil {
		s.logReleasePlanExecutionError("release_plan_execution_reconcile_claim", executionID, err)
		return nil, ErrReleasePlanExecutionTemporarilyFailed
	}
	for i := range claimed {
		if _, err := s.startReleasePlanPipelineRun(ctx, claimed[i].PipelineRunID, execution.CreatedBy); err != nil {
			s.logReleasePlanExecutionError("release_plan_execution_start_run", claimed[i].PipelineRunID, err)
			if releasePlanExecutionPermanentAdvanceError(err) {
				_ = s.failCurrentExecution(ctx, claimed[i].PipelineRunID, "发布计划启动流水线失败", err)
			}
		}
	}
	return s.FindReleasePlanExecution(ctx, executionID)
}

func (s *Service) syncReleasePlanExecutionItems(ctx context.Context, execution *model.ReleasePlanExecution) error {
	if len(execution.Items) == 0 {
		return errors.New("发布计划执行没有应用")
	}
	runIDs := make([]string, 0, len(execution.Items))
	for i := range execution.Items {
		runIDs = append(runIDs, execution.Items[i].PipelineRunID)
	}
	var runs []model.PipelineRun
	if err := s.db.WithContext(ctx).Where("id IN ?", runIDs).Find(&runs).Error; err != nil {
		return err
	}
	runByID := make(map[string]model.PipelineRun, len(runs))
	for i := range runs {
		runByID[runs[i].ID] = runs[i]
	}
	if len(runByID) != len(execution.Items) {
		return errors.New("发布计划关联的流水线运行缺失")
	}
	now := time.Now().UTC()
	automaticRuns := make([]model.PipelineRun, 0)
	for i := range execution.Items {
		item := &execution.Items[i]
		if releasePlanExecutionItemTerminal(item.Status) {
			continue
		}
		run := runByID[item.PipelineRunID]
		status, message := item.Status, item.Message
		var finishedAt *time.Time
		switch run.Status {
		case model.PipelineRunSucceeded:
			status, message, finishedAt = model.ReleasePlanExecutionItemSucceeded, "流水线执行成功", &now
		case model.PipelineRunFailed:
			status, message, finishedAt = model.ReleasePlanExecutionItemFailed, "流水线执行失败", &now
		case model.PipelineRunCanceled:
			status, message, finishedAt = model.ReleasePlanExecutionItemCanceled, "流水线已取消", &now
		case model.PipelineRunBlocked:
			if item.Status == model.ReleasePlanExecutionItemRunning {
				status, message = model.ReleasePlanExecutionItemPending, "等待恢复启动"
			}
		default:
			if item.Status == model.ReleasePlanExecutionItemPending {
				status, message = model.ReleasePlanExecutionItemRunning, "流水线正在执行"
			}
		}
		if releasePlanRunNeedsAutomaticAdvance(&run) {
			automaticRuns = append(automaticRuns, run)
		}
		if status == item.Status && message == item.Message {
			continue
		}
		updates := map[string]any{"status": status, "message": message, "updated_at": now}
		if finishedAt != nil {
			updates["finished_at"] = finishedAt
		}
		if status == model.ReleasePlanExecutionItemPending {
			updates["started_at"] = nil
			updates["finished_at"] = nil
		}
		if err := s.db.WithContext(ctx).Model(&model.ReleasePlanExecutionItem{}).
			Where("id = ? AND status = ?", item.ID, item.Status).Updates(updates).Error; err != nil {
			return err
		}
	}
	for i := range automaticRuns {
		if _, _, err := s.advanceRunIfCurrent(ctx, automaticRuns[i], execution.CreatedBy, ""); err != nil {
			if releasePlanExecutionPermanentAdvanceError(err) {
				_ = s.failExecution(ctx, failureStateForRun(automaticRuns[i]), "流水线继续执行失败", err)
			}
		}
	}
	return nil
}

func releasePlanExecutionSkips(
	snapshot releasePlanExecutionSnapshot,
	items map[string]*model.ReleasePlanExecutionItem,
) (map[string]bool, map[string]string) {
	blocked := make(map[string]bool, len(snapshot.Groups))
	skips := make(map[string]string)
	changed := true
	for changed {
		changed = false
		for i := range snapshot.Groups {
			group := snapshot.Groups[i]
			ownFailure := false
			for _, itemID := range group.ItemIDs {
				status := items[itemID].Status
				if status == model.ReleasePlanExecutionItemCanceled || status == model.ReleasePlanExecutionItemSkipped ||
					(group.FailurePolicy == model.ReleaseGroupStopOnFailure && status == model.ReleasePlanExecutionItemFailed) {
					ownFailure = true
				}
			}
			dependencyFailure := false
			for _, dependencyID := range group.DependsOn {
				if blocked[dependencyID] {
					dependencyFailure = true
					break
				}
			}
			if !ownFailure && !dependencyFailure {
				continue
			}
			if !blocked[group.ID] {
				blocked[group.ID], changed = true, true
			}
			message := "同组应用失败，已按停止策略跳过"
			if dependencyFailure {
				message = "依赖发布组失败，已跳过"
			}
			for _, itemID := range group.ItemIDs {
				if items[itemID].Status == model.ReleasePlanExecutionItemPending {
					skips[itemID] = message
					items[itemID].Status = model.ReleasePlanExecutionItemSkipped
					changed = true
				}
			}
		}
	}
	// 上面的传播需要临时改变内存状态；数据库尚未更新时恢复为 pending，
	// 由调用方在事务成功后再写入最终状态。
	for itemID := range skips {
		items[itemID].Status = model.ReleasePlanExecutionItemPending
	}
	return blocked, skips
}

func releasePlanExecutionStartItems(
	snapshot releasePlanExecutionSnapshot,
	items map[string]*model.ReleasePlanExecutionItem,
	blockedGroups map[string]bool,
) []string {
	groups := make(map[string]releasePlanExecutionGroupSnapshot, len(snapshot.Groups))
	for i := range snapshot.Groups {
		groups[snapshot.Groups[i].ID] = snapshot.Groups[i]
	}
	result := make([]string, 0)
	for i := range snapshot.Groups {
		group := snapshot.Groups[i]
		if blockedGroups[group.ID] {
			continue
		}
		ready := true
		for _, dependencyID := range group.DependsOn {
			dependency := groups[dependencyID]
			if blockedGroups[dependencyID] || !releasePlanExecutionGroupTerminal(dependency, items) {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		if group.Mode == model.ReleaseGroupParallel {
			for _, itemID := range group.ItemIDs {
				if items[itemID].Status == model.ReleasePlanExecutionItemPending {
					result = append(result, itemID)
				}
			}
			continue
		}
		running := false
		for _, itemID := range group.ItemIDs {
			if items[itemID].Status == model.ReleasePlanExecutionItemRunning {
				running = true
				break
			}
		}
		if running {
			continue
		}
		for _, itemID := range group.ItemIDs {
			if items[itemID].Status == model.ReleasePlanExecutionItemPending {
				result = append(result, itemID)
				break
			}
		}
	}
	return result
}

func releasePlanExecutionGroupTerminal(
	group releasePlanExecutionGroupSnapshot,
	items map[string]*model.ReleasePlanExecutionItem,
) bool {
	for _, itemID := range group.ItemIDs {
		if !releasePlanExecutionItemTerminal(items[itemID].Status) {
			return false
		}
	}
	return true
}

func releasePlanExecutionAllTerminal(items map[string]*model.ReleasePlanExecutionItem) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if !releasePlanExecutionItemTerminal(item.Status) {
			return false
		}
	}
	return true
}

func releasePlanExecutionItemTerminal(status model.ReleasePlanExecutionItemStatus) bool {
	return status == model.ReleasePlanExecutionItemSucceeded || status == model.ReleasePlanExecutionItemFailed ||
		status == model.ReleasePlanExecutionItemSkipped || status == model.ReleasePlanExecutionItemCanceled
}

// startReleasePlanPipelineRun 只读取 PipelineRun.WorkflowSnapshot，因此计划创建后修改当前流水线
// 不会改变本次执行路径。执行从唯一且已启用手动启动的代码源进入第一个任务。
func (s *Service) startReleasePlanPipelineRun(ctx context.Context, runID, actorID string) (*model.PipelineRun, error) {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		return nil, err
	}
	snapshot, err := parseWorkflowSnapshot(&run)
	if err != nil {
		return nil, err
	}
	current, exists := workflowFindNode(snapshot.Source, snapshot.Stages, run.CurrentNodeID)
	if !exists || !workflowNodeSupportsManualRelease(current) || run.Status != model.PipelineRunBlocked {
		return nil, ErrInvalidWorkflowTransition
	}
	advanced, _, err := s.advanceRunIfCurrent(ctx, run, actorID, "")
	return advanced, err
}

func releasePlanRunNeedsAutomaticAdvance(run *model.PipelineRun) bool {
	if run.Status == model.PipelineRunReady && run.Stage == "deploy_succeeded" {
		return true
	}
	if run.Status != model.PipelineRunRunning || run.Stage != string(model.WorkflowNodeTrigger) {
		return false
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		return false
	}
	return snapshot.Source.ID == run.CurrentNodeID && snapshot.Source.Type == model.WorkflowNodeTrigger
}

func releasePlanExecutionPermanentAdvanceError(err error) bool {
	return errors.Is(err, ErrInvalidWorkflowTransition) || errors.Is(err, ErrPipelineExecutionConfig)
}

func (s *Service) ReconcileReleasePlanExecutions(ctx context.Context) error {
	var executionIDs []string
	if err := s.db.WithContext(ctx).Model(&model.ReleasePlanExecution{}).
		Where("status IN ?", []model.ReleasePlanExecutionStatus{
			model.ReleasePlanExecutionPending, model.ReleasePlanExecutionRunning,
		}).Order("created_at ASC").Pluck("id", &executionIDs).Error; err != nil {
		s.logReleasePlanExecutionError("release_plan_execution_reconcile_list", "", err)
		return ErrReleasePlanExecutionTemporarilyFailed
	}
	var result error
	for _, executionID := range executionIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := s.ReconcileReleasePlanExecution(ctx, executionID); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Service) RunReleasePlanExecutionReconciler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	reconcile := func() {
		if err := s.ReconcileReleasePlanExecutions(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.logReleasePlanExecutionError("release_plan_execution_reconcile", "", err)
		}
	}
	reconcile()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func (s *Service) findReleasePlanExecutionByPlan(ctx context.Context, planID string) (*model.ReleasePlanExecution, error) {
	var execution model.ReleasePlanExecution
	if err := s.db.WithContext(ctx).First(&execution, "release_plan_id = ?", planID).Error; err != nil {
		return nil, err
	}
	return &execution, nil
}

func (s *Service) logReleasePlanExecutionError(operation, id string, err error) {
	if s.logger == nil || err == nil {
		return
	}
	s.logger.Error("发布计划执行失败", "operation", operation, "resource_id", id, "err", err)
}

func isReleasePlanExecutionPublicError(err error) bool {
	return errors.Is(err, ErrReleasePlanNotFound) || errors.Is(err, ErrReleasePlanDisabled) || errors.Is(err, ErrInvalidReleasePlanExecution) ||
		errors.Is(err, ErrReleasePlanExecutionExists) || errors.Is(err, ErrReleasePlanExecutionPlanChanged) ||
		errors.Is(err, ErrReleasePlanExecutionWorkflowChanged) || errors.Is(err, ErrReleasePlanExecutionVersionChanged)
}
