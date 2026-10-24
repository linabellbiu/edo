package outbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

type batchPublisherFunc func(context.Context, string, []byte, string) error

func (f batchPublisherFunc) Publish(ctx context.Context, subject string, payload []byte, id string) error {
	return f(ctx, subject, payload, id)
}

func TestPublishBatchKeepsIDSnapshotOrderAndHundredEventBoundary(t *testing.T) {
	db := testutil.NewDatabase(t)
	wanted := seedBatchEvents(t, db, maximumPublishBatchSize+1, 256, 0)
	var sent []string
	transport := batchPublisherFunc(func(_ context.Context, _ string, _ []byte, id string) error {
		sent = append(sent, id)
		if len(sent) == 1 {
			// 发布过程中进入的新事件留给下一轮，不得改变已经选好的集合。
			event := model.OutboxEvent{
				EventID: "new-event", AggregateID: "new-job", Subject: "edo.external.updated",
				DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID),
				Payload: datatypes.JSON(`{}`), NextAttemptAt: time.Now().UTC().Add(-time.Minute), CreatedAt: time.Now().UTC(),
			}
			if err := db.Create(&event).Error; err != nil {
				t.Fatalf("创建新到达事件失败: %v", err)
			}
		}
		return nil
	})
	publisher := newBatchTestPublisher(db, transport)
	if err := publisher.PublishBatch(context.Background(), 1000); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sent, wanted[:maximumPublishBatchSize]) {
		t.Fatalf("本轮事件集合、ID顺序或100条边界发生变化: sent=%v", sent)
	}
	var pending int64
	if err := db.Model(&model.OutboxEvent{}).Where("published_at IS NULL").Count(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Fatalf("未选入本轮的事件被提前发布: pending=%d", pending)
	}
}

func TestPublishBatchLoadsSmallPayloadBatchesAndDoesNotRepeatFailures(t *testing.T) {
	db := testutil.NewDatabase(t)
	wanted := seedBatchEvents(t, db, 25, 256, 60*1024)
	var loaded []int
	if err := db.Callback().Query().After("gorm:query").Register("outbox:test_payload_batches", func(tx *gorm.DB) {
		if events, ok := tx.Statement.Dest.(*[]model.OutboxEvent); ok {
			loaded = append(loaded, len(*events))
			for _, event := range *events {
				if event.LastError != "" {
					t.Error("发布读取了没有使用的上次错误大字段")
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	var sent []string
	transport := batchPublisherFunc(func(_ context.Context, _ string, _ []byte, id string) error {
		sent = append(sent, id)
		return errors.New("临时发布失败")
	})
	before := time.Now().UTC()
	if err := newBatchTestPublisher(db, transport).PublishBatch(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sent, wanted) || !slices.Equal(loaded, []int{10, 10, 5}) {
		t.Fatalf("小批加载或失败不重复语义错误: sent=%v loaded=%v", sent, loaded)
	}
	if err := db.Callback().Query().Remove("outbox:test_payload_batches"); err != nil {
		t.Fatal(err)
	}
	var events []model.OutboxEvent
	if err := db.Order("id ASC").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.PublishAttempts != 1 || event.PublishedAt != nil || event.FailedAt != nil ||
			event.NextAttemptAt.Before(before.Add(9*time.Second)) || event.LastError != "临时发布失败" {
			t.Fatalf("发布失败后的退避状态发生变化: event=%s attempts=%d", event.EventID, event.PublishAttempts)
		}
	}
}

func TestPublishBatchStopsLoadingAndPublishingAfterCancellation(t *testing.T) {
	db := testutil.NewDatabase(t)
	seedBatchEvents(t, db, 25, 256, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := 0
	loads := 0
	if err := db.Callback().Query().After("gorm:query").Register("outbox:test_cancel_batches", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]model.OutboxEvent); ok {
			loads++
		}
	}); err != nil {
		t.Fatal(err)
	}
	transport := batchPublisherFunc(func(_ context.Context, _ string, _ []byte, _ string) error {
		sent++
		if sent == 3 {
			cancel()
			return context.Canceled
		}
		return nil
	})
	err := newBatchTestPublisher(db, transport).PublishBatch(ctx, 100)
	if !errors.Is(err, context.Canceled) || sent != 3 || loads != 1 {
		t.Fatalf("取消后仍装载或发送了后续事件: sent=%d loads=%d err=%v", sent, loads, err)
	}
	if err := db.Callback().Query().Remove("outbox:test_cancel_batches"); err != nil {
		t.Fatal(err)
	}
	var published, touched int64
	if err := db.Model(&model.OutboxEvent{}).Where("published_at IS NOT NULL").Count(&published).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.OutboxEvent{}).Where("publish_attempts > 0 OR failed_at IS NOT NULL").Count(&touched).Error; err != nil {
		t.Fatal(err)
	}
	if published != 2 || touched != 0 {
		t.Fatalf("取消改变了已完成或未处理事件: published=%d touched=%d", published, touched)
	}
	sent = 0
	if err := newBatchTestPublisher(db, transport).PublishBatch(ctx, 100); !errors.Is(err, context.Canceled) || sent != 0 {
		t.Fatalf("已经取消的上下文仍发起发布: sent=%d err=%v", sent, err)
	}
}

func TestPublishBatchPreservesExhaustedRetryAndJobFailure(t *testing.T) {
	db := testutil.NewDatabase(t)
	seedBatchEvents(t, db, 1, 256, 0)
	job := model.Job{
		ID: "job-0000", DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID),
		Kind: "system.noop", Subject: "edo.task.system.noop", Payload: datatypes.JSON(`{}`),
		Status: model.JobPending, MaxAttempts: 4, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.OutboxEvent{}).Where("event_id = ?", "event-0000").Update("publish_attempts", 3).Error; err != nil {
		t.Fatal(err)
	}
	sent := 0
	transport := batchPublisherFunc(func(_ context.Context, _ string, _ []byte, id string) error {
		sent++
		if id != "event-0000" {
			t.Fatalf("事件去重 ID 被改变: %s", id)
		}
		return errors.New("临时发布失败")
	})
	if err := newBatchTestPublisher(db, transport).PublishBatch(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var event model.OutboxEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&job, "uuid = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if sent != 1 || event.PublishAttempts != 4 || event.FailedAt == nil || event.PublishedAt != nil ||
		job.Status != model.JobFailed || job.ErrorCode != "queue_publish_failed" {
		t.Fatalf("耗尽重试后的事件或任务状态发生变化: sent=%d attempts=%d job=%s", sent, event.PublishAttempts, job.Status)
	}
}

func newBatchTestPublisher(db *gorm.DB, transport MessagePublisher) *Publisher {
	return New(db, transport, slog.New(slog.NewTextHandler(io.Discard, nil)), config.NATS{MaxAttempts: 4, SubjectPrefix: "edo.task", DeadSubject: "edo.dead.task"})
}

func seedBatchEvents(tb testing.TB, db *gorm.DB, count, payloadBytes, errorBytes int) []string {
	tb.Helper()
	payload := append([]byte(`{"content":"`), bytes.Repeat([]byte("x"), payloadBytes)...)
	payload = append(payload, []byte(`"}`)...)
	lastError := strings.Repeat("e", errorBytes)
	events := make([]model.OutboxEvent, count)
	ids := make([]string, count)
	for i := range events {
		ids[i] = fmt.Sprintf("event-%04d", i)
		events[i] = model.OutboxEvent{
			EventID: ids[i], AggregateID: fmt.Sprintf("job-%04d", i), Subject: "edo.external.updated",
			DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID),
			Payload: datatypes.JSON(payload), LastError: lastError,
			NextAttemptAt: time.Now().UTC().Add(-time.Minute), CreatedAt: time.Now().UTC(),
		}
	}
	if err := db.CreateInBatches(&events, 10).Error; err != nil {
		tb.Fatal(err)
	}
	return ids
}

