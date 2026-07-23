package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"zrt/internal/model"
	"zrt/internal/repository"
	"zrt/internal/secret"
)

const maxWebhookBodyBytes = 2 * 1024 * 1024

type repositoryHandler struct {
	service *repository.Service
	logger  *slog.Logger
}

type repositoryRequest struct {
	Name              string            `json:"name" binding:"required,max=128"`
	Provider          model.GitProvider `json:"provider" binding:"required,max=16"`
	CloneURL          string            `json:"clone_url" binding:"required,max=1024"`
	DefaultBranch     string            `json:"default_branch" binding:"max=255"`
	AuthType          model.GitAuthType `json:"auth_type" binding:"required,max=16"`
	Username          string            `json:"username" binding:"max=255"`
	Credential        *string           `json:"credential" binding:"omitempty,max=65536"`
	WebhookEnabled    bool              `json:"webhook_enabled"`
	RegenerateWebhook bool              `json:"regenerate_webhook"`
	AllowInsecureHTTP bool              `json:"allow_insecure_http"`
}

type repositoryStatusRequest struct {
	Active *bool `json:"active" binding:"required"`
}

type repositoryResponse struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Provider          model.GitProvider `json:"provider"`
	CloneURL          string            `json:"clone_url"`
	DefaultBranch     string            `json:"default_branch"`
	AuthType          model.GitAuthType `json:"auth_type"`
	Username          string            `json:"username,omitempty"`
	HasCredential     bool              `json:"has_credential"`
	WebhookEnabled    bool              `json:"webhook_enabled"`
	WebhookURL        string            `json:"webhook_url"`
	AllowInsecureHTTP bool              `json:"allow_insecure_http"`
	IsActive          bool              `json:"is_active"`
	CreatedBy         string            `json:"created_by"`
	CreatedAt         string            `json:"created_at"`
	UpdatedAt         string            `json:"updated_at"`
}

func (h repositoryHandler) list(c *gin.Context) {
	repositories, err := h.service.List(c.Request.Context())
	if err != nil {
		h.logger.Error("查询代码仓库列表失败", "operation", "repository_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	result := make([]repositoryResponse, 0, len(repositories))
	for i := range repositories {
		result = append(result, toRepositoryResponse(&repositories[i]))
	}
	c.JSON(http.StatusOK, gin.H{"repositories": result})
}

func (h repositoryHandler) create(c *gin.Context) {
	var request repositoryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建代码仓库请求参数无效", "operation", "repository_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", repository.ErrInvalidRepository.Error())
		return
	}
	actor, _ := currentUser(c)
	repo, webhookSecret, err := h.service.Create(c.Request.Context(), actor.ID, toRepositoryInput(request))
	if err != nil {
		h.writeServiceError(c, "repository_create", err)
		return
	}
	setAuditResourceID(c, repo.ID)
	response := gin.H{"repository": toRepositoryResponse(repo)}
	if webhookSecret != "" {
		response["webhook_secret"] = webhookSecret
	}
	c.JSON(http.StatusCreated, response)
}

func (h repositoryHandler) update(c *gin.Context) {
	var request repositoryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新代码仓库请求参数无效", "operation", "repository_update_bind", "request_id", requestIDFrom(c), "repository_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", repository.ErrInvalidRepository.Error())
		return
	}
	repo, webhookSecret, err := h.service.Update(c.Request.Context(), c.Param("id"), toRepositoryInput(request))
	if err != nil {
		h.writeServiceError(c, "repository_update", err)
		return
	}
	response := gin.H{"repository": toRepositoryResponse(repo)}
	if webhookSecret != "" {
		response["webhook_secret"] = webhookSecret
	}
	c.JSON(http.StatusOK, response)
}

func (h repositoryHandler) setStatus(c *gin.Context) {
	var request repositoryStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改代码仓库状态参数无效", "operation", "repository_status_bind", "request_id", requestIDFrom(c), "repository_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "代码仓库状态格式无效")
		return
	}
	if err := h.service.SetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeServiceError(c, "repository_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h repositoryHandler) testConnection(c *gin.Context) {
	refs, err := h.service.TestConnection(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.logger.Error("测试代码仓库连接失败", "operation", "repository_test", "request_id", requestIDFrom(c), "repository_id", c.Param("id"), "err", err)
		if errors.Is(err, repository.ErrRepositoryNotFound) {
			writeError(c, http.StatusNotFound, "repository_not_found", repository.ErrRepositoryNotFound.Error())
		} else if errors.Is(err, repository.ErrKnownHostsRequired) {
			writeError(c, http.StatusConflict, "known_hosts_required", repository.ErrKnownHostsRequired.Error())
		} else {
			writeError(c, http.StatusBadGateway, "repository_unreachable", "无法连接代码仓库，请检查地址、凭据及网络")
		}
		return
	}
	c.JSON(http.StatusOK, refs)
}

