package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/assistant"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

// 白名单按实际 HTTP 方法和注册路径匹配，不允许新增接口因同一前缀而自动公开。
func inventoryPublicAPIRoutes() map[string]int {
	return map[string]int{
		"GET /api/v1/health/live":             http.StatusOK,
		"GET /api/v1/health/ready":            http.StatusOK,
		"POST /api/v1/auth/login":             http.StatusBadRequest,
		"POST /api/v1/auth/logout":            http.StatusNoContent,
		"GET /api/v1/auth/providers":          http.StatusOK,
		"POST /api/v1/auth/ldap/:id/login":    http.StatusBadRequest,
		"POST /api/v1/auth/email/:id/code":    http.StatusBadRequest,
		"POST /api/v1/auth/email/:id/login":   http.StatusBadRequest,
		"GET /api/v1/auth/oauth/:id/start":    http.StatusNotFound,
		"GET /api/v1/auth/oauth/:id/callback": http.StatusFound,
		"POST /api/v1/webhooks/git/:id":       http.StatusNotFound,
	}
}

func inventoryAPIPath(t *testing.T, pattern, projectID string) string {
	t.Helper()
	parts := strings.Split(pattern, "/")
	for index, part := range parts {
		if strings.HasPrefix(part, "*") {
			t.Fatalf("API 巡检尚未定义通配参数: %s", pattern)
		}
		if !strings.HasPrefix(part, ":") {
			continue
		}
		switch part {
		case ":id":
			parts[index] = "514a3880-b6a2-4233-9006-7f38638fd42b"
			if strings.HasPrefix(pattern, "/api/v1/projects/") {
				parts[index] = projectID
			}
		case ":user_id", ":workflow_id", ":application_id", ":group_id", ":endpoint_id", ":cluster_id", ":join", ":record_id":
			parts[index] = "fae9f5e6-eaae-4092-9cf7-97a0f5e05510"
		case ":resource":
			parts[index] = "deployments"
			if strings.HasSuffix(pattern, "/secret") {
				parts[index] = "secrets"
			}
		case ":name":
			parts[index] = "inventory-resource"
		case ":namespace":
			parts[index] = "default"
		case ":pod":
			parts[index] = "inventory-pod"
		case ":container":
			parts[index] = "app"
		case ":container_id":
			parts[index] = strings.Repeat("a", 64)
		case ":kind":
			parts[index] = "docker"
		default:
			t.Fatalf("API 巡检尚未定义动态参数 %s: %s", part, pattern)
		}
	}
	return strings.Join(parts, "/")
}

func inventoryAPIRequest(t *testing.T, router http.Handler, route gin.RouteInfo, projectID string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body := "{}"
	if route.Method == http.MethodPut && route.Path == "/api/v1/projects/:id" {
		body = `{"name":"认证巡检项目","is_active":true}`
	}
	request := httptest.NewRequest(route.Method, inventoryAPIPath(t, route.Path, projectID), strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
		request.Header.Set("X-EDO-Project-ID", projectID)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func inventoryBusinessCounts(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64)
	for _, table := range []string{"users", "projects", "project_members", "jobs", "outbox_events", "pipeline_runs", "build_runs", "deployment_records", "artifacts", "notifications", "assistant_conversations", "assistant_actions"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil {
			t.Fatalf("读取巡检前后记录数量失败: table=%s err=%v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func inventoryAssertNoBusinessWrites(t *testing.T, db *gorm.DB, before map[string]int64) {
	t.Helper()
	for table, after := range inventoryBusinessCounts(t, db) {
		if after != before[table] {
			t.Errorf("权限巡检不应产生业务记录: table=%s before=%d after=%d", table, before[table], after)
		}
	}
}

func TestAPIRouteAnonymousAuthenticationInventory(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, value *gorm.DB) {
		db = value
		// 真实 Docker 服务在解析配置时即降级，不读取宿主机 Docker 环境或连接 daemon。
		deps.Docker = dockerengine.NewService(value, nil, config.Runtime{DockerBuilderHost: "inventory-invalid://builder"})
	})
	t.Cleanup(cleanup)
	before := inventoryBusinessCounts(t, db)
	public := inventoryPublicAPIRoutes()
	seen := make(map[string]bool)
	protectedCount, publicCount := 0, 0
	for _, route := range router.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") {
			continue
		}
		key := route.Method + " " + route.Path
		response := inventoryAPIRequest(t, router, route, database.DefaultProjectID(database.DefaultDepartmentID), nil)
		want, isPublic := public[key]
		if isPublic {
			publicCount++
			seen[key] = true
		} else {
			protectedCount++
			want = http.StatusUnauthorized
		}
		if response.Code != want {
			t.Errorf("匿名认证门禁与显式白名单不符: route=%s status=%d want=%d body=%s", key, response.Code, want, response.Body.String())
		}
		if !isPublic {
			var payload errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Code != "authentication_required" {
				t.Errorf("受保护接口未返回认证门禁错误: route=%s code=%s err=%v", key, payload.Code, err)
			}
		}
		for _, cookie := range response.Result().Cookies() {
			if cookie.Name == "edo_session" && cookie.Value != "" && cookie.MaxAge >= 0 {
				t.Errorf("无凭据巡检不应取得登录会话: route=%s", key)
			}
		}
	}
	for key := range public {
		if !seen[key] {
			t.Errorf("公开白名单含有未注册或已改名接口: %s", key)
		}
	}
	if protectedCount == 0 {
		t.Fatal("未枚举到受保护 API")
	}
	inventoryAssertNoBusinessWrites(t, db, before)
	t.Logf("API_ROUTE_INVENTORY anonymous: protected=%d public=%d unchecked=0", protectedCount, publicCount)
}

