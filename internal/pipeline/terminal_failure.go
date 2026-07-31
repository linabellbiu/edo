package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"edo/internal/model"
)

// HandleTerminalTaskFailure 在 Worker 确认任务不会再次执行时，与 Job 失败状态同事务
// 收敛流水线。它只信任数据库中的 Job 类型和参数，并以任务、节点、状态三项 CAS
// 防止旧消息覆盖已经推进的运行。
func (s *Service) HandleTerminalTaskFailure(
	ctx context.Context,
	tx *gorm.DB,
	job model.Job,
	_ string,
	_ string,
) error {
	if tx == nil {
		if s.logger != nil {
			s.logger.Error("流水线终止失败处理缺少数据库事务", "operation", "pipeline_terminal_failure_transaction",
				"job_id", job.ID, "kind", job.Kind, "err", errors.New("数据库事务为空"))
		}
		return errors.New("流水线终止失败处理缺少数据库事务")
	}
	var runID, nodeID, message string
	switch job.Kind {
	case "pipeline.build":
		var payload BuildTaskPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.PipelineRunID == "" || payload.WorkflowNodeID == "" {
			if err == nil {
				err = errors.New("流水线运行或任务标识为空")
			}
			if s.logger != nil {
				s.logger.Error("流水线构建任务的数据库参数无效", "operation", "pipeline_terminal_failure_payload",
					"job_id", job.ID, "kind", job.Kind, "err", err)
			}
			return nil
		}
		runID, nodeID = payload.PipelineRunID, payload.WorkflowNodeID
		message = "任务执行中断，请重新执行流水线"
	case "pipeline.deploy":
		var payload DeployTaskPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.PipelineRunID == "" || payload.WorkflowNodeID == "" {
			if err == nil {
				err = errors.New("流水线运行或任务标识为空")
			}
			if s.logger != nil {
				s.logger.Error("流水线部署任务的数据库参数无效", "operation", "pipeline_terminal_failure_payload",
					"job_id", job.ID, "kind", job.Kind, "err", err)
			}
			return nil
		}
		runID, nodeID = payload.PipelineRunID, payload.WorkflowNodeID
		message = "部署执行中断，目标状态需人工确认"
	default:
		return nil
	}

	var run model.PipelineRun
	err := tx.WithContext(ctx).
		Select("id", "execution_job_id", "current_node_id", "status", "stage").
		First(&run, "id = ? AND execution_job_id = ? AND current_node_id = ?", runID, job.ID, nodeID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		if s.logger != nil {
			s.logger.Error("读取待收敛流水线状态失败", "operation", "pipeline_terminal_failure_load",
				"pipeline_run_id", runID, "job_id", job.ID, "workflow_node_id", nodeID, "err", err)
		}
		return err
	}
	if run.Status != model.PipelineRunRunning &&
		!(run.Status == model.PipelineRunReady && (run.Stage == "task_succeeded" || run.Stage == "deploy_succeeded")) {
		return nil
	}
	if run.Status == model.PipelineRunReady {
		if job.Kind == "pipeline.deploy" {
			message = "部署已完成，但流程推进中断，请重新执行流水线"
		} else {
			message = "任务已完成，但流程推进中断，请重新执行流水线"
		}
	}

	now := time.Now().UTC()
	result := tx.WithContext(ctx).Model(&model.PipelineRun{}).
		Where("id = ? AND execution_job_id = ? AND current_node_id = ? AND status = ? AND stage = ?",
			run.ID, job.ID, nodeID, run.Status, run.Stage).
		Updates(map[string]any{
			"status": model.PipelineRunFailed, "stage": "failed", "message": message, "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return nil
	}
	if err := tx.WithContext(ctx).Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", run.ID).
		Updates(map[string]any{"status": model.PipelineRunRepositoryFailed, "updated_at": now}).Error; err != nil {
		return err
	}
	if job.Kind != "pipeline.deploy" {
		return nil
	}
	return tx.WithContext(ctx).Model(&model.DeploymentRecord{}).
		Where("pipeline_run_id = ? AND workflow_node_id = ? AND status IN ?", run.ID, nodeID,
			[]model.DeploymentStatus{model.DeploymentQueued, model.DeploymentRunning}).
		Updates(map[string]any{
			"status": model.DeploymentFailed, "error_code": "pipeline_task_interrupted",
			"error_message": message, "finished_at": now, "updated_at": now,
		}).Error
}
