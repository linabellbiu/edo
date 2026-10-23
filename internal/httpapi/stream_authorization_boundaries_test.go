package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	terminalservice "edo/internal/terminal"
)

type streamBoundaryFixture struct {
	router  *gin.Engine
	deps    Dependencies
	db      *gorm.DB
	admin   *model.User
	user    *model.User
	project *model.Project
	cookie  *http.Cookie
}

func newStreamBoundaryFixture(t *testing.T) *streamBoundaryFixture {
	t.Helper()
	f := &streamBoundaryFixture{}
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, db *gorm.DB) { f.deps = *deps; f.db = db })
	t.Cleanup(cleanup)
	f.router = router
	var err error
	f.admin, err = f.deps.Accounts.FindByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	f.user, err = f.deps.Accounts.CreateUser(context.Background(), "stream-boundary", "长连接权限测试", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	f.project, err = f.deps.Access.CreateProject(context.Background(), f.admin, "长连接权限项目", "", f.user.DepartmentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, access.ProjectOwnerRoleID); err != nil {
		t.Fatal(err)
	}
	f.cookie = loginForDepartmentTest(t, router, f.user.Username)
	return f
}

func (f *streamBoundaryFixture) scope() context.Context {
	return database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: f.user.ID, DepartmentID: f.project.DepartmentID, ProjectID: f.project.ID,
	})
}

func (f *streamBoundaryFixture) authorize(permissions ...string) func() bool {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/events/ws", nil).WithContext(f.scope())
	c.Request.AddCookie(f.cookie)
	c.Set(currentUserKey, f.user)
	return streamAuthorization(f.deps, c, permissions...)
}

func TestStreamAuthorizationRechecksIdentityAndProjectBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke func(*testing.T, *streamBoundaryFixture)
	}{
		{"会话删除", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.deps.Sessions.Delete(context.Background(), f.cookie.Value); err != nil {
				t.Fatal(err)
			}
		}},
		{"账号停用", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.deps.Accounts.SetActive(context.Background(), f.user.ID, false); err != nil {
				t.Fatal(err)
			}
		}},
		{"账号删除", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.db.Where(&model.User{ID: f.user.ID}).Delete(&model.User{}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"密码认证版本变化", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.db.Model(&model.User{}).Where(&model.User{ID: f.user.ID}).Update("auth_version", gorm.Expr("auth_version + ?", 1)).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"部门调动", func(t *testing.T, f *streamBoundaryFixture) {
			department := model.Department{ID: "stream-other-department", Name: "长连接调入部门", IsActive: true}
			if err := f.db.Create(&department).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.deps.Accounts.SetDepartment(context.Background(), f.user.ID, department.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"平台身份变化", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.db.Model(&model.User{}).Where(&model.User{ID: f.user.ID}).Update("is_superuser", true).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"项目停用", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.deps.Access.UpdateProject(context.Background(), f.admin, f.project.ID, f.project.Name, "", false); err != nil {
				t.Fatal(err)
			}
		}},
		{"项目删除", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.deps.Access.DeleteProject(context.Background(), f.admin, f.project.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"移除项目成员但保留个人允许", func(t *testing.T, f *streamBoundaryFixture) {
			removeStreamBoundaryMembership(t, f)
		}},
		{"个人显式拒绝", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Deny: []string{"application.read"}}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStreamBoundaryFixture(t)
			authorized := f.authorize("application.read")
			if !authorized() {
				t.Fatal("变更前有效会话不应被拒绝")
			}
			test.revoke(t, f)
			if authorized() {
				t.Fatal("长连接仍沿用变更前的身份或范围")
			}
		})
	}
}

func removeStreamBoundaryMembership(t *testing.T, f *streamBoundaryFixture) {
	t.Helper()
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Allow: []string{"application.read"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetProjectMember(context.Background(), f.admin, f.project.ID, f.user.ID, ""); err != nil {
		t.Fatal(err)
	}
	// 单独的动作允许不能绕过项目成员边界。
	allowed, err := f.deps.Access.HasPermission(f.scope(), f.user, "application.read")
	if err != nil || !allowed {
		t.Fatalf("用例未保留个人动作允许: allowed=%v err=%v", allowed, err)
	}
}

func TestStreamAuthorizationAcceptsAnyRemainingReadPermission(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	authorized := f.authorize("application.read", "pipeline_run.read")
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Deny: []string{"application.read"}}); err != nil {
		t.Fatal(err)
	}
	if !authorized() {
		t.Fatal("仍有流水线读取权限时不应关闭综合通知连接")
	}
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.user.ID, access.UserPermissionOverrides{Deny: []string{"application.read", "pipeline_run.read"}}); err != nil {
		t.Fatal(err)
	}
	if authorized() {
		t.Fatal("全部读取权限撤销后连接授权仍有效")
	}
}