type heapTrackingPublisher struct {
	peak, base uint64
	sent       int
}

func (p *heapTrackingPublisher) Publish(context.Context, string, []byte, string) error {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	p.peak = max(p.peak, memory.HeapAlloc)
	p.sent++
	return nil
}

// 原实现保留为同负载基准，不参与生产路径或普通测试。
func publishFullPayloadBatchReference(ctx context.Context, publisher *Publisher, limit int) error {
	var events []model.OutboxEvent
	if err := publisher.db.WithContext(ctx).
		Where("published_at IS NULL AND failed_at IS NULL AND next_attempt_at <= ?", time.Now().UTC()).
		Order("id ASC").Limit(limit).Find(&events).Error; err != nil {
		return err
	}
	for i := range events {
		if err := publisher.publishOne(ctx, &events[i]); err != nil {
			return err
		}
	}
	return nil
}

func BenchmarkOutboxBatchPeakMemory(b *testing.B) {
	for _, variant := range []struct {
		name string
		run  func(context.Context, *Publisher, int) error
	}{
		{"original_full_payload", publishFullPayloadBatchReference},
		{"bounded_payload_batches", func(ctx context.Context, publisher *Publisher, limit int) error {
			return publisher.PublishBatch(ctx, limit)
		}},
	} {
		b.Run(variant.name, func(b *testing.B) {
			db := testutil.NewDatabase(b)
			seedBatchEvents(b, db, maximumPublishBatchSize, 256*1024, 60*1024)
			transport := &heapTrackingPublisher{}
			publisher := newBatchTestPublisher(db, transport)
			var highest uint64
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				if err := db.Model(&model.OutboxEvent{}).Where("1 = 1").Updates(map[string]any{
					"published_at": nil, "last_error": strings.Repeat("e", 60*1024),
				}).Error; err != nil {
					b.Fatal(err)
				}
				runtime.GC()
				var memory runtime.MemStats
				runtime.ReadMemStats(&memory)
				transport.base, transport.peak, transport.sent = memory.HeapAlloc, memory.HeapAlloc, 0
				b.StartTimer()
				if err := variant.run(context.Background(), publisher, maximumPublishBatchSize); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				if transport.sent != maximumPublishBatchSize {
					b.Fatalf("同负载基准没有发布完整100条: sent=%d", transport.sent)
				}
				highest = max(highest, transport.peak-transport.base)
			}
			// 每个发布回调采样一次；该值是发布期间观测到的增量堆峰值，不是进程 RSS。
			b.ReportMetric(float64(highest), "sampled_heap_peak_B")
		})
	}
}
