package pipeline

import (
	"context"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestStageTimingUsesWallTimeAndPreservesMissingData(t *testing.T) {
	base := time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC)
	at := func(seconds int) *time.Time { value := base.Add(time.Duration(seconds) * time.Second); return &value }
	stage := model.PipelineRunGraphStage{ID: "stage", Tasks: []model.PipelineRunGraphNode{{ID: "one"}, {ID: "two"}}}
	record := func(id string, start, end int, status model.PipelineRunStatus) model.PipelineNodeRun {
		value := model.PipelineNodeRun{NodeID: id, StageID: stage.ID, CreatedAt: *at(start), StartedAt: at(start), Status: status}
		if end >= 0 {
			value.FinishedAt = at(end)
		}
		return value
	}
	for _, tc := range []struct {
		name    string
		status  model.PipelineRunStatus
		records []model.PipelineNodeRun
		state   string
		seconds int64
		running bool
	}{
		{"并行耗时不相加", model.PipelineRunSucceeded, []model.PipelineNodeRun{record("one", 0, 10, model.PipelineRunSucceeded), record("two", 2, 15, model.PipelineRunSucceeded)}, "succeeded", 15, false},
		{"屏障与顺序任务保留阶段跨度", model.PipelineRunSucceeded, []model.PipelineNodeRun{record("one", 0, 10, model.PipelineRunSucceeded), record("two", 20, 25, model.PipelineRunSucceeded)}, "succeeded", 25, false},
		{"运行中的并行任务持续计时", model.PipelineRunRunning, []model.PipelineNodeRun{record("one", 0, 10, model.PipelineRunSucceeded), record("two", 2, -1, model.PipelineRunRunning)}, "running", 30, true},
		{"失败阶段保留已执行耗时", model.PipelineRunFailed, []model.PipelineNodeRun{record("one", 0, 10, model.PipelineRunFailed)}, "failed", 10, false},
		{"停止阶段保留跨度", model.PipelineRunCanceled, []model.PipelineNodeRun{record("one", 0, 10, model.PipelineRunCanceled), record("two", 2, 12, model.PipelineRunCanceled)}, "canceled", 12, false},
		{"全跳过不冒充零耗时", model.PipelineRunSucceeded, []model.PipelineNodeRun{record("one", 0, 0, model.PipelineRunSkipped), record("two", 0, 0, model.PipelineRunSkipped)}, "skipped", -1, false},
		{"未开始不计时", model.PipelineRunRunning, nil, "pending", -1, false},
		{"历史缺失记录保持未知", model.PipelineRunSucceeded, nil, "unavailable", -1, false},
		{"结束时间缺失不能用运行更新时间代替", model.PipelineRunSucceeded, []model.PipelineNodeRun{record("one", 0, -1, model.PipelineRunSucceeded), record("two", 2, 15, model.PipelineRunSucceeded)}, "succeeded", -1, false},
		{"无效时间不截成零", model.PipelineRunSucceeded, []model.PipelineNodeRun{record("one", 10, 1, model.PipelineRunSucceeded), record("two", 2, 15, model.PipelineRunSucceeded)}, "succeeded", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := map[string]model.PipelineNodeRun{}
			for _, value := range tc.records {
				records[value.NodeID] = value
			}
			got := stageTiming(stage, tc.status, records, *at(30))
			if got.State != tc.state || got.Running != tc.running {
				t.Fatalf("阶段状态错误: %+v", got)
			}
			if tc.seconds < 0 {
				if got.DurationMS != nil {
					t.Fatalf("缺少依据时伪造了耗时: %+v", got)
				}
			} else if got.DurationMS == nil || *got.DurationMS != tc.seconds*1000 {
				t.Fatalf("阶段耗时错误: %+v", got)
			}
			if got.Running && got.FinishedAt != nil {
				t.Fatal("运行阶段不能使用已完成分支的结束时间")
			}
		})
	}
	queued := record("one", 0, -1, model.PipelineRunRunning)
	queued.StartedAt, queued.Stage = nil, "queued"
	got := stageTiming(stage, model.PipelineRunRunning, map[string]model.PipelineNodeRun{"one": queued}, *at(30))
	if got.State != "queued" || got.DurationMS == nil || *got.DurationMS != 30000 {
		t.Fatalf("排队应从实际进入阶段时间计时: %+v", got)
	}
	queued.Status, queued.Stage = model.PipelineRunAwaitingApproval, "approval"
	got = stageTiming(stage, model.PipelineRunAwaitingApproval, map[string]model.PipelineNodeRun{"one": queued}, *at(30))
	if got.State != "awaiting_approval" || !got.Running {
		t.Fatalf("审核等待应持续计时: %+v", got)
	}
	got = stageTiming(stage, model.PipelineRunFailed, map[string]model.PipelineNodeRun{"other": {NodeID: "other", StageID: "other-stage"}}, *at(30))
	if got.State != "not_executed" || got.DurationMS != nil {
		t.Fatalf("失败后未进入的阶段应标记未执行: %+v", got)
	}
}

