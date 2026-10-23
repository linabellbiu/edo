package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"edo/internal/model"
)

func TestOperationsCredentialHTTPUpdatesSecretsAndProtectsReferences(t *testing.T) {
	fixture := newOperationsContractFixture(t)
	const firstSecret = "synthetic-credential-original"
	const secondSecret = "synthetic-credential-rotated"
	payload := map[string]any{"name": "ops-git-token", "provider": "github", "auth_type": "token", "secret": firstSecret}
	created := fixture.request(t, http.MethodPost, "/api/v1/git-credentials", payload, http.StatusCreated)
	id := operationsID(t, created, "credential")
	assertOperationsRedacted(t, created, firstSecret)
	var original model.GitCredential
	if err := fixture.db.Where(&model.GitCredential{ID: id}).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	role := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "ops-other-credential-owner", []string{"credential.read", "credential.create", "credential.update", "credential.delete"})
	createUserForHTTPTest(t, fixture.router, fixture.cookie, "ops-other-credential-owner", "", []string{role})
	other := loginForDepartmentTest(t, fixture.router, "ops-other-credential-owner")
	for _, item := range []struct {
		method, path string
		payload      any
	}{
		{http.MethodPut, "/api/v1/git-credentials/" + id, map[string]any{"name": "stolen-token", "provider": "github", "auth_type": "token", "secret": "synthetic-other-owner-secret"}},
		{http.MethodDelete, "/api/v1/git-credentials/" + id, nil},
		{http.MethodGet, "/api/v1/git-credentials/" + id + "/secret", nil},
	} {
		response := performJSONRequest(t, fixture.router, item.method, item.path, item.payload, other)
		if response.Code != http.StatusNotFound {
			t.Fatalf("其他所有者可以操作个人凭据: method=%s status=%d body=%s", item.method, response.Code, response.Body.String())
		}
		assertOperationsRedacted(t, response, firstSecret)
	}
	var ownerProtected model.GitCredential
	if err := fixture.db.Where(&model.GitCredential{ID: id}).First(&ownerProtected).Error; err != nil || ownerProtected.SecretCiphertext != original.SecretCiphertext || ownerProtected.Name != original.Name {
		t.Fatalf("其他所有者的越权请求改写或删除了个人凭据: err=%v", err)
	}
	update := map[string]any{"name": "ops-git-renamed", "provider": "github", "auth_type": "token", "username": "oauth2"}
	updated := fixture.request(t, http.MethodPut, "/api/v1/git-credentials/"+id, update, http.StatusOK)
	assertOperationsRedacted(t, updated, firstSecret)
	var retained model.GitCredential
	if err := fixture.db.Where(&model.GitCredential{ID: id}).First(&retained).Error; err != nil || retained.SecretCiphertext != original.SecretCiphertext || retained.Name != "ops-git-renamed" {
		t.Fatalf("未提供密钥时应保留原加密凭据: err=%v", err)
	}
	update["secret"] = "short"
	fixture.request(t, http.MethodPut, "/api/v1/git-credentials/"+id, update, http.StatusBadRequest, "invalid_credential")
	update["secret"] = secondSecret
	rotated := fixture.request(t, http.MethodPut, "/api/v1/git-credentials/"+id, update, http.StatusOK)
	assertOperationsRedacted(t, rotated, firstSecret, secondSecret)
	revealed := fixture.request(t, http.MethodGet, "/api/v1/git-credentials/"+id+"/secret", nil, http.StatusOK)
	var reveal struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(revealed.Body.Bytes(), &reveal); err != nil || reveal.Secret != secondSecret || revealed.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("显式查看未返回轮换后的凭据或允许缓存: err=%v", err)
	}
	listed := fixture.request(t, http.MethodGet, "/api/v1/git-credentials", nil, http.StatusOK)
	assertOperationsRedacted(t, listed, firstSecret, secondSecret)
	now := time.Now().UTC()
	repository := model.GitRepository{
		ID: uuid.NewString(), Name: "ops-credential-reference", Provider: model.GitProviderGitHub,
		CloneURL: "https://example.invalid/ops/repo.git", AuthType: model.GitAuthToken, CredentialID: &id,
		IsActive: true, CreatedBy: original.UserID, CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	delete(update, "secret")
	update["provider"] = "gitlab"
	fixture.request(t, http.MethodPut, "/api/v1/git-credentials/"+id, update, http.StatusConflict, "credential_in_use")
	fixture.request(t, http.MethodDelete, "/api/v1/git-credentials/"+id, nil, http.StatusConflict, "credential_in_use")
	fixture.request(t, http.MethodPut, "/api/v1/git-credentials/missing", update, http.StatusNotFound, "credential_not_found")
	if err := fixture.db.Delete(&repository).Error; err != nil {
		t.Fatal(err)
	}
	fixture.request(t, http.MethodDelete, "/api/v1/git-credentials/"+id, nil, http.StatusNoContent)
	fixture.request(t, http.MethodDelete, "/api/v1/git-credentials/"+id, nil, http.StatusNotFound, "credential_not_found")
	assertOperationsNoJobs(t, fixture.db)
}

