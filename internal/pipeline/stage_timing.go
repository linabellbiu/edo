package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"edo/internal/model"
)

var errStageTimingUnavailable = errors.New("读取流水线阶段耗时失败，请稍后重试")

func (s *Service) attachRunStageTimings(ctx context.Context, runs []model.PipelineRun) error {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		if run.ExecutionGraph != nil {
			ids = append(ids, run.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// 每页只增加一次有项目范围的查询，不逐行读取任务，也不返回脚本或任务日志。
	var records []model.PipelineNodeRun
	if err := s.db.WithContext(ctx).
		Select("pipeline_run_id", "node_id", "stage_id", "status", "stage", "created_at", "started_at", "finished_at").
		Where("pipeline_run_id IN ?", ids).Find(&records).Error; err != nil {
		logger := s.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.ErrorContext(ctx, "查询流水线阶段耗时失败", "operation", "pipeline_run_stage_timing", "run_count", len(ids), "err", err)
		return errStageTimingUnavailable
	}
	byRun := make(map[string]map[string]model.PipelineNodeRun, len(ids))
	for _, record := range records {
		if byRun[record.PipelineRunID] == nil {
			byRun[record.PipelineRunID] = map[string]model.PipelineNodeRun{}
		}
		byRun[record.PipelineRunID][record.NodeID] = record
	}
	now := time.Now().UTC()
	for i := range runs {
		if runs[i].ExecutionGraph == nil {
			continue
		}
		for j := range runs[i].ExecutionGraph.Stages {
			stage := &runs[i].ExecutionGraph.Stages[j]
			stage.Timing = stageTiming(*stage, runs[i].Status, byRun[runs[i].ID], now)
		}
		runs[i].Timing = runTiming(runs[i], byRun[runs[i].ID], now)
	}
	return nil
}

// 总耗时取本次实际执行的时间跨度，包含代码源与阶段间等待，不使用运行更新时间。
func runTiming(run model.PipelineRun, records map[string]model.PipelineNodeRun, now time.Time) *model.PipelineStageTiming {
	timing := &model.PipelineStageTiming{State: string(run.Status)}
	if run.ExecutionGraph == nil {
		return timing
	}
	parts := make([]*model.PipelineStageTiming, 0, len(run.ExecutionGraph.Stages)+1)
	if source, ok := records[run.ExecutionGraph.Source.ID]; ok {
		parts = append(parts, stageTiming(model.PipelineRunGraphStage{ID: source.StageID, Tasks: []model.PipelineRunGraphNode{run.ExecutionGraph.Source}}, run.Status, records, now))
	}
	for _, stage := range run.ExecutionGraph.Stages {
		parts = append(parts, stage.Timing)
	}
	terminal := nodeFinished(run.Status)
	incomplete := false
	for _, part := range parts {
		if part != nil && (part.State == "pending" || part.State == "not_executed" || part.State == "skipped") {
			continue
		}
		if part == nil || part.DurationMS == nil || part.StartedAt == nil {
			incomplete = true
			continue
		}
		if timing.StartedAt == nil || part.StartedAt.Before(*timing.StartedAt) {
			timing.StartedAt = part.StartedAt
		}
		if terminal {
			if part.FinishedAt == nil {
				incomplete = true
			} else if timing.FinishedAt == nil || part.FinishedAt.After(*timing.FinishedAt) {
				timing.FinishedAt = part.FinishedAt
			}
		}
	}
	if incomplete || timing.StartedAt == nil || terminal && timing.FinishedAt == nil {
		return timing
	}
	end := now
	if terminal {
		end = *timing.FinishedAt
	}
	if duration := end.Sub(*timing.StartedAt).Milliseconds(); duration >= 0 {
		timing.DurationMS = &duration
		timing.Running = !terminal
	}
	return timing
}

func stageTiming(stage model.PipelineRunGraphStage, runStatus model.PipelineRunStatus, records map[string]model.PipelineNodeRun, now time.Time) *model.PipelineStageTiming {
	timing := &model.PipelineStageTiming{State: "pending"}
	found, finished, skipped := 0, 0, 0
	failed, canceled, incomplete := false, false, false
	activeState := ""
	for _, node := range stage.Tasks {
		record, ok := records[node.ID]
		if !ok || record.StageID != stage.ID {
			continue
		}
		found++
		if nodeFinished(record.Status) {
			finished++
		}
		if record.Status == model.PipelineRunSkipped {
			skipped++
			continue
		}
		failed = failed || record.Status == model.PipelineRunFailed
		canceled = canceled || record.Status == model.PipelineRunCanceled
		// 创建任务即进入该阶段；包含排队、审核和放行等待，并行任务取时间跨度而非求和。
		start := record.CreatedAt
		if record.StartedAt != nil && !record.StartedAt.IsZero() && (start.IsZero() || record.StartedAt.Before(start)) {
			start = *record.StartedAt
		}
		if start.IsZero() || start.After(now) {
			incomplete = true
		} else if timing.StartedAt == nil || start.Before(*timing.StartedAt) {
			timing.StartedAt = &start
		}
		if nodeFinished(record.Status) {
			if record.FinishedAt == nil || record.FinishedAt.IsZero() || record.FinishedAt.Before(start) || record.FinishedAt.After(now) {
				incomplete = true
			} else if timing.FinishedAt == nil || record.FinishedAt.After(*timing.FinishedAt) {
				timing.FinishedAt = record.FinishedAt
			}
		} else {
			state := string(record.Status)
			if record.Status == model.PipelineRunRunning && record.Stage == "queued" {
				state = "queued"
			}
			if activeState == "" || activeState == "queued" {
				activeState = state
			}
		}
	}
	if found == 0 {
		if runStatus == model.PipelineRunSkipped {
			timing.State = "skipped"
		} else if nodeFinished(runStatus) && (len(records) == 0 || runStatus == model.PipelineRunSucceeded) {
			timing.State = "unavailable"
		} else if nodeFinished(runStatus) {
			timing.State = "not_executed"
		}
		return timing
	}
	if found == skipped && found == len(stage.Tasks) {
		timing.State = "skipped"
		return timing
	}
	terminal := finished == len(stage.Tasks) || nodeFinished(runStatus) && finished == found && (failed || canceled)
	switch {
	case failed:
		timing.State = "failed"
	case canceled:
		timing.State = "canceled"
	case terminal:
		timing.State = "succeeded"
	case activeState != "":
		timing.State = activeState
	default:
		timing.State = "running"
	}
	if !terminal {
		timing.FinishedAt = nil
		if nodeFinished(runStatus) {
			incomplete = true
		} else {
			timing.Running = true
		}
	}
	if incomplete || timing.StartedAt == nil {
		timing.Running = false
		return timing
	}
	end := now
	if terminal {
		if timing.FinishedAt == nil {
			return timing
		}
		end = *timing.FinishedAt
	}
	duration := end.Sub(*timing.StartedAt).Milliseconds()
	if duration >= 0 {
		timing.DurationMS = &duration
	}
	return timing
}
