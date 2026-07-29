package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"zrt/internal/logging"
)

type systemLogHandler struct {
	service *logging.RuntimeController
	logger  *slog.Logger
}

func (h systemLogHandler) list(c *gin.Context) {
	if h.service == nil {
		h.logger.Error("系统日志服务未初始化", "operation", "system_log_list", "request_id", requestIDFrom(c))
		writeError(c, http.StatusServiceUnavailable, "system_logs_unavailable", "系统日志暂不可用")
		return
	}
	beforeID, err := strconv.ParseUint(strings.TrimSpace(c.DefaultQuery("before_id", "0")), 10, 64)
	if err != nil {
		h.logger.Warn("系统日志游标无效", "operation", "system_log_list", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_system_log_cursor", "系统日志游标无效")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil {
		h.logger.Warn("系统日志分页数量无效", "operation", "system_log_list", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_system_log_filter", "系统日志查询条件无效")
		return
	}
	entries, hasMore, err := h.service.List(logging.Query{
		BeforeID: beforeID,
		Limit:    limit,
		Level:    c.Query("level"),
		Text:     c.Query("query"),
	})
	if err != nil {
		h.logger.Warn("系统日志查询条件无效", "operation", "system_log_list", "request_id", requestIDFrom(c), "err", err)
		if errors.Is(err, logging.ErrInvalidQuery) {
			writeError(c, http.StatusBadRequest, "invalid_system_log_filter", "系统日志查询条件无效")
			return
		}
		writeInternalError(c)
		return
	}
	nextBeforeID := uint64(0)
	if len(entries) > 0 {
		nextBeforeID = entries[len(entries)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{
		"items":          entries,
		"next_before_id": nextBeforeID,
		"has_more":       hasMore,
	})
}
