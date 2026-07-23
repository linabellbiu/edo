package scheduler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
	"zrt/internal/notification"
	"zrt/internal/secret"
)

func TestScheduleEnqueuesOnceAndExecutesIdempotently(t *testing.T) {
	service, notifier, db, channelID := newSchedulerTestService(t)
	payload, _ := json.Marshal(NotificationAction{
		ChannelID: channelID, Title: "每日报告", Message: "请检查今日发布状态", Severity: model.NotificationInfo,
	})
	item, err := service.Create(context.Background(), "admin", Input{
		Name: "daily-report", CronExpression: "0 9 * * *", Timezone: "Asia/Shanghai",
		Action: model.ScheduleNotification, Payload: payload,
	})
	if err != nil {
		t.Fatalf("创建定时任务失败: %v", err)
	}
	dueAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := db.Model(&model.ScheduledTask{}).Where("id = ?", item.ID).Update("next_run_at", dueAt).Error; err != nil {
		t.Fatalf("设置定时任务到期时间失败: %v", err)
	}
	for range 2 {
		if err := service.EnqueueDue(context.Background(), time.Now().UTC()); err != nil {
			t.Fatalf("扫描到期定时任务失败: %v", err)
		}
	}
	var jobs []model.Job
	if err := db.Where("kind = ?", "scheduler.execute").Find(&jobs).Error; err != nil || len(jobs) != 1 {
		t.Fatalf("定时任务重复投递: jobs=%d err=%v", len(jobs), err)
	}
	var taskPayload TaskPayload
	if err := json.Unmarshal(jobs[0].Payload, &taskPayload); err != nil {
		t.Fatalf("解析定时任务参数失败: %v", err)
	}
	for range 2 {
		if err := service.Execute(context.Background(), taskPayload); err != nil {
			t.Fatalf("执行定时任务失败: %v", err)
		}
	}
	items, err := notifier.List(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].Source != "scheduler" {
		t.Fatalf("定时通知幂等性错误: notifications=%+v err=%v", items, err)
	}
}

func TestScheduleRejectsNonStandardOrInvalidCron(t *testing.T) {
	service, _, _, channelID := newSchedulerTestService(t)
	payload, _ := json.Marshal(NotificationAction{
		ChannelID: channelID, Title: "报告", Message: "内容", Severity: model.NotificationInfo,
	})
	_, err := service.Create(context.Background(), "admin", Input{
		Name: "too-frequent", CronExpression: "@every 1s", Timezone: "UTC",
		Action: model.ScheduleNotification, Payload: payload,
	})
	if !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("非标准五段 Cron 未被拒绝: %v", err)
	}
}

func newSchedulerTestService(t *testing.T) (*Service, *notification.Service, *gorm.DB, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开定时任务测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移定时任务测试数据库失败: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化定时任务测试密钥失败: %v", err)
	}
	notifier := notification.NewService(db, secretManager, nil, 4)
	endpoint := "https://notify.example.com/zrt"
	channel, err := notifier.CreateChannel(context.Background(), "admin", notification.ChannelInput{
		Name: "scheduler-alerts", Type: model.NotificationChannelWebhook, Endpoint: &endpoint,
	})
	if err != nil {
		t.Fatalf("创建定时任务通知渠道失败: %v", err)
	}
	return NewService(db, notifier, 4, logger), notifier, db, channel.ID
}
