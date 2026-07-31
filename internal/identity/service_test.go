package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"gorm.io/gorm"

	"edo/internal/account"
	"edo/internal/auth"
	"edo/internal/cache"
	"edo/internal/config"
	"edo/internal/configuration"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
)

func TestGenericOAuthCreatesBoundUserAndConsumesState(t *testing.T) {
	var sawVerifier bool
	providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if err := request.ParseForm(); err != nil {
				t.Fatalf("解析令牌请求失败: %v", err)
			}
			sawVerifier = request.Form.Get("code_verifier") != ""
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"access_token":"access-token","token_type":"Bearer","expires_in":3600}`)
		case "/userinfo":
			if request.Header.Get("Authorization") != "Bearer access-token" {
				t.Fatalf("用户信息请求未携带访问令牌")
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"sub":"remote-42","preferred_username":"alice","name":"Alice","email":"alice@example.com","email_verified":true}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer providerServer.Close()

	service, db, closeTest := newIdentityTestService(t)
	defer closeTest()
	if _, err := account.NewService(db).CreateUser(context.Background(), "alice", "本地 Alice", "correct horse battery staple"); err != nil {
		t.Fatalf("创建同名本地用户失败: %v", err)
	}
	clientSecret := "oauth-client-secret"
	view, err := service.Create(context.Background(), ProviderInput{
		Type: TypeGenericOAuth, Name: "company_oauth", DisplayName: "公司账号", IsActive: true, AutoCreate: true,
		ClientID: "client-id", ClientSecret: &clientSecret, AuthorizationURL: providerServer.URL + "/authorize",
		TokenURL: providerServer.URL + "/token", UserInfoURL: providerServer.URL + "/userinfo",
		RedirectURL: "http://edo.example/api/v1/auth/oauth/company_oauth/callback", Scopes: "openid email",
		SubjectField: "sub", UsernameField: "preferred_username", NicknameField: "name", EmailField: "email",
		EmailVerifiedField: "email_verified", AllowInsecure: true,
	}, "admin-id")
	if err != nil {
		t.Fatalf("创建 OAuth 登录方式失败: %v", err)
	}
	if !view.HasClientSecret || view.ClientSecretCiphertext == clientSecret {
		t.Fatal("客户端密钥未加密保存")
	}
	authorizationURL, err := service.StartOAuth(context.Background(), view.ID, "/applications", "127.0.0.1")
	if err != nil {
		t.Fatalf("开始 OAuth 登录失败: %v", err)
	}
	parsed, _ := url.Parse(authorizationURL)
	if parsed.Query().Get("state") == "" || parsed.Query().Get("code_challenge") == "" || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("OAuth 请求缺少 state 或 PKCE: %s", authorizationURL)
	}
	result, returnTo, err := service.CompleteOAuth(context.Background(), view.Name, parsed.Query().Get("state"), "valid-code", "127.0.0.1")
	if err != nil {
		t.Fatalf("完成 OAuth 登录失败: %v", err)
	}
	if !sawVerifier || returnTo != "/applications" || !strings.HasPrefix(result.User.Username, "alice-") {
		t.Fatalf("OAuth 登录结果不符合预期: verifier=%v return=%s username=%s", sawVerifier, returnTo, result.User.Username)
	}
	var identities []model.ExternalIdentity
	if err := db.Find(&identities).Error; err != nil || len(identities) != 1 || identities[0].UserID != result.User.ID {
		t.Fatalf("外部身份绑定未保存: identities=%+v err=%v", identities, err)
	}
	if _, _, err := service.CompleteOAuth(context.Background(), view.Name, parsed.Query().Get("state"), "valid-code", "127.0.0.1"); err != ErrInvalidState {
		t.Fatalf("OAuth state 可以重复使用: %v", err)
	}
}

func TestProviderDefaultsAndSecureTransportValidation(t *testing.T) {
	service, _, closeTest := newIdentityTestService(t)
	defer closeTest()
	clientSecret := "secret"
	view, err := service.Create(context.Background(), ProviderInput{
		Type: TypeGoogle, Name: "google", DisplayName: "Google", IsActive: true,
		ClientID: "client", ClientSecret: &clientSecret, RedirectURL: "https://edo.example/api/v1/auth/oauth/google/callback",
	}, "admin-id")
	if err != nil {
		t.Fatalf("创建 Google 预设失败: %v", err)
	}
	if view.SubjectField != "sub" || view.AuthorizationURL != "https://accounts.google.com/o/oauth2/v2/auth" {
		t.Fatalf("Google 预设不完整: %+v", view)
	}
	bindPassword := "bind-secret"
	_, err = service.Create(context.Background(), ProviderInput{
		Type: TypeLDAP, Name: "plain_ldap", DisplayName: "LDAP", IsActive: true,
		LDAPURL: "ldap://ldap.example.com:389", LDAPBaseDN: "dc=example,dc=com", LDAPBindDN: "cn=edo,dc=example,dc=com",
		LDAPBindPassword: &bindPassword, LDAPUserFilter: "(uid={username})", LDAPUsernameAttribute: "uid",
	}, "admin-id")
	if err == nil || !strings.Contains(err.Error(), "StartTLS") {
		t.Fatalf("未拒绝未加密 LDAP: %v", err)
	}
}

func newIdentityTestService(t *testing.T) (*Service, *gorm.DB, func()) {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared", MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	redisServer := miniredis.RunT(t)
	redisClient, err := cache.Open(ctx, config.Redis{URL: "redis://" + redisServer.Addr() + "/0", KeyPrefix: "edo:", Timeout: time.Second})
	if err != nil {
		t.Fatalf("打开测试 Redis 失败: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化测试密钥失败: %v", err)
	}
	accounts := account.NewService(db)
	sessions := auth.NewSessionStore(redisClient, time.Hour)
	configurationService := configuration.NewService(db, secretManager)
	limiter := auth.NewLoginRateLimiter(redisClient, 3, time.Minute, configurationService)
	login, err := account.NewLoginService(accounts, sessions, limiter, logger)
	if err != nil {
		t.Fatalf("初始化登录服务失败: %v", err)
	}
	service := NewService(db, redisClient, secretManager, accounts, login, limiter)
	return service, db, func() {
		_ = redisClient.Close()
		_ = database.Close(db)
	}
}

func TestProfileFromNestedClaims(t *testing.T) {
	var claims map[string]any
	decoder := json.NewDecoder(strings.NewReader(`{"data":{"id":17,"login":"operator"}}`))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		t.Fatal(err)
	}
	profile, err := profileFromClaims("data.id", "data.login", "", "", "", claims)
	if err != nil || profile.Subject != "17" || profile.Username != "operator" {
		t.Fatalf("嵌套字段映射失败: profile=%+v err=%v", profile, err)
	}
}
