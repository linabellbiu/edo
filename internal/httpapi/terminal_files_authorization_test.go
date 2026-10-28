package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/access"
	hostmanager "edo/internal/host"
	"edo/internal/model"
	"edo/internal/terminalfiles"
)

func newTerminalAuthorizationFixture(t *testing.T) *streamBoundaryFixture {
	t.Helper()
	f := &streamBoundaryFixture{}
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, db *gorm.DB) {
		// 故意不给本地 PTY 或任何容器客户端；允许请求只到安全预检边界。
		deps.Hosts = hostmanager.NewService(db, nil, nil, nil)
		deps.Terminal = nil
		f.deps, f.db = *deps, db
	})
	t.Cleanup(cleanup)
	f.router = router
	var err error
	f.admin, err = f.deps.Accounts.FindByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	f.user, err = f.deps.Accounts.CreateUser(context.Background(), "terminal-authorization", "终端文件权限测试", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	f.project, err = f.deps.Access.CreateProject(context.Background(), f.admin, "终端文件权限项目", "", f.user.DepartmentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, access.ProjectOwnerRoleID); err != nil {
		t.Fatal(err)
	}
	f.cookie = loginForDepartmentTest(t, router, f.user.Username)
	return f
}

func terminalAuthorizationRole(t *testing.T, f *streamBoundaryFixture, permissions []string, deny ...string) {
	t.Helper()
	name := "terminal-auth-" + uuid.NewString()[:8]
	role, err := f.deps.Access.CreateRole(context.Background(), access.RoleInput{Name: name, DisplayName: "终端文件隔离角色", Permissions: permissions})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, role.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Deny: deny}); err != nil {
		t.Fatal(err)
	}
}

