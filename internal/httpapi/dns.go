package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	dnsmanager "zrt/internal/dns"
	"zrt/internal/model"
	"zrt/internal/secret"
)

type dnsHandler struct {
	service *dnsmanager.Service
	logger  *slog.Logger
}

type dnsProviderAccountRequest struct {
	Name              string            `json:"name" binding:"required,max=128"`
	Provider          model.DNSProvider `json:"provider" binding:"required,max=32"`
	Config            map[string]string `json:"config" binding:"required,max=32"`
	ClearSecretFields []string          `json:"clear_secret_fields" binding:"max=32,dive,max=64"`
}

type dnsProviderAccountResponse struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	Provider               model.DNSProvider `json:"provider"`
	Config                 map[string]string `json:"config"`
	ConfiguredSecretFields []string          `json:"configured_secret_fields"`
	IsActive               bool              `json:"is_active"`
	CreatedBy              string            `json:"created_by"`
	CreatedAt              string            `json:"created_at"`
	UpdatedAt              string            `json:"updated_at"`
}

type dnsDomainRequest struct {
	AccountID   string `json:"account_id" binding:"required,max=36"`
	Name        string `json:"name" binding:"required,max=253"`
	Description string `json:"description" binding:"max=512"`
}

type dnsDomainResponse struct {
	ID          string            `json:"id"`
	AccountID   string            `json:"account_id"`
	AccountName string            `json:"account_name"`
	Provider    model.DNSProvider `json:"provider"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	IsActive    bool              `json:"is_active"`
	CreatedBy   string            `json:"created_by"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

type dnsRecordRequest struct {
	Name  string `json:"name" binding:"required,max=253"`
	Type  string `json:"type" binding:"required,max=16"`
	Value string `json:"value" binding:"max=4096"`
	TTL   int64  `json:"ttl" binding:"required"`
}

type dnsStatusRequest struct {
	Active *bool `json:"active" binding:"required"`
}

func (h dnsHandler) listProviders(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"providers": h.service.ProviderCatalog()})
}

func (h dnsHandler) listAccounts(c *gin.Context) {
	accounts, err := h.service.ListProviderAccounts(c.Request.Context())
	if err != nil {
		h.writeServiceError(c, "dns_account_list", err)
		return
	}
	result := make([]dnsProviderAccountResponse, 0, len(accounts))
	for index := range accounts {
		response, err := h.toAccountResponse(&accounts[index])
		if err != nil {
			h.writeServiceError(c, "dns_account_list_config", err)
			return
		}
		result = append(result, response)
	}
	c.JSON(http.StatusOK, gin.H{"accounts": result})
}

func (h dnsHandler) createAccount(c *gin.Context) {
	var request dnsProviderAccountRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建 DNS 厂商账号请求参数无效", "operation", "dns_account_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_dns_account", dnsmanager.ErrInvalidProviderAccount.Error())
		return
	}
	actor, _ := currentUser(c)
	account, err := h.service.CreateProviderAccount(c.Request.Context(), actor.ID, dnsmanager.ProviderAccountInput{
		Name: request.Name, Provider: request.Provider, Config: request.Config, ClearSecretFields: request.ClearSecretFields,
	})
	if err != nil {
		h.writeServiceError(c, "dns_account_create", err)
		return
	}
	response, err := h.toAccountResponse(account)
	if err != nil {
		h.writeServiceError(c, "dns_account_create_response", err)
		return
	}
	setAuditResourceID(c, account.ID)
	c.JSON(http.StatusCreated, gin.H{"account": response})
}

func (h dnsHandler) updateAccount(c *gin.Context) {
	var request dnsProviderAccountRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新 DNS 厂商账号请求参数无效", "operation", "dns_account_update_bind", "request_id", requestIDFrom(c), "account_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_dns_account", dnsmanager.ErrInvalidProviderAccount.Error())
		return
	}
	account, err := h.service.UpdateProviderAccount(c.Request.Context(), c.Param("id"), dnsmanager.ProviderAccountInput{
		Name: request.Name, Provider: request.Provider, Config: request.Config, ClearSecretFields: request.ClearSecretFields,
	})
	if err != nil {
		h.writeServiceError(c, "dns_account_update", err)
		return
	}
	response, err := h.toAccountResponse(account)
	if err != nil {
		h.writeServiceError(c, "dns_account_update_response", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"account": response})
}

