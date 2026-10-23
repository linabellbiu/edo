package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/outbox"
	"edo/internal/task"
)

type capturedTransportMessage struct {
	subject, eventID string
	payload          []byte
}

type capturingTaskPublisher struct {
	messages []capturedTransportMessage
}

func (p *capturingTaskPublisher) Publish(_ context.Context, subject string, payload []byte, eventID string) error {
	p.messages = append(p.messages, capturedTransportMessage{subject: subject, eventID: eventID, payload: append([]byte(nil), payload...)})
	return nil
}

func TestWorkerAcceptsConfiguredTransportSubject(t *testing.T) {
	db := openWorkerTestDB(t, "worker_configured_transport_subject")
	registry := NewRegistry()
	if err := registry.Register("system.noop", func(context.Context, task.Message) error { return nil }); err != nil {
		t.Fatal(err)
	}
	job, message := createWorkerTestJob(t, db, "system.noop", 1)
	worker := newTestWorker(db, registry)
	worker.nats.SubjectPrefix = "edo.permissions.20261023.task"
	message.subject = worker.nats.SubjectPrefix + ".system.noop"
	worker.process(context.Background(), message)
	var stored model.Job
	if err := db.First(&stored, "uuid = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.JobSucceeded || !message.acked || worker.RuntimeStats().Executed != 1 {
		t.Fatalf("配置主题下的任务没有执行成功：status=%s code=%s acked=%v executed=%d", stored.Status, stored.ErrorCode, message.acked, worker.RuntimeStats().Executed)
	}
}

