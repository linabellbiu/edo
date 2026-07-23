package monitor

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

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
	"zrt/internal/notification"
	"zrt/internal/secret"
)

func TestMonitorThresholdAlertRecoveryAndEndpointProtection(t *testing.T) {
	client := &sequenceHTTPClient{statusCodes: []int{500, 503, 200, 204}}
	service, notifier, db, channelID := newMonitorTestService(t, client)
	endpoint := "https://service.example.com/health?access_token=secret"
	rule, err := service.Create(context.Background(), "admin", Input{
		Name: "production-api", Endpoint: &endpoint, Method: http.MethodGet,
		ExpectedStatusMin: 200, ExpectedStatusMax: 299, TimeoutSeconds: 5, IntervalSeconds: 60,
		FailureThreshold: 2, RecoveryThreshold: 2, NotificationChannelID: channelID,
	})
	if err != nil {
		t.Fatalf("创建监控规则失败: %v", err)
	}
	if strings.Contains(rule.Endpoint, "access_token") {
		t.Fatal("监控目标查询参数被 API 回显")
	}
	var stored model.MonitorRule
	if err := db.First(&stored, "id = ?", rule.ID).Error; err != nil {
		t.Fatalf("读取监控规则失败: %v", err)
	}
	if stored.EndpointCiphertext == endpoint || strings.Contains(stored.EndpointDisplay, "access_token") {
		t.Fatal("监控目标未加密或展示地址未脱敏")
	}

	for index := 1; index <= 4; index++ {
		payload := TaskPayload{RuleID: rule.ID, ScheduledAt: time.Now().UTC().Add(time.Duration(index) * time.Minute)}
		if err := service.Execute(context.Background(), payload, "monitor-job-"+string(rune('0'+index))); err != nil {
			t.Fatalf("执行第 %d 次监控探针失败: %v", index, err)
		}
		if index == 1 {
			if err := db.First(&stored, "id = ?", rule.ID).Error; err != nil || stored.Status != model.MonitorUnknown {
				t.Fatalf("首次失败不应立即告警: rule=%+v err=%v", stored, err)
			}
		}
	}
	if err := db.First(&stored, "id = ?", rule.ID).Error; err != nil {
		t.Fatalf("读取最终监控状态失败: %v", err)
	}
	if stored.Status != model.MonitorHealthy || stored.ConsecutiveSuccesses != 2 {
		t.Fatalf("监控恢复状态错误: %+v", stored)
	}
	notifications, err := notifier.List(context.Background(), 10)
	if err != nil || len(notifications) != 2 {
		t.Fatalf("监控告警与恢复通知数量错误: count=%d err=%v", len(notifications), err)
	}
	if notifications[0].Source != "monitor" || notifications[1].Source != "monitor" {
		t.Fatal("监控通知来源错误")
	}
	checks, err := service.ListChecks(context.Background(), rule.ID, 10)
	if err != nil || len(checks) != 4 {
		t.Fatalf("监控检查记录数量错误: count=%d err=%v", len(checks), err)
	}
	for _, check := range checks {
		if strings.Contains(check.ErrorMessage, "500") || strings.Contains(check.ErrorMessage, "503") {
			t.Fatalf("监控记录泄露内部 HTTP 状态细节: %+v", check)
		}
	}
}

func TestMonitorDueRuleOnlyEnqueuesOnce(t *testing.T) {
	service, _, db, _ := newMonitorTestService(t, &sequenceHTTPClient{statusCodes: []int{200}})
	endpoint := "https://service.example.com/health"
	rule, err := service.Create(context.Background(), "admin", Input{
		Name: "enqueue-api", Endpoint: &endpoint, Method: http.MethodHead,
		ExpectedStatusMin: 200, ExpectedStatusMax: 399, TimeoutSeconds: 5, IntervalSeconds: 60,
		FailureThreshold: 1, RecoveryThreshold: 1,
	})
	if err != nil {
		t.Fatalf("创建待扫描监控规则失败: %v", err)
	}
	for range 2 {
		if err := service.EnqueueDue(context.Background(), time.Now().UTC().Add(time.Second)); err != nil {
			t.Fatalf("扫描到期监控规则失败: %v", err)
		}
	}
	var count int64
	if err := db.Model(&model.Job{}).Where("kind = ?", "monitor.check").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("监控任务重复投递: count=%d err=%v rule=%s", count, err, rule.ID)
	}
}

type sequenceHTTPClient struct {
	statusCodes []int
	index       int
}

func (c *sequenceHTTPClient) Do(*http.Request) (*http.Response, error) {
	statusCode := c.statusCodes[c.index]
	if c.index < len(c.statusCodes)-1 {
		c.index++
	}
	return &http.Response{
		StatusCode: statusCode, Body: io.NopCloser(strings.NewReader("probe")), Header: make(http.Header),
	}, nil
}

func newMonitorTestService(t *testing.T, client HTTPDoer) (*Service, *notification.Service, *gorm.DB, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开监控测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移监控测试数据库失败: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化监控测试密钥失败: %v", err)
	}
	notifier := notification.NewService(db, secretManager, nil, 4)
	channelEndpoint := "https://notify.example.com/zrt"
	channel, err := notifier.CreateChannel(context.Background(), "admin", notification.ChannelInput{
		Name: "monitor-alerts", Type: model.NotificationChannelWebhook, Endpoint: &channelEndpoint,
	})
	if err != nil {
		t.Fatalf("创建监控通知渠道失败: %v", err)
	}
	return NewService(db, secretManager, notifier, client, 4, logger), notifier, db, channel.ID
}