func (h repositoryHandler) listDeliveries(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	deliveries, err := h.service.ListDeliveries(c.Request.Context(), c.Param("id"), limit)
	if err != nil {
		h.logger.Error("查询 Webhook 投递记录失败", "operation", "repository_delivery_list", "request_id", requestIDFrom(c), "repository_id", c.Param("id"), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deliveries": deliveries})
}

func (h repositoryHandler) webhook(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.logger.Warn("读取 Git Webhook 请求失败", "operation", "repository_webhook_read", "request_id", requestIDFrom(c), "repository_id", c.Param("id"), "err", err)
		writeError(c, http.StatusRequestEntityTooLarge, "webhook_too_large", "Webhook 请求内容过大")
		return
	}
	result, err := h.service.HandleWebhook(c.Request.Context(), c.Param("id"), c.Request.Header, body)
	if err != nil {
		h.logger.Warn("处理 Git Webhook 失败", "operation", "repository_webhook", "request_id", requestIDFrom(c), "repository_id", c.Param("id"), "err", err)
		switch {
		case errors.Is(err, repository.ErrRepositoryNotFound), errors.Is(err, repository.ErrWebhookDisabled):
			writeError(c, http.StatusNotFound, "webhook_not_found", "Webhook 不存在或未启用")
		case errors.Is(err, repository.ErrInvalidSignature):
			writeError(c, http.StatusUnauthorized, "invalid_webhook_signature", repository.ErrInvalidSignature.Error())
		case errors.Is(err, repository.ErrUnsupportedEvent):
			c.Status(http.StatusNoContent)
		default:
			writeInternalError(c)
		}
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"delivery_id": result.Delivery.ID, "job_id": result.Delivery.JobID, "duplicate": result.Duplicate,
	})
}

func (h repositoryHandler) writeServiceError(c *gin.Context, operation string, err error) {
	h.logger.Warn("代码仓库操作失败", "operation", operation, "request_id", requestIDFrom(c), "repository_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, repository.ErrInvalidRepository):
		writeError(c, http.StatusBadRequest, "invalid_repository", repository.ErrInvalidRepository.Error())
	case errors.Is(err, repository.ErrInsecureRepository):
		writeError(c, http.StatusBadRequest, "insecure_repository", repository.ErrInsecureRepository.Error())
	case errors.Is(err, repository.ErrInvalidCredential):
		writeError(c, http.StatusBadRequest, "invalid_credential", repository.ErrInvalidCredential.Error())
	case errors.Is(err, repository.ErrRepositoryExists):
		writeError(c, http.StatusConflict, "repository_exists", repository.ErrRepositoryExists.Error())
	case errors.Is(err, repository.ErrRepositoryNotFound):
		writeError(c, http.StatusNotFound, "repository_not_found", repository.ErrRepositoryNotFound.Error())
	case errors.Is(err, secret.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "secrets_unavailable", secret.ErrUnavailable.Error())
	default:
		writeInternalError(c)
	}
}

func toRepositoryInput(request repositoryRequest) repository.Input {
	return repository.Input{
		Name: request.Name, Provider: request.Provider, CloneURL: request.CloneURL,
		DefaultBranch: request.DefaultBranch, AuthType: request.AuthType,
		Username: request.Username, Credential: request.Credential,
		WebhookEnabled: request.WebhookEnabled, RegenerateWebhook: request.RegenerateWebhook,
		AllowInsecureHTTP: request.AllowInsecureHTTP,
	}
}

func toRepositoryResponse(repo *model.GitRepository) repositoryResponse {
	return repositoryResponse{
		ID: repo.ID, Name: repo.Name, Provider: repo.Provider, CloneURL: repo.CloneURL,
		DefaultBranch: repo.DefaultBranch, AuthType: repo.AuthType, Username: repo.Username,
		HasCredential: repo.CredentialCiphertext != "", WebhookEnabled: repo.WebhookEnabled,
		WebhookURL:        "/api/v1/webhooks/git/" + repo.ID,
		AllowInsecureHTTP: repo.AllowInsecureHTTP, IsActive: repo.IsActive,
		CreatedBy: repo.CreatedBy, CreatedAt: repo.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: repo.UpdatedAt.Format(time.RFC3339Nano),
	}
}
