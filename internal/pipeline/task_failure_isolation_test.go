package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/dockerengine"
	"edo/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 安全校验仍阻止异常任务；用单任务隔离运行实际核验不会生成执行 Job。
func assertTaskFailureCannotExecute(t *testing.T, s *Service, run *model.PipelineRun, err error, nodeID string) *workflowSnapshot {
	t.Helper()
	if err != nil || run == nil {
		t.Fatalf("任务异常错误阻止创建其他任务的运行: %v", err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil || snapshot.TaskFailures[nodeID] == "" {
		t.Fatalf("任务异常没有进入不可变快照: node=%s err=%v", nodeID, err)
	}
	node, ok := workflowFindNode(snapshot.Source, snapshot.Stages, nodeID)
	if !ok {
		t.Fatal("异常任务从运行中消失")
	}
	probe := *run
	probe.ID = uuid.NewString()
	copy := *snapshot
	copy.Stages = []model.WorkflowStage{{ID: "isolated-failure", Tasks: []model.WorkflowNode{node}}}
	data, err := json.Marshal(copy)
	if err != nil {
		t.Fatal(err)
	}
	probe.WorkflowSnapshot = string(data)
	rollback := errors.New("隔离任务校验完成后回滚测试数据")
	probeErr := s.db.Transaction(func(tx *gorm.DB) error {
		scoped := NewService(tx, s.repositories, s.secrets)
		scoped.logger = s.logger
		if err := scoped.db.Create(&probe).Error; err != nil {
			t.Fatal(err)
		}
		result, err := scoped.AdvanceRun(context.Background(), probe.ID, probe.CreatedBy, "")
		if err != nil || result.Status != model.PipelineRunFailed {
			t.Fatalf("异常任务没有被安全阻止: err=%v", err)
		}
		nodes, err := scoped.ListNodeRuns(context.Background(), probe.ID)
		if err != nil || len(nodes) != 1 || nodes[0].Status != model.PipelineRunFailed || nodes[0].JobID != "" {
			t.Fatalf("异常任务被投递或丢失失败原因: %+v %v", nodes, err)
		}
		var count int64
		if err := scoped.db.Model(&model.Job{}).Where("idempotency_key = ?", "pipeline-node:"+probe.ID+":"+nodeID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("异常任务创建了 Job: count=%d err=%v", count, err)
		}
		if diagnostic, exists := snapshot.TaskDiagnostics[nodeID]; exists {
			logs, _, err := scoped.ListNodeRunLogs(context.Background(), probe.ID, nodeID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range logs {
				if entry.Stage == "prepare" && entry.Level == "error" && strings.Contains(entry.Message, diagnostic.Detail) {
					found = true
				}
				if entry.Level == "output" || entry.Level == "stderr" {
					t.Fatal("没有 Job 的准备失败伪造了程序输出", entry)
				}
			}
			if !found {
				t.Fatalf("任务日志缺少冻结的准备诊断: %+v", logs)
			}
		}
		return rollback
	})
	if !errors.Is(probeErr, rollback) {
		t.Fatal(probeErr)
	}

	return snapshot
}

func TestWorkflowSnapshotFailureRunsIndependentTasksAndStopsAtSerialStage(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(map[bool]string{false: "串行", true: "并行"}[parallel], func(t *testing.T) {
			s, _, _, repositoryID := newPipelineTestService(t)
			ctx := context.Background()
			app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "task_isolation", RepositoryID: repositoryID})
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.GetWorkflow(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			good, later := parallelShell("healthy"), parallelShell("later")
			bad := model.WorkflowNode{ID: "bad", Name: "异常部署", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: "missing-plan"}}
			stages := []model.WorkflowStage{{ID: "before", Tasks: []model.WorkflowNode{good}}, {ID: "bad-stage", Tasks: []model.WorkflowNode{bad}}, {ID: "later", Tasks: []model.WorkflowNode{later}}}
			if parallel {
				stages = []model.WorkflowStage{{ID: "parallel", Tasks: []model.WorkflowNode{bad, good}}, {ID: "later", Tasks: []model.WorkflowNode{later}}}
			}
			workflow := *current.Workflow
			workflow.Stages = stages
			frozen, err := s.newResolvedWorkflowRun(ctx, app, &workflow, workflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := parseWorkflowSnapshot(frozen)
			if err != nil || snapshot.TaskFailures[bad.ID] == "" || snapshot.TaskFailures[good.ID] != "" {
				t.Fatal("任务异常范围错误", err)
			}
			if err := s.db.Create(frozen).Error; err != nil {
				t.Fatal(err)
			}
			started, err := s.AdvanceRun(ctx, frozen.ID, "admin", "")
			if err != nil || started.Status != model.PipelineRunRunning {
				t.Fatal("正常任务被整体阻断", err)
			}
			nodes, err := s.ListNodeRuns(ctx, frozen.ID)
			if err != nil {
				t.Fatal(err)
			}
			var healthy model.PipelineNodeRun
			for _, node := range nodes {
				if node.NodeID == good.ID {
					healthy = node
				}
				if node.NodeID == later.ID || node.NodeID == bad.ID && (!parallel || node.Status != model.PipelineRunFailed || node.JobID != "") {
					t.Fatalf("异常或串行任务被错误投递: %+v", node)
				}
			}
			if healthy.JobID == "" || healthy.Status != model.PipelineRunRunning {
				t.Fatal("正常任务没有真实入队")
			}
			execution := *frozen
			execution.NodeExecution, execution.CurrentNodeID, execution.ExecutionJobID = true, good.ID, healthy.JobID
			if err := s.completeNodeExecution(ctx, execution, good, nil, nil); err != nil {
				t.Fatal(err)
			}
			var final model.PipelineRun
			if err := s.db.First(&final, "uuid = ?", frozen.ID).Error; err != nil || final.Status != model.PipelineRunFailed {
				t.Fatal("异常阶段未汇总为失败", err)
			}
			nodes, err = s.ListNodeRuns(ctx, frozen.ID)
			if err != nil || len(nodes) != 2 {
				t.Fatal("跳过正常任务或错误推进下游", nodes, err)
			}
			for _, node := range nodes {
				if node.NodeID == good.ID && node.Status != model.PipelineRunSucceeded || node.NodeID == later.ID {
					t.Fatal("已完成结果丢失或启动了下游", node)
				}
			}
			if err := s.completeNodeExecution(ctx, execution, bad, nil, nil); !errors.Is(err, ErrInvalidWorkflowTransition) {
				t.Fatal("迟到结果覆盖失败任务", err)
			}
		})
	}
}

func TestParallelExecutionFailureDoesNotCancelPendingSibling(t *testing.T) {
	ctx := context.Background()
	first, second, next := parallelShell("failed-task"), parallelShell("healthy-task"), parallelShell("next-stage")
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "parallel", Tasks: []model.WorkflowNode{first, second}}, {ID: "later", Tasks: []model.WorkflowNode{next}}}}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	s.scriptRunner = parallelScriptRunner(func(_ context.Context, input dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
		if strings.Contains(input.Script, first.Config.Script) {
			return dockerengine.ScriptContainerResult{}, errors.New("隔离测试脚本失败")
		}
		return dockerengine.ScriptContainerResult{}, nil
	})
	if _, err := s.AdvanceRun(ctx, run.ID, "admin", ""); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 2 {
		t.Fatal(nodes, err)
	}
	if err := s.ExecuteBuildTask(pipelineExecutionTestContext(ctx, run), BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: first.ID}, nodes[0].JobID); err == nil {
		t.Fatal("失败任务错误报告成功")
	}
	var parent model.PipelineRun
	var sibling model.Job
	if err := db.First(&parent, "uuid = ?", run.ID).Error; err != nil || parent.Status != model.PipelineRunRunning {
		t.Fatal("单任务失败提前结束整条运行", err)
	}
	if err := db.First(&sibling, "uuid = ?", nodes[1].JobID).Error; err != nil || sibling.Status != model.JobPending {
		t.Fatal("其他排队任务被取消", err)
	}
	if err := s.ExecuteBuildTask(pipelineExecutionTestContext(ctx, run), BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: second.ID}, nodes[1].JobID); err != nil {
		t.Fatal("正常并行任务未执行", err)
	}
	if err := db.First(&parent, "uuid = ?", run.ID).Error; err != nil || parent.Status != model.PipelineRunFailed {
		t.Fatal("当前并行阶段结束后未汇总失败", err)
	}
	nodes, err = s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 2 || nodes[0].Status != model.PipelineRunFailed || nodes[1].Status != model.PipelineRunSucceeded {
		t.Fatal("结果丢失或错误推进下游", nodes, err)
	}
}

