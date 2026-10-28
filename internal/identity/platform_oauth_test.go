package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestPlatformOAuthAdapters(t *testing.T) {
	for _, providerType := range []string{TypeFeishu, TypeDingTalk} {
		t.Run(providerType, func(t *testing.T) {
			s, _, closeTest := newIdentityTestService(t)
			defer closeTest()
			tokenCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/token":
					tokenCalls++
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if providerType == TypeFeishu {
						if body["client_id"] != "app-id" || body["client_secret"] != "app-secret" || body["code_verifier"] == "" || body["grant_type"] != "authorization_code" || body["redirect_uri"] == "" {
							t.Error("飞书令牌请求不正确")
						}
						io.WriteString(w, `{"code":0,"access_token":"platform-token","expires_in":7200}`)
					} else {
						if body["clientId"] != "app-id" || body["clientSecret"] != "app-secret" || body["grantType"] != "authorization_code" || body["code"] != "auth-code" {
							t.Error("钉钉令牌请求不正确")
						}
						io.WriteString(w, `{"accessToken":"platform-token","expireIn":7200}`)
					}
				case "/user":
					if providerType == TypeFeishu {
						if r.Header.Get("Authorization") != "Bearer platform-token" {
							t.Error("飞书请求缺少令牌")
						}
						io.WriteString(w, `{"code":0,"data":{"open_id":"feishu-1","name":"飞书用户"}}`)
					} else {
						if r.Header.Get("x-acs-dingtalk-access-token") != "platform-token" || r.Header.Get("Authorization") != "" {
							t.Error("钉钉用户信息请求未使用专用请求头")
						}
						io.WriteString(w, `{"unionId":"dingtalk-1","nick":"钉钉用户"}`)
					}
				}
			}))
			defer server.Close()
			secret := "app-secret"
			view, err := s.Create(context.Background(), ProviderInput{Type: providerType, Name: providerType, DisplayName: providerType, IsActive: true, AutoCreate: true,
				ClientID: "app-id", ClientSecret: &secret, RedirectURL: "http://edo.example/callback", AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL + "/token", UserInfoURL: server.URL + "/user", AllowInsecure: true}, identityTestAdmin(t, s).ID)
			if err != nil {
				t.Fatal(err)
			}
			start, err := s.StartOAuth(context.Background(), view.ID, "/settings", "192.0.2.1")
			if err != nil {
				t.Fatal(err)
			}
			parsed, _ := url.Parse(start)
			query := parsed.Query()
			if query.Get("client_id") != "app-id" || query.Get("app_id") != "" || query.Get("state") == "" {
				t.Fatal("授权地址参数错误")
			}
			if providerType == TypeFeishu && query.Get("code_challenge_method") != "S256" {
				t.Fatal("飞书缺少 PKCE")
			}
			if providerType == TypeDingTalk && (query.Get("scope") != "openid" || query.Get("prompt") != "consent") {
				t.Fatal("钉钉授权参数错误")
			}
			result, returnTo, err := s.CompleteOAuth(context.Background(), view.ID, query.Get("state"), "auth-code", "192.0.2.1")
			if err != nil || result.Token == "" || result.User.IsSuperuser || returnTo != "/settings" || tokenCalls != 1 {
				t.Fatalf("授权登录失败: %v", err)
			}
			start, _ = s.StartOAuth(context.Background(), view.ID, "/", "192.0.2.1")
			parsed, _ = url.Parse(start)
			s.SetActiveAs(context.Background(), identityTestAdmin(t, s), view.ID, false)
			if _, _, err := s.CompleteOAuth(context.Background(), view.ID, parsed.Query().Get("state"), "auth-code", "192.0.2.1"); !errors.Is(err, ErrProviderDisabled) {
				t.Fatalf("停用后回调仍可登录: %v", err)
			}
			if tokenCalls != 1 {
				t.Fatal("停用后仍调用外部平台")
			}
		})
	}
}
