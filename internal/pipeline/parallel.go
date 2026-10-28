package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
	"edo/internal/task"
)

func nodeFinished(status model.PipelineRunStatus) bool {
	return status == model.PipelineRunSucceeded || status == model.PipelineRunSkipped || status == model.PipelineRunFailed || status == model.PipelineRunCanceled
}

// 同阶段的执行任务构成一个并行批次，审核和人工放行是批次间的屏障。
func executionBatches(stages []model.WorkflowStage) [][]model.WorkflowNode {
	var batches [][]model.WorkflowNode
	for _, stage := range stages {
		var batch []model.WorkflowNode
		flush := func() {
			if len(batch) > 0 {
				batches = append(batches, batch)
				batch = nil
			}
		}
		for _, node := range stage.Tasks {
			if node.Type == model.WorkflowNodeApproval || node.Type == model.WorkflowNodeManual || node.Config.Migration != nil {
				flush()
				batches = append(batches, []model.WorkflowNode{node})
			} else {
				if node.Type == model.WorkflowNodeDeploy {
					for _, before := range batch {
						if before.Type == model.WorkflowNodeBuild {
							flush()
							break
						}
					}
				}
				batch = append(batch, node)
			}
		}
		flush()
	}
	return batches
}

func (s *Service) advanceParallelRun(ctx context.Context, run *model.PipelineRun, snapshot *workflowSnapshot, actorID, targetNodeID string) (*model.PipelineRun, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "uuid = ?", run.ID).Error; err != nil {
			return err
		}
		if current.Stage == stoppingStage || nodeFinished(current.Status) {
			return ErrInvalidWorkflowTransition
		}
		var records []model.PipelineNodeRun
		if err := tx.Where("pipeline_run_id = ?", run.ID).Order("id ASC").Find(&records).Error; err != nil {
			return err
		}
		states := map[string]model.PipelineNodeRun{}
		for _, record := range records {
			states[record.NodeID] = record
		}
		now := time.Now().UTC()
		// 明确选用已有制品时，被服务端跳过的构建与检查不重新执行。
		if len(records) == 0 && current.ArtifactID != "" && current.CurrentNodeID != snapshot.Source.ID {
			var selected model.Artifact
			var producer model.BuildRun
			if err := tx.First(&selected, "uuid = ? AND application_id = ?", current.ArtifactID, current.ApplicationID).Error; err != nil {
				return err
			}
			if err := tx.First(&producer, "uuid = ? AND application_id = ?", selected.BuildRunID, current.ApplicationID).Error; err != nil {
				return err
			}
			for _, node := range workflowTasks(snapshot.Stages) {
				// 已有制品只能省去构建和检查，不能绕过审核、放行或数据库变更。
				if (node.Type != model.WorkflowNodeBuild && node.Type != model.WorkflowNodeShell) || node.Config.Migration != nil {
					if node.ID == current.CurrentNodeID {
						break
					}
					continue
				}
				record := newNodeRecord(current, node, snapshot, now)
				record.Status = model.PipelineRunSkipped
				record.Stage = "skipped"
				record.FinishedAt = &now
				record.Message = "本次使用已选择的制品，跳过重新构建"
				if node.Type == model.WorkflowNodeBuild && node.Config.BuildPlanID == producer.BuildPlanID && (producer.WorkflowNodeID == "" || producer.WorkflowNodeID == node.ID) {
					record.ArtifactID = selected.ID
				}
				if err := tx.Create(&record).Error; err != nil {
					return err
				}
				states[node.ID] = record
				if node.ID == current.CurrentNodeID {
					break
				}
			}
		}
		for _, batch := range executionBatches(snapshot.Stages) {
			complete := true
			for _, node := range batch {
				state, exists := states[node.ID]
				if !exists || !nodeFinished(state.Status) {
					complete = false
					break
				}
				if state.Status == model.PipelineRunFailed || state.Status == model.PipelineRunCanceled {
					return ErrInvalidWorkflowTransition
				}
			}
			if complete {
				continue
			}
			first := batch[0]
			if first.Type == model.WorkflowNodeManual || first.Type == model.WorkflowNodeApproval {
				state, exists := states[first.ID]
				if exists {
					if first.Type == model.WorkflowNodeApproval {
						var count int64
						if err := tx.Model(&model.PipelineRunApproval{}).Where("pipeline_run_id = ? AND node_id = ?", run.ID, first.ID).Count(&count).Error; err != nil {
							return err
						}
						minimum := int64(1)
						if policy, ok := snapshot.ReleasePolicies[first.ID]; ok && policy.MinimumApprovals > 1 {
							minimum = int64(policy.MinimumApprovals)
						}
						if count < minimum {
							return ErrWorkflowApprovalRequired
						}
					} else if actorID == "" {
						return ErrInvalidWorkflowTransition
					}
					updates := map[string]any{"status": model.PipelineRunSucceeded, "stage": "completed", "finished_at": now, "updated_at": now}
					if first.Type == model.WorkflowNodeApproval {
						updates["message"] = "审核已通过"
					}
					if err := tx.Model(&model.PipelineNodeRun{}).Where("uuid = ? AND status = ?", state.ID, state.Status).Updates(updates).Error; err != nil {
						return err
					}
					continue
				}
				state = newNodeRecord(current, first, snapshot, now)
				state.Status, state.Stage = model.PipelineRunBlocked, "manual"
				state.Message = "等待人工放行"
				if first.Type == model.WorkflowNodeApproval {
					state.Status, state.Stage, state.Message = model.PipelineRunAwaitingApproval, "approval", "等待其他成员审核"
				}
				if err := tx.Create(&state).Error; err != nil {
					return err
				}
				current.CurrentNodeID, current.ExecutionJobID = first.ID, ""
				current.Status, current.Stage, current.Message = state.Status, state.Stage, state.Message
				if first.Type == model.WorkflowNodeManual {
					current.Status = model.PipelineRunRunning
				}
				return saveParallelRun(tx, &current, run, now)
			}
			if targetNodeID != "" && targetNodeID != first.ID {
				return ErrInvalidWorkflowTransition
			}
			active := make([]model.PipelineNodeRun, 0, len(batch))
			for _, node := range batch {
				state, exists := states[node.ID]
				if exists {
					if !nodeFinished(state.Status) {
						active = append(active, state)
					}
					continue
				}
				state = newNodeRecord(current, node, snapshot, now)
				if node.Type == model.WorkflowNodeDeploy {
					artifactID, err := parallelUpstreamArtifact(snapshot, node, states, current.ArtifactID)
					if err != nil {
						return err
					}
					state.ArtifactID = artifactID
				}
				kind, subject := "pipeline.build", "edo.task.pipeline.build"
				var payload any = BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}
				attempts, idempotent := 1, false
				if node.Type == model.WorkflowNodeBuild && snapshot.BuildPlans[node.ID].Kind == model.BuildPlanDockerfile {
					attempts, idempotent = 4, true
				}
				if node.Type == model.WorkflowNodeDeploy {
					kind, subject = "pipeline.deploy", "edo.task.pipeline.deploy"
					payload = DeployTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}
				}
				job, err := task.NewService(tx, 4).Create(ctx, task.CreateInput{ProjectID: current.ProjectID, DepartmentID: current.DepartmentID, Kind: kind, Subject: subject, Payload: payload, IdempotencyKey: "pipeline-node:" + run.ID + ":" + node.ID, MaxAttempts: attempts, Idempotent: idempotent})
				if err != nil {
					return err
				}
				state.JobID = job.ID
				if err = tx.Create(&state).Error; err != nil {
					return err
				}
				active = append(active, state)
			}
			if len(active) == 0 {
				return ErrInvalidWorkflowTransition
			}
			current.CurrentNodeID, current.ExecutionJobID = active[0].NodeID, active[0].JobID
			current.Status, current.Stage = model.PipelineRunRunning, active[0].Stage
			current.Message = "当前任务：" + active[0].Name + "；" + active[0].Message
			if len(active) > 1 {
				current.Stage = "parallel"
				current.Message = fmt.Sprintf("当前阶段有 %d 个任务正在执行", len(active))
			}
			return saveParallelRun(tx, &current, run, now)
		}
		current.Status, current.Stage, current.Message = model.PipelineRunSucceeded, "completed", "流水线任务已全部完成"
		if err := tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", run.ID).Updates(map[string]any{"status": model.PipelineRunRepositorySucceeded, "updated_at": now}).Error; err != nil {
			return err
		}
		return saveParallelRun(tx, &current, run, now)
	})
	if err != nil {
		s.logger.Error("推进流水线阶段失败", "operation", "pipeline_parallel_advance", "pipeline_run_id", run.ID, "err", err)
		return nil, err
	}
	s.realtime.Changed(ctx, "pipeline_runs", run.ID)
	return run, nil
}

