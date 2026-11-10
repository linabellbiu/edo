package pipeline

import (
	"context"
	"errors"

	"edo/internal/model"
)

func pipelineRunRetryAllowed(run *model.PipelineRun, workflow *model.ReleaseWorkflow) bool {
	return run.Status == model.PipelineRunFailed ||
		run.Status == model.PipelineRunSucceeded && workflow != nil &&
			workflow.ID == run.WorkflowID && workflow.ApplicationID == run.ApplicationID &&
			workflowHasManualReleaseSource(workflow)
}

// attachRunRetryEligibility 按当前流程计算展示状态，不修改原运行快照或持久化数据。
func (s *Service) attachRunRetryEligibility(ctx context.Context, runs []model.PipelineRun) error {
	ids := make(map[string]struct{})
	for i := range runs {
		runs[i].RetryAllowed = runs[i].Status == model.PipelineRunFailed
		if runs[i].Status == model.PipelineRunSucceeded && runs[i].WorkflowID != "" {
			ids[runs[i].WorkflowID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	workflowIDs := make([]string, 0, len(ids))
	for id := range ids {
		workflowIDs = append(workflowIDs, id)
	}
	var workflows []model.ReleaseWorkflow
	if err := s.db.WithContext(ctx).Select("uuid", "application_id", "source").Where("uuid IN ?", workflowIDs).Find(&workflows).Error; err != nil {
		s.logger.Error("读取流水线重新执行状态失败", "operation", "pipeline_run_retry_eligibility", "workflow_count", len(workflowIDs), "err", err)
		return errors.New("读取流水线重新执行状态失败，请稍后重试")
	}
	byID := make(map[string]*model.ReleaseWorkflow, len(workflows))
	for i := range workflows {
		byID[workflows[i].ID] = &workflows[i]
	}
	for i := range runs {
		runs[i].RetryAllowed = pipelineRunRetryAllowed(&runs[i], byID[runs[i].WorkflowID])
	}
	return nil
}
