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
	path := "/api/v1/system/metrics/history?window=900"
	if result := performJSONRequest(t, router, http.MethodGet, path, nil, nil); result.Code != http.StatusUnauthorized {
		t.Fatalf("未登录可以读取监控历史: %d", result.Code)
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
	if result := performJSONRequest(t, router, http.MethodGet, path, nil, cookie); result.Code != http.StatusForbidden {
		t.Fatalf("没有查看权限可以读取历史: %d", result.Code)
	}
	if err := dependencies.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Allow: []string{access.PermissionMonitorRead}}); err != nil {
		t.Fatal(err)
	}
	result := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
	var data systemmetrics.History
	if err := json.Unmarshal(result.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if result.Code != http.StatusOK || data.IntervalSeconds != 5 || data.Points == nil || time.Since(data.Latest.CollectedAt) > time.Minute {
		t.Fatalf("历史响应不完整: status=%d data=%+v", result.Code, data)
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
