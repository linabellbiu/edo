package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/dockerengine"
	"edo/internal/model"
)

type stoppingScriptRunner struct {
	started, canceled, release chan struct{}
	lateSuccess                bool
}

func (r *stoppingScriptRunner) RunScriptContainer(ctx context.Context, _ dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	<-r.release
	if r.lateSuccess {
		return dockerengine.ScriptContainerResult{}, nil
	}
	return dockerengine.ScriptContainerResult{}, ctx.Err()
}

func TestStopExecutingPipelineWaitsForCleanupAndPreventsLateResults(t *testing.T) {
	for index, lateSuccess := range []bool{false, true, false} {
		name := "取消错误"
		if lateSuccess {
			name = "迟到成功"
		}
		if index == 2 {
			name = "其他服务实例发起停止"
		}
		t.Run(name, func(t *testing.T) {
			node := model.WorkflowNode{ID: "stop-shell", Type: model.WorkflowNodeShell, Name: "长时间脚本", Config: model.WorkflowNodeConfig{RuntimeImage: model.DefaultRuntimeImage, Script: "sleep 60", TimeoutSeconds: 120}}
			next := model.WorkflowNode{ID: "next", Type: model.WorkflowNodeShell, Name: "不应执行", Config: node.Config}
			service, db, _, run := newBuildExecutorTestRun(t, node, workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node, next}}}}, map[string]string{"README": "停止测试"})
			if err := db.Create(&model.Job{ID: run.ExecutionJobID, Kind: "pipeline.build", Payload: []byte(`{}`), Status: model.JobRunning, MaxAttempts: 4}).Error; err != nil {
				t.Fatal(err)
			}
			runner := &stoppingScriptRunner{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), lateSuccess: lateSuccess}
			defer func() {
				select {
				case <-runner.release:
				default:
					close(runner.release)
				}
			}()
			service.scriptRunner = runner
			stopper := service
			if index == 2 {
				stopper = NewService(db, service.repositories, service.secrets)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- service.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, run.ExecutionJobID)
			}()
			select {
			case <-runner.started:
			case err := <-done:
				t.Fatalf("脚本未开始: %v", err)
			case <-ctx.Done():
				t.Fatal("等待脚本超时")
			}
			stopped, err := stopper.StopRun(ctx, run.ID, "admin")
			if err != nil || stopped.Stage != stoppingStage || stopped.Status != model.PipelineRunRunning {
				t.Fatalf("未进入停止中: %+v %v", stopped, err)
			}
			select {
			case <-runner.canceled:
			case <-ctx.Done():
				t.Fatal("当前任务未收到取消信号")
			}
			if _, err := stopper.StopRun(ctx, run.ID, "admin"); err != nil {
				t.Fatal(err)
			}
			if _, err := service.AdvanceRun(ctx, run.ID, "admin", ""); !errors.Is(err, ErrInvalidWorkflowTransition) {
				t.Fatalf("停止中仍可推进: %v", err)
			}
			close(runner.release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("执行器未退出")
			}
			var stored model.PipelineRun
			if err := db.First(&stored, "id = ?", run.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != model.PipelineRunCanceled || stored.CurrentNodeID != node.ID {
				t.Fatalf("停止结果被覆盖或执行了后续节点: %+v", stored)
			}
			var job model.Job
			if err := db.First(&job, "id = ?", run.ExecutionJobID).Error; err != nil || job.Status != model.JobCanceled {
				t.Fatalf("任务未停止: %+v %v", job, err)
			}
			var logs []model.PipelineRunLog
			db.Where("pipeline_run_id = ?", run.ID).Find(&logs)
			requested, completed := 0, 0
			for _, entry := range logs {
				if entry.Stage == stoppingStage {
					requested++
				}
				if entry.Stage == "canceled" {
					completed++
				}
				if entry.Stage == "failed" || strings.Contains(entry.Message, "系统将自动重试") {
					t.Fatalf("人工停止被误记为失败或重试: %+v", entry)
				}
			}
			if requested != 1 || completed != 1 {
				t.Fatalf("停止日志重复或缺失: %d %d", requested, completed)
			}
			if _, err := service.AdvanceRun(ctx, run.ID, "admin", ""); !errors.Is(err, ErrInvalidWorkflowTransition) {
				t.Fatalf("已停止运行被复活: %v", err)
			}
		})
	}
}

func TestStopQueuedPipelineAndRejectCompletedRun(t *testing.T) {
	node := model.WorkflowNode{ID: "queued-shell", Type: model.WorkflowNodeShell, Config: model.WorkflowNodeConfig{RuntimeImage: model.DefaultRuntimeImage, Script: "true", TimeoutSeconds: 30}}
	service, db, _, run := newBuildExecutorTestRun(t, node, workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}}, nil)
	if err := db.Create(&model.Job{ID: run.ExecutionJobID, Kind: "pipeline.build", Payload: []byte(`{}`), Status: model.JobPending, MaxAttempts: 1}).Error; err != nil {
		t.Fatal(err)
	}
	stopped, err := service.StopRun(context.Background(), run.ID, "admin")
	if err != nil || stopped.Status != model.PipelineRunCanceled {
		t.Fatalf("排队任务未停止: %+v %v", stopped, err)
	}
	if err := service.ExecuteBuildTask(context.Background(), BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, run.ExecutionJobID); err != nil {
		t.Fatal(err)
	}
	var job model.Job
	db.First(&job, "id = ?", run.ExecutionJobID)
	if job.Status != model.JobCanceled {
		t.Fatalf("排队任务仍可领取: %+v", job)
	}
	// 另一个服务实例可能已读到旧快照，不能凭旧状态重新创建执行任务。
	if _, err := service.enqueueBuildExecution(context.Background(), &run, node); !errors.Is(err, ErrInvalidWorkflowTransition) {
		t.Fatalf("迟到任务重新入队: %v", err)
	}
	stale := run
	stale.Status, stale.Stage = model.PipelineRunReady, "task_succeeded"
	if _, err := service.advanceLoadedRun(context.Background(), &stale, "admin", ""); !errors.Is(err, ErrInvalidWorkflowTransition) {
		t.Fatalf("迟到完成覆盖停止: %v", err)
	}
	// 人工放行保留的上一任务 ID 不能使无执行器的运行卡在停止中。
	if err := db.Model(&model.Job{}).Where("id = ?", job.ID).Update("status", model.JobRunning).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.PipelineRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": model.PipelineRunRunning, "stage": "manual"}).Error; err != nil {
		t.Fatal(err)
	}
	stopped, err = service.StopRun(context.Background(), run.ID, "admin")
	if err != nil || stopped.Status != model.PipelineRunCanceled {
		t.Fatalf("人工放行停止卡在旧任务: %+v %v", stopped, err)
	}
	if err := db.Model(&model.PipelineRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": model.PipelineRunSucceeded, "stage": "completed"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.StopRun(context.Background(), run.ID, "admin"); !errors.Is(err, ErrPipelineStopState) {
		t.Fatalf("已完成运行允许停止: %v", err)
	}
}