func terminalAuthorizationRequest(t *testing.T, f *streamBoundaryFixture, method, path, project, body, etag string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if project != "" {
		request.Header.Set("X-EDO-Project-ID", project)
	}
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

func terminalAuthorizationHost(t *testing.T, f *streamBoundaryFixture) string {
	t.Helper()
	id := uuid.NewString()
	current := model.Host{ID: id, Name: "无有效指纹的隔离主机", Mode: model.HostModeSSH, Address: "192.0.2.252", SSHPort: 22, SSHUsername: "must-not-be-returned", SSHCredentialCiphertext: "must-not-be-returned-ciphertext", IsActive: true}
	if err := f.db.WithContext(f.scope()).Create(&current).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.HostCapability{HostID: id, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTerminalAuthorizationUnauthenticatedRoutesRequireLogin(t *testing.T) {
	f := newTerminalAuthorizationFixture(t)
	for _, target := range []string{
		"/terminals/hosts/isolated-host",
		"/terminals/docker/isolated-endpoint/containers/isolated-container",
		"/terminals/kubernetes/isolated-cluster/namespaces/default/pods/isolated-pod/containers/main",
	} {
		for _, request := range []struct{ method, suffix, body string }{
			{http.MethodGet, "/ws", ""},
			{http.MethodGet, "/files?path=/", ""},
			{http.MethodGet, "/files/content?path=/demo.txt", ""},
			{http.MethodGet, "/files/download?path=/demo.txt", ""},
			{http.MethodPost, "/files/upload?path=/demo.txt", "not-stored"},
			{http.MethodPut, "/files/content", `{"path":"/demo.txt","content":"not-stored","etag":"` + strings.Repeat("0", 64) + `"}`},
		} {
			response := terminalAuthorizationRequest(t, f, request.method, target+request.suffix, f.project.ID, request.body, "", nil)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("未登录终端或文件接口非401: %s %s status=%d", request.method, target+request.suffix, response.Code)
			}
		}
	}
}

func TestTerminalAuthorizationContainerPermissionNeverGrantsHostShell(t *testing.T) {
	f := newTerminalAuthorizationFixture(t)
	const path = "/terminals/hosts/isolated-host/ws"
	terminalAuthorizationRole(t, f, []string{access.PermissionClusterRead, access.PermissionTerminalOpen})
	response := terminalAuthorizationRequest(t, f, http.MethodGet, path, f.project.ID, "", "", f.cookie)
	if response.Code != http.StatusForbidden {
		t.Fatalf("容器终端权限被自动升级成主机终端: %d %s", response.Code, response.Body)
	}
	terminalAuthorizationRole(t, f, []string{access.PermissionClusterRead, access.PermissionHostTerminalOpen})
	response = terminalAuthorizationRequest(t, f, http.MethodGet, path, f.project.ID, "", "", f.cookie)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("独立主机权限未到达安全的未初始化服务边界: %d %s", response.Code, response.Body)
	}
	response = terminalAuthorizationRequest(t, f, http.MethodGet, "/terminals/docker/isolated-endpoint/containers/isolated-container/ws", f.project.ID, "", "", f.cookie)
	if response.Code != http.StatusForbidden {
		t.Fatalf("主机终端权限反向授予了容器终端: %d %s", response.Code, response.Body)
	}
	terminalAuthorizationRole(t, f, []string{access.PermissionClusterRead, access.PermissionHostTerminalOpen}, access.PermissionHostTerminalOpen)
	response = terminalAuthorizationRequest(t, f, http.MethodGet, path, f.project.ID, "", "", f.cookie)
	if response.Code != http.StatusForbidden {
		t.Fatalf("个人拒绝未覆盖主机终端角色: %d", response.Code)
	}
}

func TestTerminalFileAuthorizationReadUploadAndEditAreIndependent(t *testing.T) {
	f := newTerminalAuthorizationFixture(t)
	hostID := terminalAuthorizationHost(t, f)
	base := "/terminals/hosts/" + hostID + "/files"
	const originalETag = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	edit := `{"path":"/demo.txt","content":"not-stored","etag":"` + originalETag + `"}`
	for _, test := range []struct {
		name                             string
		permissions, deny                []string
		list, upload, overwrite, editing int
	}{
		{"仅容器终端", []string{access.PermissionTerminalOpen}, nil, 403, 403, 403, 403},
		{"仅主机终端", []string{access.PermissionHostTerminalOpen}, nil, 403, 403, 403, 403},
		{"文件读取", []string{access.PermissionTerminalFileRead}, nil, 503, 403, 403, 403},
		{"仅上传新文件", []string{access.PermissionTerminalFileUpload}, nil, 403, 503, 403, 403},
		{"读取与编辑", []string{access.PermissionTerminalFileRead, access.PermissionTerminalFileEdit}, nil, 503, 403, 403, 503},
		{"上传与编辑缺读取", []string{access.PermissionTerminalFileUpload, access.PermissionTerminalFileEdit}, nil, 403, 503, 403, 403},
		{"上传与读取缺编辑", []string{access.PermissionTerminalFileUpload, access.PermissionTerminalFileRead}, nil, 503, 503, 403, 403},
		{"完整文件能力", []string{access.PermissionTerminalFileRead, access.PermissionTerminalFileUpload, access.PermissionTerminalFileEdit}, nil, 503, 503, 503, 503},
		{"个人拒绝读取", []string{access.PermissionTerminalFileRead, access.PermissionTerminalFileUpload, access.PermissionTerminalFileEdit}, []string{access.PermissionTerminalFileRead}, 403, 503, 403, 403},
		{"个人拒绝上传", []string{access.PermissionTerminalFileRead, access.PermissionTerminalFileUpload, access.PermissionTerminalFileEdit}, []string{access.PermissionTerminalFileUpload}, 503, 403, 403, 503},
		{"个人拒绝编辑", []string{access.PermissionTerminalFileRead, access.PermissionTerminalFileUpload, access.PermissionTerminalFileEdit}, []string{access.PermissionTerminalFileEdit}, 503, 503, 403, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminalAuthorizationRole(t, f, append([]string{access.PermissionClusterRead}, test.permissions...), test.deny...)
			for _, request := range []struct {
				method, suffix, body, etag string
				status                     int
			}{
				{http.MethodGet, "?path=/", "", "", test.list},
				{http.MethodPost, "/upload?path=/demo.txt", "not-stored", "", test.upload},
				{http.MethodPost, "/upload?path=/demo.txt", "not-stored", originalETag, test.overwrite},
				{http.MethodPut, "/content", edit, "", test.editing},
			} {
				response := terminalAuthorizationRequest(t, f, request.method, base+request.suffix, f.project.ID, request.body, request.etag, f.cookie)
				if response.Code != request.status {
					t.Fatalf("文件权限组合错误: %s %s got=%d want=%d body=%s", request.method, request.suffix, response.Code, request.status, response.Body)
				}
				for _, private := range []string{"192.0.2.252", "must-not-be-returned", "must-not-be-returned-ciphertext"} {
					if strings.Contains(response.Body.String(), private) {
						t.Fatal("文件错误响应泄漏主机内部配置")
					}
				}
			}
		})
	}
}

