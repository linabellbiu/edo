package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"zrt/internal/model"
	"zrt/internal/task"
)

type taskHandler struct {
	service *task.Service
	logger  *slog.Logger
}

func (h taskHandler) list(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	items, err := h.service.List(c.Request.Context(), model.JobStatus(c.Query("status")), limit)
	if err != nil {
		h.logger.Error("查询任务失败", "operation", "task_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": items})
}

func (h taskHandler) cancel(c *gin.Context) {
	if err := h.service.Cancel(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, "task_cancel", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h taskHandler) retry(c *gin.Context) {
	job, err := h.service.Retry(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "task_retry", err)
		return
	}
	setAuditResourceID(c, job.ID)
	c.JSON(http.StatusAccepted, gin.H{"task_id": job.ID})
}

func (h taskHandler) writeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("任务操作失败", "operation", operation, "request_id", requestIDFrom(c), "task_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, task.ErrJobNotFound):
		writeError(c, http.StatusNotFound, "task_not_found", task.ErrJobNotFound.Error())
	case errors.Is(err, task.ErrJobState):
		writeError(c, http.StatusConflict, "invalid_task_state", task.ErrJobState.Error())
	case errors.Is(err, task.ErrJobNotRetryable):
		writeError(c, http.StatusConflict, "task_not_retryable", task.ErrJobNotRetryable.Error())
	default:
		writeInternalError(c)
	}
}
