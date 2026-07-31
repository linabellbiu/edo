package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"edo/internal/model"
	"edo/internal/scheduler"
)

type schedulerHandler struct {
	service *scheduler.Service
	logger  *slog.Logger
}

type schedulerRequest struct {
	Name           string               `json:"name" binding:"required,max=128"`
	CronExpression string               `json:"cron_expression" binding:"required,max=128"`
	Timezone       string               `json:"timezone" binding:"omitempty,max=64"`
	Action         model.ScheduleAction `json:"action" binding:"required,max=32"`
	Payload        json.RawMessage      `json:"payload" binding:"required"`
}

func (h schedulerHandler) list(c *gin.Context) {
	items, err := h.service.List(c.Request.Context())
	if err != nil {
		h.logger.Error("查询定时任务失败", "operation", "scheduler_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"schedules": items})
}

func (h schedulerHandler) create(c *gin.Context) {
	var request schedulerRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建定时任务参数无效", "operation", "scheduler_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_schedule", scheduler.ErrInvalidSchedule.Error())
		return
	}
	actor, _ := currentUser(c)
	item, err := h.service.Create(c.Request.Context(), actor.ID, toSchedulerInput(request))
	if err != nil {
		h.writeError(c, "scheduler_create", err)
		return
	}
	setAuditResourceID(c, item.ID)
	c.JSON(http.StatusCreated, gin.H{"schedule": item})
}

func (h schedulerHandler) update(c *gin.Context) {
	var request schedulerRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新定时任务参数无效", "operation", "scheduler_update_bind", "request_id", requestIDFrom(c), "schedule_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_schedule", scheduler.ErrInvalidSchedule.Error())
		return
	}
	item, err := h.service.Update(c.Request.Context(), c.Param("id"), toSchedulerInput(request))
	if err != nil {
		h.writeError(c, "scheduler_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"schedule": item})
}

func (h schedulerHandler) setStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改定时任务状态参数无效", "operation", "scheduler_status_bind", "request_id", requestIDFrom(c), "schedule_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_schedule", scheduler.ErrInvalidSchedule.Error())
		return
	}
	if err := h.service.SetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeError(c, "scheduler_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h schedulerHandler) writeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("定时任务操作失败", "operation", operation, "request_id", requestIDFrom(c), "schedule_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, scheduler.ErrInvalidSchedule):
		writeError(c, http.StatusBadRequest, "invalid_schedule", scheduler.ErrInvalidSchedule.Error())
	case errors.Is(err, scheduler.ErrScheduleExists):
		writeError(c, http.StatusConflict, "schedule_exists", scheduler.ErrScheduleExists.Error())
	case errors.Is(err, scheduler.ErrScheduleNotFound):
		writeError(c, http.StatusNotFound, "schedule_not_found", scheduler.ErrScheduleNotFound.Error())
	default:
		writeInternalError(c)
	}
}

func toSchedulerInput(request schedulerRequest) scheduler.Input {
	return scheduler.Input{
		Name: request.Name, CronExpression: request.CronExpression,
		Timezone: request.Timezone, Action: request.Action, Payload: request.Payload,
	}
}
