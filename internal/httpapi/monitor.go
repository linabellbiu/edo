package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"edo/internal/monitor"
	"edo/internal/secret"
)

type monitorHandler struct {
	service *monitor.Service
	logger  *slog.Logger
}

type monitorRequest struct {
	Name                  string  `json:"name" binding:"required,max=128"`
	Endpoint              *string `json:"endpoint"`
	Method                string  `json:"method" binding:"required,max=8"`
	ExpectedStatusMin     int     `json:"expected_status_min" binding:"required,min=100,max=599"`
	ExpectedStatusMax     int     `json:"expected_status_max" binding:"required,min=100,max=599"`
	TimeoutSeconds        int     `json:"timeout_seconds" binding:"required,min=1,max=60"`
	IntervalSeconds       int     `json:"interval_seconds" binding:"required,min=30,max=86400"`
	FailureThreshold      int     `json:"failure_threshold" binding:"required,min=1,max=10"`
	RecoveryThreshold     int     `json:"recovery_threshold" binding:"required,min=1,max=10"`
	NotificationChannelID string  `json:"notification_channel_id" binding:"omitempty,max=36"`
	AllowHTTP             bool    `json:"allow_http"`
}

func (h monitorHandler) list(c *gin.Context) {
	rules, err := h.service.List(c.Request.Context())
	if err != nil {
		h.logger.Error("查询监控规则失败", "operation", "monitor_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"rules": rules})
}

func (h monitorHandler) create(c *gin.Context) {
	var request monitorRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Endpoint == nil {
		h.logger.Warn("创建监控规则参数无效", "operation", "monitor_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_monitor_rule", monitor.ErrInvalidRule.Error())
		return
	}
	actor, _ := currentUser(c)
	rule, err := h.service.Create(c.Request.Context(), actor.ID, toMonitorInput(request))
	if err != nil {
		h.writeError(c, "monitor_create", err)
		return
	}
	setAuditResourceID(c, rule.ID)
	c.JSON(http.StatusCreated, gin.H{"rule": rule})
}

func (h monitorHandler) update(c *gin.Context) {
	var request monitorRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新监控规则参数无效", "operation", "monitor_update_bind", "request_id", requestIDFrom(c), "rule_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_monitor_rule", monitor.ErrInvalidRule.Error())
		return
	}
	rule, err := h.service.Update(c.Request.Context(), c.Param("id"), toMonitorInput(request))
	if err != nil {
		h.writeError(c, "monitor_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"rule": rule})
}

func (h monitorHandler) setStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改监控规则状态参数无效", "operation", "monitor_status_bind", "request_id", requestIDFrom(c), "rule_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_monitor_rule", monitor.ErrInvalidRule.Error())
		return
	}
	if err := h.service.SetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeError(c, "monitor_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h monitorHandler) checks(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	checks, err := h.service.ListChecks(c.Request.Context(), c.Param("id"), limit)
	if err != nil {
		h.logger.Error("查询监控记录失败", "operation", "monitor_check_list", "request_id", requestIDFrom(c), "rule_id", c.Param("id"), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"checks": checks})
}

func (h monitorHandler) writeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("监控操作失败", "operation", operation, "request_id", requestIDFrom(c), "rule_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, monitor.ErrInvalidRule):
		writeError(c, http.StatusBadRequest, "invalid_monitor_rule", monitor.ErrInvalidRule.Error())
	case errors.Is(err, monitor.ErrRuleExists):
		writeError(c, http.StatusConflict, "monitor_rule_exists", monitor.ErrRuleExists.Error())
	case errors.Is(err, monitor.ErrRuleNotFound):
		writeError(c, http.StatusNotFound, "monitor_rule_not_found", monitor.ErrRuleNotFound.Error())
	case errors.Is(err, secret.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "secret_encryption_unavailable", secret.ErrUnavailable.Error())
	default:
		writeInternalError(c)
	}
}

func toMonitorInput(request monitorRequest) monitor.Input {
	return monitor.Input{
		Name: request.Name, Endpoint: request.Endpoint, Method: request.Method,
		ExpectedStatusMin: request.ExpectedStatusMin, ExpectedStatusMax: request.ExpectedStatusMax,
		TimeoutSeconds: request.TimeoutSeconds, IntervalSeconds: request.IntervalSeconds,
		FailureThreshold: request.FailureThreshold, RecoveryThreshold: request.RecoveryThreshold,
		NotificationChannelID: request.NotificationChannelID, AllowHTTP: request.AllowHTTP,
	}
}
