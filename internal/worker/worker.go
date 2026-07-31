package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/messaging"
	"edo/internal/model"
	"edo/internal/task"
)

const ConsumerName = "edo_worker_v1"

type queueMessage interface {
	Metadata() (*jetstream.MsgMetadata, error)
	Data() []byte
	Subject() string
	DoubleAck(context.Context) error
	NakWithDelay(time.Duration) error
	InProgress() error
	Term() error
}

type Worker struct {
	db        *gorm.DB
	messages  *messaging.NATS
	registry  *Registry
	logger    *slog.Logger
	nats      config.NATS
	config    config.Worker
	ownerID   string
	active    atomic.Int64
	executed  atomic.Uint64
	succeeded atomic.Uint64
	failed    atomic.Uint64
	retried   atomic.Uint64
}

type RuntimeStats struct {
	Instances int    `json:"instances"`
	Consumer  string `json:"consumer"`
	Capacity  int    `json:"capacity"`
	Active    int64  `json:"active"`
	Executed  uint64 `json:"executed"`
	Succeeded uint64 `json:"succeeded"`
	Failed    uint64 `json:"failed"`
	Retried   uint64 `json:"retried"`
}

type deadLetter struct {
	Version       int          `json:"version"`
	JobID         string       `json:"job_id,omitempty"`
	Kind          string       `json:"kind,omitempty"`
	Attempt       int          `json:"attempt"`
	MaxAttempts   int          `json:"max_attempts"`
	ErrorCode     string       `json:"error_code"`
	ErrorMessage  string       `json:"error_message"`
	FailedAt      time.Time    `json:"failed_at"`
	Task          task.Message `json:"task,omitempty"`
	SourceSubject string       `json:"source_subject,omitempty"`
	PayloadSHA256 string       `json:"payload_sha256,omitempty"`
}

func New(
	db *gorm.DB,
	messages *messaging.NATS,
	registry *Registry,
	logger *slog.Logger,
	natsConfig config.NATS,
	workerConfig config.Worker,
) *Worker {
	return &Worker{
		db: db, messages: messages, registry: registry, logger: logger,
		nats: natsConfig, config: workerConfig, ownerID: uuid.NewString(),
	}
}

func (w *Worker) Run(ctx context.Context) error {
	consumer, err := w.messages.EnsureConsumer(
		ctx, ConsumerName, w.nats.SubjectPrefix+".>", w.nats.MaxAttempts,
	)
	if err != nil {
		return fmt.Errorf("初始化任务 Consumer 失败: %w", err)
	}
	executionCtx, cancelExecutions := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelExecutions()
	workQueue := make(chan queueMessage)
	processorsDone := w.startProcessors(executionCtx, workQueue)
	consumeContext, err := consumer.Consume(
		func(message jetstream.Msg) {
			select {
			case workQueue <- message:
			case <-ctx.Done():
				w.nak(message, 5*time.Second, "shutdown")
			}
		},
		jetstream.PullMaxMessages(w.config.Concurrency),
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			w.logger.Error("NATS Consumer 接收消息失败", "operation", "worker_consume", "consumer", ConsumerName, "err", err)
		}),
	)
	if err != nil {
		close(workQueue)
		<-processorsDone
		return fmt.Errorf("启动任务 Consumer 失败: %w", err)
	}

	select {
	case <-ctx.Done():
		consumeContext.Drain()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), w.config.ShutdownTimeout)
		defer cancelShutdown()
		select {
		case <-consumeContext.Closed():
		case <-shutdownCtx.Done():
			consumeContext.Stop()
			cancelExecutions()
			return errors.New("Worker 等待任务退出超时")
		}
		close(workQueue)
		select {
		case <-processorsDone:
			return nil
		case <-shutdownCtx.Done():
			cancelExecutions()
			return errors.New("Worker 等待任务退出超时")
		}
	case <-consumeContext.Closed():
		close(workQueue)
		cancelExecutions()
		select {
		case <-processorsDone:
		case <-time.After(w.config.ShutdownTimeout):
			return errors.New("NATS Consumer 停止后仍有任务未退出")
		}
		return errors.New("NATS Consumer 意外停止")
	}
}

func (w *Worker) startProcessors(ctx context.Context, workQueue <-chan queueMessage) <-chan struct{} {
	done := make(chan struct{})
	var processors sync.WaitGroup
	processors.Add(w.config.Concurrency)
	for range w.config.Concurrency {
		go func() {
			defer processors.Done()
			for message := range workQueue {
				w.process(ctx, message)
			}
		}()
	}
	go func() {
		processors.Wait()
		close(done)
	}()
	return done
}

