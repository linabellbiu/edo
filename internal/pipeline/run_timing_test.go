package pipeline

import (
	"testing"
	"time"

	"edo/internal/model"
)

func TestRunTimingUsesActualSpanAndFreezesTerminalRuns(t *testing.T) {
	base := time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC)
	at := func(seconds int) *time.Time { value := base.Add(time.Duration(seconds) * time.Second); return &value }
	record := func(id, stage string, start, end int, status model.PipelineRunStatus) model.PipelineNodeRun {
		value := model.PipelineNodeRun{NodeID: id, StageID: stage, CreatedAt: *at(start), Status: status}
		if end >= 0 {
			value.FinishedAt = at(end)
		}
		return value
	}
	for _, tc := range []struct {
		name     string
		status   model.PipelineRunStatus
		records  []model.PipelineNodeRun
		duration int64
		running  bool
	}{
		{"并行任务、代码源和阶段间等待取总跨度", model.PipelineRunSucceeded, []model.PipelineNodeRun{record("source", "", 0, 3, model.PipelineRunSucceeded), record("one", "build", 5, 10, model.PipelineRunSucceeded), record("two", "build", 6, 15, model.PipelineRunSucceeded), record("deploy", "deploy", 20, 25, model.PipelineRunSucceeded)}, 25000, false},
		{"运行中保留跨阶段等待并持续计时", model.PipelineRunRunning, []model.PipelineNodeRun{record("one", "build", 0, 10, model.PipelineRunSucceeded), record("two", "build", 2, 15, model.PipelineRunSucceeded), record("deploy", "deploy", 20, -1, model.PipelineRunRunning)}, 30000, true},
		{"失败不计入未执行的后续阶段", model.PipelineRunFailed, []model.PipelineNodeRun{record("one", "build", 0, 10, model.PipelineRunFailed)}, 10000, false},
		{"停止后不继续累计", model.PipelineRunCanceled, []model.PipelineNodeRun{record("one", "build", 0, 10, model.PipelineRunCanceled)}, 10000, false},
		{"人工审核仍计算真实等待时间", model.PipelineRunAwaitingApproval, []model.PipelineNodeRun{record("one", "build", 0, -1, model.PipelineRunAwaitingApproval)}, 30000, true},
		{"代码源执行失败也有总耗时", model.PipelineRunFailed, []model.PipelineNodeRun{record("source", "", 0, 2, model.PipelineRunFailed)}, 2000, false},
		{"没有任务时间不使用运行更新时间", model.PipelineRunSucceeded, nil, -1, false},
		{"缺少结束时间不能算成成功耗时", model.PipelineRunSucceeded, []model.PipelineNodeRun{record("one", "build", 0, -1, model.PipelineRunSucceeded)}, -1, false},
		{"无效开始结束不补零", model.PipelineRunFailed, []model.PipelineNodeRun{record("one", "build", 10, 2, model.PipelineRunFailed)}, -1, false},
		{"全部跳过不伪造执行时间", model.PipelineRunSkipped, []model.PipelineNodeRun{record("one", "build", 0, 0, model.PipelineRunSkipped)}, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := model.PipelineRun{Status: tc.status, UpdatedAt: *at(1000), ExecutionGraph: &model.PipelineRunGraph{
				Source: model.PipelineRunGraphNode{ID: "source"}, Stages: []model.PipelineRunGraphStage{
					{ID: "build", Tasks: []model.PipelineRunGraphNode{{ID: "one"}, {ID: "two"}}},
					{ID: "deploy", Tasks: []model.PipelineRunGraphNode{{ID: "deploy"}}},
				},
			}}
			records := map[string]model.PipelineNodeRun{}
			for _, row := range tc.records {
				records[row.NodeID] = row
			}
			for i := range run.ExecutionGraph.Stages {
				stage := &run.ExecutionGraph.Stages[i]
				stage.Timing = stageTiming(*stage, run.Status, records, *at(30))
			}
			got := runTiming(run, records, *at(30))
			if got.Running != tc.running {
				t.Fatalf("运行计时状态错误: %+v", got)
			}
			if tc.duration < 0 {
				if got.DurationMS != nil {
					t.Fatalf("没有时间依据却返回了耗时: %+v", got)
				}
			} else if got.DurationMS == nil || *got.DurationMS != tc.duration {
				t.Fatalf("整次运行耗时错误: %+v", got)
			}
		})
	}
}