func TestAPIRouteUnprivilegedActionInventory(t *testing.T) {
	var deps Dependencies
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, valueDB *gorm.DB) {
		// 助手可用性检查在动作中间件之前，使用无模型凭据的真实服务避免 503 掩盖授权。
		value.Assistant = assistant.NewService(assistant.Dependencies{Database: valueDB, Access: value.Access, Audits: value.Audits, Logger: value.Logger})
		value.Docker = dockerengine.NewService(valueDB, nil, config.Runtime{DockerBuilderHost: "inventory-invalid://builder"})
		deps, db = *value, valueDB
	})
	t.Cleanup(cleanup)
	user, err := deps.Accounts.CreateUser(context.Background(), "route-inventory", "无动作权限巡检", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := model.Project{ID: uuid.NewString(), DepartmentID: user.DepartmentID, Name: "认证巡检项目", IsActive: true, CreatedAt: now, UpdatedAt: now}
	role := model.Role{ID: uuid.NewString(), Name: "route_inventory_empty", DisplayName: "无动作权限", CreatedAt: now, UpdatedAt: now}
	for _, record := range []any{&project, &role, &model.ProjectMember{ProjectID: project.ID, UserID: user.ID, RoleID: role.ID, CreatedAt: now}} {
		if err := db.Create(record).Error; err != nil {
			t.Fatal(err)
		}
	}
	permissions, err := deps.Access.UserPermissions(database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: project.ID}), user)
	if err != nil || len(permissions) != 0 || user.IsSuperuser {
		t.Fatalf("巡检用户必须已加入项目且无任何动作权限: permissions=%v superuser=%v err=%v", permissions, user.IsSuperuser, err)
	}
	token, _, err := deps.Sessions.Create(context.Background(), user.ID, user.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: deps.AuthConfig.CookieName, Value: token}
	before := inventoryBusinessCounts(t, db)
	public := inventoryPublicAPIRoutes()
	// 个人会话与项目成员自助查询不要求业务动作；密码使用空请求，仅验证必填校验。
	selfService := map[string]int{
		"GET /api/v1/auth/me":       http.StatusOK,
		"PUT /api/v1/auth/password": http.StatusBadRequest,
		"GET /api/v1/projects":      http.StatusOK,
		"GET /api/v1/projects/:id":  http.StatusOK,
	}
	seenSelfService := make(map[string]bool)
	deniedCount, selfCount, publicCount := 0, 0, 0
	for _, route := range router.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") {
			continue
		}
		key := route.Method + " " + route.Path
		if _, ok := public[key]; ok {
			publicCount++
			continue
		}
		response := inventoryAPIRequest(t, router, route, project.ID, cookie)
		want, isSelfService := selfService[key]
		if isSelfService {
			selfCount++
			seenSelfService[key] = true
		} else {
			deniedCount++
			want = http.StatusForbidden
		}
		if response.Code != want {
			t.Errorf("普通无权限用户动作门禁错误: route=%s status=%d want=%d body=%s", key, response.Code, want, response.Body.String())
		}
		if !isSelfService {
			var payload errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || (payload.Code != "permission_denied" && payload.Code != "superuser_required") {
				t.Errorf("未到达动作/超级管理员门禁，不能把项目准入失败当授权测试: route=%s code=%s err=%v", key, payload.Code, err)
			}
		}
	}
	for key := range selfService {
		if !seenSelfService[key] {
			t.Errorf("自助接口例外未对应实际路由: %s", key)
		}
	}
	inventoryAssertNoBusinessWrites(t, db, before)
	storedUser, err := deps.Accounts.FindByID(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedUser.AuthVersion != user.AuthVersion || storedUser.DepartmentID != user.DepartmentID || !storedUser.IsActive {
		t.Fatalf("门禁巡检不应改变登录账号: user_id=%s version=%d department=%s active=%v", storedUser.ID, storedUser.AuthVersion, storedUser.DepartmentID, storedUser.IsActive)
	}
	var storedProject model.Project
	if err := db.Where(&model.Project{ID: project.ID}).First(&storedProject).Error; err != nil {
		t.Fatal(err)
	}
	if storedProject.Name != project.Name || !storedProject.IsActive {
		t.Errorf("被拒绝的项目修改仍改变了项目: name=%q active=%v", storedProject.Name, storedProject.IsActive)
	}
	t.Logf("API_ROUTE_INVENTORY unprivileged: forbidden=%d self_service=%d public_not_repeated=%d unchecked=0", deniedCount, selfCount, publicCount)
}