func TestTerminalFileAuthorizationContainerAndPodResourceReadRemainRequired(t *testing.T) {
	f := newTerminalAuthorizationFixture(t)
	for _, target := range []struct{ path, read string }{
		{"/terminals/docker/isolated-endpoint/containers/isolated-container/files", access.PermissionClusterRead},
		{"/terminals/kubernetes/isolated-cluster/namespaces/default/pods/isolated-pod/containers/main/files", "kubernetes.workload.read"},
	} {
		terminalAuthorizationRole(t, f, []string{access.PermissionTerminalFileRead})
		response := terminalAuthorizationRequest(t, f, http.MethodGet, target.path+"?path=/", f.project.ID, "", "", f.cookie)
		if response.Code != http.StatusForbidden {
			t.Fatalf("文件读取权限绕过了资源读取权限: %s %d", target.path, response.Code)
		}
		terminalAuthorizationRole(t, f, []string{target.read, access.PermissionTerminalFileRead})
		response = terminalAuthorizationRequest(t, f, http.MethodGet, target.path+"?path=/", f.project.ID, "", "", f.cookie)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("文件授权未到达未初始化 provider 边界: %s %d %s", target.path, response.Code, response.Body)
		}
		terminalAuthorizationRole(t, f, []string{target.read, access.PermissionTerminalFileRead}, target.read)
		response = terminalAuthorizationRequest(t, f, http.MethodGet, target.path+"?path=/", f.project.ID, "", "", f.cookie)
		if response.Code != http.StatusForbidden {
			t.Fatalf("个人拒绝资源读取仍可打开文件: %s %d", target.path, response.Code)
		}
	}
}

func TestTerminalFileAuthorizationProjectIsolationAndMembershipRemoval(t *testing.T) {
	f := newTerminalAuthorizationFixture(t)
	hostID := terminalAuthorizationHost(t, f)
	permissions := []string{access.PermissionClusterRead, access.PermissionTerminalFileRead}
	terminalAuthorizationRole(t, f, permissions)
	other, err := f.deps.Access.CreateProject(context.Background(), f.admin, "终端文件另一个项目", "", f.user.DepartmentID)
	if err != nil {
		t.Fatal(err)
	}
	role, err := f.deps.Access.CreateRole(context.Background(), access.RoleInput{Name: "terminal-other-reader", DisplayName: "另项目文件读取", Permissions: permissions})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, other.ID, f.user.ID, role.ID); err != nil {
		t.Fatal(err)
	}
	path := "/terminals/hosts/" + hostID + "/files?path=/"
	if response := terminalAuthorizationRequest(t, f, http.MethodGet, path, f.project.ID, "", "", f.cookie); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("本项目无有效指纹的主机应只到预检边界: %d %s", response.Code, response.Body)
	}
	for _, id := range []string{hostID, uuid.NewString()} {
		response := terminalAuthorizationRequest(t, f, http.MethodGet, "/terminals/hosts/"+id+"/files?path=/", other.ID, "", "", f.cookie)
		if response.Code != http.StatusNotFound {
			t.Fatalf("外项目和不存在主机应同为404: %d %s", response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), hostID) || strings.Contains(response.Body.String(), "192.0.2.252") {
			t.Fatal("外项目主机错误泄漏目标信息")
		}
	}
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Allow: permissions}); err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, ""); err != nil {
		t.Fatal(err)
	}
	response := terminalAuthorizationRequest(t, f, http.MethodGet, path, f.project.ID, "", "", f.cookie)
	if response.Code != http.StatusForbidden {
		t.Fatalf("个人文件允许绕过项目成员边界: %d %s", response.Code, response.Body)
	}
}

