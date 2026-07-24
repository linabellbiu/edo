package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"zrt/internal/credential"
	"zrt/internal/model"
	"zrt/internal/secret"
)

type credentialHandler struct {
	service *credential.Service
	logger  *slog.Logger
}

type credentialRequest struct {
	Name     string            `json:"name" binding:"required,max=128"`
	Provider model.GitProvider `json:"provider" binding:"required,max=16"`
	AuthType model.GitAuthType `json:"auth_type" binding:"required,max=16"`
	Username string            `json:"username" binding:"max=255"`
	Secret   *string           `json:"secret" binding:"omitempty,max=65536"`
}

type credentialResponse struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Provider   model.GitProvider `json:"provider"`
	AuthType   model.GitAuthType `json:"auth_type"`
	Username   string            `json:"username,omitempty"`
	SecretHint string            `json:"secret_hint"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`
}

func (h credentialHandler) list(c *gin.Context) {
	actor, _ := currentUser(c)
	items, err := h.service.List(c.Request.Context(), actor.ID)
	if err != nil {
		h.logger.Error("查询个人 Git 令牌失败", "operation", "credential_list", "request_id", requestIDFrom(c), "user_id", actor.ID, "err", err)
		writeInternalError(c)
		return
	}
	result := make([]credentialResponse, 0, len(items))
	for i := range items {
		result = append(result, toCredentialResponse(&items[i]))
	}
	c.JSON(http.StatusOK, gin.H{"credentials": result})
}

func (h credentialHandler) create(c *gin.Context) {
	var request credentialRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Secret == nil {
		h.logger.Warn("创建个人 Git 令牌参数无效", "operation", "credential_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_credential", credential.ErrInvalidCredential.Error())
		return
	}
	actor, _ := currentUser(c)
	item, err := h.service.Create(c.Request.Context(), actor.ID, toCredentialInput(request))
	if err != nil {
		h.writeServiceError(c, "credential_create", err)
		return
	}
	setAuditResourceID(c, item.ID)
	c.JSON(http.StatusCreated, gin.H{"credential": toCredentialResponse(item)})
}

func (h credentialHandler) update(c *gin.Context) {
	var request credentialRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新个人 Git 令牌参数无效", "operation", "credential_update_bind", "request_id", requestIDFrom(c), "credential_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_credential", credential.ErrInvalidCredential.Error())
		return
	}
	actor, _ := currentUser(c)
	item, err := h.service.Update(c.Request.Context(), actor.ID, c.Param("id"), toCredentialInput(request))
	if err != nil {
		h.writeServiceError(c, "credential_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"credential": toCredentialResponse(item)})
}

func (h credentialHandler) remove(c *gin.Context) {
	actor, _ := currentUser(c)
	if err := h.service.Delete(c.Request.Context(), actor.ID, c.Param("id")); err != nil {
		h.writeServiceError(c, "credential_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h credentialHandler) reveal(c *gin.Context) {
	actor, _ := currentUser(c)
	plaintext, err := h.service.RevealOwned(c.Request.Context(), actor.ID, c.Param("id"))
	if err != nil {
		h.writeServiceError(c, "credential_reveal", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"secret": plaintext})
}

func (h credentialHandler) writeServiceError(c *gin.Context, operation string, err error) {
	h.logger.Warn("个人 Git 令牌操作失败", "operation", operation, "request_id", requestIDFrom(c), "credential_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, credential.ErrInvalidCredential):
		writeError(c, http.StatusBadRequest, "invalid_credential", credential.ErrInvalidCredential.Error())
	case errors.Is(err, credential.ErrCredentialExists):
		writeError(c, http.StatusConflict, "credential_exists", credential.ErrCredentialExists.Error())
	case errors.Is(err, credential.ErrCredentialNotFound):
		writeError(c, http.StatusNotFound, "credential_not_found", credential.ErrCredentialNotFound.Error())
	case errors.Is(err, credential.ErrCredentialInUse):
		writeError(c, http.StatusConflict, "credential_in_use", credential.ErrCredentialInUse.Error())
	case errors.Is(err, secret.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "secrets_unavailable", secret.ErrUnavailable.Error())
	default:
		writeInternalError(c)
	}
}

func toCredentialInput(request credentialRequest) credential.Input {
	return credential.Input{
		Name: request.Name, Provider: request.Provider, AuthType: request.AuthType,
		Username: request.Username, Secret: request.Secret,
	}
}

func toCredentialResponse(item *model.GitCredential) credentialResponse {
	return credentialResponse{
		ID: item.ID, Name: item.Name, Provider: item.Provider, AuthType: item.AuthType,
		Username: item.Username, SecretHint: item.SecretHint,
		CreatedAt: item.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: item.UpdatedAt.Format(time.RFC3339Nano),
	}
}
