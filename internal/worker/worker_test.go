package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
	"zrt/internal/task"
)

type fakeQueueMessage struct {
	data       []byte
	subject    string
	deliveries uint64
	acked      bool
	naked      bool
	terminated bool
	delay      time.Duration
}

func (m *fakeQueueMessage) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{NumDelivered: m.deliveries}, nil
}
func (m *fakeQueueMessage) Data() []byte                    { return m.data }
func (m *fakeQueueMessage) Subject() string                 { return m.subject }
func (m *fakeQueueMessage) InProgress() error               { return nil }
func (m *fakeQueueMessage) DoubleAck(context.Context) error { m.acked = true; return nil }
func (m *fakeQueueMessage) Term() error                     { m.terminated = true; return nil }
func (m *fakeQueueMessage) NakWithDelay(delay time.Duration) error {
	m.naked = true
	m.delay = delay
	return nil
}

func TestWorkerCompletesTaskAndAcknowledges(t *testing.T) {
	db := openWorkerTestDB(t, "worker_success")
	registry := NewRegistry()
	if err := registry.Register("system.noop", func(context.Context, task.Message) error { return nil }); err != nil {
		t.Fatalf("注册任务处理器失败: %v", err)
	}
	job, message := createWorkerTestJob(t, db, "system.noop", 1)
	worker := newTestWorker(db, registry)
	worker.process(context.Background(), message)

	var stored model.Job
	if err := db.First(&stored, "id = ?", job.ID).Error; err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.Status != model.JobSucceeded || stored.Attempt != 1 || !message.acked {
		t.Fatalf("任务未正确完成: status=%s attempt=%d acked=%v", stored.Status, stored.Attempt, message.acked)
	}
}

func TestWorkerRetriesThenWritesDeadLetter(t *testing.T) {
	db := openWorkerTestDB(t, "worker_retry")
	registry := NewRegistry()
	if err := registry.Register("build.image", func(context.Context, task.Message) error {
		return NewRetryableError("registry_timeout", "镜像仓库暂时不可用", errors.New("dial timeout"))
	}); err != nil {
		t.Fatalf("注册任务处理器失败: %v", err)
	}
	job, first := createWorkerTestJob(t, db, "build.image", 2)
	worker := newTestWorker(db, registry)
	worker.process(context.Background(), first)
	if !first.naked || first.delay != 10*time.Second {
		t.Fatalf("首次临时错误未按退避重投: naked=%v delay=%v", first.naked, first.delay)
	}

	second := &fakeQueueMessage{data: first.data, subject: first.subject, deliveries: 2}
	worker.process(context.Background(), second)
	var stored model.Job
	if err := db.First(&stored, "id = ?", job.ID).Error; err != nil {
		t.Fatalf("读取失败任务失败: %v", err)
	}
	if stored.Status != model.JobFailed || stored.Attempt != 2 || !second.terminated {
		t.Fatalf("耗尽重试后状态错误: status=%s attempt=%d terminated=%v", stored.Status, stored.Attempt, second.terminated)
	}
	var deadCount int64
	if err := db.Model(&model.OutboxEvent{}).Where("aggregate_id = ? AND subject = ?", job.ID, "zrt.dead.task.v1").Count(&deadCount).Error; err != nil {
		t.Fatalf("查询死信 Outbox 失败: %v", err)
	}
	if deadCount != 1 {
		t.Fatalf("死信 Outbox 数量错误: %d", deadCount)
	}
	stats := worker.RuntimeStats()
	if stats.Active != 0 || stats.Executed != 2 || stats.Failed != 2 || stats.Retried != 1 {
		t.Fatalf("Worker 运行统计错误: %+v", stats)
	}
}

func TestWorkerReportsActiveExecution(t *testing.T) {
	db := openWorkerTestDB(t, "worker_active")
	registry := NewRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	if err := registry.Register("system.wait", func(context.Context, task.Message) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("注册任务处理器失败: %v", err)
	}
	_, message := createWorkerTestJob(t, db, "system.wait", 1)
	taskWorker := newTestWorker(db, registry)
	done := make(chan struct{})
	go func() {
		taskWorker.process(context.Background(), message)
		close(done)
	}()

	<-started
	stats := taskWorker.RuntimeStats()
	if stats.Instances != 1 || stats.Capacity != 1 || stats.Active != 1 || stats.Executed != 1 {
		t.Fatalf("执行中的 Worker 统计错误: %+v", stats)
	}
	close(release)
	<-done
	stats = taskWorker.RuntimeStats()
	if stats.Active != 0 || stats.Succeeded != 1 {
		t.Fatalf("执行完成后的 Worker 统计错误: %+v", stats)
	}
}

func TestWorkerProcessorsExecuteTasksConcurrently(t *testing.T) {
	db := openWorkerTestDB(t, "worker_concurrency")
	registry := NewRegistry()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	if err := registry.Register("system.parallel", func(context.Context, task.Message) error {
		started <- struct{}{}
		<-release
		return nil
	}); err != nil {
		t.Fatalf("注册并行任务处理器失败: %v", err)
	}
	_, first := createWorkerTestJob(t, db, "system.parallel", 1)
	_, second := createWorkerTestJob(t, db, "system.parallel", 1)
	taskWorker := newTestWorker(db, registry)
	taskWorker.config.Concurrency = 2
	workQueue := make(chan queueMessage)
	processorsDone := taskWorker.startProcessors(context.Background(), workQueue)

	go func() {
		workQueue <- first
		workQueue <- second
		close(workQueue)
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("两个 Worker 没有并行开始执行任务")
		}
	}
	if stats := taskWorker.RuntimeStats(); stats.Active != 2 || stats.Capacity != 2 {
		close(release)
		t.Fatalf("并行执行统计错误: %+v", stats)
	}
	close(release)
	select {
	case <-processorsDone:
	case <-time.After(2 * time.Second):
		t.Fatal("并行任务没有正常退出")
	}
	if stats := taskWorker.RuntimeStats(); stats.Succeeded != 2 || stats.Active != 0 {
		t.Fatalf("并行任务完成统计错误: %+v", stats)
	}
}

func openWorkerTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开 Worker 测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移 Worker 测试数据库失败: %v", err)
	}
	return db
}

func createWorkerTestJob(t *testing.T, db *gorm.DB, kind string, maxAttempts int) (*model.Job, *fakeQueueMessage) {
	t.Helper()
	service := task.NewService(db, config.DefaultMaxAttempts)
	subject := "zrt.task." + kind
	job, err := service.Create(context.Background(), task.CreateInput{
		Kind: kind, Subject: subject, Payload: map[string]string{"ref": "test"},
		MaxAttempts: maxAttempts, Idempotent: true,
	})
	if err != nil {
		t.Fatalf("创建 Worker 测试任务失败: %v", err)
	}
	var event model.OutboxEvent
	if err := db.Where("aggregate_id = ?", job.ID).First(&event).Error; err != nil {
		t.Fatalf("读取 Worker 测试消息失败: %v", err)
	}
	return job, &fakeQueueMessage{data: event.Payload, subject: subject, deliveries: 1}
}

func newTestWorker(db *gorm.DB, registry *Registry) *Worker {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(db, nil, registry, logger, config.NATS{
		SubjectPrefix: "zrt.task", DeadSubject: "zrt.dead.task.v1",
		MaxAttempts: config.DefaultMaxAttempts, Timeout: time.Second,
	}, config.Worker{
		Concurrency: 1, TaskTimeout: time.Second,
		LeaseDuration: 30 * time.Second, ShutdownTimeout: time.Second,
	})
}
