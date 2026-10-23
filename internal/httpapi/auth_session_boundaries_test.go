package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/auth"
	"edo/internal/cache"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
)

// 会话有效性与项目准入分别验证，避免默认项目请求头掩盖认证结果。
func sessionBoundaryRequest(t *testing.T, router http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestAuthSessionPasswordChangeInvalidatesOtherDevicesHTTP(t *testing.T) {
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) { deps = *value })
	t.Cleanup(cleanup)
	first := loginForDepartmentTest(t, router, "admin")
	second := loginForDepartmentTest(t, router, "admin")
	if first.Value == second.Value {
		t.Fatal("不同设备的会话凭据不应相同")
	}
	for _, cookie := range []*http.Cookie{first, second} {
		if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", cookie); response.Code != http.StatusOK {
			t.Fatalf("变更前会话不可用: status=%d", response.Code)
		}
	}
	response := performJSONRequest(t, router, http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "correct horse battery staple", "new_password": "another correct password 123",
	}, first)
	if response.Code != http.StatusOK {
		t.Fatalf("修改密码失败: status=%d body=%s", response.Code, response.Body.String())
	}
	// 第二台设备仍有 Redis 记录，必须由认证版本阻止访问，不能只退出当前设备。
	if _, err := deps.Sessions.Get(context.Background(), second.Value); err != nil {
		t.Fatalf("未请求的另一设备会话记录意外消失: %v", err)
	}
	for _, cookie := range []*http.Cookie{first, second} {
		if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", cookie); response.Code != http.StatusUnauthorized {
			t.Fatalf("密码变更后旧设备会话仍可访问: status=%d", response.Code)
		}
		if _, err := deps.Sessions.Get(context.Background(), cookie.Value); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("失效会话未被清理: %v", err)
		}
	}
	response = performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "another correct password 123",
	}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("新密码无法创建新会话: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthSessionDisabledAccountStopsHTTPAccess(t *testing.T) {
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) { deps = *value })
	t.Cleanup(cleanup)
	user, err := deps.Accounts.CreateUser(context.Background(), "session-disabled", "会话停用测试", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	admin := loginForDepartmentTest(t, router, "admin")
	devices := []*http.Cookie{
		loginForDepartmentTest(t, router, user.Username),
		loginForDepartmentTest(t, router, user.Username),
	}
	for _, cookie := range devices {
		if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", cookie); response.Code != http.StatusOK {
			t.Fatalf("停用前会话不可用: status=%d", response.Code)
		}
	}
	response := performJSONRequest(t, router, http.MethodPatch, "/api/v1/users/"+user.ID+"/status", map[string]bool{"active": false}, admin)
	if response.Code != http.StatusNoContent {
		t.Fatalf("停用用户失败: status=%d body=%s", response.Code, response.Body.String())
	}
	for _, cookie := range devices {
		if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", cookie); response.Code != http.StatusUnauthorized {
			t.Fatalf("停用账户的旧会话仍可访问: status=%d", response.Code)
		}
	}
	response = performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": user.Username, "password": "correct horse battery staple",
	}, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("停用账户仍能登录: status=%d body=%s", response.Code, response.Body.String())
	}
	response = performJSONRequest(t, router, http.MethodPatch, "/api/v1/users/"+user.ID+"/status", map[string]bool{"active": true}, admin)
	if response.Code != http.StatusNoContent {
		t.Fatalf("重新启用用户失败: status=%d body=%s", response.Code, response.Body.String())
	}
	for _, cookie := range devices {
		if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", cookie); response.Code != http.StatusUnauthorized {
			t.Fatalf("已拒绝并清理的会话被重新启用: status=%d", response.Code)
		}
	}
	newCookie := loginForDepartmentTest(t, router, user.Username)
	if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", newCookie); response.Code != http.StatusOK {
		t.Fatalf("重新启用后新会话不可用: status=%d", response.Code)
	}
}