func TestRunStageTimingsUseRunSnapshotAndProjectScope(t *testing.T) {
	db := testutil.NewDatabase(t)
	service := NewService(db, nil, nil)
	base := time.Now().UTC().Add(-time.Hour)
	end := base.Add(15 * time.Second)
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	run := queryRunFixture("timing-run", base, model.PipelineRunRunning)
	run.WorkflowSnapshot = `{"schema_version":1,"source":{"id":"source","name":"代码源"},"stages":[{"id":"stage","name":"构建","tasks":[{"id":"one","name":"构建","type":"build"}]},{"id":"deploy","name":"部署","tasks":[{"id":"two","name":"部署","type":"deploy"}]}]}`
	for _, value := range []any{
		&model.GitRepository{ID: "query-repository", Name: "query-repository"},
		&model.Application{ID: "query-application", Name: "query-app", RepositoryID: "query-repository"},
		&run,
		&model.PipelineNodeRun{ID: "node-one", PipelineRunID: run.ID, NodeID: "one", StageID: "stage", ProjectID: project, DepartmentID: database.DefaultDepartmentID, Status: model.PipelineRunSucceeded, CreatedAt: base, StartedAt: &base, FinishedAt: &end},
		&model.PipelineNodeRun{ID: "foreign-node", PipelineRunID: run.ID, NodeID: "two", StageID: "deploy", ProjectID: "foreign-project", DepartmentID: database.DefaultDepartmentID, Status: model.PipelineRunSucceeded, CreatedAt: base, StartedAt: &base, FinishedAt: &end},
	} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project, DepartmentID: database.DefaultDepartmentID})
	page, err := service.QueryRuns(ctx, RunQueryOptions{})
	if err != nil || len(page.Runs) != 1 {
		t.Fatalf("读取带阶段耗时的运行失败: %+v %v", page, err)
	}
	stages := page.Runs[0].ExecutionGraph.Stages
	if stages[0].Timing.DurationMS == nil || *stages[0].Timing.DurationMS != 15000 || stages[1].Timing.DurationMS != nil || stages[1].Timing.State != "pending" {
		t.Fatalf("阶段数据或项目隔离错误: %+v %+v", stages[0].Timing, stages[1].Timing)
	}
	if timing := page.Runs[0].Timing; timing == nil || timing.DurationMS == nil || !timing.Running || timing.StartedAt == nil || !timing.StartedAt.Equal(base) {
		t.Fatalf("整体耗时未继承实际任务时间或混入其他项目: %+v", timing)
	}
	found, err := service.FindRun(ctx, run.ID)
	if err != nil || found.ExecutionGraph.Stages[0].Timing.DurationMS == nil || found.Timing == nil || found.Timing.DurationMS == nil {
		t.Fatalf("详情遗漏阶段耗时: %+v %v", found, err)
	}
}
