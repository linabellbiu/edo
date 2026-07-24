package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestRBACAndAuditAPI(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	adminLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if adminLogin.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: %s", adminLogin.Body.String())
	}
	adminCookie := adminLogin.Result().Cookies()[0]

	roleResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "user-reader", "display_name": "用户查看员", "permissions": []string{"user.read"},
	}, adminCookie)
	if roleResponse.Code != http.StatusCreated {
		t.Fatalf("创建角色失败: status=%d body=%s", roleResponse.Code, roleResponse.Body.String())
	}
	var rolePayload struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if err := json.Unmarshal(roleResponse.Body.Bytes(), &rolePayload); err != nil || rolePayload.Role.ID == "" {
		t.Fatalf("解析角色响应失败: payload=%+v err=%v", rolePayload, err)
	}

	userResponse := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "reader", "nickname": "只读用户", "password": "correct horse battery staple",
		"role_ids": []string{rolePayload.Role.ID},
	}, adminCookie)
	if userResponse.Code != http.StatusCreated {
		t.Fatalf("创建普通用户失败: status=%d body=%s", userResponse.Code, userResponse.Body.String())
	}
	var userPayload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(userResponse.Body.Bytes(), &userPayload); err != nil || userPayload.User.ID == "" {
		t.Fatalf("解析用户响应失败: payload=%+v err=%v", userPayload, err)
	}

	readerLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "reader", "password": "correct horse battery staple",
	}, nil)
	if readerLogin.Code != http.StatusOK || !bytes.Contains(readerLogin.Body.Bytes(), []byte(`"user.read"`)) {
		t.Fatalf("普通用户登录或权限响应错误: status=%d body=%s", readerLogin.Code, readerLogin.Body.String())
	}
	readerCookie := readerLogin.Result().Cookies()[0]

	users := performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, readerCookie)
	if users.Code != http.StatusOK {
		t.Fatalf("已授权用户不能查看用户列表: status=%d body=%s", users.Code, users.Body.String())
	}
	denied := performJSONRequest(t, router, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "forbidden", "display_name": "不应创建", "permissions": []string{},
	}, readerCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("越权创建角色未被拒绝: status=%d body=%s", denied.Code, denied.Body.String())
	}
	override := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userPayload.User.ID+"/permissions", map[string]any{
		"allow": []string{"role.read"}, "deny": []string{"user.read"},
	}, adminCookie)
	if override.Code != http.StatusNoContent {
		t.Fatalf("配置用户权限覆盖失败: status=%d body=%s", override.Code, override.Body.String())
	}
	users = performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, readerCookie)
	if users.Code != http.StatusForbidden {
		t.Fatalf("显式拒绝未覆盖角色授权: status=%d body=%s", users.Code, users.Body.String())
	}
	roles := performJSONRequest(t, router, http.MethodGet, "/api/v1/roles", nil, readerCookie)
	if roles.Code != http.StatusOK {
		t.Fatalf("用户级额外授权未生效: status=%d body=%s", roles.Code, roles.Body.String())
	}

	audits := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs", nil, adminCookie)
	if audits.Code != http.StatusOK || !bytes.Contains(audits.Body.Bytes(), []byte(`"role.create"`)) ||
		!bytes.Contains(audits.Body.Bytes(), []byte(`"denied"`)) {
		t.Fatalf("审计日志未记录成功或拒绝操作: status=%d body=%s", audits.Code, audits.Body.String())
	}
}