func (w *Worker) process(runContext context.Context, message queueMessage) {
	metadata, err := message.Metadata()
	if err != nil {
		w.logger.Error("读取任务消息元数据失败", "operation", "worker_metadata", "subject", message.Subject(), "err", err)
		w.nak(message, 10*time.Second, "metadata")
		return
	}
	attempt := int(metadata.NumDelivered)
	var taskMessage task.Message
	if err := json.Unmarshal(message.Data(), &taskMessage); err != nil || !validMessage(taskMessage, w.nats.MaxAttempts) {
		w.logger.Error("任务消息格式无效", "operation", "worker_decode", "subject", message.Subject(), "attempt", attempt, "err", err)
		w.handleMalformed(runContext, message, attempt)
		return
	}

	job, claimed, err := w.claim(runContext, taskMessage, attempt)
	if err != nil {
		w.logger.Error("领取任务失败", "operation", "worker_claim", "job_id", taskMessage.JobID, "attempt", attempt, "err", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			w.handleMalformed(runContext, message, attempt)
			return
		}
		w.nak(message, retryDelay(attempt), "claim")
		return
	}
	if !claimed {
		if job.Status == model.JobSucceeded {
			w.ack(message, taskMessage.JobID)
			return
		}
		if job.Status == model.JobFailed || job.Status == model.JobCanceled {
			w.term(message, taskMessage.JobID)
			return
		}
		w.nak(message, leaseBusyRetryDelay(job, time.Now().UTC()), "lease_busy")
		return
	}

	if attempt > job.MaxAttempts {
		w.finishFailure(runContext, message, taskMessage, attempt, "attempts_exhausted", "任务已达到最大执行次数")
		return
	}
	if job.Kind != taskMessage.Kind || job.MaxAttempts != taskMessage.MaxAttempts || job.Subject != message.Subject() {
		w.logger.Error("任务消息与数据库记录不一致", "operation", "worker_message_mismatch", "job_id", taskMessage.JobID, "attempt", attempt)
		w.finishFailure(runContext, message, taskMessage, attempt, "message_mismatch", "任务消息校验失败")
		return
	}

	handler, exists := w.registry.Handler(taskMessage.Kind)
	if !exists {
		w.finishFailure(runContext, message, taskMessage, attempt, "handler_not_found", "任务类型暂不受支持")
		return
	}

	taskContext, cancel := context.WithTimeout(runContext, w.config.TaskTimeout)
	heartbeatDone := make(chan struct{})
	go w.heartbeat(taskContext, message, taskMessage.JobID, heartbeatDone)
	w.active.Add(1)
	w.executed.Add(1)
	executionErr := w.invoke(taskContext, handler, taskMessage)
	w.active.Add(-1)
	cancel()
	<-heartbeatDone

	if executionErr == nil {
		if err := w.finishSuccess(context.WithoutCancel(runContext), taskMessage.JobID); err != nil {
			w.logger.Error("记录任务成功状态失败", "operation", "worker_finish_success", "job_id", taskMessage.JobID, "attempt", attempt, "err", err)
			w.nak(message, retryDelay(attempt), "finish_success")
			return
		}
		w.succeeded.Add(1)
		w.ack(message, taskMessage.JobID)
		return
	}

	w.failed.Add(1)
	code, publicMessage, retryable := classifyError(executionErr)
	w.logger.Error("任务执行失败", "operation", "worker_execute", "job_id", taskMessage.JobID, "kind", taskMessage.Kind, "attempt", attempt, "max_attempts", job.MaxAttempts, "retryable", retryable, "err", executionErr)
	if retryable && attempt < job.MaxAttempts {
		w.retried.Add(1)
		if err := w.scheduleRetry(context.WithoutCancel(runContext), taskMessage.JobID, code, publicMessage); err != nil {
			w.logger.Error("记录任务重试状态失败", "operation", "worker_retry_state", "job_id", taskMessage.JobID, "attempt", attempt, "err", err)
		}
		w.nak(message, retryDelay(attempt), "execution_retry")
		return
	}
	w.finishFailure(context.WithoutCancel(runContext), message, taskMessage, attempt, code, publicMessage)
}

func (w *Worker) RuntimeStats() RuntimeStats {
	return RuntimeStats{
		Instances: 1,
		Consumer:  ConsumerName,
		Capacity:  w.config.Concurrency,
		Active:    w.active.Load(),
		Executed:  w.executed.Load(),
		Succeeded: w.succeeded.Load(),
		Failed:    w.failed.Load(),
		Retried:   w.retried.Load(),
	}
}

