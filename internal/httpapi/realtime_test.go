package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

func realtimeLogin(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	response := performJSONRequest(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "correct horse battery staple"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("登录失败: %s", response.Body.String())
	}
	return response.Result().Cookies()[0]
}

func dialRealtime(t *testing.T, serverURL, path, protocol string, cookie *http.Cookie) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	if cookie != nil {
		header.Set("Cookie", cookie.String())
	}
	dialer := websocket.Dialer{Subprotocols: []string{protocol}}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(serverURL, "http")+path, header)
	if err != nil {
		t.Fatalf("连接失败: response=%v err=%v", response, err)
	}
	t.Cleanup(func() { connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(4 * time.Second))
	return connection
}

func TestStateWebSocketUsesAuthAndIgnoresFailedMutation(t *testing.T) {
	router, cleanup := newAuthTestRouter(t)
	defer cleanup()
	server := httptest.NewServer(router)
	defer server.Close()
	dialer := websocket.Dialer{Subprotocols: []string{stateSubprotocol}}
	_, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/events/ws", nil)
	if err == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录连接未被拒绝: %v %v", response, err)
	}
	cookie := realtimeLogin(t, router)
	connection := dialRealtime(t, server.URL, "/api/v1/events/ws", stateSubprotocol, cookie)
	var event stateEvent
	if err := connection.ReadJSON(&event); err != nil || event.Type != "resync" {
		t.Fatalf("首次连接没有补同步: %+v %v", event, err)
	}
	result := performJSONRequest(t, router, http.MethodPost, "/api/v1/release-plans", map[string]any{"description": "事件测试"}, cookie)
	if result.Code < 400 {
		t.Fatalf("无应用计划应校验失败: %d", result.Code)
	}
	// 两次失败的修改都不得产生成功变更通知。
	result = performJSONRequest(t, router, http.MethodPatch, "/api/v1/deployment-plans/missing/status", map[string]any{"is_active": false}, cookie)
	if result.Code < 400 {
		t.Fatalf("缺失方案不应成功: %d", result.Code)
	}
	if err := connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _, err = connection.ReadMessage()
	if !websocket.IsCloseError(err, 1000) {
		t.Fatalf("未完成正常关闭握手或失败操作产生了通知: %v", err)
	}
}

func TestStateWebSocketPushAndLogout(t *testing.T) {
	var deps Dependencies
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, database *gorm.DB) { deps = *value; db = database })
	defer cleanup()
	server := httptest.NewServer(router)
	defer server.Close()
	cookie := realtimeLogin(t, router)
	connection := dialRealtime(t, server.URL, "/api/v1/events/ws", stateSubprotocol, cookie)
	var event stateEvent
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID})
	if err := db.Create(&model.GitRepository{ID: "state-repository", Name: "state_repository", Provider: model.GitProviderGeneric, CloneURL: "https://example.com/repo.git", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/applications", map[string]any{"name": "state_application", "repository_id": "state-repository", "poll_interval_seconds": 60}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("创建应用失败: %s", created.Body.String())
	}
	if err := connection.ReadJSON(&event); err != nil || event.Type != "changed" || len(event.Resources) != 1 || event.Resources[0] != "applications" {
		t.Fatalf("未收到变化通知: %+v %v", event, err)
	}
	performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/logout", nil, cookie)
	deps.Realtime.Changed(ctx, "pipeline_runs", "")
	_, _, err := connection.ReadMessage()
	if !websocket.IsCloseError(err, 1008) {
		t.Fatalf("注销后的连接未停止: %v", err)
	}
}