func TestOperationsRepositoryDeliveryHTTPListsHistoryWithoutSecrets(t *testing.T) {
	fixture := newOperationsContractFixture(t)
	created := fixture.request(t, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "ops-webhook-history", "provider": "generic", "auth_type": "none",
		"clone_url": "https://example.invalid/ops/history.git", "webhook_enabled": true,
	}, http.StatusCreated)
	repositoryID := operationsID(t, created, "repository")
	var repositoryResponse struct {
		WebhookSecret string `json:"webhook_secret"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &repositoryResponse); err != nil || repositoryResponse.WebhookSecret == "" {
		t.Fatalf("测试仓库没有生成Webhook密钥: err=%v", err)
	}
	newestID := ""
	for index := range 3 {
		delivery := model.GitWebhookDelivery{
			ID: uuid.NewString(), RepositoryID: repositoryID, DeliveryID: uuid.NewString(), EventType: "push", Ref: "refs/heads/main",
			CommitSHA: strings.Repeat("a", 40), Message: "synthetic webhook history", PayloadHash: strings.Repeat("b", 64),
			Status: model.WebhookProcessed, ReceivedAt: time.Now().UTC().Add(time.Duration(index) * time.Second),
		}
		if err := fixture.db.Create(&delivery).Error; err != nil {
			t.Fatal(err)
		}
		newestID = delivery.ID
	}
	readerRole := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "ops-delivery-reader", []string{"repository.read"})
	otherRole := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "ops-delivery-other", []string{"application.read"})
	createUserForHTTPTest(t, fixture.router, fixture.cookie, "ops-delivery-reader", "", []string{readerRole})
	createUserForHTTPTest(t, fixture.router, fixture.cookie, "ops-delivery-other", "", []string{otherRole})
	reader := loginForDepartmentTest(t, fixture.router, "ops-delivery-reader")
	other := loginForDepartmentTest(t, fixture.router, "ops-delivery-other")
	path := "/api/v1/repositories/" + repositoryID + "/webhook-deliveries"
	for _, item := range []struct {
		name   string
		cookie *http.Cookie
		status int
	}{
		{"未登录", nil, http.StatusUnauthorized},
		{"缺少仓库查看权限", other, http.StatusForbidden},
	} {
		t.Run(item.name, func(t *testing.T) {
			response := performJSONRequest(t, fixture.router, http.MethodGet, path, nil, item.cookie)
			if response.Code != item.status {
				t.Fatalf("Webhook记录读取权限错误: got=%d want=%d body=%s", response.Code, item.status, response.Body.String())
			}
		})
	}
	listed := performJSONRequest(t, fixture.router, http.MethodGet, path+"?limit=1", nil, reader)
	var response struct {
		Deliveries []model.GitWebhookDelivery `json:"deliveries"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil || listed.Code != http.StatusOK || len(response.Deliveries) != 1 || response.Deliveries[0].ID != newestID {
		t.Fatalf("Webhook记录没有按时间倒序和limit返回: status=%d body=%s err=%v", listed.Code, listed.Body.String(), err)
	}
	assertOperationsRedacted(t, listed, repositoryResponse.WebhookSecret, "webhook_secret", "credential_ciphertext")
	assertOperationsNoJobs(t, fixture.db)
}

func TestOperationsCredentialHTTPProtectsReferencesAcrossProjects(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newOperationsContractFixture(t)
			project := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]string{"name": "凭据引用项目"}, http.StatusCreated)
			projectID := operationsID(t, project, "project")
			credential := fixture.request(t, http.MethodPost, "/api/v1/git-credentials", map[string]any{
				"name": "cross-project-token", "provider": "github", "auth_type": "token", "secret": "synthetic-cross-project-secret",
			}, http.StatusCreated)
			credentialID := operationsID(t, credential, "credential")
			fixture.request(t, http.MethodPost, "/api/v1/repositories?project_id="+projectID, map[string]any{
				"name": "private-credential-repository", "provider": "github", "auth_type": "token",
				"clone_url": "https://github.com/example/private.git", "credential_id": credentialID,
			}, http.StatusCreated)
			var response *httptest.ResponseRecorder
			if operation == "delete" {
				response = performJSONRequest(t, fixture.router, http.MethodDelete, "/api/v1/git-credentials/"+credentialID, nil, fixture.cookie)
			} else {
				response = performJSONRequest(t, fixture.router, http.MethodPut, "/api/v1/git-credentials/"+credentialID, map[string]any{
					"name": "cross-project-token", "provider": "gitlab", "auth_type": "token",
				}, fixture.cookie)
			}
			if response.Code != http.StatusConflict {
				t.Fatalf("个人凭据忽略其他项目仓库引用，允许破坏在用凭据: operation=%s got=%d want=409 body=%s", operation, response.Code, response.Body.String())
			}
		})
	}
}
