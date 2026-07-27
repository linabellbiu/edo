package httpapi

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type Pinger interface {
	Ping(context.Context) error
}

type builderPinger interface {
	PingBuilder(context.Context) error
}

type healthHandler struct {
	database *sql.DB
	redis    Pinger
	nats     Pinger
	builder  builderPinger
	logger   *slog.Logger
}

func (h healthHandler) live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h healthHandler) ready(c *gin.Context) {
	checks := map[string]string{
		"database": "ok",
		"redis":    "ok",
		"nats":     "ok",
		"builder":  "ok",
	}
	failed := false
	checkCtx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	if err := h.database.PingContext(checkCtx); err != nil {
		failed = true
		checks["database"] = "failed"
		h.logFailure(c, "database", err)
	}
	if err := h.redis.Ping(checkCtx); err != nil {
		failed = true
		checks["redis"] = "failed"
		h.logFailure(c, "redis", err)
	}
	if err := h.nats.Ping(checkCtx); err != nil {
		failed = true
		checks["nats"] = "failed"
		h.logFailure(c, "nats", err)
	}
	if h.builder != nil {
		if err := h.builder.PingBuilder(checkCtx); err != nil {
			failed = true
			checks["builder"] = "failed"
			h.logFailure(c, "builder", err)
		}
	}
	if failed {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":     "failed",
			"message":    "依赖服务未就绪",
			"request_id": requestIDFrom(c),
			"checks":     checks,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "checks": checks})
}

func (h healthHandler) logFailure(c *gin.Context, dependency string, err error) {
	h.logger.Error("依赖服务健康检查失败",
		"operation", "health_ready",
		"request_id", requestIDFrom(c),
		"dependency", dependency,
		"err", err,
	)
}
