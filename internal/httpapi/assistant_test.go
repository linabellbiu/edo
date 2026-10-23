package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/assistant"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type assistantHTTPFixture struct {
	router  *gin.Engine
	db      *gorm.DB
	deps    Dependencies
	secrets *secret.Manager
	admin   *http.Cookie
}

func newAssistantHTTPFixture(t *testing.T) *assistantHTTPFixture {
	t.Helper()
	fixture := &assistantHTTPFixture{}
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32))))
	if err != nil {
		t.Fatal(err)
	}
	fixture.secrets = manager
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, db *gorm.DB) {
		if err := db.AutoMigrate(&model.AssistantSettings{}, &model.AssistantConversation{}, &model.AssistantMessage{}, &model.AssistantAction{}); err != nil {
			t.Fatal(err)
		}
		deps.Assistant = assistant.NewService(assistant.Dependencies{Database: db, Secrets: manager, Access: deps.Access, Pipeline: deps.Pipelines, Kubernetes: deps.Kubernetes, Audits: deps.Audits, Logger: deps.Logger})
		fixture.db, fixture.deps = db, *deps
	})
	t.Cleanup(cleanup)
	fixture.router = router
	fixture.admin = loginForDepartmentTest(t, router, "admin")
	return fixture
}

func (f *assistantHTTPFixture) session(t *testing.T, superuser bool) *http.Cookie {
	t.Helper()
	now := time.Now().UTC()
	user := model.User{ID: uuid.NewString(), Username: "assistant-" + uuid.NewString()[:8], Nickname: "助手测试用户", DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: superuser, AuthVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := f.db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if !superuser {
		role := model.Role{ID: uuid.NewString(), Name: "assistant-empty-" + uuid.NewString()[:8], DisplayName: "无助手权限", CreatedAt: now, UpdatedAt: now}
		if err := f.db.Create(&role).Error; err != nil {
			t.Fatal(err)
		}
		member := model.ProjectMember{ProjectID: database.DefaultProjectID(user.DepartmentID), UserID: user.ID, RoleID: role.ID, CreatedAt: now}
		if err := f.db.Create(&member).Error; err != nil {
			t.Fatal(err)
		}
	}
	token, _, err := f.deps.Sessions.Create(context.Background(), user.ID, user.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "edo_session", Value: token}
}

func (f *assistantHTTPFixture) save(t *testing.T, input assistant.SettingsInput) assistant.SettingsView {
	t.Helper()
	response := performJSONRequest(t, f.router, http.MethodPut, "/api/v1/settings/assistant", input, f.admin)
	if response.Code != http.StatusOK {
		t.Fatalf("保存助手设置失败: status=%d body=%s", response.Code, response.Body.String())
	}
	var view assistant.SettingsView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func (f *assistantHTTPFixture) conversation(t *testing.T) string {
	t.Helper()
	response := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/assistant/conversations", map[string]string{"title": "接口测试对话"}, f.admin)
	if response.Code != http.StatusCreated {
		t.Fatalf("新建助手会话失败: status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Conversation assistant.ConversationView `json:"conversation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Conversation.ID == "" {
		t.Fatalf("会话响应无效: %v %s", err, response.Body.String())
	}
	return result.Conversation.ID
}

func TestAssistantHTTPPermissionsDraftSettingsAndIsolation(t *testing.T) {
	var modelCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		modelCalls.Add(1)
		http.Error(w, "不应调用模型", http.StatusInternalServerError)
	}))
	defer provider.Close()
	f := newAssistantHTTPFixture(t)
	for _, cookie := range []*http.Cookie{nil, f.session(t, false)} {
		want := http.StatusUnauthorized
		if cookie != nil {
			want = http.StatusForbidden
		}
		response := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/assistant/conversations", nil, cookie)
		if response.Code != want {
			t.Fatalf("助手读取权限未生效: got=%d want=%d body=%s", response.Code, want, response.Body.String())
		}
	}
	input := assistant.SettingsInput{Enabled: true, BaseURL: provider.URL + "/v1", Model: "http-fixture-model", MaxSteps: 3, TimeoutSeconds: 10}
	view := f.save(t, input)
	if view.HasAPIKey || !view.Enabled || view.Model != input.Model {
		t.Fatalf("无密钥配置未正确保存: %+v", view)
	}
	id := f.conversation(t)
	for _, request := range []struct {
		path string
		body any
	}{
		{"/api/v1/assistant/conversations/" + id + "/messages", map[string]string{"content": "检查项目状态"}},
		{"/api/v1/settings/assistant/test", input},
	} {
		response := performJSONRequest(t, f.router, http.MethodPost, request.path, request.body, f.admin)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "尚未配置") || strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("无密钥请求未在调用模型前明确失败: status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if modelCalls.Load() != 0 {
		t.Fatal("未配置密钥时调用了模型服务")
	}
	input.APIKey, input.ExpectedVersion = "fixture-private-api-key", view.Version
	view = f.save(t, input)
	var encrypted model.AssistantSettings
	if err := f.db.First(&encrypted, "uuid = ?", "global").Error; err != nil {
		t.Fatal(err)
	}
	plain, err := f.secrets.Decrypt(encrypted.APIKeyCiphertext, []byte("assistant:global:api_key"))
	if err != nil || plain != input.APIKey || strings.Contains(encrypted.APIKeyCiphertext, input.APIKey) {
		t.Fatal("HTTP 保存的密钥未加密")
	}
	read := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/settings/assistant", nil, f.admin)
	if read.Code != http.StatusOK || !bytes.Contains(read.Body.Bytes(), []byte(`"has_api_key":true`)) || bytes.Contains(read.Body.Bytes(), []byte(input.APIKey)) || bytes.Contains(read.Body.Bytes(), []byte(`"api_key":`)) || bytes.Contains(read.Body.Bytes(), []byte(encrypted.APIKeyCiphertext)) || read.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("设置读取泄露密钥或缺少状态: status=%d body=%s", read.Code, read.Body.String())
	}
	input.APIKey, input.ExpectedVersion, input.Model = "", view.Version, "http-updated-model"
	view = f.save(t, input)
	var kept model.AssistantSettings
	if err := f.db.First(&kept, "uuid = ?", "global").Error; err != nil || kept.APIKeyCiphertext != encrypted.APIKeyCiphertext || !view.HasAPIKey {
		t.Fatalf("空密钥输入未保留原密钥: %v", err)
	}
	conflict := performJSONRequest(t, f.router, http.MethodPut, "/api/v1/settings/assistant", input, f.admin)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "配置已被修改") {
		t.Fatalf("陈旧配置未拒绝: status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	input.ExpectedVersion, input.ClearAPIKey = view.Version, true
	view = f.save(t, input)
	if view.HasAPIKey {
		t.Fatal("明确清除后仍报告存在密钥")
	}
	var cleared model.AssistantSettings
	if err := f.db.First(&cleared, "uuid = ?", "global").Error; err != nil || cleared.APIKeyCiphertext != "" {
		t.Fatalf("明确清除未删除密文: %v", err)
	}
	otherCookie := f.session(t, true)
	otherProject := model.Project{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, Name: "助手隔离项目", IsActive: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := f.db.Create(&otherProject).Error; err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		cookie *http.Cookie
		query  string
	}{
		{otherCookie, ""},
		{f.admin, "?project_id=" + otherProject.ID},
	} {
		response := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/assistant/conversations/"+id+request.query, nil, request.cookie)
		if response.Code != http.StatusNotFound {
			t.Fatalf("跨用户或项目读取成功: status=%d body=%s", response.Code, response.Body.String())
		}
		list := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/assistant/conversations"+request.query, nil, request.cookie)
		if list.Code != http.StatusOK || bytes.Contains(list.Body.Bytes(), []byte(id)) {
			t.Fatalf("跨用户或项目列表泄露会话: status=%d body=%s", list.Code, list.Body.String())
		}
	}
}

func assistantHTTPEvents(t *testing.T, body string) []assistant.Event {
	t.Helper()
	events := []assistant.Event{}
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event assistant.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
			t.Fatalf("SSE 事件无法解析: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func TestAssistantHTTPRealEinoSSEAndSessionRevocation(t *testing.T) {
	var calls atomic.Int32
	var block atomic.Bool
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-http-secret" {
			t.Errorf("Eino 模型请求地址、方法或认证错误: method=%s path=%s", r.Method, r.URL.Path)
		}
		var input struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "http-fixture-model" || !input.Stream {
			t.Errorf("未发出真实 Eino 流式请求: model=%s stream=%v err=%v", input.Model, input.Stream, err)
		}
		if block.Load() {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-http\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"http-fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"HTTP模拟回复\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-http\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"http-fixture-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer func() { unblock(); provider.Close() }()
	f := newAssistantHTTPFixture(t)
	f.save(t, assistant.SettingsInput{Enabled: true, BaseURL: provider.URL + "/v1", Model: "http-fixture-model", APIKey: "fixture-http-secret", MaxSteps: 3, TimeoutSeconds: 10})
	server := httptest.NewServer(f.router)
	defer func() { unblock(); server.Close() }()
	client := server.Client()
	client.Timeout = 5 * time.Second
	request := func(id string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/assistant/conversations/"+id+"/messages", strings.NewReader(`{"content":"查看项目状态"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(f.admin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") || response.Header.Get("Cache-Control") != "no-store" {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("未返回真实 SSE: status=%d body=%s", response.StatusCode, body)
		}
		return response
	}
	id := f.conversation(t)
	response := request(id)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	events := assistantHTTPEvents(t, string(body))
	if len(events) < 3 || events[0].Type != "start" || events[len(events)-1].Type != "done" || !bytes.Contains(body, []byte("HTTP模拟回复")) {
		t.Fatalf("Eino SSE 缺少 start/delta/done: %s", body)
	}
	var delta bool
	for _, event := range events {
		delta = delta || event.Type == "delta"
		if event.Type == "error" {
			t.Fatalf("Eino SSE 出错: %s", body)
		}
	}
	if !delta || calls.Load() != 1 {
		t.Fatalf("未经过一次真实模型调用: calls=%d delta=%v", calls.Load(), delta)
	}
	detail := performJSONRequest(t, f.router, http.MethodGet, "/api/v1/assistant/conversations/"+id, nil, f.admin)
	if detail.Code != http.StatusOK || !bytes.Contains(detail.Body.Bytes(), []byte("HTTP模拟回复")) {
		t.Fatalf("流式回复未持久化: status=%d body=%s", detail.Code, detail.Body.String())
	}
	blockedID := f.conversation(t)
	block.Store(true)
	response = request(blockedID)
	defer response.Body.Close()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		unblock()
		t.Fatal("阻塞模型请求未开始")
	}
	if err := f.deps.Sessions.Delete(context.Background(), f.admin.Value); err != nil {
		unblock()
		t.Fatal(err)
	}
	unblock()
	body, err = io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("撤销会话后 SSE 未及时结束: %v", err)
	}
	for _, event := range assistantHTTPEvents(t, string(body)) {
		if event.Type == "delta" || event.Type == "done" || strings.Contains(event.Text, "HTTP模拟回复") {
			t.Fatalf("撤销登录后仍输出模型内容: %s", body)
		}
	}
	var audit model.AuditLog
	if err := f.db.Where("action = ? AND resource_id = ?", "assistant.chat", blockedID).First(&audit).Error; err != nil {
		t.Fatalf("撤销会话后未记录对话审计: %v", err)
	}
	var metadata struct {
		Canceled         bool `json:"canceled"`
		PermissionDenied bool `json:"permission_denied"`
	}
	if err := json.Unmarshal(audit.Metadata, &metadata); err != nil || (!metadata.Canceled && !metadata.PermissionDenied) || audit.Result != model.AuditFailed {
		t.Fatalf("会话撤销未取消流式处理: result=%s metadata=%s err=%v", audit.Result, audit.Metadata, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var row model.AssistantConversation
		if err := f.db.First(&row, "uuid = ?", blockedID).Error; err != nil {
			t.Fatal(err)
		}
		if row.LeaseUntil == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("会话撤销后模型任务未释放会话锁")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Fatalf("会话撤销触发了额外模型调用: %d", calls.Load())
	}
}