func TestStateAndLogsWebSocketEnforceDepartmentAndPermission(t *testing.T) {
	var deps Dependencies
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, database *gorm.DB) { deps = *value; db = database })
	defer cleanup()
	if err := db.Create(&model.Department{ID: "other-department", Name: "另一部门", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	user, err := deps.Accounts.CreateUser(context.Background(), "ws-reader", "读者", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "ws-reader", "password": "correct horse battery staple"}, nil)
	cookie := login.Result().Cookies()[0]
	server := httptest.NewServer(router)
	defer server.Close()
	header := http.Header{"Cookie": []string{cookie.String()}}
	dialer := websocket.Dialer{Subprotocols: []string{stateSubprotocol}}
	_, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/events/ws", header)
	if err == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("无权限订阅未被拒绝: %v %v", response, err)
	}
	if err := deps.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Allow: []string{access.PermissionDeliveryRead}}); err != nil {
		t.Fatal(err)
	}
	connection := dialRealtime(t, server.URL, "/api/v1/events/ws", stateSubprotocol, cookie)
	var event stateEvent
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	other := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: "other-department"})
	current := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID})
	deps.Realtime.Changed(other, "pipeline_runs", "")
	deps.Realtime.Changed(current, "applications", "")
	if err := connection.ReadJSON(&event); err != nil || len(event.Resources) != 1 || event.Resources[0] != "applications" {
		t.Fatalf("部门通知发生泄露: %+v %v", event, err)
	}
	run := seedRealtimeRun(t, db, model.PipelineRunRunning)
	if err := db.Model(&run).Update("department_id", "other-department").Error; err != nil {
		t.Fatal(err)
	}
	dialer.Subprotocols = []string{pipelineLogSubprotocol}
	_, response, err = dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/pipeline-runs/"+run.ID+"/logs/ws", header)
	if err == nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("跨部门日志未在握手前拒绝: %v %v", response, err)
	}
	if err := deps.Access.SetUserPermissions(context.Background(), user.ID, access.UserPermissionOverrides{Deny: []string{access.PermissionDeliveryRead}}); err != nil {
		t.Fatal(err)
	}
	deps.Realtime.Changed(current, "applications", "")
	_, _, err = connection.ReadMessage()
	if !websocket.IsCloseError(err, 1008) {
		t.Fatalf("撤销权限后仍可订阅: %v", err)
	}
}

func seedRealtimeRun(t *testing.T, db *gorm.DB, status model.PipelineRunStatus) model.PipelineRun {
	t.Helper()
	for _, value := range []any{
		&model.GitRepository{ID: "ws-repository", Name: "ws_repository", Provider: model.GitProviderGeneric, CloneURL: "https://example.com/repo.git"},
		&model.Application{ID: "ws-application", Name: "ws_application", RepositoryID: "ws-repository"},
	} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	run := model.PipelineRun{ID: "ws-run", ApplicationID: "ws-application", DepartmentID: database.DefaultDepartmentID, Status: status, Stage: "build"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	return run
}

func TestPipelineLogsDrainHistoryAndCloseNormally(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	run := seedRealtimeRun(t, db, model.PipelineRunSucceeded)
	for range 505 {
		if err := db.Create(&model.PipelineRunLog{PipelineRunID: run.ID, Stage: "build", Message: "一条日志"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(router)
	defer server.Close()
	connection := dialRealtime(t, server.URL, "/api/v1/pipeline-runs/"+run.ID+"/logs/ws", pipelineLogSubprotocol, realtimeLogin(t, router))
	count := 0
	for {
		var event pipelineLogEvent
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		count += len(event.Logs)
		if event.Type == "complete" {
			break
		}
	}
	if count != 505 {
		t.Fatalf("历史日志未全部补齐: %d", count)
	}
	_, _, err := connection.ReadMessage()
	if !websocket.IsCloseError(err, 1000) {
		t.Fatalf("执行完成后没有正常关闭: %v", err)
	}
}

func TestPipelineLogsWaitingStateRemainsOpenAndReceivesWakeup(t *testing.T) {
	var db *gorm.DB
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, database *gorm.DB) { db = database; deps = *value })
	defer cleanup()
	run := seedRealtimeRun(t, db, model.PipelineRunAwaitingApproval)
	server := httptest.NewServer(router)
	defer server.Close()
	connection := dialRealtime(t, server.URL, "/api/v1/pipeline-runs/"+run.ID+"/logs/ws", pipelineLogSubprotocol, realtimeLogin(t, router))
	var event pipelineLogEvent
	if err := connection.ReadJSON(&event); err != nil || event.Type != "status" || event.Status != model.PipelineRunAwaitingApproval {
		t.Fatalf("等待审核不应结束日志: %+v %v", event, err)
	}
	line := model.PipelineRunLog{PipelineRunID: run.ID, Stage: "build", Message: "批准后开始构建"}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	deps.Realtime.LogsChanged(context.Background(), run.ID)
	if err := connection.ReadJSON(&event); err != nil || len(event.Logs) != 1 || event.Logs[0].ID != line.ID {
		t.Fatalf("未收到实时日志: %+v %v", event, err)
	}
	if err := db.Model(&run).Update("status", model.PipelineRunSucceeded).Error; err != nil {
		t.Fatal(err)
	}
	deps.Realtime.Changed(context.Background(), "pipeline_runs", run.ID)
	for event.Type != "complete" {
		if err := connection.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
	}
}
