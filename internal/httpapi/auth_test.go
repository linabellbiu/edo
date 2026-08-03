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

	"edo/internal/access"
	"edo/internal/account"
	artifactmanager "edo/internal/artifact"
	"edo/internal/audit"
	"edo/internal/auth"
	"edo/internal/cache"
	"edo/internal/config"
	"edo/internal/configuration"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/department"
	"edo/internal/deployment"
	"edo/internal/dockerengine"
	"edo/internal/kube"
	"edo/internal/logging"
	"edo/internal/model"
	"edo/internal/pipeline"
	"edo/internal/repository"
	"edo/internal/secret"
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
	if len(cookies) != 1 || cookies[0].Name != "edo_session" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
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

func TestChangePasswordInvalidatesCurrentSession(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("登录失败: status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	wrong := performJSONRequest(t, router, http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "wrong password", "new_password": "new correct password 123",
	}, cookie)
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("错误的当前密码未被拒绝: status=%d body=%s", wrong.Code, wrong.Body.String())
	}
	changed := performJSONRequest(t, router, http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "correct horse battery staple", "new_password": "new correct password 123",
	}, cookie)
	if changed.Code != http.StatusOK {
		t.Fatalf("修改密码失败: status=%d body=%s", changed.Code, changed.Body.String())
	}
	me := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, cookie)
	if me.Code != http.StatusUnauthorized {
		t.Fatalf("修改密码后旧会话仍有效: status=%d", me.Code)
	}
	oldLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("旧密码仍可登录: status=%d", oldLogin.Code)
	}
	newLogin := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "new correct password 123",
	}, nil)
	if newLogin.Code != http.StatusOK {
		t.Fatalf("新密码无法登录: status=%d body=%s", newLogin.Code, newLogin.Body.String())
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
	departmentService := department.NewService(db)
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
	configurationService := configuration.NewService(db, secretManager)
	kubernetesService := kube.NewService(db, secretManager, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second})
	repositoryService := repository.NewService(
		db, secretManager, credentialService,
		repository.NewGitClient(config.Git{Timeout: time.Second}), 4,
		repository.WithWebhookGate(configurationService),
	)
	repositoryDirectories, err := repositoryService.PrepareDirectories(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("准备接口测试运行目录失败: %v", err)
	}
	repositoryService.ApplyDirectories(repositoryDirectories)
	pipelineService := pipeline.NewService(db, repositoryService, secretManager)
	artifactService, err := artifactmanager.NewService(
		db, t.TempDir(), 1024*1024, logger,
		artifactmanager.WithBuildDirectory(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("初始化接口测试制品服务失败: %v", err)
	}
	now := time.Now().UTC()
	testEnvironment := model.Environment{
		ID: "httpapi-deployment-environment", Name: "接口测试部署环境", IsActive: true,
		CreatedBy: "system", CreatedAt: now, UpdatedAt: now,
	}
	testHost := model.Host{
		ID: "httpapi-deployment-host", Name: "接口测试部署主机", Mode: model.HostModeSSH,
		Address: "192.0.2.20", SSHPort: 22, SSHUsername: "deployer",
		IsActive: true, CreatedBy: "system",
		CreatedAt: now, UpdatedAt: now,
	}
	testCapability := model.HostCapability{
		HostID: testHost.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&testEnvironment).Error; err != nil {
		t.Fatalf("创建接口测试部署环境失败: %v", err)
	}
	if err := db.Create(&testHost).Error; err != nil {
		t.Fatalf("创建接口测试部署主机失败: %v", err)
	}
	if err := db.Create(&model.EnvironmentHost{
		EnvironmentID: testEnvironment.ID, HostID: testHost.ID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("创建接口测试环境主机关联失败: %v", err)
	}
	if err := db.Create(&testCapability).Error; err != nil {
		t.Fatalf("创建接口测试部署能力失败: %v", err)
	}
	deploymentService := deployment.NewService(db, nil, nil, nil, nil, "", logger)
	pipelineService.ConfigureExecution(nil, deploymentService, logger)
	pipelineService.ConfigureWorkflowRuntimeManager(authTestWorkflowRuntimeManager{})
	if _, err := accounts.CreateAdmin(context.Background(), "admin", "管理员", "correct horse battery staple"); err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
	server := miniredis.RunT(t)
	redisClient, err := cache.Open(context.Background(), config.Redis{
		URL: "redis://" + server.Addr() + "/0", KeyPrefix: "edo:", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("打开测试 Redis 失败: %v", err)
	}
	authConfig := config.Auth{
		SessionTTL: time.Hour, CookieName: "edo_session", LoginMaxFailure: 3, LoginWindow: time.Minute,
	}
	sessions := auth.NewSessionStore(redisClient, authConfig.SessionTTL)
	limiter := auth.NewLoginRateLimiter(redisClient, authConfig.LoginMaxFailure, authConfig.LoginWindow, configurationService)
	login, err := account.NewLoginService(accounts, sessions, limiter, logger)
	if err != nil {
		t.Fatalf("初始化登录服务失败: %v", err)
	}
	sqlDB, _ := db.DB()
	_, runtimeLogs := logging.NewRuntime("info")
	router := NewRouter(Dependencies{
		Environment: "test", Database: sqlDB, Redis: healthyDependency{}, NATS: healthyDependency{},
		Logger: logger, RuntimeLogs: runtimeLogs, Version: "test", AuthConfig: authConfig,
		Accounts: accounts, Login: login, LoginLimiter: limiter, Sessions: sessions,
		Access: accessService, Audits: auditService,
		Departments: departmentService,
		Credentials: credentialService, Repositories: repositoryService, Pipelines: pipelineService,
		Artifacts:      artifactService,
		Kubernetes:     kubernetesService,
		Deployments:    deploymentService,
		Configurations: configurationService,
	})
	return router, func() {
		_ = redisClient.Close()
		_ = database.Close(db)
	}
}

type authTestWorkflowRuntimeManager struct{}

func (authTestWorkflowRuntimeManager) InspectScriptRuntimeImage(_ context.Context, image string) (dockerengine.ScriptRuntimeImageStatus, error) {
	return dockerengine.ScriptRuntimeImageStatus{Image: image, ImageID: "sha256:test-runtime", Installed: true}, nil
}

func (authTestWorkflowRuntimeManager) PrepareScriptRuntimeImage(_ context.Context, image string) (dockerengine.ScriptRuntimeImageStatus, error) {
	return dockerengine.ScriptRuntimeImageStatus{Image: image, ImageID: "sha256:test-runtime", Installed: true}, nil
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
