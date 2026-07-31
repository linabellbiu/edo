package notification

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
)

func TestNotificationSecretsAndSuccessfulDispatch(t *testing.T) {
	client := &fakeHTTPClient{statusCode: http.StatusNoContent}
	service, db := newNotificationTestService(t, client, 4)
	endpoint := "https://notify.example.com/hooks/edo"
	token := "notify-token"
	channel, err := service.CreateChannel(context.Background(), "admin", ChannelInput{
		Name: "production-alerts", Type: model.NotificationChannelWebhook,
		Endpoint: &endpoint, Token: &token,
	})
	if err != nil {
		t.Fatalf("创建通知渠道失败: %v", err)
	}
	if !channel.HasToken {
		t.Fatal("通知渠道未标记已配置 Token")
	}
	var storedChannel model.NotificationChannel
	if err := db.First(&storedChannel, "id = ?", channel.ID).Error; err != nil {
		t.Fatalf("读取通知渠道失败: %v", err)
	}
	if storedChannel.EndpointCiphertext == endpoint || storedChannel.TokenCiphertext == token {
		t.Fatal("通知渠道密钥未加密")
	}
	item, err := service.Enqueue(context.Background(), EnqueueInput{
		ChannelID: channel.ID, Title: "发布完成", Message: "生产 API 已发布",
		Severity: model.NotificationInfo, Source: "deployment", SourceID: "deploy-1",
		DedupeKey: "deployment:deploy-1:succeeded",
	})
	if err != nil {
		t.Fatalf("创建通知任务失败: %v", err)
	}
	if err := service.Dispatch(context.Background(), item.ID, item.JobID); err != nil {
		t.Fatalf("发送通知失败: %v", err)
	}
	if client.request == nil || client.request.Header.Get("Authorization") != "Bearer "+token ||
		client.request.Header.Get("Idempotency-Key") != item.ID {
		t.Fatalf("通知请求头错误: %+v", client.request)
	}
	var sent model.Notification
	if err := db.First(&sent, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("读取通知记录失败: %v", err)
	}
	if sent.Status != model.NotificationSucceeded || sent.SentAt == nil {
		t.Fatalf("通知成功状态错误: %+v", sent)
	}
	duplicate, err := service.Enqueue(context.Background(), EnqueueInput{
		ChannelID: channel.ID, Title: "重复发布", Message: "不应重复",
		Severity: model.NotificationInfo, DedupeKey: "deployment:deploy-1:succeeded",
	})
	if err != nil || duplicate.ID != item.ID {
		t.Fatalf("通知去重失败: notification=%+v err=%v", duplicate, err)
	}
}

func TestNotificationFiniteRetryState(t *testing.T) {
	client := &fakeHTTPClient{statusCode: http.StatusInternalServerError}
	service, db := newNotificationTestService(t, client, 2)
	endpoint := "https://notify.example.com/hooks/edo"
	channel, err := service.CreateChannel(context.Background(), "admin", ChannelInput{
		Name: "retry-alerts", Type: model.NotificationChannelWebhook, Endpoint: &endpoint,
	})
	if err != nil {
		t.Fatalf("创建重试通知渠道失败: %v", err)
	}
	item, err := service.Enqueue(context.Background(), EnqueueInput{
		ChannelID: channel.ID, Title: "告警", Message: "探针失败", Severity: model.NotificationCritical,
	})
	if err != nil {
		t.Fatalf("创建重试通知失败: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := db.Model(&model.Job{}).Where("id = ?", item.JobID).Update("attempt", attempt).Error; err != nil {
			t.Fatalf("设置通知任务执行次数失败: %v", err)
		}
		err := service.Dispatch(context.Background(), item.ID, item.JobID)
		if !IsRetryable(err) {
			t.Fatalf("HTTP 500 应归类为可重试错误: %v", err)
		}
	}
	var failed model.Notification
	if err := db.First(&failed, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("读取失败通知状态失败: %v", err)
	}
	if failed.Status != model.NotificationFailed || failed.Attempts != 2 || strings.Contains(failed.ErrorMessage, "500") {
		t.Fatalf("通知最终失败状态或对外错误内容错误: %+v", failed)
	}
}

type fakeHTTPClient struct {
	statusCode int
	request    *http.Request
}

func (c *fakeHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.request = request.Clone(request.Context())
	return &http.Response{
		StatusCode: c.statusCode, Body: io.NopCloser(strings.NewReader("response")), Header: make(http.Header),
	}, nil
}

func newNotificationTestService(t *testing.T, client HTTPDoer, maxAttempts int) (*Service, *gorm.DB) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开通知测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移通知测试数据库失败: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化通知测试密钥失败: %v", err)
	}
	return NewService(db, secretManager, client, maxAttempts), db
}