func (h dnsHandler) setAccountStatus(c *gin.Context) {
	var request dnsStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改 DNS 厂商账号状态参数无效", "operation", "dns_account_status_bind", "request_id", requestIDFrom(c), "account_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_dns_account_status", "DNS 厂商账号状态格式无效")
		return
	}
	if err := h.service.SetProviderAccountActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeServiceError(c, "dns_account_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h dnsHandler) deleteAccount(c *gin.Context) {
	if err := h.service.DeleteProviderAccount(c.Request.Context(), c.Param("id")); err != nil {
		h.writeServiceError(c, "dns_account_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h dnsHandler) listDomains(c *gin.Context) {
	domains, err := h.service.ListDomains(c.Request.Context())
	if err != nil {
		h.writeServiceError(c, "dns_domain_list", err)
		return
	}
	result := make([]dnsDomainResponse, 0, len(domains))
	for index := range domains {
		result = append(result, toDomainResponse(&domains[index]))
	}
	c.JSON(http.StatusOK, gin.H{"domains": result})
}

func (h dnsHandler) createDomain(c *gin.Context) {
	var request dnsDomainRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建域名请求参数无效", "operation", "dns_domain_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_domain", dnsmanager.ErrInvalidDomain.Error())
		return
	}
	actor, _ := currentUser(c)
	domain, err := h.service.CreateDomain(c.Request.Context(), actor.ID, dnsmanager.DomainInput{
		AccountID: request.AccountID, Name: request.Name, Description: request.Description,
	})
	if err != nil {
		h.writeServiceError(c, "dns_domain_create", err)
		return
	}
	setAuditResourceID(c, domain.ID)
	c.JSON(http.StatusCreated, gin.H{"domain": toDomainResponse(domain)})
}

func (h dnsHandler) updateDomain(c *gin.Context) {
	var request dnsDomainRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新域名请求参数无效", "operation", "dns_domain_update_bind", "request_id", requestIDFrom(c), "domain_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_domain", dnsmanager.ErrInvalidDomain.Error())
		return
	}
	domain, err := h.service.UpdateDomain(c.Request.Context(), c.Param("id"), dnsmanager.DomainInput{
		AccountID: request.AccountID, Name: request.Name, Description: request.Description,
	})
	if err != nil {
		h.writeServiceError(c, "dns_domain_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"domain": toDomainResponse(domain)})
}

func (h dnsHandler) setDomainStatus(c *gin.Context) {
	var request dnsStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改域名状态参数无效", "operation", "dns_domain_status_bind", "request_id", requestIDFrom(c), "domain_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_domain_status", "域名状态格式无效")
		return
	}
	if err := h.service.SetDomainActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeServiceError(c, "dns_domain_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h dnsHandler) deleteDomain(c *gin.Context) {
	if err := h.service.DeleteDomain(c.Request.Context(), c.Param("id")); err != nil {
		h.writeServiceError(c, "dns_domain_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h dnsHandler) listRecords(c *gin.Context) {
	records, err := h.service.ListRecords(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeServiceError(c, "dns_record_list", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"records": records})
}

func (h dnsHandler) testDomain(c *gin.Context) {
	records, err := h.service.ListRecords(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeServiceError(c, "dns_domain_test", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"reachable": true, "record_count": len(records)})
}

func (h dnsHandler) createRecord(c *gin.Context) {
	var request dnsRecordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建 DNS 解析记录参数无效", "operation", "dns_record_create_bind", "request_id", requestIDFrom(c), "domain_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_dns_record", dnsmanager.ErrInvalidRecord.Error())
		return
	}
	record, err := h.service.CreateRecord(c.Request.Context(), c.Param("id"), dnsmanager.RecordInput{
		Name: request.Name, Type: request.Type, Value: request.Value, TTL: request.TTL,
	})
	if err != nil {
		h.writeServiceError(c, "dns_record_create", err)
		return
	}
	setAuditResourceID(c, c.Param("id")+":"+record.ID)
	c.JSON(http.StatusCreated, gin.H{"record": record})
}

func (h dnsHandler) updateRecord(c *gin.Context) {
	var request dnsRecordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新 DNS 解析记录参数无效", "operation", "dns_record_update_bind", "request_id", requestIDFrom(c), "domain_id", c.Param("id"), "record_id", c.Param("record_id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_dns_record", dnsmanager.ErrInvalidRecord.Error())
		return
	}
	record, err := h.service.UpdateRecord(c.Request.Context(), c.Param("id"), c.Param("record_id"), dnsmanager.RecordInput{
		Name: request.Name, Type: request.Type, Value: request.Value, TTL: request.TTL,
	})
	if err != nil {
		h.writeServiceError(c, "dns_record_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"record": record})
}

func (h dnsHandler) deleteRecord(c *gin.Context) {
	if err := h.service.DeleteRecord(c.Request.Context(), c.Param("id"), c.Param("record_id")); err != nil {
		h.writeServiceError(c, "dns_record_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h dnsHandler) toAccountResponse(account *model.DNSProviderAccount) (dnsProviderAccountResponse, error) {
	config, secretFields, err := h.service.PublicConfig(account)
	if err != nil {
		return dnsProviderAccountResponse{}, err
	}
	return dnsProviderAccountResponse{
		ID: account.ID, Name: account.Name, Provider: account.Provider, Config: config,
		ConfiguredSecretFields: secretFields, IsActive: account.IsActive, CreatedBy: account.CreatedBy,
		CreatedAt: account.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: account.UpdatedAt.Format(time.RFC3339Nano),
	}, nil
}

func toDomainResponse(domain *model.DNSDomain) dnsDomainResponse {
	return dnsDomainResponse{
		ID: domain.ID, AccountID: domain.AccountID, AccountName: domain.Account.Name,
		Provider: domain.Account.Provider, Name: domain.Name, Description: domain.Description,
		IsActive: domain.IsActive, CreatedBy: domain.CreatedBy,
		CreatedAt: domain.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: domain.UpdatedAt.Format(time.RFC3339Nano),
	}
}

func (h dnsHandler) writeServiceError(c *gin.Context, operation string, err error) {
	h.logger.Warn("域名管理操作失败", "operation", operation, "request_id", requestIDFrom(c), "account_id", c.Param("id"), "domain_id", c.Param("id"), "record_id", c.Param("record_id"), "err", err)
	switch {
	case errors.Is(err, dnsmanager.ErrInvalidProviderAccount):
		writeError(c, http.StatusBadRequest, "invalid_dns_account", dnsmanager.ErrInvalidProviderAccount.Error())
	case errors.Is(err, dnsmanager.ErrInvalidProviderConfig), errors.Is(err, dnsmanager.ErrUnsupportedProvider):
		writeError(c, http.StatusBadRequest, "invalid_dns_provider_config", dnsmanager.ErrInvalidProviderConfig.Error())
	case errors.Is(err, dnsmanager.ErrProviderAccountExists):
		writeError(c, http.StatusConflict, "dns_account_exists", dnsmanager.ErrProviderAccountExists.Error())
	case errors.Is(err, dnsmanager.ErrProviderAccountMissing):
		writeError(c, http.StatusNotFound, "dns_account_not_found", dnsmanager.ErrProviderAccountMissing.Error())
	case errors.Is(err, dnsmanager.ErrProviderAccountInUse):
		writeError(c, http.StatusConflict, "dns_account_in_use", dnsmanager.ErrProviderAccountInUse.Error())
	case errors.Is(err, dnsmanager.ErrProviderAccountOff):
		writeError(c, http.StatusConflict, "dns_account_disabled", dnsmanager.ErrProviderAccountOff.Error())
	case errors.Is(err, dnsmanager.ErrInvalidDomain):
		writeError(c, http.StatusBadRequest, "invalid_domain", dnsmanager.ErrInvalidDomain.Error())
	case errors.Is(err, dnsmanager.ErrDomainExists):
		writeError(c, http.StatusConflict, "domain_exists", dnsmanager.ErrDomainExists.Error())
	case errors.Is(err, dnsmanager.ErrDomainNotFound):
		writeError(c, http.StatusNotFound, "domain_not_found", dnsmanager.ErrDomainNotFound.Error())
	case errors.Is(err, dnsmanager.ErrDomainDisabled):
		writeError(c, http.StatusConflict, "domain_disabled", dnsmanager.ErrDomainDisabled.Error())
	case errors.Is(err, dnsmanager.ErrInvalidRecord):
		writeError(c, http.StatusBadRequest, "invalid_dns_record", dnsmanager.ErrInvalidRecord.Error())
	case errors.Is(err, dnsmanager.ErrRecordExists):
		writeError(c, http.StatusConflict, "dns_record_exists", dnsmanager.ErrRecordExists.Error())
	case errors.Is(err, dnsmanager.ErrRecordNotFound):
		writeError(c, http.StatusConflict, "dns_record_changed", dnsmanager.ErrRecordNotFound.Error())
	case errors.Is(err, dnsmanager.ErrRecordReadOnly):
		writeError(c, http.StatusConflict, "dns_record_read_only", dnsmanager.ErrRecordReadOnly.Error())
	case errors.Is(err, dnsmanager.ErrRecordIdentityChange):
		writeError(c, http.StatusConflict, "dns_record_identity_change", dnsmanager.ErrRecordIdentityChange.Error())
	case errors.Is(err, dnsmanager.ErrProviderRecordSetLimit):
		writeError(c, http.StatusConflict, "dns_provider_record_set_limit", dnsmanager.ErrProviderRecordSetLimit.Error())
	case errors.Is(err, dnsmanager.ErrProviderRequest):
		writeError(c, http.StatusBadGateway, "dns_provider_unavailable", "无法访问 DNS 厂商，请检查凭据、域名归属及网络")
	case errors.Is(err, secret.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "secrets_unavailable", secret.ErrUnavailable.Error())
	default:
		writeInternalError(c)
	}
}
