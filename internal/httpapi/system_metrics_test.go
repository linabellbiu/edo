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

	"github.com/gin-gonic/gin"

	"edo/internal/messaging"
	"edo/internal/systemmetrics"
)

type purgeQueueStub struct {
	purged uint64
	err    error
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