func TestStateWebSocketClosesAfterProjectAccessRevocation(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke func(*testing.T, *streamBoundaryFixture)
	}{
		{"移除成员", removeStreamBoundaryMembership},
		{"停用项目", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.deps.Access.UpdateProject(context.Background(), f.admin, f.project.ID, f.project.Name, "", false); err != nil {
				t.Fatal(err)
			}
		}},
		{"删除会话", func(t *testing.T, f *streamBoundaryFixture) {
			if err := f.deps.Sessions.Delete(context.Background(), f.cookie.Value); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStreamBoundaryFixture(t)
			server := httptest.NewServer(f.router)
			t.Cleanup(server.Close)
			header := http.Header{"Cookie": []string{f.cookie.String()}, "X-EDO-Project-ID": []string{f.project.ID}}
			dialer := websocket.Dialer{Subprotocols: []string{stateSubprotocol}, HandshakeTimeout: 3 * time.Second}
			connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/events/ws", header)
			if err != nil {
				t.Fatalf("建立真实状态连接失败: response=%v err=%v", response, err)
			}
			t.Cleanup(func() { _ = connection.Close() })
			_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
			var event stateEvent
			if err := connection.ReadJSON(&event); err != nil || event.Type != "resync" {
				t.Fatalf("连接未进入已建立状态: event=%+v err=%v", event, err)
			}
			test.revoke(t, f)
			// 事件触发正式路由持续鉴权，测试不依赖 20 秒心跳，也不更改生产定时器。
			f.deps.Realtime.Changed(f.scope(), "applications", "")
			for {
				event = stateEvent{}
				err := connection.ReadJSON(&event)
				if websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
					break
				}
				if err != nil {
					t.Fatalf("撤销后未按策略关闭真实连接: %v", err)
				}
				if event.Type == "changed" {
					t.Fatalf("撤销后仍收到业务变更: %+v", event)
				}
			}
		})
	}
}

func TestPipelineLogsWebSocketRejectsOtherProjectBeforeUpgrade(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	other, err := f.deps.Access.CreateProject(context.Background(), f.admin, "长连接隔离项目", "", f.user.DepartmentID)
	if err != nil {
		t.Fatal(err)
	}
	run := seedRealtimeRun(t, f.db, model.PipelineRunRunning)
	if err := f.db.Model(&run).Update("project_id", other.ID).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.router)
	t.Cleanup(server.Close)
	for _, test := range []struct {
		name      string
		projectID string
		want      int
	}{
		{"本项目访问外部运行", f.project.ID, http.StatusNotFound},
		{"伪造未加入项目", other.ID, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{"Cookie": []string{f.cookie.String()}, "X-EDO-Project-ID": []string{test.projectID}}
			dialer := websocket.Dialer{Subprotocols: []string{pipelineLogSubprotocol}, HandshakeTimeout: 3 * time.Second}
			connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/pipeline-runs/"+run.ID+"/logs/ws", header)
			if connection != nil {
				_ = connection.Close()
			}
			if response != nil && response.Body != nil {
				defer response.Body.Close()
			}
			if err == nil || response == nil || response.StatusCode != test.want {
				t.Fatalf("跨项目日志未在握手前拒绝: response=%v err=%v want=%d", response, err, test.want)
			}
		})
	}
}

func TestPipelineLogsWebSocketStopsDataAfterSessionRevocation(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	run := seedRealtimeRun(t, f.db, model.PipelineRunRunning)
	if err := f.db.Model(&run).Update("project_id", f.project.ID).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.router)
	t.Cleanup(server.Close)
	header := http.Header{"Cookie": []string{f.cookie.String()}, "X-EDO-Project-ID": []string{f.project.ID}}
	dialer := websocket.Dialer{Subprotocols: []string{pipelineLogSubprotocol}, HandshakeTimeout: 3 * time.Second}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/pipeline-runs/"+run.ID+"/logs/ws", header)
	if err != nil {
		t.Fatalf("建立真实日志连接失败: response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var event pipelineLogEvent
	if err := connection.ReadJSON(&event); err != nil || event.Type != "status" {
		t.Fatalf("日志连接未进入已建立状态: event=%+v err=%v", event, err)
	}
	if err := f.deps.Sessions.Delete(context.Background(), f.cookie.Value); err != nil {
		t.Fatal(err)
	}
	line := model.PipelineRunLog{PipelineRunID: run.ID, Stage: "build", Message: "会话撤销后产生的测试日志"}
	if err := f.db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	f.deps.Realtime.LogsChanged(f.scope(), run.ID)
	// 允许现有低频鉴权周期关闭连接，但在此期间不得推送撤销后新生成的日志。
	_ = connection.SetReadDeadline(time.Now().Add(25 * time.Second))
	for {
		event = pipelineLogEvent{}
		err := connection.ReadJSON(&event)
		if websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
			break
		}
		if err != nil {
			t.Fatalf("会话撤销后日志连接未关闭: %v", err)
		}
		if len(event.Logs) > 0 {
			t.Fatalf("会话撤销后仍收到新日志: count=%d first_id=%d", len(event.Logs), event.Logs[0].ID)
		}
	}
}