func newNodeRecord(run model.PipelineRun, node model.WorkflowNode, snapshot *workflowSnapshot, now time.Time) model.PipelineNodeRun {
	stage, _ := workflowStageForTask(snapshot.Stages, node.ID)
	return model.PipelineNodeRun{ID: uuid.NewString(), ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, PipelineRunID: run.ID, NodeID: node.ID, StageID: stage.ID, Name: node.Name, Type: node.Type, Status: model.PipelineRunRunning, Stage: "queued", Message: "等待执行", CreatedAt: now, UpdatedAt: now}
}

func saveParallelRun(tx *gorm.DB, current, out *model.PipelineRun, now time.Time) error {
	result := tx.Model(&model.PipelineRun{}).Where("uuid = ? AND status NOT IN ? AND stage <> ?", current.ID, []model.PipelineRunStatus{model.PipelineRunFailed, model.PipelineRunCanceled, model.PipelineRunSucceeded}, stoppingStage).
		Updates(map[string]any{"current_node_id": current.CurrentNodeID, "execution_job_id": current.ExecutionJobID, "status": current.Status, "stage": current.Stage, "message": current.Message, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInvalidWorkflowTransition
	}
	current.UpdatedAt = now
	*out = *current
	return nil
}

func parallelUpstreamArtifact(snapshot *workflowSnapshot, node model.WorkflowNode, states map[string]model.PipelineNodeRun, fallback string) (string, error) {
	if node.Config.ArtifactNodeID != "" {
		state, ok := states[node.Config.ArtifactNodeID]
		if !ok || (state.Status != model.PipelineRunSucceeded && state.Status != model.PipelineRunSkipped) || state.Type != model.WorkflowNodeBuild || state.ArtifactID == "" {
			return "", ErrPipelineExecutionConfig
		}
		return state.ArtifactID, nil
	}
	sources, err := artifactSources(snapshot.Stages)
	if err != nil {
		return "", err
	}
	if source := sources[node.ID]; source != "" {
		if state, ok := states[source]; ok && state.Status == model.PipelineRunSucceeded && state.ArtifactID != "" {
			return state.ArtifactID, nil
		}
	}

	if fallback != "" {
		return fallback, nil
	}
	return "", ErrPipelineExecutionConfig
}

func (s *Service) loadNodeExecution(ctx context.Context, runID, jobID, nodeID string) (*model.PipelineRun, bool, error) {
	var node model.PipelineNodeRun
	err := s.db.WithContext(ctx).Where("pipeline_run_id = ? AND job_id = ? AND node_id = ?", runID, jobID, nodeID).First(&node).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	var run model.PipelineRun
	if err = s.db.WithContext(ctx).First(&run, "uuid = ?", runID).Error; err != nil {
		return nil, true, err
	}
	if nodeFinished(node.Status) {
		return nil, true, errPipelineExecutionComplete
	}
	if run.Status != model.PipelineRunRunning || run.Stage == stoppingStage {
		return nil, true, ErrInvalidWorkflowTransition
	}
	run.NodeExecution = true
	run.CurrentNodeID = node.NodeID
	run.ExecutionJobID = node.JobID
	run.Status = node.Status
	run.Stage = node.Stage
	if node.ArtifactID != "" {
		run.ArtifactID = node.ArtifactID
	}
	run.UpdatedAt = node.UpdatedAt
	return &run, true, nil
}

func (s *Service) updateNodePhase(ctx context.Context, run *model.PipelineRun, stage, message string) error {
	var parent model.PipelineRun
	if err := s.db.WithContext(ctx).Select("uuid", "status", "stage").First(&parent, "uuid = ?", run.ID).Error; err != nil {
		return err
	}
	if parent.Status != model.PipelineRunRunning || parent.Stage == stoppingStage {
		return ErrInvalidWorkflowTransition
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ? AND node_id = ? AND job_id = ? AND status = ?", run.ID, run.CurrentNodeID, run.ExecutionJobID, model.PipelineRunRunning).
		Updates(map[string]any{"stage": stage, "message": message, "started_at": gorm.Expr("COALESCE(started_at, ?)", now), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInvalidWorkflowTransition
	}
	if err := s.db.WithContext(ctx).Model(&model.PipelineRun{}).Where("uuid = ? AND status = ? AND stage <> ?", run.ID, model.PipelineRunRunning, stoppingStage).Update("updated_at", now).Error; err != nil {
		return err
	}
	s.realtime.Changed(ctx, "pipeline_runs", run.ID)
	run.Stage, run.Message, run.UpdatedAt = stage, message, now
	s.appendRunLog(ctx, run.ID, stage, "info", message)
	return nil
}

func (s *Service) completeNodeExecution(ctx context.Context, run model.PipelineRun, node model.WorkflowNode, produced *model.Artifact, record *model.DeploymentRecord) error {
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var parent model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "uuid = ?", run.ID).Error; err != nil {
			return err
		}
		if parent.Status != model.PipelineRunRunning || parent.Stage == stoppingStage {
			return ErrInvalidWorkflowTransition
		}
		updates := map[string]any{"status": model.PipelineRunSucceeded, "stage": "completed", "message": "任务已完成", "finished_at": now, "updated_at": now}
		rootUpdates := map[string]any{"updated_at": now}
		if produced != nil {
			updates["artifact_id"] = produced.ID
			rootUpdates["artifact_id"] = produced.ID
			if produced.ImageRef != "" {
				rootUpdates["image"] = produced.ImageRef
			}
		}
		if record != nil {
			updates["deployment_id"] = record.ID
			rootUpdates["deployment_id"] = record.ID
			rootUpdates["image"] = record.Image
		}
		result := tx.Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ? AND node_id = ? AND job_id = ? AND status = ?", run.ID, node.ID, run.ExecutionJobID, model.PipelineRunRunning).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		return tx.Model(&parent).Updates(rootUpdates).Error
	})
	if err != nil {
		return err
	}
	s.appendRunLog(ctx, run.ID, "completed", "success", "任务“"+node.Name+"”已完成")
	s.notifyWorkflowNode(ctx, &run, node, workflowNotificationSucceeded, "任务已完成")
	return s.resumeNodeAdvance(ctx, run.ID)
}

func (s *Service) resumeNodeAdvance(ctx context.Context, runID string) error {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "uuid = ?", runID).Error; err != nil {
		return err
	}
	if nodeFinished(run.Status) || run.Stage == stoppingStage {
		return nil
	}
	advanceContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := s.AdvanceRun(advanceContext, runID, run.CreatedBy, "")
	return err
}

func artifactSources(stages []model.WorkflowStage) (map[string]string, error) {
	sources := map[string]string{}
	available := map[string]bool{}
	var latest []string
	for _, batch := range executionBatches(stages) {
		for _, node := range batch {
			if node.Type != model.WorkflowNodeDeploy {
				continue
			}
			source := node.Config.ArtifactNodeID
			if source == "" {
				if len(latest) != 1 {
					return nil, ErrPipelineExecutionConfig
				}
				source = latest[0]
			}
			if !available[source] {
				return nil, ErrPipelineExecutionConfig
			}
			sources[node.ID] = source
		}
		var built []string
		for _, node := range batch {
			if node.Type == model.WorkflowNodeBuild {
				available[node.ID] = true
				built = append(built, node.ID)
			}
		}
		if len(built) > 0 {
			latest = built
		}
	}
	return sources, nil
}