func (w *Worker) claim(ctx context.Context, message task.Message, attempt int) (model.Job, bool, error) {
	var job model.Job
	if err := w.db.WithContext(ctx).First(&job, "id = ?", message.JobID).Error; err != nil {
		return job, false, err
	}
	if job.Status == model.JobSucceeded || job.Status == model.JobFailed || job.Status == model.JobCanceled {
		return job, false, nil
	}
	now := time.Now().UTC()
	leaseExpiresAt := now.Add(w.config.LeaseDuration)
	updates := map[string]any{
		"status": model.JobRunning, "attempt": attempt, "lease_owner": w.ownerID,
		"lease_expires_at": leaseExpiresAt, "updated_at": now,
		"error_code": "", "error_message": "", "finished_at": nil,
	}
	if job.StartedAt == nil {
		updates["started_at"] = now
	}
	result := w.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ?", message.JobID).
		Where("status = ? OR (status = ? AND (lease_expires_at IS NULL OR lease_expires_at < ?))", model.JobPending, model.JobRunning, now).
		Updates(updates)
	if result.Error != nil {
		return job, false, result.Error
	}
	if result.RowsAffected == 0 {
		if err := w.db.WithContext(ctx).First(&job, "id = ?", message.JobID).Error; err != nil {
			return job, false, err
		}
		return job, false, nil
	}
	job.Status = model.JobRunning
	job.Attempt = attempt
	job.LeaseOwner = w.ownerID
	job.LeaseExpiresAt = &leaseExpiresAt
	return job, true, nil
}

func (w *Worker) heartbeat(ctx context.Context, message queueMessage, jobID string, done chan<- struct{}) {
	defer close(done)
	interval := w.config.LeaseDuration / 3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := message.InProgress(); err != nil {
				w.logger.Error("续期 NATS 任务确认时间失败", "operation", "worker_ack_progress", "job_id", jobID, "err", err)
			}
			leaseExpiresAt := time.Now().UTC().Add(w.config.LeaseDuration)
			result := w.db.WithContext(ctx).Model(&model.Job{}).
				Where("id = ? AND status = ? AND lease_owner = ?", jobID, model.JobRunning, w.ownerID).
				Updates(map[string]any{"lease_expires_at": leaseExpiresAt, "updated_at": time.Now().UTC()})
			if result.Error != nil {
				w.logger.Error("续期任务数据库租约失败", "operation", "worker_lease_renew", "job_id", jobID, "err", result.Error)
			}
		}
	}
}

func (w *Worker) invoke(ctx context.Context, handler Handler, message task.Message) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			w.logger.Error("任务处理器发生未处理异常", "operation", "worker_panic", "job_id", message.JobID, "kind", message.Kind, "err", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			err = NewPermanentError("handler_panic", "任务处理器发生异常", errors.New("任务处理器 panic"))
		}
	}()
	return handler(ctx, message)
}