func TestTerminalFileAuthorizationDeniedBeforeProviderAndSafeAudit(t *testing.T) {
	f := newTerminalAuthorizationFixture(t)
	backend := &fakeFileBackend{value: []byte("READ-CONTENT-MUST-NOT-ENTER-AUDIT")}
	var opened atomic.Int32
	h := terminalFileHandler{logger: f.deps.Logger, audits: f.deps.Audits, key: func(*gin.Context) string { return "host:authorization-provider" }, open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) {
		opened.Add(1)
		return backend, nil
	}, checks: func(c *gin.Context, permissions ...string) func() bool {
		checks := make([]func() bool, len(permissions))
		for index, permission := range permissions {
			checks[index] = streamAuthorization(f.deps, c, permission)
		}
		return func() bool {
			for _, check := range checks {
				if !check() {
					return false
				}
			}
			return true
		}
	}}
	group := f.router.Group("/api/v1/terminals/authorization-provider")
	group.Use(requireAuth(f.deps.Accounts, f.deps.Sessions, f.deps.Logger, f.deps.AuthConfig.CookieName), projectScope(f.deps.Access, f.deps.Logger))
	group.GET("/files/content", h.audit("terminal.file.read"), requirePermission(f.deps.Access, f.deps.Logger, access.PermissionClusterRead), requirePermission(f.deps.Access, f.deps.Logger, access.PermissionTerminalFileRead), h.content)
	group.POST("/files/upload", h.audit("terminal.file.upload"), requirePermission(f.deps.Access, f.deps.Logger, access.PermissionClusterRead), requirePermission(f.deps.Access, f.deps.Logger, access.PermissionTerminalFileUpload), h.upload)
	group.PUT("/files/content", h.audit("terminal.file.edit"), requirePermission(f.deps.Access, f.deps.Logger, access.PermissionClusterRead), requirePermission(f.deps.Access, f.deps.Logger, access.PermissionTerminalFileRead), requirePermission(f.deps.Access, f.deps.Logger, access.PermissionTerminalFileEdit), h.edit)
	base := "/terminals/authorization-provider/files"
	terminalAuthorizationRole(t, f, []string{access.PermissionClusterRead, access.PermissionTerminalFileUpload})
	for _, request := range []struct{ method, suffix, body, etag string }{
		{http.MethodGet, "/content?path=/demo.txt", "", ""},
		{http.MethodPost, "/upload?path=/demo.txt", "DENIED-UPLOAD-CONTENT", strings.Repeat("0", 64)},
		{http.MethodPut, "/content", `{"path":"/demo.txt","content":"DENIED-EDIT-CONTENT","etag":"` + strings.Repeat("0", 64) + `"}`, ""},
	} {
		response := terminalAuthorizationRequest(t, f, request.method, base+request.suffix, f.project.ID, request.body, request.etag, f.cookie)
		if response.Code != http.StatusForbidden || opened.Load() != 0 || backend.puts != 0 {
			t.Fatalf("权限被拒绝后仍触发 provider: %s status=%d opened=%d puts=%d", request.method, response.Code, opened.Load(), backend.puts)
		}
	}
	terminalAuthorizationRole(t, f, []string{access.PermissionClusterRead, access.PermissionTerminalFileRead, access.PermissionTerminalFileUpload, access.PermissionTerminalFileEdit})
	for _, request := range []struct{ method, suffix, body, etag string }{
		{http.MethodGet, "/content?path=/demo.txt", "", ""},
		{http.MethodPost, "/upload?path=/demo.txt", "UPLOAD-CONTENT-MUST-NOT-ENTER-AUDIT", ""},
		{http.MethodPut, "/content", `{"path":"/demo.txt","content":"EDIT-CONTENT-MUST-NOT-ENTER-AUDIT","etag":"` + strings.Repeat("0", 64) + `"}`, ""},
	} {
		response := terminalAuthorizationRequest(t, f, request.method, base+request.suffix, f.project.ID, request.body, request.etag, f.cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("有权限文件操作失败: %s %d %s", request.method, response.Code, response.Body)
		}
	}
	var logs []model.AuditLog
	if err := f.db.Where("resource_type = ? AND resource_id = ?", "terminal_file", "host:authorization-provider").Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 6 {
		t.Fatalf("文件成功/拒绝审计记录缺失: %d", len(logs))
	}
	serialized, err := json.Marshal(logs)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"READ-CONTENT-MUST-NOT-ENTER-AUDIT", "UPLOAD-CONTENT-MUST-NOT-ENTER-AUDIT", "EDIT-CONTENT-MUST-NOT-ENTER-AUDIT", "DENIED-UPLOAD-CONTENT", "DENIED-EDIT-CONTENT"} {
		if bytes.Contains(serialized, []byte(marker)) {
			t.Fatal("文件内容进入操作审计")
		}
	}
	for _, log := range logs {
		var fields map[string]any
		if err := json.Unmarshal(log.Metadata, &fields); err != nil || len(fields) != 2 || fields["path"] == nil || fields["status"] == nil {
			t.Fatalf("文件审计混入非必要数据: %s %v", log.Metadata, err)
		}
		if log.ProjectID != f.project.ID || log.ActorUserID == nil || *log.ActorUserID != f.user.ID {
			t.Fatal("文件审计未绑定当前用户和项目")
		}
	}
}
