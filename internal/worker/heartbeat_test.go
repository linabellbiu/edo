package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"edo/internal/model"
	"edo/internal/task"
)

type heartbeatQueueMessage struct {
	*fakeQueueMessage
	progress atomic.Int64
	mu       sync.Mutex
	times    []time.Time
}

func (m *heartbeatQueueMessage) InProgress() error {
	m.mu.Lock()
	m.times = append(m.times, time.Now())
	m.mu.Unlock()
	m.progress.Add(1)
	return nil
}

func TestHeartbeatUsesActualConsumerDeadline(t *testing.T) {
	for _, tt := range []struct {
		name  string
		lease time.Duration
		info  *jetstream.ConsumerInfo
		want  time.Duration
	}{
		{"默认退避首项早于租约心跳", 45 * time.Second, &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{AckWait: 30 * time.Second, BackOff: []time.Duration{10 * time.Second, 30 * time.Second, 120 * time.Second}}}, 10 * time.Second / 3},
		{"服务端实际 AckWait 优先", 45 * time.Second, &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{AckWait: 3 * time.Second}}, time.Second},
		{"更短租约仍及时续期", 15 * time.Second, &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{AckWait: 30 * time.Second}}, 5 * time.Second},
		{"不假设退避递增", 45 * time.Second, &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{AckWait: 30 * time.Second, BackOff: []time.Duration{10 * time.Second, 3 * time.Second, 120 * time.Second}}}, time.Second},
		{"状态缺失", 45 * time.Second, nil, 0},
		{"期限无效", 45 * time.Second, &jetstream.ConsumerInfo{}, 0},
		{"退避无效", 45 * time.Second, &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{AckWait: time.Second, BackOff: []time.Duration{0}}}, 0},
		{"租约无效", 0, &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{AckWait: time.Second}}, 0},
		{"期限不足以计算非零周期", 45 * time.Second, &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{AckWait: time.Nanosecond}}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			worker := newTestWorker(nil, NewRegistry())
			worker.config.LeaseDuration = tt.lease
			err := worker.configureHeartbeat(tt.info)
			if tt.want == 0 {
				if err == nil {
					t.Fatal("无效确认期限不应启动心跳")
				}
				return
			}
			if err != nil || worker.ackPeriod != tt.want {
				t.Fatalf("心跳周期不正确：period=%v want=%v err=%v", worker.ackPeriod, tt.want, err)
			}
		})
	}
}

func TestLongTaskRenewsBeforeEachAckDeadlineAndStopsAfterCompletion(t *testing.T) {
	db := openWorkerTestDB(t, "worker_long_task_ack_progress")
	job, queued := createWorkerTestJob(t, db, "system.noop", 1)
	message := &heartbeatQueueMessage{fakeQueueMessage: queued}
	ackDeadline := 200 * time.Millisecond
	var handlerStarted, handlerFinished time.Time
	registry := NewRegistry()
	if err := registry.Register("system.noop", func(context.Context, task.Message) error {
		handlerStarted = time.Now()
		time.Sleep(5 * ackDeadline)
		handlerFinished = time.Now()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorker(db, registry)
	// 按 45s 租约与 10s 确认窗口同比缩短；旧租约心跳 300ms 会晚于 200ms。
	worker.config.LeaseDuration = 900 * time.Millisecond
	worker.config.TaskTimeout = 5 * time.Second
	if err := worker.configureHeartbeat(&jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{AckWait: time.Second, BackOff: []time.Duration{ackDeadline, 3 * ackDeadline, 12 * ackDeadline}}}); err != nil {
		t.Fatal(err)
	}
	if worker.config.LeaseDuration/3 <= ackDeadline {
		t.Fatal("回归场景没有覆盖旧心跳晚于确认期限")
	}
	worker.process(context.Background(), message)
	message.mu.Lock()
	times := append([]time.Time(nil), message.times...)
	message.mu.Unlock()
	if len(times) < 4 || times[0].After(handlerStarted) {
		t.Fatalf("长任务没有在启动前及运行中续期：count=%d", len(times))
	}
	for index := 1; index < len(times); index++ {
		if gap := times[index].Sub(times[index-1]); gap >= ackDeadline {
			t.Fatalf("运行中存在超过确认期限的心跳间隔：gap=%v deadline=%v", gap, ackDeadline)
		}
	}
	if gap := handlerFinished.Sub(times[len(times)-1]); gap >= ackDeadline {
		t.Fatalf("任务结束前确认已超时：gap=%v", gap)
	}
	var stored model.Job
	if err := db.First(&stored, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.JobSucceeded || stored.Attempt != 1 || !message.acked {
		t.Fatalf("长任务没有按单次执行完成：status=%s attempt=%d acked=%v", stored.Status, stored.Attempt, message.acked)
	}
	count := message.progress.Load()
	time.Sleep(2 * worker.ackPeriod)
	if message.progress.Load() != count {
		t.Fatal("任务完成后心跳仍在运行")
	}
}

func TestWorkerRenewsConsumerAckBeforeHandlerStarts(t *testing.T) {
	db := openWorkerTestDB(t, "worker_initial_ack_progress")
	_, queued := createWorkerTestJob(t, db, "system.noop", 1)
	message := &heartbeatQueueMessage{fakeQueueMessage: queued}
	registry := NewRegistry()
	if err := registry.Register("system.noop", func(context.Context, task.Message) error {
		if message.progress.Load() == 0 {
			t.Error("处理器启动前没有立即续期 Consumer 确认时间")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newTestWorker(db, registry).process(context.Background(), message)
}
