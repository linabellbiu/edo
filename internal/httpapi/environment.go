package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	environmentmanager "edo/internal/environment"
	hostmanager "edo/internal/host"
)

type environmentHandler struct {
	service *environmentmanager.Service
	hosts   *hostmanager.Service
	logger  *slog.Logger
}

type environmentRequest struct {
	Name        string   `json:"name" binding:"required,max=128"`
	Description string   `json:"description" binding:"max=500"`
	HostIDs     []string `json:"host_ids" binding:"max=100"`
}

type environmentProfileRequest struct {
	Name        string `json:"name" binding:"required,max=128"`
	Description string `json:"description" binding:"max=500"`
}

type environmentHostsRequest struct {
	HostIDs *[]string `json:"host_ids" binding:"required,max=100"`
}

type environmentResponse struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	IsActive    bool           `json:"is_active"`
	Hosts       []hostResponse `json:"hosts"`
	CreatedBy   string         `json:"created_by"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func (h environmentHandler) list(c *gin.Context) {
	environments, err := h.service.List(c.Request.Context())
	if err != nil {
		h.logger.Error("查询环境列表失败", "operation", "environment_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	hostDetails, err := h.hostDetails(c)
	if err != nil {
		return
	}
	response := make([]environmentResponse, 0, len(environments))
	for i := range environments {
		response = append(response, toEnvironmentResponse(environments[i], hostDetails))
	}
	c.JSON(http.StatusOK, gin.H{"environments": response})
}

func (h environmentHandler) get(c *gin.Context) {
	current, err := h.service.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "environment_get", err)
		return
	}
	hostDetails, err := h.hostDetails(c)
	if err != nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"environment": toEnvironmentResponse(*current, hostDetails)})
}

func (h environmentHandler) create(c *gin.Context) {
	var request environmentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建环境参数无效", "operation", "environment_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", environmentmanager.ErrInvalidEnvironment.Error())
		return
	}
	actor, _ := currentUser(c)
	current, err := h.service.Create(c.Request.Context(), actor.ID, toEnvironmentInput(request))
	if err != nil {
		h.writeError(c, "environment_create", err)
		return
	}
	setAuditResourceID(c, current.Environment.ID)
	hostDetails, err := h.hostDetails(c)
	if err != nil {
		return
	}
	c.JSON(http.StatusCreated, gin.H{"environment": toEnvironmentResponse(*current, hostDetails)})
}

func (h environmentHandler) update(c *gin.Context) {
	var request environmentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新环境参数无效", "operation", "environment_update_bind", "request_id", requestIDFrom(c), "environment_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", environmentmanager.ErrInvalidEnvironment.Error())
		return
	}
	current, err := h.service.Update(c.Request.Context(), c.Param("id"), toEnvironmentInput(request))
	if err != nil {
		h.writeError(c, "environment_update", err)
		return
	}
	hostDetails, err := h.hostDetails(c)
	if err != nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"environment": toEnvironmentResponse(*current, hostDetails)})
}

func (h environmentHandler) updateProfile(c *gin.Context) {
	var request environmentProfileRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新环境基本信息参数无效", "operation", "environment_profile_update_bind", "request_id", requestIDFrom(c), "environment_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", environmentmanager.ErrInvalidEnvironment.Error())
		return
	}
	current, err := h.service.UpdateProfile(c.Request.Context(), c.Param("id"), environmentmanager.ProfileInput{
		Name: request.Name, Description: request.Description,
	})
	if err != nil {
		h.writeError(c, "environment_profile_update", err)
		return
	}
	h.writeEnvironment(c, current)
}

func (h environmentHandler) replaceHosts(c *gin.Context) {
	var request environmentHostsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("调整环境主机参数无效", "operation", "environment_hosts_update_bind", "request_id", requestIDFrom(c), "environment_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", environmentmanager.ErrInvalidEnvironment.Error())
		return
	}
	current, err := h.service.ReplaceHosts(c.Request.Context(), c.Param("id"), *request.HostIDs)
	if err != nil {
		h.writeError(c, "environment_hosts_update", err)
		return
	}
	h.writeEnvironment(c, current)
}

func (h environmentHandler) setStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改环境状态参数无效", "operation", "environment_status_bind", "request_id", requestIDFrom(c), "environment_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "环境状态格式无效")
		return
	}
	if err := h.service.SetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeError(c, "environment_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h environmentHandler) remove(c *gin.Context) {
	if err := h.service.Remove(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, "environment_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h environmentHandler) hostDetails(c *gin.Context) (map[string]hostmanager.Detail, error) {
	hosts, err := h.hosts.List(c.Request.Context())
	if err != nil {
		h.logger.Error("查询环境主机详情失败", "operation", "environment_host_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return nil, err
	}
	result := make(map[string]hostmanager.Detail, len(hosts))
	for i := range hosts {
		result[hosts[i].Host.ID] = hosts[i]
	}
	return result, nil
}

func (h environmentHandler) writeEnvironment(c *gin.Context, current *environmentmanager.Detail) {
	hostDetails, err := h.hostDetails(c)
	if err != nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"environment": toEnvironmentResponse(*current, hostDetails)})
}

func (h environmentHandler) writeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("环境操作失败", "operation", operation, "request_id", requestIDFrom(c), "environment_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, environmentmanager.ErrInvalidEnvironment):
		writeError(c, http.StatusBadRequest, "invalid_environment", environmentmanager.ErrInvalidEnvironment.Error())
	case errors.Is(err, environmentmanager.ErrEnvironmentExists):
		writeError(c, http.StatusConflict, "environment_exists", environmentmanager.ErrEnvironmentExists.Error())
	case errors.Is(err, environmentmanager.ErrEnvironmentNotFound):
		writeError(c, http.StatusNotFound, "environment_not_found", environmentmanager.ErrEnvironmentNotFound.Error())
	case errors.Is(err, environmentmanager.ErrEnvironmentReferenced):
		writeError(c, http.StatusConflict, "environment_referenced", environmentmanager.ErrEnvironmentReferenced.Error())
	case errors.Is(err, environmentmanager.ErrHostMembershipReferenced):
		writeError(c, http.StatusConflict, "environment_host_referenced", environmentmanager.ErrHostMembershipReferenced.Error())
	case errors.Is(err, environmentmanager.ErrHostNotFound):
		writeError(c, http.StatusBadRequest, "environment_host_not_found", environmentmanager.ErrHostNotFound.Error())
	default:
		writeInternalError(c)
	}
}

func toEnvironmentInput(request environmentRequest) environmentmanager.Input {
	return environmentmanager.Input{
		Name: request.Name, Description: request.Description, HostIDs: request.HostIDs,
	}
}

func toEnvironmentResponse(
	detail environmentmanager.Detail,
	hostDetails map[string]hostmanager.Detail,
) environmentResponse {
	hosts := make([]hostResponse, 0, len(detail.Hosts))
	for i := range detail.Hosts {
		hostDetail, ok := hostDetails[detail.Hosts[i].ID]
		if !ok {
			hostDetail = hostmanager.Detail{Host: detail.Hosts[i]}
		}
		hosts = append(hosts, toHostResponse(hostDetail))
	}
	return environmentResponse{
		ID: detail.Environment.ID, Name: detail.Environment.Name, Description: detail.Environment.Description,
		IsActive: detail.Environment.IsActive, Hosts: hosts,
		CreatedBy: detail.Environment.CreatedBy,
		CreatedAt: detail.Environment.CreatedAt, UpdatedAt: detail.Environment.UpdatedAt,
	}
}
