package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"edo/internal/model"
	"edo/internal/pipeline"
	"edo/internal/task"
)

func TestWorkerDoesNotRetryOrDeadLetterStoppedTask(t *testing.T) {
	db := openWorkerTestDB(t, "worker_stop")
	registry := NewRegistry()
	if err := registry.Register("pipeline.build", func(ctx context.Context, message task.Message) error {
		if err := db.WithContext(ctx).Model(&model.Job{}).Where("uuid = ?", message.JobID).Update("status", model.JobCanceled).Error; err != nil {
			return err
		}
		return NewRetryableError("interrupted", "任务中断", errors.New("测试停止后的迟到错误"))
	}); err != nil {
		t.Fatal(err)
	}
	job, message := createWorkerTestJob(t, db, "pipeline.build", 4)
	worker := newTestWorker(db, registry)
	worker.process(context.Background(), message)
	var stored model.Job
	db.First(&stored, "uuid = ?", job.ID)
	if stored.Status != model.JobCanceled || !message.terminated || message.naked || message.acked {
		t.Fatalf("停止任务被重试或覆盖: %+v %+v", stored, message)
	}
	var dead int64
	db.Model(&model.OutboxEvent{}).Where("subject = ?", "edo.dead.task.v1").Count(&dead)
	if dead != 0 || worker.RuntimeStats().Failed != 0 || worker.RuntimeStats().Retried != 0 {
		t.Fatal("主动停止计入失败、死信或重试")
	}
	redelivered := &fakeQueueMessage{data: message.data, subject: message.subject, deliveries: 2}
	worker.process(context.Background(), redelivered)
	if !redelivered.terminated || worker.RuntimeStats().Executed != 1 {
		t.Fatal("停止任务重投后再次执行")
	}
}

func TestStoppedPipelineRecoversWithoutDeadLetterAfterWorkerLoss(t *testing.T) {
	db := openWorkerTestDB(t, "worker_stop_recovery")
	service := pipeline.NewService(db, nil, nil)
	registry := NewRegistry()
	if err := registry.RegisterTerminalFailureHook("pipeline.build", service.HandleTerminalTaskFailure); err != nil {
		t.Fatal(err)
	}
	job, message := createWorkerTestJob(t, db, "pipeline.build", 4)
	for _, record := range []any{
		&model.GitRepository{ID: "stop-repo", Name: "stop_repo", CloneURL: "https://example.com/stop.git"},
		&model.Application{ID: "stop-app", Name: "stop_app", RepositoryID: "stop-repo"},
		&model.PipelineRun{ID: "stop-run", ApplicationID: "stop-app", Status: model.PipelineRunRunning, Stage: "stopping", CurrentNodeID: "build", ExecutionJobID: job.ID},
	} {
		if err := db.Create(record).Error; err != nil {
			t.Fatal(err)
		}
	}
	payload, err := json.Marshal(pipeline.BuildTaskPayload{PipelineRunID: "stop-run", WorkflowNodeID: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Job{}).Where("uuid = ?", job.ID).Update("payload", payload).Error; err != nil {
		t.Fatal(err)
	}
	message.deliveries = 5
	worker := newTestWorker(db, registry)
	worker.process(context.Background(), message)
	var run model.PipelineRun
	if err := db.First(&run, "uuid = ?", "stop-run").Error; err != nil {
		t.Fatal(err)
	}
	var stored model.Job
	if err := db.First(&stored, "uuid = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunCanceled || stored.Status != model.JobCanceled || !message.terminated {
		t.Fatalf("停止请求恢复错误: run=%s job=%s", run.Status, stored.Status)
	}
	var dead int64
	db.Model(&model.OutboxEvent{}).Where("subject = ?", "edo.dead.task.v1").Count(&dead)
	if dead != 0 {
		t.Fatal("已停止的流水线被写入死信")
	}
}
