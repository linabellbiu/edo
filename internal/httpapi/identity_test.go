package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"edo/internal/identity"
)

func TestIdentitySettingsPermissionsAndSecretMasking(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/identity-providers", nil, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("未登录可读取配置: %d", response.Code)
	}
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "correct horse battery staple"}, nil)
	cookie := login.Result().Cookies()[0]
	password := "email-server-secret"
	input := identity.ProviderInput{Type: identity.TypeEmail, Name: "email", DisplayName: "邮箱验证码", IsActive: true,
		Email: &identity.EmailSettings{Host: "smtp.example.com", Port: 465, Username: "mailer", Password: &password, From: "noreply@example.com", TLSMode: "tls", AllowedDomains: []string{"example.com"}}}
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/identity-providers", input, cookie)
	if created.Code != http.StatusCreated || bytes.Contains(created.Body.Bytes(), []byte(password)) {
		t.Fatalf("邮件配置创建失败或泄密: %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Provider identity.ProviderView `json:"provider"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	public := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/providers", nil, nil)
	if public.Code != http.StatusOK || !bytes.Contains(public.Body.Bytes(), []byte(`"type":"email"`)) || bytes.Contains(public.Body.Bytes(), []byte("smtp.example.com")) {
		t.Fatalf("公开登录方式列表不正确: %s", public.Body.String())
	}
	// 自动创建关闭时不向未绑定邮箱发信，响应不暴露账号是否存在。
	sent := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/email/"+body.Provider.ID+"/code", map[string]string{"email": "unknown@example.com"}, nil)
	if sent.Code != http.StatusOK || !bytes.Contains(sent.Body.Bytes(), []byte(`"retry_after":60`)) {
		t.Fatalf("发码响应不正确: %d %s", sent.Code, sent.Body.String())
	}
	failed := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/email/"+body.Provider.ID+"/login", map[string]string{"email": "unknown@example.com", "code": "123456"}, nil)
	if failed.Code != http.StatusUnauthorized || len(failed.Result().Cookies()) != 0 {
		t.Fatalf("无效验证码创建了会话: %d", failed.Code)
	}
	disabled := performJSONRequest(t, router, http.MethodPatch, "/api/v1/identity-providers/"+body.Provider.ID+"/status", map[string]bool{"is_active": false}, cookie)
	if disabled.Code != http.StatusNoContent {
		t.Fatal("未能停用登录方式")
	}
	public = performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/providers", nil, nil)
	if bytes.Contains(public.Body.Bytes(), []byte(`"type":"email"`)) {
		t.Fatal("停用方式仍在登录页展示")
	}
	failed = performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/email/"+body.Provider.ID+"/login", map[string]string{"email": "unknown@example.com", "code": "123456"}, nil)
	if failed.Code != http.StatusForbidden {
		t.Fatalf("停用后未拦截: %d", failed.Code)
	}
	user := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{"username": "identity-reader", "nickname": "普通用户", "password": "correct horse battery staple"}, cookie)
	if user.Code != http.StatusCreated {
		t.Fatalf("创建测试用户失败: %s", user.Body.String())
	}
	userLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "identity-reader", "password": "correct horse battery staple"}, nil)
	userCookie := userLogin.Result().Cookies()[0]
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		denied := performJSONRequest(t, router, method, "/api/v1/identity-providers", input, userCookie)
		if denied.Code != http.StatusForbidden {
			t.Fatalf("普通用户获得身份配置权限: method=%s status=%d", method, denied.Code)
		}
	}
}

func TestDingTalkCallbackUsesAuthCodeAndBindsBrowserState(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			var input map[string]string
			json.NewDecoder(r.Body).Decode(&input)
			if input["code"] != "ding-auth-code" {
				t.Error("未读取钉钉 authCode 回调参数")
			}
			io.WriteString(w, `{"accessToken":"ding-token","expireIn":7200}`)
		} else {
			io.WriteString(w, `{"unionId":"ding-user-1","nick":"钉钉用户"}`)
		}
	}))
	defer platform.Close()
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "correct horse battery staple"}, nil)
	secret := "ding-secret"
	create := performJSONRequest(t, router, http.MethodPost, "/api/v1/identity-providers", identity.ProviderInput{Type: identity.TypeDingTalk, Name: "dingtalk", DisplayName: "钉钉", IsActive: true, AutoCreate: true, ClientID: "ding-app", ClientSecret: &secret,
		AuthorizationURL: platform.URL + "/authorize", TokenURL: platform.URL + "/token", UserInfoURL: platform.URL + "/user", RedirectURL: "http://edo.example/api/v1/auth/oauth/dingtalk/callback", AllowInsecure: true}, login.Result().Cookies()[0])
	if create.Code != http.StatusCreated {
		t.Fatalf("创建钉钉配置失败: %s", create.Body.String())
	}
	start := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/oauth/dingtalk/start?return_to=%2Fsettings", nil, nil)
	location, _ := url.Parse(start.Header().Get("Location"))
	if len(start.Result().Cookies()) != 1 || start.Result().Cookies()[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("未设置 OAuth 浏览器状态 Cookie")
	}
	callback := "/api/v1/auth/oauth/dingtalk/callback?authCode=ding-auth-code&state=" + url.QueryEscape(location.Query().Get("state"))
	missing := performJSONRequest(t, router, http.MethodGet, callback, nil, nil)
	if !strings.Contains(missing.Header().Get("Location"), "external_error=expired") {
		t.Fatal("未拦截跨浏览器 OAuth 回调")
	}
	success := performJSONRequest(t, router, http.MethodGet, callback, nil, start.Result().Cookies()[0])
	if success.Code != http.StatusFound || success.Header().Get("Location") != "/settings" {
		t.Fatalf("钉钉回调失败: %d %s", success.Code, success.Body.String())
	}
	foundSession := false
	for _, cookie := range success.Result().Cookies() {
		if cookie.Name == "edo_session" && cookie.Value != "" {
			foundSession = true
		}
	}
	if !foundSession {
		t.Fatal("钉钉回调未建立会话")
	}
	replay := performJSONRequest(t, router, http.MethodGet, callback, nil, start.Result().Cookies()[0])
	if !strings.Contains(replay.Header().Get("Location"), "external_error=expired") {
		t.Fatal("OAuth 回调可重复使用")
	}
}
