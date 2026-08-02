package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"edo/internal/dockerengine"
	hostmanager "edo/internal/host"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/secret"
)

type hostHandler struct {
	service *hostmanager.Service
	logger  *slog.Logger
}

type hostRequest struct {
	Name                string                     `json:"name" binding:"required,max=128"`
	Address             string                     `json:"address" binding:"max=253"`
	SSHPort             int                        `json:"ssh_port" binding:"omitempty,min=1,max=65535"`
	SSHUsername         string                     `json:"ssh_username" binding:"max=255"`
	SSHAuthType         model.SSHAuthType          `json:"ssh_auth_type" binding:"max=16"`
	SSH                 *dockerengine.SSHBundle    `json:"ssh"`
	Password            string                     `json:"password" binding:"max=4096"`
	PrivateKey          string                     `json:"private_key" binding:"max=1048576"`
	Passphrase          string                     `json:"passphrase" binding:"max=4096"`
	UseSudo             *bool                      `json:"use_sudo"`
	CapabilityKinds     []model.HostCapabilityKind `json:"capability_kinds" binding:"max=4"`
	KubernetesClusterID string                     `json:"kubernetes_cluster_id" binding:"max=36"`
	TestToken           string                     `json:"test_token" binding:"max=64"`
	ReuseCredential     bool                       `json:"reuse_credential"`
}

type hostCapabilityResponse struct {
	Kind      model.HostCapabilityKind   `json:"kind"`
	RuntimeID string                     `json:"runtime_id"`
	Status    model.HostCapabilityStatus `json:"status"`
	Version   string                     `json:"version,omitempty"`
	UseSudo   bool                       `json:"use_sudo"`
}

type hostResponse struct {
	ID                    string                         `json:"id"`
	Name                  string                         `json:"name"`
	Mode                  model.HostMode                 `json:"mode"`
	Address               string                         `json:"address"`
	SSHPort               int                            `json:"ssh_port"`
	SSHUsername           string                         `json:"ssh_username"`
	SSHAuthType           model.SSHAuthType              `json:"ssh_auth_type"`
	SSHHostKeyFingerprint string                         `json:"ssh_host_key_fingerprint,omitempty"`
	Architecture          model.HostArchitecture         `json:"architecture,omitempty"`
	EnvironmentID         string                         `json:"environment_id"`
	EnvironmentIDs        []string                       `json:"environment_ids"`
	IsBuiltin             bool                           `json:"is_builtin"`
	IsActive              bool                           `json:"is_active"`
	Capabilities          []hostCapabilityResponse       `json:"capabilities"`
	CapabilityOptions     []hostmanager.CapabilityOption `json:"capability_options,omitempty"`
	CredentialConfigured  bool                           `json:"credential_configured"`
	CreatedBy             string                         `json:"created_by"`
	CreatedAt             time.Time                      `json:"created_at"`
	UpdatedAt             time.Time                      `json:"updated_at"`
}

type hostStatusResponse struct {
	ID           string                   `json:"id"`
	IsActive     bool                     `json:"is_active"`
	Capabilities []hostCapabilityResponse `json:"capabilities"`
}

