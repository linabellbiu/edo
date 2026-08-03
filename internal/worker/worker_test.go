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

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/pipeline"
	"edo/internal/task"
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
	var handlerScope database.DepartmentScope
	if err := registry.Register("system.noop", func(ctx context.Context, _ task.Message) error {
		handlerScope, _ = database.DepartmentScopeFromContext(ctx)
		return nil
	}); err != nil {
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
	if handlerScope.DepartmentID != job.DepartmentID || handlerScope.AllDepartments {
		t.Fatalf("Worker 没有使用任务冻结的部门范围: scope=%+v job_department=%s", handlerScope, job.DepartmentID)
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
	var deadEvents []model.OutboxEvent
	if err := db.Where("aggregate_id = ? AND subject = ?", job.ID, "edo.dead.task.v1").Find(&deadEvents).Error; err != nil {
		t.Fatalf("查询死信 Outbox 失败: %v", err)
	}
	if len(deadEvents) != 1 || deadEvents[0].DepartmentID != job.DepartmentID {
		t.Fatalf("死信 Outbox 部门传播错误: events=%+v job_department=%s", deadEvents, job.DepartmentID)
	}
	stats := worker.RuntimeStats()
	if stats.Active != 0 || stats.Executed != 2 || stats.Failed != 2 || stats.Retried != 1 {
		t.Fatalf("Worker 运行统计错误: %+v", stats)
	}
}

func TestAttemptsExhaustedConvergesPipelineAndDeploymentInSameFailure(t *testing.T) {
	db := openWorkerTestDB(t, "worker_pipeline_attempts_exhausted")
	pipelineService := pipeline.NewService(db, nil, nil)
	registry := NewRegistry()
	if err := registry.RegisterTerminalFailureHook("pipeline.deploy", pipelineService.HandleTerminalTaskFailure); err != nil {
		t.Fatalf("注册流水线终止失败处理器失败: %v", err)
	}
	payload := pipeline.DeployTaskPayload{PipelineRunID: "run-interrupted", WorkflowNodeID: "deploy-interrupted"}
	job, err := task.NewService(db, config.DefaultMaxAttempts).Create(context.Background(), task.CreateInput{
		DepartmentID: database.DefaultDepartmentID,
		Kind:         "pipeline.deploy", Subject: "edo.task.pipeline.deploy", Payload: payload,
		MaxAttempts: 1, Idempotent: false,
	})
	if err != nil {
		t.Fatalf("创建流水线部署任务失败: %v", err)
	}
	now := time.Now().UTC()
	repository := model.GitRepository{
		ID: "repository-interrupted", Name: "中断部署仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/interrupted.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	application := model.Application{
		ID: "app-interrupted", Name: "中断部署应用", RepositoryID: repository.ID,
		PollIntervalSeconds: 30, SyncStatus: model.ApplicationSyncIdle, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	run := model.PipelineRun{
		ID: payload.PipelineRunID, ApplicationID: application.ID, Trigger: "manual", Ref: "refs/heads/main",
		CommitSHA: "0123456789012345678901234567890123456789", Status: model.PipelineRunRunning,
		Stage: "deploy", CurrentNodeID: payload.WorkflowNodeID, ExecutionJobID: job.ID,
		WorkflowSnapshot: "{}", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	component := model.PipelineRunRepository{
		ID: "component-interrupted", PipelineRunID: run.ID, RepositoryID: repository.ID,
		Ref: run.Ref, CommitSHA: run.CommitSHA, Status: model.PipelineRunRepositoryReady,
		CreatedAt: now, UpdatedAt: now,
	}
	record := model.DeploymentRecord{
		ID: "deployment-interrupted", PipelineRunID: run.ID, WorkflowNodeID: payload.WorkflowNodeID,
		TargetID: "target", TargetName: "目标", Platform: model.DeploymentDocker, RuntimeID: "runtime",
		WorkloadName: "api", Operation: model.DeploymentRelease, Image: "example.invalid/api:fixed",
		Status: model.DeploymentRunning, RequestedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&repository).Error; err != nil {
			return err
		}
		if err := tx.Create(&application).Error; err != nil {
			return err
		}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		if err := tx.Create(&component).Error; err != nil {
			return err
		}
		return tx.Create(&record).Error
	}); err != nil {
		t.Fatal(err)
	}
	var event model.OutboxEvent
	if err := db.Where("aggregate_id = ?", job.ID).First(&event).Error; err != nil {
		t.Fatal(err)
	}
	message := &fakeQueueMessage{data: event.Payload, subject: job.Subject, deliveries: 2}
	newTestWorker(db, registry).process(context.Background(), message)

	if err := db.First(job, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != model.JobFailed || !message.terminated {
		t.Fatalf("耗尽执行次数后 Job 未终止: job=%+v terminated=%v", job, message.terminated)
	}
	if err := db.First(&run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunFailed || run.Stage != "failed" || run.Message != "部署执行中断，目标状态需人工确认" {
		t.Fatalf("中断的部署 Job 没有收敛流水线: %+v", run)
	}
	if err := db.First(&component, "id = ?", component.ID).Error; err != nil {
		t.Fatal(err)
	}
	if component.Status != model.PipelineRunRepositoryFailed {
		t.Fatalf("中断的部署 Job 没有收敛仓库状态: %+v", component)
	}
	if err := db.First(&record, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.Status != model.DeploymentFailed || record.ErrorCode != "pipeline_task_interrupted" ||
		record.ErrorMessage != "部署执行中断，目标状态需人工确认" || record.FinishedAt == nil {
		t.Fatalf("中断的部署 Job 没有收敛发布记录: %+v", record)
	}
}

func TestAttemptsExhaustedConvergesNonIdempotentBuildTask(t *testing.T) {
	db := openWorkerTestDB(t, "worker_build_attempts_exhausted")
	pipelineService := pipeline.NewService(db, nil, nil)
	registry := NewRegistry()
	if err := registry.RegisterTerminalFailureHook("pipeline.build", pipelineService.HandleTerminalTaskFailure); err != nil {
		t.Fatalf("注册流水线终止失败处理器失败: %v", err)
	}
	payload := pipeline.BuildTaskPayload{PipelineRunID: "run-shell-interrupted", WorkflowNodeID: "shell-interrupted"}
	job, err := task.NewService(db, config.DefaultMaxAttempts).Create(context.Background(), task.CreateInput{
		DepartmentID: database.DefaultDepartmentID,
		Kind:         "pipeline.build", Subject: "edo.task.pipeline.build", Payload: payload,
		MaxAttempts: 1, Idempotent: false,
	})
	if err != nil {
		t.Fatalf("创建 Shell 任务失败: %v", err)
	}
	now := time.Now().UTC()
	repository := model.GitRepository{
		ID: "repository-shell-interrupted", Name: "中断脚本仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/shell-interrupted.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	application := model.Application{
		ID: "app-shell-interrupted", Name: "中断脚本应用", RepositoryID: repository.ID,
		PollIntervalSeconds: 60, SyncStatus: model.ApplicationSyncIdle, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	run := model.PipelineRun{
		ID: payload.PipelineRunID, ApplicationID: application.ID, Trigger: "manual", Ref: "refs/heads/main",
		CommitSHA: "0123456789012345678901234567890123456789", Status: model.PipelineRunRunning,
		Stage: "shell", CurrentNodeID: payload.WorkflowNodeID, ExecutionJobID: job.ID,
		WorkflowSnapshot: "{}", CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	component := model.PipelineRunRepository{
		ID: "component-shell-interrupted", PipelineRunID: run.ID, RepositoryID: repository.ID,
		Ref: run.Ref, CommitSHA: run.CommitSHA, Status: model.PipelineRunRepositoryReady,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, value := range []any{&repository, &application, &run, &component} {
			if err := tx.Create(value).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var event model.OutboxEvent
	if err := db.Where("aggregate_id = ?", job.ID).First(&event).Error; err != nil {
		t.Fatal(err)
	}
	leaseExpiresAt := time.Now().UTC().Add(30 * time.Second)
	if err := db.Model(&model.Job{}).Where("id = ?", job.ID).Updates(map[string]any{
		"status": model.JobRunning, "attempt": 1, "lease_owner": "dead-worker", "lease_expires_at": leaseExpiresAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	taskWorker := newTestWorker(db, registry)
	busyDelivery := &fakeQueueMessage{data: event.Payload, subject: job.Subject, deliveries: 2}
	taskWorker.process(context.Background(), busyDelivery)
	if !busyDelivery.naked || busyDelivery.delay < 25*time.Second {
		t.Fatalf("旧进程租约未过期时不应短间隔耗尽投递次数: naked=%v delay=%v", busyDelivery.naked, busyDelivery.delay)
	}
	expired := time.Now().UTC().Add(-time.Second)
	if err := db.Model(&model.Job{}).Where("id = ?", job.ID).Update("lease_expires_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	terminalDelivery := &fakeQueueMessage{data: event.Payload, subject: job.Subject, deliveries: 3}
	taskWorker.process(context.Background(), terminalDelivery)
	if err := db.First(&run, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunFailed || run.Stage != "failed" || run.Message != "任务执行中断，请重新执行流水线" {
		t.Fatalf("耗尽执行次数后 Shell 流水线仍悬挂: %+v", run)
	}
	if err := db.First(&component, "id = ?", component.ID).Error; err != nil {
		t.Fatal(err)
	}
	if component.Status != model.PipelineRunRepositoryFailed || !terminalDelivery.terminated {
		t.Fatalf("耗尽执行次数后关联状态不完整: component=%+v terminated=%v", component, terminalDelivery.terminated)
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
	subject := "edo.task." + kind
	job, err := service.Create(context.Background(), task.CreateInput{
		DepartmentID: database.DefaultDepartmentID,
		Kind:         kind, Subject: subject, Payload: map[string]string{"ref": "test"},
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
		SubjectPrefix: "edo.task", DeadSubject: "edo.dead.task.v1",
		MaxAttempts: config.DefaultMaxAttempts, Timeout: time.Second,
	}, config.Worker{
		Concurrency: 1, TaskTimeout: time.Second,
		LeaseDuration: 30 * time.Second, ShutdownTimeout: time.Second,
	})
}