func TestParallelTerminalFailureKeepsSiblingAndRecordsExactNode(t *testing.T) {
	ctx := context.Background()
	first, second := parallelShell("interrupted"), parallelShell("healthy")
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "parallel", Tasks: []model.WorkflowNode{first, second}}}}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	if _, err := s.AdvanceRun(ctx, run.ID, "admin", ""); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 2 {
		t.Fatal(err)
	}
	var job model.Job
	if err := db.First(&job, "uuid = ?", nodes[0].JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return s.HandleTerminalTaskFailure(ctx, tx, job, "worker_crash", "private-internal-marker")
	}); err != nil {
		t.Fatal(err)
	}
	var parent model.PipelineRun
	var sibling model.Job
	if err := db.First(&parent, "uuid = ?", run.ID).Error; err != nil || parent.Status != model.PipelineRunRunning {
		t.Fatal("终止钩子提前停止其他任务", err)
	}
	if err := db.First(&sibling, "uuid = ?", nodes[1].JobID).Error; err != nil || sibling.Status != model.JobPending {
		t.Fatal("终止钩子取消了独立任务", err)
	}
	logs, _, err := s.ListRunLogs(ctx, run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, log := range logs {
		if log.NodeID == first.ID && log.Stage == "failed" {
			found = true
		}
		if strings.Contains(log.Message, "private-internal-marker") {
			t.Fatal("内部故障信息泄漏")
		}
	}
	if !found {
		t.Fatal("中断日志没有归属准确任务")
	}
	execution := run
	execution.CurrentNodeID, execution.ExecutionJobID, execution.NodeExecution = second.ID, nodes[1].JobID, true
	if err := s.completeNodeExecution(ctx, execution, second, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&parent, "uuid = ?", run.ID).Error; err != nil || parent.Status != model.PipelineRunFailed {
		t.Fatal("终止后正常结果覆盖整体失败", err)
	}
}

