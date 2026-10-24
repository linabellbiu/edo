package pipeline

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"edo/internal/model"
)

const stoppingStage = "stopping"

var (
	ErrPipelineStopState  = errors.New("流水线已经结束或状态已变化，不能停止，请刷新后重试")
	ErrPipelineStopFailed = errors.New("停止流水线失败，请稍后重试")
)

type runExecutionControl struct{ cancel context.CancelFunc }

// 停止请求先持久化，执行器退出并完成清理后才显示已停止。
func (s *Service) StopRun(ctx context.Context, runID, actorID string) (*model.PipelineRun, error) {
	s.pipelineAdvanceMu.Lock()
	defer s.pipelineAdvanceMu.Unlock()
	var parallelCount int64
	if err := s.db.WithContext(ctx).Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ?", runID).Count(&parallelCount).Error; err != nil {
		return nil, ErrPipelineStopFailed
	}
	if parallelCount > 0 {
		return s.stopParallelRun(ctx, runID, actorID)
	}
	var run model.PipelineRun
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&run, "uuid = ?", runID).Error; err != nil {
			return err
		}
		if run.Status == model.PipelineRunCanceled || run.Stage == stoppingStage {
			return nil
		}
		switch run.Status {
		case model.PipelineRunDetected, model.PipelineRunReady, model.PipelineRunBlocked, model.PipelineRunAwaitingApproval, model.PipelineRunRunning:
		default:
			return ErrPipelineStopState
		}
		now := time.Now().UTC()
		active := s.runningExecutions[run.ExecutionJobID] != nil
		if run.ExecutionJobID != "" {
			// 与 Worker 领取任务的条件更新竞争，先取消待执行任务，不能先查后改。
			if err := tx.Model(&model.Job{}).Where("uuid = ? AND status = ?", run.ExecutionJobID, model.JobPending).
				Updates(map[string]any{"status": model.JobCanceled, "error_code": "canceled_by_user", "error_message": "流水线已由用户停止", "finished_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
			var job model.Job
			err := tx.Select("uuid", "status").First(&job, "uuid = ?", run.ExecutionJobID).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			active = active || job.Status == model.JobRunning
		}
		// 审核和人工放行可能仍保留上一节点的 Job ID，不代表当前有执行任务。
		if run.Stage == "manual" || run.Status == model.PipelineRunAwaitingApproval || run.Status == model.PipelineRunBlocked || run.Status == model.PipelineRunDetected {
			active = false
		}
		status, stage, message := model.PipelineRunCanceled, "canceled", "流水线已停止；不会执行后续节点，已完成的发布操作不会自动回滚"
		if active {
			status, stage, message = model.PipelineRunRunning, stoppingStage, "正在停止当前任务并清理执行资源，请等待确认"
		}
		result := tx.Model(&model.PipelineRun{}).
			Where("uuid = ? AND status = ? AND stage = ? AND current_node_id = ? AND execution_job_id = ?", run.ID, run.Status, run.Stage, run.CurrentNodeID, run.ExecutionJobID).
			Updates(map[string]any{"status": status, "stage": stage, "message": message, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPipelineStopState
		}
		run.Status, run.Stage, run.Message = status, stage, message
		changed = true
		return nil
	})
	if err != nil {
		s.logger.Error("停止流水线失败", "operation", "pipeline_stop", "pipeline_run_id", runID, "actor_id", actorID, "err", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPipelineRunNotFound
		}
		if errors.Is(err, ErrPipelineStopState) {
			return nil, err
		}
		return nil, ErrPipelineStopFailed
	}
	if changed {
		s.logger.Info("用户停止流水线", "operation", "pipeline_stop", "pipeline_run_id", run.ID, "actor_id", actorID, "execution_job_id", run.ExecutionJobID)
		s.appendRunLog(ctx, run.ID, run.Stage, "warning", run.Message)
	}
	if control := s.runningExecutions[run.ExecutionJobID]; control != nil {
		control.cancel()
	}
	s.realtime.Changed(ctx, "pipeline_runs", run.ID)
	return s.FindRun(ctx, run.ID)
}

func (s *Service) ExecuteBuildTask(ctx context.Context, payload BuildTaskPayload, jobID string) error {
	return s.withRunCancellation(ctx, payload.PipelineRunID, jobID, func(ctx context.Context) error { return s.executeBuildTask(ctx, payload, jobID) })
}

func (s *Service) ExecuteDeployTask(ctx context.Context, payload DeployTaskPayload, jobID string) error {
	return s.withRunCancellation(ctx, payload.PipelineRunID, jobID, func(ctx context.Context) error { return s.executeDeployTask(ctx, payload, jobID) })
}