func (w *Worker) finishSuccess(ctx context.Context, jobID string) error {
	now := time.Now().UTC()
	result := w.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ? AND status = ? AND lease_owner = ?", jobID, model.JobRunning, w.ownerID).
		Updates(map[string]any{
			"status": model.JobSucceeded, "finished_at": now, "updated_at": now,
			"lease_owner": "", "lease_expires_at": nil, "error_code": "", "error_message": "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("任务租约已失效，不能写入成功状态")
	}
	return nil
}

func (w *Worker) scheduleRetry(ctx context.Context, jobID, code, publicMessage string) error {
	now := time.Now().UTC()
	result := w.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ? AND status = ? AND lease_owner = ?", jobID, model.JobRunning, w.ownerID).
		Updates(map[string]any{
			"status": model.JobPending, "updated_at": now, "lease_owner": "", "lease_expires_at": nil,
			"error_code": code, "error_message": truncate(publicMessage, 255),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("任务租约已失效，不能写入重试状态")
	}
	return nil
}

func (w *Worker) finishFailure(ctx context.Context, queueMessage queueMessage, message task.Message, attempt int, code, publicMessage string) {
	failedAt := time.Now().UTC()
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.Job
		if err := tx.First(&job, "id = ? AND status = ? AND lease_owner = ?", message.JobID, model.JobRunning, w.ownerID).Error; err != nil {
			return err
		}
		authoritativeMessage := task.Message{
			Version: 1, JobID: job.ID, Kind: job.Kind, MaxAttempts: job.MaxAttempts, Payload: json.RawMessage(job.Payload),
		}
		payload, err := json.Marshal(deadLetter{
			Version: 1, JobID: job.ID, Kind: job.Kind, Attempt: attempt,
			MaxAttempts: job.MaxAttempts, ErrorCode: code, ErrorMessage: publicMessage,
			FailedAt: failedAt, Task: authoritativeMessage, SourceSubject: queueMessage.Subject(),
		})
		if err != nil {
			return fmt.Errorf("序列化任务死信失败: %w", err)
		}
		result := tx.Model(&model.Job{}).
			Where("id = ? AND status = ? AND lease_owner = ?", job.ID, model.JobRunning, w.ownerID).
			Updates(map[string]any{
				"status": model.JobFailed, "finished_at": failedAt, "updated_at": failedAt,
				"lease_owner": "", "lease_expires_at": nil,
				"error_code": code, "error_message": truncate(publicMessage, 255),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("任务租约已失效，不能写入失败状态")
		}
		if hook, ok := w.registry.TerminalFailureHook(job.Kind); ok {
			if err := hook(ctx, tx, job, code, publicMessage); err != nil {
				return fmt.Errorf("收敛关联业务状态失败: %w", err)
			}
		}
		return tx.Create(&model.OutboxEvent{
			EventID: uuid.NewString(), AggregateID: job.ID, Subject: w.nats.DeadSubject,
			Payload: datatypes.JSON(payload), NextAttemptAt: failedAt, CreatedAt: failedAt,
		}).Error
	})
	if err != nil {
		w.logger.Error("记录任务失败和死信失败", "operation", "worker_finish_failure", "job_id", message.JobID, "attempt", attempt, "err", err)
		w.nak(queueMessage, retryDelay(attempt), "finish_failure")
		return
	}
	w.term(queueMessage, message.JobID)
}

func (w *Worker) handleMalformed(ctx context.Context, message queueMessage, attempt int) {
	digest := sha256.Sum256(message.Data())
	letter := deadLetter{
		Version: 1, Attempt: attempt, MaxAttempts: w.nats.MaxAttempts,
		ErrorCode: "invalid_message", ErrorMessage: "任务消息格式无效",
		FailedAt: time.Now().UTC(), SourceSubject: message.Subject(),
		PayloadSHA256: hex.EncodeToString(digest[:]),
	}
	payload, err := json.Marshal(letter)
	if err != nil {
		w.logger.Error("序列化无效消息死信失败", "operation", "worker_invalid_encode", "subject", message.Subject(), "err", err)
		w.nak(message, retryDelay(attempt), "invalid_encode")
		return
	}
	messageID := "invalid-" + hex.EncodeToString(digest[:16])
	if err := w.messages.PublishDeadLetter(ctx, payload, messageID); err != nil {
		w.logger.Error("投递无效消息死信失败", "operation", "worker_invalid_dead", "subject", message.Subject(), "attempt", attempt, "err", err)
		w.nak(message, retryDelay(attempt), "invalid_dead")
		return
	}
	w.term(message, "")
}

func (w *Worker) ack(message queueMessage, jobID string) {
	ctx, cancel := context.WithTimeout(context.Background(), w.nats.Timeout)
	defer cancel()
	if err := message.DoubleAck(ctx); err != nil {
		w.logger.Error("确认任务消息失败", "operation", "worker_ack", "job_id", jobID, "err", err)
	}
}

func (w *Worker) nak(message queueMessage, delay time.Duration, reason string) {
	if err := message.NakWithDelay(delay); err != nil {
		w.logger.Error("请求任务消息重投失败", "operation", "worker_nak", "reason", reason, "err", err)
	}
}

func (w *Worker) term(message queueMessage, jobID string) {
	if err := message.Term(); err != nil {
		w.logger.Error("终止任务消息重投失败", "operation", "worker_term", "job_id", jobID, "err", err)
	}
}

func validMessage(message task.Message, maxAttempts int) bool {
	return message.Version == 1 && message.JobID != "" && message.Kind != "" &&
		message.MaxAttempts >= 1 && message.MaxAttempts <= maxAttempts && message.Payload != nil
}

func retryDelay(attempt int) time.Duration {
	delays := []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute}
	if attempt <= 1 {
		return delays[0]
	}
	if attempt > len(delays) {
		return delays[len(delays)-1]
	}
	return delays[attempt-1]
}

func leaseBusyRetryDelay(job model.Job, now time.Time) time.Duration {
	const minimum = 5 * time.Second
	if job.Status != model.JobRunning || job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) {
		return minimum
	}
	// 旧进程崩溃后，消息可能先于数据库租约过期重新投递。等待至租约边界之后再取回，
	// 避免短间隔 NAK 提前耗尽 JetStream 的有限投递次数，使终态失败钩子没有机会执行。
	delay := job.LeaseExpiresAt.Sub(now) + time.Second
	if delay < minimum {
		return minimum
	}
	return delay
}

func truncate(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
