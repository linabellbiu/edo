package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/identity"
	"edo/internal/model"
)

func identityDelegationHTTPInput(name, roleID string) identity.ProviderInput {
	secret := "synthetic-oauth-key"
	return identity.ProviderInput{Type: identity.TypeGenericOAuth, Name: name, DisplayName: name, IsActive: true, AutoCreate: true,
		DefaultRoleID: roleID, ClientID: "synthetic", ClientSecret: &secret, AuthorizationURL: "https://example.invalid/authorize",
		TokenURL: "https://example.invalid/token", UserInfoURL: "https://example.invalid/userinfo", RedirectURL: "https://edo.invalid/callback", SubjectField: "sub", UsernameField: "username"}
}

func TestIdentityDefaultRoleHTTPRequiresCurrentDelegationForCreateUpdateAndEnable(t *testing.T) {
	f := newOperationsContractFixture(t)
	strong := createRoleForHTTPTest(t, f.router, f.cookie, "identity-strong-default", []string{"user.update", "role.update"})
	safe := createRoleForHTTPTest(t, f.router, f.cookie, "identity-safe-default", []string{"audit.read"})
	manager := createRoleForHTTPTest(t, f.router, f.cookie, "identity-limited-manager", []string{"identity.read", "identity.create", "identity.update", "audit.read"})
	managerID := createUserForHTTPTest(t, f.router, f.cookie, "identity-limited-manager", "", []string{manager})
	cookie := loginForDepartmentTest(t, f.router, "identity-limited-manager")
	request := func(method, path string, input any, want int) {
		t.Helper()
		response := performJSONRequest(t, f.router, method, path, input, cookie)
		if response.Code != want {
			t.Fatalf("登录方式委派状态不符: %s %s got=%d want=%d body=%s", method, path, response.Code, want, response.Body.String())
		}
	}
	request(http.MethodPost, "/api/v1/users", map[string]any{}, http.StatusForbidden)
	request(http.MethodPost, "/api/v1/identity-providers", identityDelegationHTTPInput("strong_default", strong), http.StatusForbidden)
	var count int64
	if err := f.db.Model(&model.IdentityProvider{}).Where("name = ?", "strong_default").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("拒绝越权创建后仍有配置: count=%d err=%v", count, err)
	}
	input := identityDelegationHTTPInput("safe_default", safe)
	created := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/identity-providers", input, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("合法默认角色无法保存: %d %s", created.Code, created.Body.String())
	}
	id := operationsID(t, created, "provider")
	input.DefaultRoleID = strong
	request(http.MethodPut, "/api/v1/identity-providers/"+id, input, http.StatusForbidden)
	var stored model.IdentityProvider
	if err := f.db.First(&stored, "uuid = ?", id).Error; err != nil || stored.DefaultRoleID != safe {
		t.Fatalf("被拒绝的更新改变默认角色: role=%s err=%v", stored.DefaultRoleID, err)
	}
	strongCreated := f.request(t, http.MethodPost, "/api/v1/identity-providers", identityDelegationHTTPInput("admin_default", strong), http.StatusCreated)
	strongID := operationsID(t, strongCreated, "provider")
	request(http.MethodPatch, "/api/v1/identity-providers/"+strongID+"/status", map[string]bool{"is_active": false}, http.StatusNoContent)
	request(http.MethodPatch, "/api/v1/identity-providers/"+strongID+"/status", map[string]bool{"is_active": true}, http.StatusForbidden)
	if err := f.db.Create(&model.UserPermission{UserID: managerID, Permission: "audit.read", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	input.DefaultRoleID = safe
	request(http.MethodPut, "/api/v1/identity-providers/"+id, input, http.StatusForbidden)
}

func TestIdentityDefaultRoleChoicesExposeOnlyDelegatableSubsetWithoutRoleRead(t *testing.T) {
	f := newOperationsContractFixture(t)
	strong := createRoleForHTTPTest(t, f.router, f.cookie, "identity-choice-strong", []string{"user.update"})
	safe := createRoleForHTTPTest(t, f.router, f.cookie, "identity-choice-safe", []string{"audit.read"})
	role := createRoleForHTTPTest(t, f.router, f.cookie, "identity-choice-manager", []string{"identity.create", "audit.read"})
	createUserForHTTPTest(t, f.router, f.cookie, "identity-choice-manager", "", []string{role})
	cookie := loginForDepartmentTest(t, f.router, "identity-choice-manager")
	fullList := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/roles", nil, cookie)
	if fullList.Code != http.StatusForbidden {
		t.Fatalf("测试账号意外获得角色目录读取权: %d", fullList.Code)
	}
	response := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/identity-providers/role-choices", nil, cookie)
	var body struct {
		Roles []identity.RoleChoice `json:"roles"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("默认角色候选接口不可用: %d %s", response.Code, response.Body.String())
	}
	found := false
	for _, item := range body.Roles {
		if item.ID == strong {
			t.Fatal("候选接口返回了不可委派角色")
		}
		found = found || item.ID == safe
	}
	if !found {
		t.Fatal("候选接口遗漏可委派角色")
	}
	reader := createRoleForHTTPTest(t, f.router, f.cookie, "identity-reader-only", []string{"identity.read"})
	createUserForHTTPTest(t, f.router, f.cookie, "identity-reader-only", "", []string{reader})
	readerCookie := loginForDepartmentTest(t, f.router, "identity-reader-only")
	denied := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/identity-providers/role-choices", nil, readerCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("只有查看权限即可读取授权候选: %d", denied.Code)
	}
}