func (s *Service) withRunCancellation(ctx context.Context, runID, jobID string, execute func(context.Context) error) (resultErr error) {
	s.pipelineAdvanceMu.Lock()
	var run model.PipelineRun
	err := s.db.WithContext(ctx).Select("uuid", "stage", "status", "execution_job_id").First(&run, "uuid = ?", runID).Error
	isNodeJob := s.hasNodeJob(ctx, runID, jobID)
	if err != nil || (run.ExecutionJobID != jobID && !isNodeJob) {
		s.pipelineAdvanceMu.Unlock()
		return execute(ctx)
	}
	if run.Stage == stoppingStage || run.Status == model.PipelineRunCanceled || (isNodeJob && run.Status == model.PipelineRunFailed) {
		s.pipelineAdvanceMu.Unlock()
		if isNodeJob {
			_, err := s.finishStoppedNode(ctx, runID, jobID)
			return err
		}
		_, err := s.finishStoppedRun(ctx, runID, jobID)
		return err
	}
	if s.runningExecutions == nil {
		s.runningExecutions = make(map[string]*runExecutionControl)
	}
	executionCtx, cancel := context.WithCancel(ctx)
	control := &runExecutionControl{cancel: cancel}
	s.runningExecutions[jobID] = control
	s.pipelineAdvanceMu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 持久化检查覆盖跨进程请求以及领取任务与停止请求的竞态。
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-executionCtx.Done():
				return
			case <-ticker.C:
				checkCtx, checkCancel := context.WithTimeout(executionCtx, 2*time.Second)
				var current model.PipelineRun
				query := s.db.WithContext(checkCtx).Select("stage", "status").Where("uuid = ?", runID)
				if !isNodeJob {
					query = query.Where("execution_job_id = ?", jobID)
				}
				err := query.First(&current).Error
				checkCancel()
				if err == nil && (current.Stage == stoppingStage || current.Status == model.PipelineRunCanceled || (isNodeJob && current.Status == model.PipelineRunFailed)) {
					cancel()
					return
				}
				if err != nil && executionCtx.Err() == nil {
					s.logger.Warn("检查流水线停止请求失败", "operation", "pipeline_stop_check", "pipeline_run_id", runID, "err", err)
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-done
		s.pipelineAdvanceMu.Lock()
		defer s.pipelineAdvanceMu.Unlock()
		if s.runningExecutions[jobID] == control {
			delete(s.runningExecutions, jobID)
		}
		if isNodeJob {
			stopped, err := s.finishStoppedNode(context.WithoutCancel(ctx), runID, jobID)
			if err != nil {
				resultErr = err
			} else if stopped {
				resultErr = nil
			}
			return
		}
		stopped, stopErr := s.finishStoppedRunLocked(ctx, runID, jobID)
		if stopErr != nil {
			resultErr = stopErr
		} else if stopped {
			resultErr = nil
		}
	}()
	return execute(executionCtx)
}

func (s *Service) finishStoppedRun(ctx context.Context, runID, jobID string) (bool, error) {
	s.pipelineAdvanceMu.Lock()
	defer s.pipelineAdvanceMu.Unlock()
	return s.finishStoppedRunLocked(ctx, runID, jobID)
}

func (s *Service) finishStoppedRunLocked(ctx context.Context, runID, jobID string) (bool, error) {
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	changed := false
	message := "流水线已停止；不会执行后续节点。已提交的部署操作可能已经生效，请确认目标环境，系统不会自动回滚"
	err := s.db.WithContext(updateCtx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		result := tx.Model(&model.PipelineRun{}).Where("uuid = ? AND execution_job_id = ? AND stage = ? AND status = ?", runID, jobID, stoppingStage, model.PipelineRunRunning).
			Updates(map[string]any{"status": model.PipelineRunCanceled, "stage": "canceled", "message": message, "updated_at": now})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		changed = true
		return tx.Model(&model.Job{}).Where("uuid = ? AND status IN ?", jobID, []model.JobStatus{model.JobPending, model.JobRunning}).
			Updates(map[string]any{"status": model.JobCanceled, "error_code": "canceled_by_user", "error_message": "流水线已由用户停止", "lease_owner": "", "lease_expires_at": nil, "finished_at": now, "updated_at": now}).Error
	})
	if err != nil {
		s.logger.Error("确认流水线停止状态失败", "operation", "pipeline_stop_complete", "pipeline_run_id", runID, "execution_job_id", jobID, "err", err)
		return false, ErrPipelineStopFailed
	}
	if changed {
		s.appendRunLog(updateCtx, runID, "canceled", "warning", message)
		s.realtime.Changed(updateCtx, "pipeline_runs", runID)
	}
	return changed, nil
}