func TestFailedSiblingKeepsStageAndRunTimingActive(t *testing.T) {
	now := time.Now().UTC()
	start, end := now.Add(-2*time.Second), now.Add(-time.Second)
	stage := model.PipelineRunGraphStage{ID: "parallel", Tasks: []model.PipelineRunGraphNode{{ID: "failed"}, {ID: "active"}}}
	records := map[string]model.PipelineNodeRun{
		"failed": {NodeID: "failed", StageID: stage.ID, Status: model.PipelineRunFailed, CreatedAt: start, FinishedAt: &end},
		"active": {NodeID: "active", StageID: stage.ID, Status: model.PipelineRunRunning, Stage: "build", CreatedAt: start},
	}
	timing := stageTiming(stage, model.PipelineRunRunning, records, now)
	if timing.State != "running" || !timing.Running || timing.FinishedAt != nil {
		t.Fatal("并行任务仍运行时阶段提前结束", timing)
	}
	records["active"] = model.PipelineNodeRun{NodeID: "active", StageID: stage.ID, Status: model.PipelineRunSucceeded, CreatedAt: start, FinishedAt: &end}
	timing = stageTiming(stage, model.PipelineRunFailed, records, now)
	if timing.State != "failed" || timing.Running || timing.FinishedAt == nil {
		t.Fatal("并行阶段完成后未汇总失败", timing)
	}
}

func TestArtifactSourceFailureOnlyAffectsItsConsumer(t *testing.T) {
	build := model.WorkflowNode{ID: "image", Type: model.WorkflowNodeBuild}
	bad := model.WorkflowNode{ID: "bad-consumer", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{ArtifactNodeID: "missing"}}
	good := model.WorkflowNode{ID: "good-consumer", Type: model.WorkflowNodeDeploy}
	stages := []model.WorkflowStage{{ID: "build", Tasks: []model.WorkflowNode{build}}, {ID: "deploy", Tasks: []model.WorkflowNode{bad, good}}}
	sources, failures := collectArtifactSourcesByTask(stages, false)
	if sources[good.ID] != build.ID || failures[bad.ID] == nil || failures[good.ID] != nil {
		t.Fatal("错误制品来源影响了其他消费任务", sources, failures)
	}
	snapshot := workflowSnapshot{Stages: stages}
	artifact, err := parallelUpstreamArtifact(&snapshot, good, map[string]model.PipelineNodeRun{build.ID: {Type: model.WorkflowNodeBuild, Status: model.PipelineRunSucceeded, ArtifactID: "actual-artifact"}}, "")
	if err != nil || artifact != "actual-artifact" {
		t.Fatal("独立任务无法使用已完成构建的实际制品", artifact, err)
	}
}
