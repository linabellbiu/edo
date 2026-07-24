package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"

	"zrt/internal/access"
	"zrt/internal/account"
	"zrt/internal/audit"
	"zrt/internal/auth"
	"zrt/internal/cache"
	"zrt/internal/config"
	"zrt/internal/credential"
	"zrt/internal/database"
	"zrt/internal/repository"
	"zrt/internal/secret"
)

type healthyDependency struct{}

func (healthyDependency) Ping(context.Context) error { return nil }

func TestLoginMeAndLogout(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("登录失败: status=%d body=%s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "zrt_session" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("登录 Cookie 安全属性错误: %+v", cookies)
	}

	me := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, cookies[0])
	if me.Code != http.StatusOK || !bytes.Contains(me.Body.Bytes(), []byte(`"username":"admin"`)) {
		t.Fatalf("读取当前用户失败: status=%d body=%s", me.Code, me.Body.String())
	}

	logout := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/logout", nil, cookies[0])
	if logout.Code != http.StatusNoContent {
		t.Fatalf("退出登录失败: status=%d body=%s", logout.Code, logout.Body.String())
	}
	me = performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, cookies[0])
	if me.Code != http.StatusUnauthorized {
		t.Fatalf("退出后会话仍然有效: status=%d", me.Code)
	}
}

func newAuthTestRouter(t *testing.T) (*gin.Engine, func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file::memory:?cache=shared", MaxOpenConns: 1,
		MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移测试数据库失败: %v", err)
	}
	accounts := account.NewService(db)
	accessService, err := access.NewService(db)
	if err != nil {
		t.Fatalf("初始化 Casbin 权限服务失败: %v", err)
	}
	auditService := audit.NewService(db)
	secretManager, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatalf("初始化测试密钥管理器失败: %v", err)
	}
	credentialService := credential.NewService(db, secretManager)
	repositoryService := repository.NewService(
		db, secretManager, credentialService,
		repository.NewGitClient(config.Git{Timeout: time.Second}), 4,
	)
	if _, err := accounts.CreateAdmin(context.Background(), "admin", "管理员", "correct horse battery staple"); err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
	server := miniredis.RunT(t)
	redisClient, err := cache.Open(context.Background(), config.Redis{
		URL: "redis://" + server.Addr() + "/0", KeyPrefix: "zrt:", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("打开测试 Redis 失败: %v", err)
	}
	authConfig := config.Auth{
		SessionTTL: time.Hour, CookieName: "zrt_session", LoginMaxFailure: 3, LoginWindow: time.Minute,
	}
	sessions := auth.NewSessionStore(redisClient, authConfig.SessionTTL)
	limiter := auth.NewLoginRateLimiter(redisClient, authConfig.LoginMaxFailure, authConfig.LoginWindow)
	login, err := account.NewLoginService(accounts, sessions, limiter, logger)
	if err != nil {
		t.Fatalf("初始化登录服务失败: %v", err)
	}
	sqlDB, _ := db.DB()
	router := NewRouter(Dependencies{
		Environment: "test", Database: sqlDB, Redis: healthyDependency{}, NATS: healthyDependency{},
		Logger: logger, Version: "test", AuthConfig: authConfig,
		Accounts: accounts, Login: login, Sessions: sessions,
		Access: accessService, Audits: auditService,
		Credentials: credentialService, Repositories: repositoryService,
	})
	return router, func() {
		_ = redisClient.Close()
		_ = database.Close(db)
	}
}

func performJSONRequest(t *testing.T, handler http.Handler, method, path string, payload any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化测试请求失败: %v", err)
		}
		body = bytes.NewReader(data)
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
