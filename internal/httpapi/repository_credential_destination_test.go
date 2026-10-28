package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/model"
	"edo/internal/repository"
)

func TestRepositoryCredentialDestinationHTTPBlocksHiddenCredentialForwarding(t *testing.T) {
	f := newOperationsContractFixture(t)
	credential := f.request(t, http.MethodPost, "/api/v1/git-credentials", map[string]any{
		"name": "destination owner token", "provider": "generic", "auth_type": "token", "secret": "synthetic-personal-token",
	}, http.StatusCreated)
	credentialID := operationsID(t, credential, "credential")
	created := f.request(t, http.MethodPost, "/api/v1/repositories", map[string]any{
		"name": "destination-repo", "provider": "generic", "auth_type": "token", "clone_url": "https://original.invalid/a.git", "credential_id": credentialID,
	}, http.StatusCreated)
	id := operationsID(t, created, "repository")
	role := createRoleForHTTPTest(t, f.router, f.cookie, "repository-destination-editor", []string{"repository.update"})
	createUserForHTTPTest(t, f.router, f.cookie, "repository-destination-editor", "", []string{role})
	cookie := loginForDepartmentTest(t, f.router, "repository-destination-editor")
	payload := map[string]any{"name": "destination-repo", "provider": "generic", "auth_type": "token", "clone_url": "https://new-destination.invalid/a.git"}
	response := performJSONRequest(t, f.router, http.MethodPut, "/api/v1/repositories/"+id, payload, cookie)
	var body errorResponse
	if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Code != "repository_credential_rebind_required" || body.Message != repository.ErrCredentialRebindRequired.Error() {
		t.Fatalf("隐藏凭据转发没有安全拒绝: %d %s", response.Code, response.Body.String())
	}
	var stored model.GitRepository
	if err := f.db.First(&stored, "uuid = ?", id).Error; err != nil || stored.CloneURL != "https://original.invalid/a.git" || stored.CredentialID == nil || *stored.CredentialID != credentialID {
		t.Fatalf("被拒绝的请求改变原仓库: err=%v", err)
	}
	payload["clone_url"], payload["default_branch"] = "https://original.invalid:443/b.git", "develop"
	response = performJSONRequest(t, f.router, http.MethodPut, "/api/v1/repositories/"+id, payload, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("原认证目的地普通编辑被阻断: %d %s", response.Code, response.Body.String())
	}
	assertOperationsNoJobs(t, f.db)
}
