package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"edo/internal/access"
	"edo/internal/model"
)

func newDeliveryVariableProjectFixture(t *testing.T) *streamBoundaryFixture {
	t.Helper()
	f := newStreamBoundaryFixture(t)
	f.deps.Pipelines.ConfigureDeliveryAccess(f.deps.Access)
	return f
}

func deliveryVariableProjectRequest(t *testing.T, f *streamBoundaryFixture, cookie *http.Cookie, method, path, projectID string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if projectID != "" {
		request.Header.Set("X-EDO-Project-ID", projectID)
	}
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

func deliveryVariableProjectPayload(name, value string, version uint64) map[string]any {
	return map[string]any{"name": name, "value": value, "kind": "env", "usage": "build", "is_secret": true, "is_active": true, "expected_version": version}
}

func deliveryVariableProjectCreate(t *testing.T, f *streamBoundaryFixture, cookie *http.Cookie, projectID, name, value string) model.DeliveryVariable {
	t.Helper()
	response := deliveryVariableProjectRequest(t, f, cookie, http.MethodPost, "/delivery-variables", projectID, deliveryVariableProjectPayload(name, value, 0))
	var payload struct {
		Variable model.DeliveryVariable `json:"variable"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Variable.ID == "" {
		t.Fatalf("创建项目变量失败: status=%d", response.Code)
	}
	if bytes.Contains(response.Body.Bytes(), []byte(value)) {
		t.Fatal("创建响应泄漏密钥内容")
	}
	return payload.Variable
}

func deliveryVariableProjectRole(t *testing.T, f *streamBoundaryFixture, name string, permissions []string) string {
	t.Helper()
	role, err := f.deps.Access.CreateRole(context.Background(), access.RoleInput{Name: name, DisplayName: name, Permissions: permissions})
	if err != nil {
		t.Fatal(err)
	}
	return role.ID
}

func TestDeliveryVariableHTTPProjectRoleAndResourceIsolation(t *testing.T) {
	f := newDeliveryVariableProjectFixture(t)
	adminCookie := loginForDepartmentTest(t, f.router, f.admin.Username)
	other, err := f.deps.Access.CreateProject(context.Background(), f.admin, "变量只读项目", "", f.user.DepartmentID)
	if err != nil {
		t.Fatal(err)
	}
	readerRole := deliveryVariableProjectRole(t, f, "variable-project-reader", []string{"project.read", "delivery_variable.read"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, other.ID, f.user.ID, readerRole); err != nil {
		t.Fatal(err)
	}
	variableA := deliveryVariableProjectCreate(t, f, f.cookie, f.project.ID, "SAME_PROJECT_KEY", "project-a-secret")
	variableB := deliveryVariableProjectCreate(t, f, adminCookie, other.ID, "SAME_PROJECT_KEY", "project-b-secret")
	for _, test := range []struct {
		projectID string
		wantID    string
		otherID   string
	}{{f.project.ID, variableA.ID, variableB.ID}, {other.ID, variableB.ID, variableA.ID}} {
		response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, "/delivery-variables", test.projectID, nil)
		var payload struct {
			Variables []model.DeliveryVariable `json:"variables"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || len(payload.Variables) != 1 || payload.Variables[0].ID != test.wantID {
			t.Fatalf("项目变量列表未隔离: status=%d count=%d", response.Code, len(payload.Variables))
		}
		for _, forbidden := range []string{test.otherID, "project-a-secret", "project-b-secret"} {
			if bytes.Contains(response.Body.Bytes(), []byte(forbidden)) {
				t.Fatal("项目变量列表泄漏其他项目标识或密钥")
			}
		}
	}
	for _, test := range []struct {
		method string
		path   string
		body   any
	}{{http.MethodPost, "/delivery-variables", deliveryVariableProjectPayload("READER_CREATE", "not-stored", 0)},
		{http.MethodPut, "/delivery-variables/" + variableB.ID, deliveryVariableProjectPayload(variableB.Name, "not-stored", 1)},
		{http.MethodDelete, "/delivery-variables/" + variableB.ID + "?version=1", nil}} {
		response := deliveryVariableProjectRequest(t, f, f.cookie, test.method, test.path, other.ID, test.body)
		if response.Code != http.StatusForbidden {
			t.Fatalf("另一项目的只读角色允许写入: method=%s status=%d", test.method, response.Code)
		}
	}
	// 在有写权限的项目中提交外项目 UUID，也不能修改外项目资源。
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPut, "/delivery-variables/"+variableB.ID, f.project.ID, deliveryVariableProjectPayload(variableB.Name, "cross-project-not-stored", 1))
	if response.Code != http.StatusNotFound {
		t.Fatalf("跨项目变量 UUID 修改未按不存在处理: status=%d", response.Code)
	}
	response = deliveryVariableProjectRequest(t, f, f.cookie, http.MethodDelete, "/delivery-variables/"+variableB.ID+"?version=1", f.project.ID, nil)
	if response.Code < http.StatusBadRequest {
		t.Fatalf("跨项目变量 UUID 删除未被拒绝: status=%d", response.Code)
	}
	// 请求体不能覆盖当前请求所确定的项目范围。
	spoofed := deliveryVariableProjectPayload("BODY_PROJECT_SPOOF", "body-scoped-secret", 0)
	spoofed["project_id"] = other.ID
	response = deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPost, "/delivery-variables", f.project.ID, spoofed)
	var created struct {
		Variable model.DeliveryVariable `json:"variable"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &created) != nil || created.Variable.ProjectID != f.project.ID {
		t.Fatalf("请求体篡改了项目归属: status=%d", response.Code)
	}
	response = deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPut, "/delivery-variables/"+variableA.ID, f.project.ID, deliveryVariableProjectPayload(variableA.Name, "project-a-revised-secret", 1))
	if response.Code != http.StatusOK {
		t.Fatalf("本项目有权限的修改失败: status=%d", response.Code)
	}
	var persisted model.DeliveryVariable
	if err := f.db.Where("uuid = ?", variableB.ID).First(&persisted).Error; err != nil || persisted.Version != 1 || persisted.ProjectID != other.ID {
		t.Fatalf("拒绝操作仍修改了其他项目变量: version=%d err=%v", persisted.Version, err)
	}
}

func TestDeliveryVariableHTTPRejectsMissingProjectAndPlatformRoleGrant(t *testing.T) {
	f := newDeliveryVariableProjectFixture(t)
	variable := deliveryVariableProjectCreate(t, f, f.cookie, f.project.ID, "PROJECT_REQUIRED", "project-required-secret")
	for _, test := range []struct {
		method string
		path   string
		body   any
	}{{http.MethodGet, "/delivery-variables", nil},
		{http.MethodPost, "/delivery-variables", deliveryVariableProjectPayload("MISSING_CONTEXT", "not-stored", 0)},
		{http.MethodPut, "/delivery-variables/" + variable.ID, deliveryVariableProjectPayload(variable.Name, "not-stored", 1)},
		{http.MethodDelete, "/delivery-variables/" + variable.ID + "?version=1", nil}} {
		response := deliveryVariableProjectRequest(t, f, f.cookie, test.method, test.path, "", test.body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("缺少项目上下文仍处理了变量请求: method=%s status=%d", test.method, response.Code)
		}
	}
	roleID := deliveryVariableProjectRole(t, f, "variable-platform-grant", []string{"delivery_variable.read", "delivery_variable.create", "delivery_variable.update", "delivery_variable.delete"})
	if err := f.deps.Access.SetUserRoles(context.Background(), f.user.ID, []string{roleID}); err != nil {
		t.Fatal(err)
	}
	memberRoleID := deliveryVariableProjectRole(t, f, "variable-project-no-grant", []string{"project.read"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, memberRoleID); err != nil {
		t.Fatal(err)
	}
	response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, "/delivery-variables", f.project.ID, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("平台角色向项目授予了变量权限: status=%d", response.Code)
	}
	response = deliveryVariableProjectRequest(t, f, f.cookie, http.MethodPut, "/delivery-variables/"+variable.ID, f.project.ID, deliveryVariableProjectPayload(variable.Name, "not-stored", 1))
	if response.Code != http.StatusForbidden {
		t.Fatalf("平台角色向项目授予了变量修改权限: status=%d", response.Code)
	}
	other, err := f.deps.Access.CreateProject(context.Background(), f.admin, "变量未加入项目", "", f.user.DepartmentID)
	if err != nil {
		t.Fatal(err)
	}
	response = deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, "/delivery-variables", other.ID, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("未加入项目仍能读取变量: status=%d", response.Code)
	}
}

func TestDeliveryVariableHTTPFreshAuthorizationRejectsStalePolicy(t *testing.T) {
	for _, change := range []string{"撤销角色动作", "变更项目角色", "个人显式拒绝"} {
		t.Run(change, func(t *testing.T) {
			f := newDeliveryVariableProjectFixture(t)
			permissions := []string{"project.read", "delivery_variable.read", "delivery_variable.create", "delivery_variable.update", "delivery_variable.delete"}
			roleID := deliveryVariableProjectRole(t, f, "variable-fresh-writer", permissions)
			if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, roleID); err != nil {
				t.Fatal(err)
			}
			variable := deliveryVariableProjectCreate(t, f, f.cookie, f.project.ID, "FRESH_AUTH_SECRET", "original-encrypted-secret")
			var before model.DeliveryVariableRevision
			if err := f.db.Where("variable_id = ?", variable.ID).First(&before).Error; err != nil {
				t.Fatal(err)
			}
			// 直接修改测试数据库，故意不触发策略重载，模拟多实例同步延迟。
			switch change {
			case "撤销角色动作":
				if err := f.db.Where("role_id = ? AND permission IN ?", roleID, permissions[1:]).Delete(&model.RolePermission{}).Error; err != nil {
					t.Fatal(err)
				}
			case "变更项目角色":
				readerID := deliveryVariableProjectRole(t, f, "variable-fresh-reader", []string{"project.read"})
				if err := f.db.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", f.project.ID, f.user.ID).Update("role_id", readerID).Error; err != nil {
					t.Fatal(err)
				}
			case "个人显式拒绝":
				for _, permission := range permissions[1:] {
					if err := f.db.Create(&model.UserPermission{UserID: f.user.ID, Permission: permission, Effect: model.PermissionDeny, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			allowed, err := f.deps.Access.HasPermission(f.scope(), f.user, "delivery_variable.update")
			if err != nil || !allowed {
				t.Fatalf("用例未保留旧策略，无法验证实时授权: allowed=%v err=%v", allowed, err)
			}
			for _, test := range []struct {
				method string
				path   string
				body   any
			}{{http.MethodGet, "/delivery-variables", nil},
				{http.MethodPost, "/delivery-variables", deliveryVariableProjectPayload("REVOKED_CREATE", "rejected-created-secret", 0)},
				{http.MethodPut, "/delivery-variables/" + variable.ID, deliveryVariableProjectPayload(variable.Name, "rejected-revised-secret", 1)},
				{http.MethodDelete, "/delivery-variables/" + variable.ID + "?version=1", nil}} {
				response := deliveryVariableProjectRequest(t, f, f.cookie, test.method, test.path, f.project.ID, test.body)
				if response.Code != http.StatusForbidden {
					t.Fatalf("变量接口沿用过期项目授权: method=%s status=%d", test.method, response.Code)
				}
			}
			var after model.DeliveryVariableRevision
			if err := f.db.Where("variable_id = ?", variable.ID).First(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("撤权后的请求修改了原密钥修订: err=%v", err)
			}
			var persisted model.DeliveryVariable
			if err := f.db.Where("uuid = ?", variable.ID).First(&persisted).Error; err != nil || persisted.Version != 1 || persisted.DeletedAt.Valid {
				t.Fatalf("撤权后的请求修改了变量版本或删除状态: version=%d err=%v", persisted.Version, err)
			}
			var count int64
			if err := f.db.Model(&model.DeliveryVariableRevision{}).Where("variable_id = ?", variable.ID).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("撤权后的请求新增了密钥修订: count=%d err=%v", count, err)
			}
			if err := f.db.Model(&model.DeliveryVariable{}).Where("project_id = ?", f.project.ID).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("撤权后的请求创建了变量: count=%d err=%v", count, err)
			}
		})
	}
}

func TestDeliveryVariableHTTPMembershipRevocationOverridesDirectAllow(t *testing.T) {
	f := newDeliveryVariableProjectFixture(t)
	variable := deliveryVariableProjectCreate(t, f, f.cookie, f.project.ID, "MEMBERSHIP_SECRET", "membership-secret")
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Allow: []string{"delivery_variable.read", "delivery_variable.update"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method string
		path   string
		body   any
	}{{http.MethodGet, "/delivery-variables", nil},
		{http.MethodPut, "/delivery-variables/" + variable.ID, deliveryVariableProjectPayload(variable.Name, "not-stored", 1)}} {
		response := deliveryVariableProjectRequest(t, f, f.cookie, test.method, test.path, f.project.ID, test.body)
		if response.Code != http.StatusForbidden {
			t.Fatalf("个人允许绕过项目成员撤销: method=%s status=%d", test.method, response.Code)
		}
	}
}

func TestDeliveryHTTPWriteMiddlewareRejectsStaleProjectRoleAndDeny(t *testing.T) {
	for _, change := range []string{"角色撤销动作", "个人拒绝动作"} {
		t.Run(change, func(t *testing.T) {
			f := newDeliveryVariableProjectFixture(t)
			request := func(method, path string, payload any, want int) *httptest.ResponseRecorder {
				t.Helper()
				response := deliveryVariableProjectRequest(t, f, f.cookie, method, path, f.project.ID, payload)
				if response.Code != want {
					t.Fatalf("交付项目动作返回异常: method=%s path=%s status=%d want=%d", method, path, response.Code, want)
				}
				return response
			}
			id := func(response *httptest.ResponseRecorder, key string) string {
				t.Helper()
				var payload map[string]struct{ ID string }
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload[key].ID == "" {
					t.Fatalf("读取交付资源标识失败: key=%s err=%v", key, err)
				}
				return payload[key].ID
			}
			repositoryID := id(request(http.MethodPost, "/repositories", map[string]any{"name": "middleware_repo", "provider": "generic", "auth_type": "none", "clone_url": "https://example.invalid/project.git"}, http.StatusCreated), "repository")
			applicationID := id(request(http.MethodPost, "/applications", map[string]any{"name": "middleware_app", "repository_id": repositoryID}, http.StatusCreated), "application")
			buildID := id(request(http.MethodPost, "/build-plans", map[string]any{"name": "middleware_build", "kind": "dockerfile", "dockerfile_path": "Dockerfile"}, http.StatusCreated), "build_plan")
			var beforeApplication model.Application
			var beforeBuild model.BuildPlan
			var beforeWorkflow model.ReleaseWorkflow
			if err := f.db.Where("uuid = ?", applicationID).First(&beforeApplication).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Where("uuid = ?", buildID).First(&beforeBuild).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Where("application_id = ?", applicationID).First(&beforeWorkflow).Error; err != nil {
				t.Fatal(err)
			}
			readCodes := []string{"project.read", "application.read", "build_plan.read", "workflow.read"}
			writeCodes := []string{"application.create", "application.update", "build_plan.create", "build_plan.update", "build_plan.delete", "workflow.create", "workflow.update", "workflow.delete"}
			roleID := deliveryVariableProjectRole(t, f, "delivery-middleware-writer", append(readCodes, writeCodes...))
			if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, roleID); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/applications", "/build-plans", "/applications/" + applicationID + "/workflows"} {
				request(http.MethodGet, path, nil, http.StatusOK)
			}
			// 模拟别的实例已提交撤权、当前实例的 Casbin 还没收到重载通知。
			if change == "角色撤销动作" {
				if err := f.db.Where("role_id = ? AND permission IN ?", roleID, writeCodes).Delete(&model.RolePermission{}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				for _, permission := range writeCodes {
					if err := f.db.Create(&model.UserPermission{UserID: f.user.ID, Permission: permission, Effect: model.PermissionDeny, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, permission := range writeCodes {
				if allowed, err := f.deps.Access.HasPermission(f.scope(), f.user, permission); err != nil || !allowed {
					t.Fatalf("回归夹具没有保留过期动作: permission=%s allowed=%v err=%v", permission, allowed, err)
				}
			}
			for _, action := range []struct {
				method, path string
				payload      any
			}{
				{http.MethodPost, "/applications", map[string]any{"name": "rejected_app", "repository_id": repositoryID}},
				{http.MethodPut, "/applications/" + applicationID, map[string]any{"name": "rejected_app", "repository_id": repositoryID}},
				{http.MethodPatch, "/applications/" + applicationID + "/status", map[string]any{"active": false}},
				{http.MethodPost, "/build-plans", map[string]any{"name": "rejected_build", "kind": "dockerfile", "dockerfile_path": "Dockerfile"}},
				{http.MethodPut, "/build-plans/" + buildID, map[string]any{"name": "rejected_build", "kind": "dockerfile", "dockerfile_path": "Dockerfile"}},
				{http.MethodPatch, "/build-plans/" + buildID + "/status", map[string]any{"active": false}},
				{http.MethodDelete, "/build-plans/" + buildID, nil},
				{http.MethodPost, "/applications/" + applicationID + "/workflows", map[string]any{"name": "rejected_workflow"}},
				{http.MethodPut, "/applications/" + applicationID + "/workflows/" + beforeWorkflow.ID, map[string]any{"schema_version": 1, "name": "rejected_workflow", "revision": beforeWorkflow.Revision, "source": beforeWorkflow.Source, "stages": beforeWorkflow.Stages}},
				{http.MethodDelete, "/applications/" + applicationID + "/workflows/" + beforeWorkflow.ID, nil},
			} {
				request(action.method, action.path, action.payload, http.StatusForbidden)
			}
			// 撤销写动作不应同时剥夺仍有授权的项目读取能力。
			for _, path := range []string{"/applications", "/build-plans", "/applications/" + applicationID + "/workflows"} {
				request(http.MethodGet, path, nil, http.StatusOK)
			}
			var afterApplication model.Application
			var afterBuild model.BuildPlan
			var afterWorkflow model.ReleaseWorkflow
			for _, item := range []struct {
				id    string
				value any
			}{{applicationID, &afterApplication}, {buildID, &afterBuild}, {beforeWorkflow.ID, &afterWorkflow}} {
				if err := f.db.Where("uuid = ?", item.id).First(item.value).Error; err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(beforeApplication, afterApplication) || !reflect.DeepEqual(beforeBuild, afterBuild) || !reflect.DeepEqual(beforeWorkflow, afterWorkflow) {
				t.Fatal("拒绝交付写操作后仍改变了资源、状态或流水线版本")
			}
			for _, value := range []any{&model.Application{}, &model.BuildPlan{}, &model.ReleaseWorkflow{}} {
				var count int64
				if err := f.db.Model(value).Where("project_id = ?", f.project.ID).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("撤权后创建或删除了交付资源: count=%d err=%v", count, err)
				}
			}
		})
	}
}

func TestDeliveryHTTPAnyProjectPermissionRechecksEachCurrentGrant(t *testing.T) {
	f := newDeliveryVariableProjectFixture(t)
	// 替换业务处理器以免依赖主机采样，保留正式登录、项目及任一动作中间件。
	f.router.GET("/api/v1/hosts/authorization-probe",
		requireAuth(f.deps.Accounts, f.deps.Sessions, f.deps.Logger, f.deps.AuthConfig.CookieName),
		projectScope(f.deps.Access, f.deps.Logger),
		requireAnyPermission(f.deps.Access, f.deps.Logger, "cluster.read", "deployment.read"),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)
	roleID := deliveryVariableProjectRole(t, f, "delivery-any-reader", []string{"project.read", "cluster.read", "deployment.read"})
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, roleID); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		revoke string
		want   int
	}{{"", http.StatusOK}, {"cluster.read", http.StatusOK}, {"deployment.read", http.StatusForbidden}} {
		if step.revoke != "" {
			if err := f.db.Where("role_id = ? AND permission = ?", roleID, step.revoke).Delete(&model.RolePermission{}).Error; err != nil {
				t.Fatal(err)
			}
			if allowed, err := f.deps.Access.HasPermission(f.scope(), f.user, step.revoke); err != nil || !allowed {
				t.Fatalf("未保留过期的多选读取策略: allowed=%v err=%v", allowed, err)
			}
		}
		response := deliveryVariableProjectRequest(t, f, f.cookie, http.MethodGet, "/hosts/authorization-probe", f.project.ID, nil)
		if response.Code != step.want {
			t.Fatalf("任一权限中间件没有以实时项目授权判断: revoked=%s status=%d want=%d", step.revoke, response.Code, step.want)
		}
	}
}
