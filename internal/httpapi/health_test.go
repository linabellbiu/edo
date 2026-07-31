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

	"github.com/gin-gonic/gin"

	"edo/internal/config"
	"edo/internal/database"
)

type healthBuilderStub struct {
	err error
}

func (stub healthBuilderStub) PingBuilder(context.Context) error { return stub.err }

type unhealthyDependency struct{}

func (unhealthyDependency) Ping(context.Context) error { return errors.New("依赖不可用") }

func TestReadyAllowsUnavailableOptionalDockerBuilder(t *testing.T) {
	handler, closeDatabase := newHealthTestHandler(t)
	defer closeDatabase()
	handler.builder = healthBuilderStub{err: errors.New("Docker daemon 不可用")}

	response := performHealthReady(handler)
	if response.Code != http.StatusOK {
		t.Fatalf("本地 Docker 不可用时 readiness 不应整体失败: status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析 readiness 响应失败: %v", err)
	}
	if body.Status != "degraded" || body.Checks["builder"] != "unavailable" {
		t.Fatalf("Docker 构建运行时降级状态错误: %+v", body)
	}
}

func TestReadyStillFailsForRequiredDependencies(t *testing.T) {
	handler, closeDatabase := newHealthTestHandler(t)
	defer closeDatabase()
	handler.redis = unhealthyDependency{}

	response := performHealthReady(handler)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("必需依赖不可用时 readiness 应当失败: status=%d body=%s", response.Code, response.Body.String())
	}
}

func newHealthTestHandler(t *testing.T) (healthHandler, func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:health_ready_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开 readiness 测试数据库失败: %v", err)
	}
	sqlDatabase, err := db.DB()
	if err != nil {
		t.Fatalf("获取 readiness 测试数据库连接失败: %v", err)
	}
	return healthHandler{
		database: sqlDatabase,
		redis:    healthyDependency{},
		nats:     healthyDependency{},
		builder:  healthBuilderStub{},
		logger:   logger,
	}, func() { _ = database.Close(db) }
}

func performHealthReady(handler healthHandler) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/health/ready", nil)
	handler.ready(context)
	return response
}
