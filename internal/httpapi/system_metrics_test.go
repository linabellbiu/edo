package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/messaging"
	"edo/internal/systemmetrics"
)

type purgeQueueStub struct {
	purged uint64
	err    error
}

func TestMetricsHistoryRejectsInvalidRangesAndHidesInternalErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, query := range []string{"?window=9999999999999999999999", "?window=10", "?window=900&after=not-a-time"} {
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/system/metrics/history"+query, nil)
		(systemMetricsHandler{logger: logger}).history(ctx)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("无效范围未拒绝: %s status=%d", query, response.Code)
		}
	}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/system/metrics/history", nil)
	(systemMetricsHandler{logger: logger}).history(ctx)
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusServiceUnavailable || body.Message != systemmetrics.ErrHistoryUnavailable.Error() {
		t.Fatalf("历史错误不是安全的稳定文案: %+v status=%d", body, response.Code)
	}
}

func TestMetricsHistoryRequiresMonitorReadAndDoesNotGrantQueueActions(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	var dependencies Dependencies
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, db *gorm.DB) {
		sqlDB, _ := db.DB()
		deps.SystemMetrics = systemmetrics.New(db, sqlDB, nil, nil, deps.Logger)
		deps.SystemMetrics.ConfigureHistory(client, "test:history", ":8080")
		dependencies = *deps
	})
	defer cleanup()
	const currentPath = "/api/v1/system/metrics"
	const historyPath = currentPath + "/history?window=900"
	for _, path := range []string{currentPath, historyPath} {
		if result := performJSONRequest(t, router, http.MethodGet, path, nil, nil); result.Code != http.StatusUnauthorized {
			t.Fatalf("未登录可以读取监控数据: %s status=%d", path, result.Code)
		}
	}
	user, err := dependencies.Accounts.CreateUser(context.Background(), "metrics-reader", "指标读者", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "metrics-reader", "password": "correct horse battery staple"}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("登录失败: %d", login.Code)
	}
	cookie := login.Result().Cookies()[0]
	for _, path := range []string{currentPath, historyPath} {
		if result := performJSONRequest(t, router, http.MethodGet, path, nil, cookie); result.Code != http.StatusForbidden {
			t.Fatalf("没有查看权限可以读取监控数据: %s status=%d", path, result.Code)
		}
	}
	if err := dependencies.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Allow: []string{access.PermissionMonitorRead}}); err != nil {
		t.Fatal(err)
	}
	cold := performJSONRequest(t, router, http.MethodGet, currentPath, nil, cookie)
	var pending errorResponse
	if err := json.Unmarshal(cold.Body.Bytes(), &pending); err != nil || cold.Code != http.StatusServiceUnavailable || pending.Code != "metrics_pending" {
		t.Fatalf("未采样时应返回明确的等待状态: status=%d code=%s err=%v", cold.Code, pending.Code, err)
	}
	coldHistory := performJSONRequest(t, router, http.MethodGet, historyPath, nil, cookie)
	var history systemmetrics.History
	if err := json.Unmarshal(coldHistory.Body.Bytes(), &history); err != nil || coldHistory.Code != http.StatusOK ||
		history.Latest != nil || history.Points == nil || len(history.Points) != 0 || dependencies.SystemMetrics.Current() != nil {
		t.Fatalf("普通读取提前触发了采样或返回了虚假样本: status=%d data=%+v err=%v", coldHistory.Code, history, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- dependencies.SystemMetrics.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		result := performJSONRequest(t, router, http.MethodGet, historyPath, nil, cookie)
		var data systemmetrics.History
		if err := json.Unmarshal(result.Body.Bytes(), &data); err != nil || result.Code != http.StatusOK || data.IntervalSeconds != 5 || data.Points == nil {
			t.Fatalf("历史响应不完整: status=%d data=%+v err=%v", result.Code, data, err)
		}
		if data.Latest != nil && len(data.Points) > 0 && time.Since(data.Latest.CollectedAt) <= time.Minute {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台采样没有写入监控历史: data=%+v", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := dependencies.SystemMetrics.Current()
	current := performJSONRequest(t, router, http.MethodGet, currentPath, nil, cookie)
	after := dependencies.SystemMetrics.Current()
	var sample systemmetrics.Snapshot
	if err := json.Unmarshal(current.Body.Bytes(), &sample); err != nil || current.Code != http.StatusOK ||
		before == nil || after == nil || sample.CollectedAt.Before(before.CollectedAt) || sample.CollectedAt.After(after.CollectedAt) {
		t.Fatalf("采样完成后应返回后台快照: status=%d data=%+v err=%v", current.Code, sample, err)
	}
	if result := performJSONRequest(t, router, http.MethodDelete, "/api/v1/system/metrics/queue/dead-messages", nil, cookie); result.Code != http.StatusForbidden {
		t.Fatal("查看历史权限意外授予了清空死信能力")
	}
}

func (q *purgeQueueStub) QueueStats(context.Context, string) (messaging.QueueStats, error) {
	return messaging.QueueStats{}, nil
}

func (q *purgeQueueStub) PurgeDeadLetters(context.Context) (uint64, error) {
	return q.purged, q.err
}

func TestPurgeDeadLettersReturnsPurgedCount(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := systemmetrics.New(nil, nil, nil, &purgeQueueStub{purged: 7}, logger)
	handler := systemMetricsHandler{service: service, logger: logger}

	response := performPurgeDeadLetters(handler)
	if response.Code != http.StatusOK {
		t.Fatalf("清空死信队列响应错误: status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		PurgedMessages uint64 `json:"purged_messages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析清空响应失败: %v", err)
	}
	if body.PurgedMessages != 7 {
		t.Fatalf("清空数量错误: %d", body.PurgedMessages)
	}
}

func TestPurgeDeadLettersReturnsSafeError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := systemmetrics.New(nil, nil, nil, &purgeQueueStub{err: errors.New("nats: connection closed")}, logger)
	handler := systemMetricsHandler{service: service, logger: logger}

	response := performPurgeDeadLetters(handler)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("队列异常状态码错误: status=%d body=%s", response.Code, response.Body.String())
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if body.Code != "dead_letter_purge_failed" || body.Message != "清空死信队列失败，请稍后重试" {
		t.Fatalf("对外错误不稳定: %+v", body)
	}
}

func TestRouterRegistersPurgeDeadLetters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(Dependencies{Environment: "test", Logger: logger})
	for _, route := range router.Routes() {
		if route.Method == http.MethodDelete && route.Path == "/api/v1/system/metrics/queue/dead-messages" {
			return
		}
	}
	t.Fatal("未注册清空死信队列路由")
}

func performPurgeDeadLetters(handler systemMetricsHandler) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/system/metrics/queue/dead-messages", nil)
	handler.purgeDeadLetters(context)
	return response
}