func TestHTTPProjectHeaderAndQueryKeepOneResourceScope(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	other, err := f.deps.Access.CreateProject(context.Background(), f.admin, "请求范围隔离项目", "", f.user.DepartmentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []*model.Project{f.project, other} {
		repository := model.GitRepository{
			ID: "scope-repository-" + project.ID, DepartmentID: project.DepartmentID, ProjectID: project.ID,
			Name: "scope_repository", Provider: model.GitProviderGeneric, CloneURL: "https://example.com/scope.git",
		}
		if err := f.db.Create(&repository).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name   string
		header string
		query  string
		want   int
	}{
		{"缺少项目", "", "", http.StatusBadRequest},
		{"只使用查询参数", "", f.project.ID, http.StatusOK},
		{"请求头优先且不混入另一项目", f.project.ID, other.ID, http.StatusOK},
		{"不可访问的请求头不得回退到查询参数", other.ID, f.project.ID, http.StatusForbidden},
		{"不存在的请求头不得回退到查询参数", "missing-project", f.project.ID, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := "/api/v1/repositories"
			if test.query != "" {
				path += "?project_id=" + test.query
			}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(f.cookie)
			if test.header != "" {
				request.Header.Set("X-EDO-Project-ID", test.header)
			}
			response := httptest.NewRecorder()
			f.router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("项目范围处理错误: status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			if response.Code == http.StatusOK {
				var payload struct {
					Repositories []model.GitRepository `json:"repositories"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Repositories) != 1 || payload.Repositories[0].ID != "scope-repository-"+f.project.ID {
					t.Fatalf("项目列表范围发生混用: repositories=%+v err=%v", payload.Repositories, err)
				}
			}
		})
	}
}

func TestHTTPProjectOwnerCannotReadPlatformResources(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	for _, path := range []string{
		"/api/v1/users", "/api/v1/roles", "/api/v1/permissions", "/api/v1/departments",
		"/api/v1/organization", "/api/v1/settings/runtime-logging", "/api/v1/identity-providers",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(f.cookie)
			request.Header.Set("X-EDO-Project-ID", f.project.ID)
			response := httptest.NewRecorder()
			f.router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("项目管理员越权读取平台资源: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestTerminalWebSocketRevokesEstablishedSession(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	session := newFakeTerminalSession()
	t.Cleanup(func() { _ = session.Close() })
	handler := terminalHandler{
		service: terminalservice.NewService(nil, nil, time.Minute), audits: f.deps.Audits, logger: f.deps.Logger,
		streamAuth: func(c *gin.Context) func() bool { return streamAuthorization(f.deps, c, "terminal.open") },
	}
	// 只替换终端后端，保留正式会话、项目、动作中间件与 open/bridge 流程。
	f.router.GET("/api/v1/terminals/test-session/ws",
		requireAuth(f.deps.Accounts, f.deps.Sessions, f.deps.Logger, f.deps.AuthConfig.CookieName),
		projectScope(f.deps.Access, f.deps.Logger),
		requirePermission(f.deps.Access, f.deps.Logger, "terminal.open"),
		func(c *gin.Context) {
			handler.open(c, "docker", "test-session", map[string]any{}, func(context.Context, string, terminalservice.Size) (terminalservice.Session, error) {
				return session, nil
			})
		},
	)
	server := httptest.NewServer(f.router)
	t.Cleanup(server.Close)
	header := http.Header{"Cookie": []string{f.cookie.String()}, "X-EDO-Project-ID": []string{f.project.ID}}
	dialer := websocket.Dialer{Subprotocols: []string{terminalSubprotocol}, HandshakeTimeout: 3 * time.Second}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/terminals/test-session/ws", header)
	if err != nil {
		t.Fatalf("建立真实终端连接失败: response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var event terminalEvent
	if err := connection.ReadJSON(&event); err != nil || event.Type != "ready" {
		t.Fatalf("终端未进入已建立状态: event=%+v err=%v", event, err)
	}
	if err := f.deps.Sessions.Delete(context.Background(), f.cookie.Value); err != nil {
		t.Fatal(err)
	}
	// 正式终端每 10 秒持续鉴权；不给后端输出，不改定时器来加速结果。
	_ = connection.SetReadDeadline(time.Now().Add(15 * time.Second))
	event = terminalEvent{}
	if err := connection.ReadJSON(&event); err != nil {
		var closeError *websocket.CloseError
		if !errors.As(err, &closeError) {
			t.Fatalf("撤销后终端未在鉴权周期内关闭: %v", err)
		}
		return
	}
	if event.Type != "exit" {
		t.Fatalf("撤销后终端仍在发送非退出事件: %+v", event)
	}
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("终端退出后仍可接收数据")
	}
}

func TestPipelineLogsWebSocketRechecksSessionAfterReadingBatch(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	run := seedRealtimeRun(t, f.db, model.PipelineRunRunning)
	if err := f.db.Model(&run).Update("project_id", f.project.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.PipelineRunLog{PipelineRunID: run.ID, Stage: "build", Message: "读取后撤销的测试日志"}).Error; err != nil {
		t.Fatal(err)
	}
	var batches atomic.Int32
	revoked := make(chan error, 1)
	const callback = "test:revoke_after_log_batch"
	if err := f.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		// 第一次是升级前归属检查；第二次是已经读取、尚未发送的正文批次。
		if tx.Statement.Table == "pipeline_run_logs" && batches.Add(1) == 2 {
			err := f.deps.Sessions.Delete(context.Background(), f.cookie.Value)
			revoked <- err
			if err != nil {
				tx.AddError(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	server := httptest.NewServer(f.router)
	t.Cleanup(server.Close)
	header := http.Header{"Cookie": []string{f.cookie.String()}, "X-EDO-Project-ID": []string{f.project.ID}}
	dialer := websocket.Dialer{Subprotocols: []string{pipelineLogSubprotocol}, HandshakeTimeout: 3 * time.Second}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/pipeline-runs/"+run.ID+"/logs/ws", header)
	if err != nil {
		t.Fatalf("建立真实日志连接失败: response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var event pipelineLogEvent
	if err := connection.ReadJSON(&event); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("批次读取后撤销会话仍发送数据: type=%s logs=%d err=%v", event.Type, len(event.Logs), err)
	}
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("用例没有在批次读取后完成会话撤销")
	}
}

func TestPipelineLogsWebSocketAuthorizesBeforeNextHistoryBatch(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	run := seedRealtimeRun(t, f.db, model.PipelineRunRunning)
	if err := f.db.Model(&run).Update("project_id", f.project.ID).Error; err != nil {
		t.Fatal(err)
	}
	lines := make([]model.PipelineRunLog, 201)
	for index := range lines {
		lines[index] = model.PipelineRunLog{PipelineRunID: run.ID, Stage: "build", Message: "分批读取的测试日志"}
	}
	if err := f.db.Create(&lines).Error; err != nil {
		t.Fatal(err)
	}
	var checks, batches atomic.Int32
	const callback = "test:count_log_batches"
	if err := f.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "pipeline_run_logs" {
			batches.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	handler := pipelineHandler{service: f.deps.Pipelines, logger: f.deps.Logger, streamAuth: func(c *gin.Context) func() bool {
		authorized := streamAuthorization(f.deps, c, "pipeline_run.read")
		return func() bool {
			// 第一批读前、发送前允许；下一批读前撤销，不等待定时器或客户端时序。
			return checks.Add(1) <= 2 && authorized()
		}
	}}
	f.router.GET("/api/v1/pipeline-runs/:id/inventory-logs/ws",
		requireAuth(f.deps.Accounts, f.deps.Sessions, f.deps.Logger, f.deps.AuthConfig.CookieName),
		projectScope(f.deps.Access, f.deps.Logger),
		requirePermission(f.deps.Access, f.deps.Logger, "pipeline_run.read"), handler.streamRunLogs)
	server := httptest.NewServer(f.router)
	t.Cleanup(server.Close)
	header := http.Header{"Cookie": []string{f.cookie.String()}, "X-EDO-Project-ID": []string{f.project.ID}}
	dialer := websocket.Dialer{Subprotocols: []string{pipelineLogSubprotocol}, HandshakeTimeout: 3 * time.Second}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/pipeline-runs/"+run.ID+"/inventory-logs/ws", header)
	if err != nil {
		t.Fatalf("建立分批日志连接失败: response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var event pipelineLogEvent
	if err := connection.ReadJSON(&event); err != nil || event.Type != "logs" || len(event.Logs) != 200 {
		t.Fatalf("授权期间第一批日志不完整: type=%s logs=%d err=%v", event.Type, len(event.Logs), err)
	}
	event = pipelineLogEvent{}
	if err := connection.ReadJSON(&event); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("下一批读取前撤权未关闭: type=%s logs=%d err=%v", event.Type, len(event.Logs), err)
	}
	if batches.Load() != 2 {
		t.Fatalf("撤销后仍查询下一批日志: batches=%d want=2（含升级前检查）", batches.Load())
	}
}