func TestOutboxToWorkerRespectsConfiguredNamespaceAndRetryScope(t *testing.T) {
	for _, prefix := range []string{"edo.task", "edo.permissions.20261023.task"} {
		t.Run(prefix, func(t *testing.T) {
			db := openWorkerTestDB(t, "worker_outbox_routing_"+prefix)
			projectID := "11111111-1111-1111-1111-111111111111"
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
				ProjectID: projectID, DepartmentID: database.DefaultDepartmentID,
			})
			service := task.NewService(db, 4)
			job, err := service.Create(ctx, task.CreateInput{
				Kind: "pipeline.build", Subject: "edo.task.pipeline.build",
				Payload: map[string]string{"pipeline_run_id": "run-1", "workflow_node_id": "build-1"}, MaxAttempts: 2, Idempotent: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			other, err := service.Create(context.Background(), task.CreateInput{
				ProjectID: "22222222-2222-2222-2222-222222222222", DepartmentID: database.DefaultDepartmentID,
				Kind: "pipeline.build", Subject: "edo.task.pipeline.build", Payload: map[string]string{"pipeline_run_id": "other-run"}, MaxAttempts: 2, Idempotent: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			var original model.OutboxEvent
			if err := db.First(&original, "aggregate_id = ?", job.ID).Error; err != nil {
				t.Fatal(err)
			}
			transport := &capturingTaskPublisher{}
			publisher := outbox.New(db, transport, slog.New(slog.NewTextHandler(io.Discard, nil)), config.NATS{MaxAttempts: 4, SubjectPrefix: prefix})
			if err := publisher.PublishBatch(ctx, 10); err != nil {
				t.Fatal(err)
			}
			if len(transport.messages) != 1 {
				t.Fatalf("范围内应只发布一个项目的任务，实际 %d", len(transport.messages))
			}
			wire := transport.messages[0]
			if wire.subject != prefix+".pipeline.build" || wire.eventID != original.EventID || !bytes.Equal(wire.payload, original.Payload) {
				t.Fatalf("传输主题、事件去重 ID 或载荷不正确：subject=%s event=%s", wire.subject, wire.eventID)
			}
			var envelope task.Message
			if err := json.Unmarshal(wire.payload, &envelope); err != nil || envelope.JobID != job.ID || envelope.MaxAttempts != 2 {
				t.Fatalf("任务封套与重试上限被改变：message=%+v err=%v", envelope, err)
			}
			registry := NewRegistry()
			invoked := 0
			if err := registry.Register("pipeline.build", func(handlerContext context.Context, message task.Message) error {
				invoked++
				scope, ok := database.DepartmentScopeFromContext(handlerContext)
				if !ok || scope.ProjectID != projectID || scope.DepartmentID != job.DepartmentID || scope.AllDepartments || message.JobID != job.ID {
					t.Errorf("处理器未继承冻结项目范围：scope=%+v message=%+v", scope, message)
				}
				if invoked == 1 {
					return NewRetryableError("temporary_build", "构建暂时失败", errors.New("temporary"))
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			worker := newTestWorker(db, registry)
			worker.nats.SubjectPrefix = prefix
			first := &fakeQueueMessage{data: wire.payload, subject: wire.subject, deliveries: 1}
			worker.process(context.Background(), first)
			var pending model.Job
			if err := db.First(&pending, "uuid = ?", job.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !first.naked || pending.Status != model.JobPending || pending.Attempt != 1 || pending.MaxAttempts != 2 || pending.Subject != original.Subject || pending.ProjectID != projectID {
				t.Fatalf("首次失败应保留项目、逻辑主题与重试语义：job=%+v nak=%v", pending, first.naked)
			}
			second := &fakeQueueMessage{data: wire.payload, subject: wire.subject, deliveries: 2}
			worker.process(context.Background(), second)
			var completed, untouched model.Job
			if err := db.First(&completed, "uuid = ?", job.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&untouched, "uuid = ?", other.ID).Error; err != nil {
				t.Fatal(err)
			}
			var published model.OutboxEvent
			if err := db.First(&published, "id = ?", original.ID).Error; err != nil {
				t.Fatal(err)
			}
			if completed.Status != model.JobSucceeded || completed.Attempt != 2 || !second.acked || invoked != 2 || untouched.Status != model.JobPending || untouched.Attempt != 0 {
				t.Fatalf("任务重试完成或项目隔离不正确：status=%s attempt=%d ack=%v invoked=%d other=%s/%d", completed.Status, completed.Attempt, second.acked, invoked, untouched.Status, untouched.Attempt)
			}
			if published.PublishedAt == nil || published.EventID != original.EventID || published.Subject != original.Subject || published.ProjectID != projectID || !bytes.Equal(published.Payload, original.Payload) {
				t.Fatal("Outbox 发布状态、逻辑主题、项目或事件身份被改变")
			}
			if stats := worker.RuntimeStats(); stats.Executed != 2 || stats.Retried != 1 || stats.Succeeded != 1 {
				t.Fatalf("实际执行统计不正确：%+v", stats)
			}
		})
	}
}

func TestWorkerRejectsWrongTransportPrefixAndPreservesDeadSubject(t *testing.T) {
	db := openWorkerTestDB(t, "worker_reject_wrong_transport_prefix")
	registry := NewRegistry()
	if err := registry.Register("system.noop", func(context.Context, task.Message) error {
		t.Error("错误命名空间的消息不能进入处理器")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	job, message := createWorkerTestJob(t, db, "system.noop", 1)
	worker := newTestWorker(db, registry)
	worker.nats.SubjectPrefix = "edo.permissions.20261023.task"
	worker.nats.DeadSubject = "edo.task.dead.v1"
	worker.process(context.Background(), message)
	var stored model.Job
	if err := db.First(&stored, "uuid = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.JobFailed || stored.ErrorCode != "message_mismatch" || !message.terminated || worker.RuntimeStats().Executed != 0 {
		t.Fatalf("错误主题没有被拒绝：status=%s code=%s terminated=%v", stored.Status, stored.ErrorCode, message.terminated)
	}
	var dead model.OutboxEvent
	if err := db.First(&dead, "aggregate_id = ? AND subject = ?", job.ID, worker.nats.DeadSubject).Error; err != nil {
		t.Fatal(err)
	}
	transport := &capturingTaskPublisher{}
	publisher := outbox.New(db, transport, slog.New(slog.NewTextHandler(io.Discard, nil)), worker.nats)
	// 原任务的消息在此测试中由伪队列直接消费，只单独投递新建的死信记录。
	if err := db.Model(&model.OutboxEvent{}).Where("aggregate_id = ? AND subject = ?", job.ID, job.Subject).Update("published_at", stored.FinishedAt).Error; err != nil {
		t.Fatal(err)
	}
	if err := publisher.PublishBatch(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(transport.messages) != 1 || transport.messages[0].subject != worker.nats.DeadSubject || transport.messages[0].eventID != dead.EventID || !bytes.Equal(transport.messages[0].payload, dead.Payload) {
		t.Fatal("死信主题、事件身份或载荷不应被任务映射改变")
	}
}