func (h hostHandler) list(c *gin.Context) {
	hosts, err := h.service.List(c.Request.Context())
	if err != nil {
		h.logger.Error("查询主机列表失败", "operation", "host_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	response := make([]hostResponse, 0, len(hosts))
	for i := range hosts {
		response = append(response, toHostResponse(hosts[i]))
	}
	c.JSON(http.StatusOK, gin.H{"hosts": response})
}

func (h hostHandler) listStatuses(c *gin.Context) {
	statuses, err := h.service.ListStatuses(c.Request.Context())
	if err != nil {
		h.logger.Error("查询主机状态失败", "operation", "host_status_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	response := make([]hostStatusResponse, 0, len(statuses))
	for i := range statuses {
		response = append(response, hostStatusResponse{
			ID: statuses[i].HostID, IsActive: statuses[i].IsActive,
			Capabilities: toHostCapabilityResponses(statuses[i].Capabilities),
		})
	}
	c.JSON(http.StatusOK, gin.H{"hosts": response})
}

func (h hostHandler) get(c *gin.Context) {
	current, err := h.service.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "host_get", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"host": toHostResponse(*current)})
}

func (h hostHandler) ping(c *gin.Context) {
	capability := model.HostCapabilityKind(strings.TrimSpace(c.Query("capability")))
	if capability != "" && capability != model.HostCapabilitySSH && capability != model.HostCapabilityDocker &&
		capability != model.HostCapabilityKubernetes && capability != model.HostCapabilityLocalExec {
		h.logger.Warn("主机能力检测参数无效", "operation", "host_ping_bind", "request_id", requestIDFrom(c), "host_id", c.Param("id"), "capability", capability)
		writeError(c, http.StatusBadRequest, "invalid_host_capability", hostmanager.ErrInvalidHost.Error())
		return
	}
	result, err := h.service.Ping(c.Request.Context(), c.Param("id"), capability)
	if err != nil {
		h.writeError(c, "host_ping", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h hostHandler) testExisting(c *gin.Context) {
	var request hostRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("测试已有主机参数无效", "operation", "host_update_test_bind", "request_id", requestIDFrom(c), "host_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", hostmanager.ErrInvalidHost.Error())
		return
	}
	result, err := h.service.TestExisting(c.Request.Context(), c.Param("id"), toHostInput(request))
	if err != nil {
		h.writeError(c, "host_update_test", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h hostHandler) test(c *gin.Context) {
	var request hostRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("测试主机参数无效", "operation", "host_test_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", hostmanager.ErrInvalidHost.Error())
		return
	}
	result, err := h.service.Test(c.Request.Context(), toHostInput(request))
	if err != nil {
		h.writeError(c, "host_test", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h hostHandler) create(c *gin.Context) {
	var request hostRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建主机参数无效", "operation", "host_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", hostmanager.ErrInvalidHost.Error())
		return
	}
	actor, _ := currentUser(c)
	current, err := h.service.Create(c.Request.Context(), actor.ID, toHostInput(request))
	if err != nil {
		h.writeError(c, "host_create", err)
		return
	}
	setAuditResourceID(c, current.Host.ID)
	c.JSON(http.StatusCreated, gin.H{"host": toHostResponse(*current)})
}

func (h hostHandler) update(c *gin.Context) {
	var request hostRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新主机参数无效", "operation", "host_update_bind", "request_id", requestIDFrom(c), "host_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", hostmanager.ErrInvalidHost.Error())
		return
	}
	current, err := h.service.Update(c.Request.Context(), c.Param("id"), toHostInput(request))
	if err != nil {
		h.writeError(c, "host_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"host": toHostResponse(*current)})
}

func (h hostHandler) setStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改主机状态参数无效", "operation", "host_status_bind", "request_id", requestIDFrom(c), "host_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "主机状态格式无效")
		return
	}
	if err := h.service.SetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeError(c, "host_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h hostHandler) remove(c *gin.Context) {
	if err := h.service.Remove(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, "host_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h hostHandler) writeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("主机操作失败", "operation", operation, "request_id", requestIDFrom(c), "host_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, hostmanager.ErrInvalidHost):
		writeError(c, http.StatusBadRequest, "invalid_host", hostmanager.ErrInvalidHost.Error())
	case errors.Is(err, hostmanager.ErrHostTestRequired):
		writeError(c, http.StatusConflict, "host_test_required", hostmanager.ErrHostTestRequired.Error())
	case errors.Is(err, hostmanager.ErrHostExists):
		writeError(c, http.StatusConflict, "host_exists", hostmanager.ErrHostExists.Error())
	case errors.Is(err, hostmanager.ErrHostNotFound):
		writeError(c, http.StatusNotFound, "host_not_found", hostmanager.ErrHostNotFound.Error())
	case errors.Is(err, hostmanager.ErrHostReferenced):
		writeError(c, http.StatusConflict, "host_referenced", hostmanager.ErrHostReferenced.Error())
	case errors.Is(err, hostmanager.ErrCapabilityUnavailable):
		writeError(c, http.StatusConflict, "host_capability_unavailable", hostmanager.ErrCapabilityUnavailable.Error())
	case errors.Is(err, hostmanager.ErrBuiltinHost):
		writeError(c, http.StatusConflict, "builtin_host", hostmanager.ErrBuiltinHost.Error())
	case errors.Is(err, dockerengine.ErrInvalidSSH):
		writeError(c, http.StatusBadRequest, "invalid_host_ssh", dockerengine.ErrInvalidSSH.Error())
	case errors.Is(err, dockerengine.ErrSSHUnreachable):
		writeError(c, http.StatusBadGateway, "host_ssh_unreachable", dockerengine.ErrSSHUnreachable.Error())
	case errors.Is(err, dockerengine.ErrSSHDockerDenied):
		writeError(c, http.StatusBadGateway, "host_docker_unavailable", dockerengine.ErrSSHDockerDenied.Error())
	case errors.Is(err, dockerengine.ErrUnsupportedArchitecture):
		writeError(c, http.StatusConflict, "host_architecture_unsupported", dockerengine.ErrUnsupportedArchitecture.Error())
	case errors.Is(err, kube.ErrClusterNotFound):
		writeError(c, http.StatusBadRequest, "host_kubernetes_cluster_not_found", kube.ErrClusterNotFound.Error())
	case errors.Is(err, secret.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "secrets_unavailable", secret.ErrUnavailable.Error())
	default:
		writeInternalError(c)
	}
}

func toHostInput(request hostRequest) hostmanager.Input {
	ssh := dockerengine.SSHBundle{}
	if request.SSH != nil {
		ssh = *request.SSH
	}
	if request.Password != "" {
		ssh.Password = request.Password
	}
	if request.PrivateKey != "" {
		ssh.PrivateKey = request.PrivateKey
	}
	if request.Passphrase != "" {
		ssh.Passphrase = request.Passphrase
	}
	if request.UseSudo != nil {
		ssh.UseSudo = *request.UseSudo
	}
	return hostmanager.Input{
		Name: request.Name, Address: request.Address, SSHPort: request.SSHPort,
		SSHUsername: request.SSHUsername, SSHAuthType: request.SSHAuthType, SSH: &ssh,
		CapabilityKinds: request.CapabilityKinds, KubernetesClusterID: request.KubernetesClusterID,
		TestToken: request.TestToken, ReuseCredential: request.ReuseCredential, UseSudo: request.UseSudo,
	}
}

func toHostResponse(detail hostmanager.Detail) hostResponse {
	capabilities := toHostCapabilityResponses(detail.Capabilities)
	environmentIDs := detail.EnvironmentIDs
	if environmentIDs == nil {
		environmentIDs = []string{}
	}
	legacyEnvironmentID := ""
	if len(environmentIDs) == 1 {
		legacyEnvironmentID = environmentIDs[0]
	}
	return hostResponse{
		ID: detail.Host.ID, Name: detail.Host.Name, Mode: detail.Host.Mode,
		Address: detail.Host.Address, SSHPort: detail.Host.SSHPort, SSHUsername: detail.Host.SSHUsername,
		SSHAuthType: detail.Host.SSHAuthType, SSHHostKeyFingerprint: detail.Host.SSHHostKeyFingerprint,
		Architecture:  detail.Host.Architecture,
		EnvironmentID: legacyEnvironmentID, EnvironmentIDs: environmentIDs,
		IsBuiltin: detail.Host.IsBuiltin,
		IsActive:  detail.Host.IsActive, Capabilities: capabilities,
		CapabilityOptions:    detail.CapabilityOptions,
		CredentialConfigured: detail.Host.SSHCredentialCiphertext != "",
		CreatedBy:            detail.Host.CreatedBy, CreatedAt: detail.Host.CreatedAt, UpdatedAt: detail.Host.UpdatedAt,
	}
}

func toHostCapabilityResponses(capabilities []model.HostCapability) []hostCapabilityResponse {
	response := make([]hostCapabilityResponse, 0, len(capabilities))
	for i := range capabilities {
		response = append(response, hostCapabilityResponse{
			Kind: capabilities[i].Kind, RuntimeID: capabilities[i].RuntimeID,
			Status: capabilities[i].Status, Version: capabilities[i].Version,
			UseSudo: capabilities[i].UseSudo,
		})
	}
	return response
}