func TestAuthSessionDepartmentMoveInvalidatesOldDevicesHTTP(t *testing.T) {
	var deps Dependencies
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, valueDB *gorm.DB) { deps = *value; db = valueDB })
	t.Cleanup(cleanup)
	user, err := deps.Accounts.CreateUser(context.Background(), "session-moved", "部门调整测试", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	admin := loginForDepartmentTest(t, router, "admin")
	targetDepartment := createDepartmentForHTTPTest(t, router, admin, "会话调入部门")
	devices := []*http.Cookie{
		loginForDepartmentTest(t, router, user.Username),
		loginForDepartmentTest(t, router, user.Username),
	}
	repository := model.GitRepository{
		ID: "session-frozen-repository", DepartmentID: user.DepartmentID,
		ProjectID: database.DefaultProjectID(user.DepartmentID), CreatedBy: user.ID,
		Name: "session_frozen_repository", Provider: model.GitProviderGeneric, CloneURL: "https://example.com/session.git",
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	response := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+user.ID+"/department", map[string]string{"department_id": targetDepartment}, admin)
	if response.Code != http.StatusNoContent {
		t.Fatalf("调动部门失败: status=%d body=%s", response.Code, response.Body.String())
	}
	for _, cookie := range devices {
		if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", cookie); response.Code != http.StatusUnauthorized {
			t.Fatalf("部门调动后旧会话仍可访问: status=%d", response.Code)
		}
	}
	newCookie := loginForDepartmentTest(t, router, user.Username)
	response = sessionBoundaryRequest(t, router, "/api/v1/auth/me", newCookie)
	var payload struct {
		User struct {
			DepartmentID string `json:"department_id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != http.StatusOK || payload.User.DepartmentID != targetDepartment {
		t.Fatalf("新会话未采用新部门: status=%d department=%s err=%v", response.Code, payload.User.DepartmentID, err)
	}
	var frozen model.GitRepository
	if err := db.Where(&model.GitRepository{ID: repository.ID}).First(&frozen).Error; err != nil {
		t.Fatal(err)
	}
	if frozen.DepartmentID != repository.DepartmentID || frozen.ProjectID != repository.ProjectID || frozen.CreatedBy != user.ID {
		t.Fatalf("调动用户不应迁移历史资源: %+v", frozen)
	}
}

func TestAuthSessionRolePermissionUpdatesApplyWithoutLoginHTTP(t *testing.T) {
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) { deps = *value })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	roleID := createRoleForHTTPTest(t, router, admin, "session-live-role", []string{access.PermissionSystemRead})
	user, err := deps.Access.CreateUser(context.Background(), "session-role", "角色热更新测试", "correct horse battery staple", []string{roleID})
	if err != nil {
		t.Fatal(err)
	}
	cookie := loginForDepartmentTest(t, router, user.Username)
	if response := sessionBoundaryRequest(t, router, "/api/v1/system/info", cookie); response.Code != http.StatusOK {
		t.Fatalf("角色授权未生效: status=%d body=%s", response.Code, response.Body.String())
	}
	for _, permissions := range [][]string{{}, {access.PermissionSystemRead}} {
		response := performJSONRequest(t, router, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions", map[string]any{"permissions": permissions}, admin)
		if response.Code != http.StatusOK {
			t.Fatalf("更新角色权限失败: status=%d body=%s", response.Code, response.Body.String())
		}
		want := http.StatusForbidden
		if len(permissions) > 0 {
			want = http.StatusOK
		}
		if response := sessionBoundaryRequest(t, router, "/api/v1/system/info", cookie); response.Code != want {
			t.Fatalf("既有会话未立即采用角色权限: status=%d want=%d body=%s", response.Code, want, response.Body.String())
		}
	}
}

func TestAuthSessionHTTPHandlesExpiryCorruptionAndRedisFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *miniredis.Miniredis, *cache.Redis, string)
		want   int
	}{
		{"会话过期", func(_ *testing.T, server *miniredis.Miniredis, _ *cache.Redis, _ string) {
			server.FastForward(2 * time.Hour)
		}, http.StatusUnauthorized},
		{"会话记录仍在但有效期已过", func(t *testing.T, server *miniredis.Miniredis, client *cache.Redis, token string) {
			digest := sha256.Sum256([]byte(token))
			key := client.Key("session", hex.EncodeToString(digest[:]))
			value, err := server.Get(key)
			if err != nil {
				t.Fatal(err)
			}
			var session auth.Session
			if err := json.Unmarshal([]byte(value), &session); err != nil {
				t.Fatal(err)
			}
			session.ExpiresAt = time.Now().Add(-time.Minute)
			encoded, err := json.Marshal(session)
			if err != nil {
				t.Fatal(err)
			}
			if err := server.Set(key, string(encoded)); err != nil {
				t.Fatal(err)
			}
		}, http.StatusUnauthorized},
		{"会话内容损坏", func(t *testing.T, server *miniredis.Miniredis, client *cache.Redis, token string) {
			digest := sha256.Sum256([]byte(token))
			if err := server.Set(client.Key("session", hex.EncodeToString(digest[:])), "invalid-session-json"); err != nil {
				t.Fatal(err)
			}
		}, http.StatusInternalServerError},
		{"Redis服务不可用", func(_ *testing.T, server *miniredis.Miniredis, _ *cache.Redis, _ string) {
			server.Close()
		}, http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			address := server.Addr()
			client, err := cache.Open(context.Background(), config.Redis{URL: "redis://" + address, KeyPrefix: "edo:session-boundary:", Timeout: 100 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			var deps Dependencies
			router, cleanup := newAuthTestRouter(t, func(value *Dependencies, _ *gorm.DB) {
				value.Sessions = auth.NewSessionStore(client, time.Hour)
				deps = *value
			})
			t.Cleanup(cleanup)
			user, err := deps.Accounts.FindByUsername(context.Background(), "admin")
			if err != nil {
				t.Fatal(err)
			}
			token, _, err := deps.Sessions.Create(context.Background(), user.ID, user.AuthVersion)
			if err != nil {
				t.Fatal(err)
			}
			cookie := &http.Cookie{Name: deps.AuthConfig.CookieName, Value: token}
			if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", cookie); response.Code != http.StatusOK {
				t.Fatalf("故障前会话不可用: status=%d", response.Code)
			}
			test.mutate(t, server, client, token)
			response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", cookie)
			if response.Code != test.want {
				t.Fatalf("失效会话处理错误: status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			var payload struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			wantCode := "authentication_required"
			if test.want == http.StatusInternalServerError {
				wantCode = "auth_unavailable"
			}
			if payload.Code != wantCode || payload.Message == "" {
				t.Fatalf("认证错误未使用稳定文案: code=%s message=%q", payload.Code, payload.Message)
			}
			if strings.Contains(response.Body.String(), token) || strings.Contains(response.Body.String(), address) || strings.Contains(response.Body.String(), "invalid-session-json") {
				t.Fatal("认证错误向客户端泄露了内部诊断信息")
			}
		})
	}
}
