package pipeline

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/model"
)

func (s *Service) hasNodeJob(ctx context.Context, runID, jobID string) bool {
	if runID == "" || jobID == "" {
		return false
	}
	identityCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var count int64
	err := s.db.WithContext(identityCtx).Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ? AND job_id = ?", runID, jobID).Count(&count).Error
	if err != nil {
		s.logger.Error("检查任务执行记录失败", "operation", "pipeline_node_identity", "pipeline_run_id", runID, "job_id", jobID, "err", err)
	}
	return err != nil || count == 1
}

func (s *Service) finishNodeFailure(ctx context.Context, expected executionFailureState, allowRetry bool, message string, cause error) error {
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	s.logger.Error("流水线任务执行失败", "operation", "pipeline_node_failure", "pipeline_run_id", expected.runID, "node_id", expected.nodeID, "job_id", expected.jobID, "err", cause)
	var live model.PipelineRun
	stateErr := s.db.WithContext(updateCtx).Select("uuid", "status", "stage").First(&live, "uuid = ?", expected.runID).Error
	if stateErr != nil {
		s.logger.Error("读取失败任务所属流水线失败", "operation", "pipeline_node_failure_parent", "run_id", expected.runID, "err", stateErr)
		return ErrPipelineExecutionState
	}
	if allowRetry && live.Status == model.PipelineRunRunning && live.Stage != stoppingStage && transientBuildError(cause) {
		var job model.Job
		if err := s.db.WithContext(updateCtx).First(&job, "uuid = ?", expected.jobID).Error; err == nil && job.Attempt < job.MaxAttempts {
			result := s.db.WithContext(updateCtx).Model(&model.PipelineNodeRun{}).
				Where("pipeline_run_id = ? AND node_id = ? AND job_id = ? AND status = ?", expected.runID, expected.nodeID, expected.jobID, model.PipelineRunRunning).
				Updates(map[string]any{"stage": "retrying", "message": message + "；系统将自动重试", "updated_at": time.Now().UTC()})
			if result.Error == nil && result.RowsAffected == 1 {
				return &buildTaskExecutionError{cause: cause, retryable: true}
			}
		}
	}
	now := time.Now().UTC()
	err := s.db.WithContext(updateCtx).Transaction(func(tx *gorm.DB) error {
		var parent model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "uuid = ?", expected.runID).Error; err != nil {
			return err
		}
		if parent.Stage == stoppingStage || nodeFinished(parent.Status) {
			return errExecutionFailureStateChanged
		}
		result := tx.Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ? AND node_id = ? AND job_id = ? AND status = ?", expected.runID, expected.nodeID, expected.jobID, model.PipelineRunRunning).
			Updates(map[string]any{"status": model.PipelineRunFailed, "stage": "failed", "message": message, "finished_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errExecutionFailureStateChanged
		}
		if err := tx.Model(&parent).Updates(map[string]any{"status": model.PipelineRunFailed, "stage": "failed", "current_node_id": expected.nodeID, "execution_job_id": expected.jobID, "message": message, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", parent.ID).Updates(map[string]any{"status": model.PipelineRunRepositoryFailed, "updated_at": now}).Error; err != nil {
			return err
		}
		return cancelPendingNodeJobs(tx, parent.ID, now)
	})
	if errors.Is(err, errExecutionFailureStateChanged) {
		return &buildTaskExecutionError{cause: cause, retryable: false}
	}
	if err != nil {
		s.logger.Error("记录并行任务失败结果失败", "operation", "pipeline_node_failure_update", "pipeline_run_id", expected.runID, "err", err)
		return ErrPipelineExecutionState
	}
	s.appendRunLog(updateCtx, expected.runID, "failed", "error", message)
	s.notifyFailedWorkflowNode(updateCtx, expected.runID, expected.nodeID, message)
	s.realtime.Changed(updateCtx, "pipeline_runs", expected.runID)
	return &buildTaskExecutionError{cause: cause, retryable: false}
}

func cancelPendingNodeJobs(tx *gorm.DB, runID string, now time.Time) error {
	var nodes []model.PipelineNodeRun
	if err := tx.Where("pipeline_run_id = ? AND status IN ?", runID, []model.PipelineRunStatus{model.PipelineRunRunning, model.PipelineRunBlocked, model.PipelineRunAwaitingApproval}).Find(&nodes).Error; err != nil {
		return err
	}
	for _, node := range nodes {
		if node.JobID != "" {
			result := tx.Model(&model.Job{}).Where("uuid = ? AND status = ?", node.JobID, model.JobPending).
				Updates(map[string]any{"status": model.JobCanceled, "error_code": "canceled_by_pipeline", "error_message": "流水线已停止推进", "finished_at": now, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
		}
		if err := tx.Model(&model.PipelineNodeRun{}).Where("uuid = ? AND status = ?", node.ID, node.Status).
			Updates(map[string]any{"status": model.PipelineRunCanceled, "stage": "canceled", "message": "流水线已停止推进", "finished_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

// 调用方已持有 pipelineAdvanceMu；不等待执行器，以免执行器清理产生死锁。
func (s *Service) stopParallelRun(ctx context.Context, runID, actorID string) (*model.PipelineRun, error) {
	var run model.PipelineRun
	var jobIDs []string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, "uuid = ?", runID).Error; err != nil {
			return err
		}
		if run.Stage == stoppingStage || run.Status == model.PipelineRunCanceled {
			return nil
		}
		if nodeFinished(run.Status) {
			return ErrPipelineStopState
		}
		now := time.Now().UTC()
		if err := cancelPendingNodeJobs(tx, runID, now); err != nil {
			return err
		}
		var nodes []model.PipelineNodeRun
		if err := tx.Where("pipeline_run_id = ? AND status = ?", runID, model.PipelineRunRunning).Find(&nodes).Error; err != nil {
			return err
		}
		active := false
		for _, node := range nodes {
			jobIDs = append(jobIDs, node.JobID)
			var count int64
			if err := tx.Model(&model.Job{}).Where("uuid = ? AND status = ?", node.JobID, model.JobRunning).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 || s.runningExecutions[node.JobID] != nil {
				active = true
			}
		}
		run.Status, run.Stage, run.Message = model.PipelineRunCanceled, "canceled", "流水线已停止，已完成的发布操作保留"
		if active {
			run.Status, run.Stage, run.Message = model.PipelineRunRunning, stoppingStage, "正在停止并行任务并清理执行资源"
		}
		return tx.Model(&run).Updates(map[string]any{"status": run.Status, "stage": run.Stage, "message": run.Message, "updated_at": now}).Error
	})
	if err != nil {
		s.logger.Error("停止并行流水线失败", "operation", "pipeline_parallel_stop", "pipeline_run_id", runID, "actor_id", actorID, "err", err)
		return nil, ErrPipelineStopFailed
	}
	for _, jobID := range jobIDs {
		if control := s.runningExecutions[jobID]; control != nil {
			control.cancel()
		}
	}
	s.appendRunLog(ctx, runID, run.Stage, "warning", run.Message)
	s.realtime.Changed(ctx, "pipeline_runs", runID)
	return s.FindRun(ctx, runID)
}

func (s *Service) finishStoppedNode(ctx context.Context, runID, jobID string) (bool, error) {
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	ctx = updateCtx
	var node model.PipelineNodeRun
	if err := s.db.WithContext(ctx).Where("pipeline_run_id = ? AND job_id = ?", runID, jobID).First(&node).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		s.logger.Error("读取停止任务记录失败", "operation", "pipeline_node_stop_read", "run_id", runID, "job_id", jobID, "err", err)
		return true, err
	}
	var parent model.PipelineRun
	if err := s.db.WithContext(ctx).First(&parent, "uuid = ?", runID).Error; err != nil {
		return true, err
	}
	if parent.Stage != stoppingStage && !nodeFinished(parent.Status) {
		return false, nil
	}
	if node.Status == model.PipelineRunFailed || node.Status == model.PipelineRunSucceeded {
		return false, nil
	}
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.PipelineNodeRun{}).Where("uuid = ? AND status IN ?", node.ID, []model.PipelineRunStatus{model.PipelineRunRunning, model.PipelineRunBlocked, model.PipelineRunAwaitingApproval}).
			Updates(map[string]any{"status": model.PipelineRunCanceled, "stage": "canceled", "message": "执行已停止", "finished_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Job{}).Where("uuid = ? AND status IN ?", jobID, []model.JobStatus{model.JobPending, model.JobRunning}).
			Updates(map[string]any{"status": model.JobCanceled, "error_code": "canceled_by_pipeline", "error_message": "执行已停止", "finished_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		var active int64
		if err := tx.Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ? AND status = ?", runID, model.PipelineRunRunning).Count(&active).Error; err != nil {
			return err
		}
		if active == 0 && parent.Stage == stoppingStage {
			return tx.Model(&model.PipelineRun{}).Where("uuid = ? AND stage = ?", runID, stoppingStage).Updates(map[string]any{"status": model.PipelineRunCanceled, "stage": "canceled", "message": "流水线已停止，已完成的发布操作保留", "updated_at": now}).Error
		}
		return nil
	})
	if err == nil {
		err = s.db.WithContext(ctx).Model(&model.PipelineRun{}).Where("uuid = ?", runID).Update("updated_at", now).Error
		if err == nil {
			s.realtime.Changed(ctx, "pipeline_runs", runID)
		}
	}
	return true, err
}
