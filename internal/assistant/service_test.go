package assistant

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/audit"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/pipeline"
	"edo/internal/secret"
	"edo/internal/testutil"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
)

type fakeModel struct {
	respond func(context.Context, []*schema.Message) (*schema.Message, error)
}

func (m *fakeModel) Generate(ctx context.Context, messages []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	return m.respond(ctx, messages)
}
func (m *fakeModel) Stream(ctx context.Context, messages []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.respond(ctx, messages)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func assistantFixture(t *testing.T) (*Service, *gorm.DB, *model.User, context.Context) {
	t.Helper()
	db := testutil.NewDatabase(t)
	if err := db.AutoMigrate(&model.AssistantSettings{}, &model.AssistantConversation{}, &model.AssistantMessage{}, &model.AssistantAction{}); err != nil {
		t.Fatal(err)
	}
	authorizer, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))))
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{ID: newID(), Username: "assistant-admin", Nickname: "测试管理员", DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: true, AuthVersion: 1}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: database.DefaultProjectID(user.DepartmentID), AllDepartments: true})
	s := NewService(Dependencies{Database: db, Secrets: secrets, Access: authorizer, Audits: audit.NewService(db), Pipeline: pipeline.NewService(db, nil, secrets), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return s, db, user, ctx
}

func saveTestSettings(t *testing.T, s *Service, user *model.User, ctx context.Context, key string) SettingsView {
	t.Helper()
	view, err := s.SaveSettings(ctx, user.ID, SettingsInput{Enabled: true, BaseURL: "https://example.invalid/v1", Model: "test-model", APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestAssistantDefaultsToDisabled(t *testing.T) {
	s, db, _, ctx := assistantFixture(t)
	view, err := s.Settings(ctx)
	if err != nil || view.Enabled || view.Version != 0 {
		t.Fatalf("初始助手设置应为关闭: %+v %v", view, err)
	}
	status, err := s.Status(ctx)
	if err != nil || status.Enabled || status.Reason != ErrDisabled.Error() {
		t.Fatalf("初始助手状态应为关闭: %+v %v", status, err)
	}
	var count int64
	if err := db.Model(&model.AssistantSettings{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("读取默认设置不应写入数据库: %d %v", count, err)
	}
}

func TestMissingKeyNeverCallsProviderAndDraftCanBeSaved(t *testing.T) {
	s, _, user, ctx := assistantFixture(t)
	var calls atomic.Int32
	s.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
		calls.Add(1)
		return nil, errors.New("不应调用")
	}
	view, err := s.SaveSettings(ctx, user.ID, SettingsInput{BaseURL: "http://localhost:11434/v1", Model: "draft-model"})
	if err != nil || view.Enabled || view.HasAPIKey {
		t.Fatalf("草稿失败: %+v %v", view, err)
	}
	_, err = s.SaveSettings(ctx, user.ID, SettingsInput{Enabled: true, BaseURL: view.BaseURL, Model: view.Model, ExpectedVersion: view.Version})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Chat(ctx, user, conversation.ID, "检查部署", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("无Key未拒绝: %v", err)
	}
	if _, err := s.TestSettings(ctx, SettingsInput{Model: "draft-model"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("测试无Key未拒绝: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("无Key调用了模型")
	}
}

func TestSettingsKeepEncryptedKeyAndDetectConflict(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	view := saveTestSettings(t, s, user, ctx, "private-test-key")
	view, err := s.SaveSettings(ctx, user.ID, SettingsInput{Enabled: true, Model: "next-model", ExpectedVersion: view.Version})
	if err != nil || !view.HasAPIKey {
		t.Fatalf("空密钥未保留: %+v %v", view, err)
	}
	row, _ := s.settings(ctx)
	key, err := s.deps.Secrets.Decrypt(row.APIKeyCiphertext, []byte("assistant:global:api_key"))
	if err != nil || key != "private-test-key" || strings.Contains(row.APIKeyCiphertext, key) {
		t.Fatal("密钥未正确加密保存")
	}
	if _, err := s.SaveSettings(ctx, user.ID, SettingsInput{Model: "stale", ExpectedVersion: view.Version - 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧配置覆盖成功: %v", err)
	}
	var count int64
	db.Model(&model.AuditLog{}).Where("action = ?", "assistant.settings.update").Count(&count)
	if count != 2 {
		t.Fatalf("失败配置写了成功审计: %d", count)
	}
	view, err = s.SaveSettings(ctx, user.ID, SettingsInput{Model: view.Model, ClearAPIKey: true, ExpectedVersion: view.Version})
	if err != nil || view.HasAPIKey {
		t.Fatalf("清空密钥失败: %+v %v", view, err)
	}
}

func TestEinoChatIsBoundedRedactedAndOwned(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	saveTestSettings(t, s, user, ctx, "private-key-cross-boundary")
	s.newModel = func(_ context.Context, _ model.AssistantSettings, key string) (einomodel.BaseChatModel, error) {
		return &fakeModel{respond: func(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
			for _, message := range messages {
				if strings.Contains(message.Content, key) {
					t.Error("模型提示词出现API Key")
				}
			}
			return schema.AssistantMessage("当前状态正常；password=hunter2；"+key, nil), nil
		}}, nil
	}
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{}
	if err := s.Chat(ctx, user, conversation.ID, "查询 token=private-user-token", func(event Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Conversation(ctx, user, conversation.ID)
	if err != nil || len(detail.Messages) != 2 {
		t.Fatalf("消息未保存: %+v %v", detail, err)
	}
	for _, message := range detail.Messages {
		if strings.Contains(message.Content, "hunter2") || strings.Contains(message.Content, "private-key-cross-boundary") || strings.Contains(message.Content, "private-user-token") {
			t.Fatalf("消息泄露敏感信息: %q", message.Content)
		}
	}
	if len(events) == 0 || events[len(events)-1].Type != "done" {
		t.Fatalf("无完成事件: %+v", events)
	}
	other := *user
	other.RowID = 0
	other.ID = newID()
	other.Username = "assistant-other"
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherCtx := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: other.ID, DepartmentID: other.DepartmentID, ProjectID: database.DefaultProjectID(other.DepartmentID), AllDepartments: true})
	if _, err := s.Conversation(otherCtx, &other, conversation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨用户读取成功: %v", err)
	}
	project := model.Project{ID: newID(), DepartmentID: user.DepartmentID, Name: "其他项目", IsActive: true}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	crossCtx := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: project.ID, AllDepartments: true})
	if _, err := s.Conversation(crossCtx, user, conversation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨项目读取成功: %v", err)
	}
}

func TestConversationLeaseAndCancellation(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	saveTestSettings(t, s, user, ctx, "test-key")
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	lease := time.Now().Add(time.Minute)
	db.Model(&model.AssistantConversation{}).Where("uuid = ?", conversation.ID).Update("lease_until", lease)
	if err := s.Chat(ctx, user, conversation.ID, "测试", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("并发会话未拒绝: %v", err)
	}
	db.Model(&model.AssistantConversation{}).Where("uuid = ?", conversation.ID).Update("lease_until", nil)
	started := make(chan struct{})
	s.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
		return &fakeModel{respond: func(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}}, nil
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Chat(cancelCtx, user, conversation.ID, "测试取消", nil) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCanceled) {
			t.Fatalf("取消错误不安全: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消未传到模型")
	}
	var row model.AssistantConversation
	db.First(&row, "uuid = ?", conversation.ID)
	if row.LeaseUntil != nil {
		t.Fatal("取消未释放会话锁")
	}
}

func TestActionConfirmationCASRejectsReplayAndStaleState(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	saveTestSettings(t, s, user, ctx, "test-key")
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: newID(), Name: "assistant_app", RepositoryID: newID(), ProjectID: database.DefaultProjectID(user.DepartmentID), DepartmentID: user.DepartmentID, CreatedBy: user.ID, IsActive: true}
	if err := db.WithContext(ctx).Create(&model.GitRepository{ID: app.RepositoryID, Name: "assistant_repo", CloneURL: "https://example.invalid/repo.git", ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, CreatedBy: user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Omit("Repository", "Repositories", "Workflows").Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{ID: newID(), ApplicationID: app.ID, ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, Status: model.PipelineRunDetected, Stage: "detected", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), CreatedBy: user.ID}
	if err := db.WithContext(ctx).Omit("Application", "Repositories").Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	action, err := s.propose(ctx, user, conversation.ID, "pipeline_stop", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			view, err := s.ConfirmAction(ctx, user, action.ID)
			if err == nil && view.Status == "succeeded" {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("执行次数错误: %d", successes.Load())
	}
	if _, err := s.ConfirmAction(ctx, user, action.ID); !errors.Is(err, ErrActionState) {
		t.Fatalf("重复确认未拒绝: %v", err)
	}
	db.WithContext(ctx).Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Updates(map[string]any{"status": model.PipelineRunDetected, "stage": "detected"})
	action, err = s.propose(ctx, user, conversation.ID, "pipeline_stop", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	db.WithContext(ctx).Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("stage", "changed")
	if _, err := s.ConfirmAction(ctx, user, action.ID); !errors.Is(err, ErrActionState) {
		t.Fatalf("旧快照仍可执行: %v", err)
	}
	db.Model(&model.AssistantAction{}).Where("uuid = ?", action.ID).Update("expires_at", time.Now().Add(-time.Second))
	if _, err := s.ConfirmAction(ctx, user, action.ID); !errors.Is(err, ErrActionExpired) {
		t.Fatalf("过期确认仍可执行: %v", err)
	}
}

func TestRedactionAndResourceAllowlist(t *testing.T) {
	input := "Authorization: Bearer sample-token password=hunter2 api_key='private-value' https://user:pass@example.com -----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----"
	output := redact(input)
	for _, secret := range []string{"sample-token", "hunter2", "private-value", "user:pass", "abc"} {
		if strings.Contains(output, secret) {
			t.Fatalf("未脱敏%s: %s", secret, output)
		}
	}
	for _, example := range []string{`password="alpha beta"`, `secret: 'a,b'`, `{"api_key":"alpha beta, gamma"}`, `password="escaped\"quoted secret"`} {
		text := redact(example)
		for _, forbidden := range []string{"alpha", "beta", "gamma", "a,b", "quoted secret"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("带引号秘密未完整脱敏: %q -> %q", example, text)
			}
		}
	}
	resource := resourceSummary(map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "pod", "annotations": map[string]any{"token": "hidden"}}, "spec": map[string]any{"env": "SECRET"}, "data": map[string]any{"password": "hidden"}})
	if _, ok := resource["spec"]; ok {
		t.Fatal("资源详情暴露spec")
	}
	if _, ok := resource["data"]; ok {
		t.Fatal("资源详情暴露Secret")
	}
}

func ordinaryAssistantUser(t *testing.T, s *Service, db *gorm.DB, admin *model.User, adminCtx context.Context) (*model.User, context.Context) {
	t.Helper()
	user := &model.User{ID: newID(), Username: "assistant-reader", Nickname: "测试成员", DepartmentID: admin.DepartmentID, IsActive: true, AuthVersion: 1}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ProjectMember{ProjectID: database.DefaultProjectID(user.DepartmentID), UserID: user.ID, RoleID: "none"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.deps.Access.SetUserPermissions(adminCtx, user.ID, access.UserPermissionOverrides{Allow: []string{"assistant.read", "assistant.execute", "pipeline_run.read"}}); err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: database.DefaultProjectID(user.DepartmentID)})
	return user, ctx
}

func TestToolPermissionsAndInjectedProjectFailClosed(t *testing.T) {
	s, db, admin, adminCtx := assistantFixture(t)
	user, ctx := ordinaryAssistantUser(t, s, db, admin, adminCtx)
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	findTool := func(name string) tool.InvokableTool {
		for _, candidate := range s.tools(user, conversation.ID, func(Event) {}) {
			info, _ := candidate.Info(ctx)
			if info.Name == name {
				return candidate.(tool.InvokableTool)
			}
		}
		t.Fatalf("缺少工具%s", name)
		return nil
	}
	if _, err := findTool("list_applications").InvokableRun(ctx, `{}`); !errors.Is(err, ErrForbidden) {
		t.Fatalf("缺少业务查看权限仍可读应用: %v", err)
	}
	if _, err := findTool("list_pipeline_runs").InvokableRun(ctx, `{"project_id":"other"}`); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("模型可注入项目: %v", err)
	}
	if _, err := findTool("prepare_pipeline_stop").InvokableRun(ctx, `{"run_id":"anything"}`); !errors.Is(err, ErrForbidden) {
		t.Fatalf("只读用户可准备写操作: %v", err)
	}
	if _, err := s.ListConversations(context.Background(), user); !errors.Is(err, ErrForbidden) {
		t.Fatalf("缺少可信项目上下文未拒绝: %v", err)
	}
}

func TestBusinessReadRevocationStopsModelOutput(t *testing.T) {
	s, db, admin, adminCtx := assistantFixture(t)
	saveTestSettings(t, s, admin, adminCtx, "test-key")
	user, ctx := ordinaryAssistantUser(t, s, db, admin, adminCtx)
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
		return &fakeModel{respond: func(_ context.Context, _ []*schema.Message) (*schema.Message, error) {
			if calls.Add(1) == 1 {
				return schema.AssistantMessage("", []schema.ToolCall{{ID: "read-runs", Type: "function", Function: schema.FunctionCall{Name: "list_pipeline_runs", Arguments: `{}`}}}), nil
			}
			if err := s.deps.Access.SetUserPermissions(adminCtx, user.ID, access.UserPermissionOverrides{Allow: []string{"assistant.read", "assistant.execute"}}); err != nil {
				return nil, err
			}
			return schema.AssistantMessage("不可在撤权后继续输出的运行信息", nil), nil
		}}, nil
	}
	var emitted atomic.Bool
	err = s.Chat(ctx, user, conversation.ID, "查询流水线", func(event Event) {
		if event.Type == "delta" {
			emitted.Store(true)
		}
	})
	if !errors.Is(err, ErrForbidden) || emitted.Load() {
		t.Fatalf("业务权限撤销后仍输出: err=%v emitted=%v calls=%d", err, emitted.Load(), calls.Load())
	}
}

func TestProviderErrorIsStableAndOutputBounded(t *testing.T) {
	s, _, user, ctx := assistantFixture(t)
	saveTestSettings(t, s, user, ctx, "private-provider-key")
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	s.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
		return &fakeModel{respond: func(context.Context, []*schema.Message) (*schema.Message, error) {
			return nil, errors.New("provider SQL 192.0.2.1 Authorization: Bearer private-provider-key")
		}}, nil
	}
	if err := s.Chat(ctx, user, conversation.ID, "测试", nil); !errors.Is(err, ErrModelRequest) || strings.Contains(err.Error(), "SQL") {
		t.Fatalf("对外错误泄露: %v", err)
	}
	s.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
		return &fakeModel{respond: func(context.Context, []*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage(strings.Repeat("a", maxReplyBytes+1), nil), nil
		}}, nil
	}
	if err := s.Chat(ctx, user, conversation.ID, "测试长度", nil); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("未限制模型输出: %v", err)
	}
}

func TestOfficialOpenAIAdapterUsesUnsavedSettings(t *testing.T) {
	s, _, _, ctx := assistantFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer mock-only-key" {
			t.Errorf("模型请求契约错误: path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"mock","object":"chat.completion","created":1,"model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"连接正常"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	result, err := s.TestSettings(ctx, SettingsInput{BaseURL: server.URL + "/v1", Model: "mock-model", APIKey: "mock-only-key"})
	if err != nil || !result.Success || calls.Load() != 1 {
		t.Fatalf("官方适配器连接测试失败: %+v err=%v calls=%d", result, err, calls.Load())
	}
	settings, err := s.Settings(ctx)
	if err != nil || settings.HasAPIKey || settings.Version != 0 {
		t.Fatalf("测试不应保存配置: %+v %v", settings, err)
	}
}

func TestHistoricalRepliesRespectRevokedBusinessPermission(t *testing.T) {
	s, db, admin, adminCtx := assistantFixture(t)
	user, ctx := ordinaryAssistantUser(t, s, db, admin, adminCtx)
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	row := model.AssistantMessage{ID: newID(), ConversationID: conversation.ID, UserID: user.ID, ProjectID: database.DefaultProjectID(user.DepartmentID), DepartmentID: user.DepartmentID, Role: "assistant", Content: "历史流水线诊断", Status: "completed", RequiredPermissions: `["pipeline_run.read"]`, CreatedAt: time.Now().UTC()}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	before, err := s.Conversation(ctx, user, conversation.ID)
	if err != nil || before.Messages[0].Content != row.Content {
		t.Fatalf("有效权限无法读取: %+v %v", before, err)
	}
	if err := s.deps.Access.SetUserPermissions(adminCtx, user.ID, access.UserPermissionOverrides{Allow: []string{"assistant.read", "assistant.execute"}}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Conversation(ctx, user, conversation.ID)
	if err != nil || after.Messages[0].Status != "restricted" || strings.Contains(after.Messages[0].Content, "历史流水线") {
		t.Fatalf("撤权后泄露历史回复: %+v %v", after, err)
	}
}

func TestSessionRevocationStopsOutputAndTools(t *testing.T) {
	s, _, user, ctx := assistantFixture(t)
	saveTestSettings(t, s, user, ctx, "test-key")
	var active atomic.Bool
	active.Store(true)
	ctx = WithSessionAuthorization(ctx, active.Load)
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	s.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
		return &fakeModel{respond: func(context.Context, []*schema.Message) (*schema.Message, error) {
			active.Store(false)
			return schema.AssistantMessage("退出后不应输出", nil), nil
		}}, nil
	}
	err = s.Chat(ctx, user, conversation.ID, "测试退出", nil)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("退出后仍能回复: %v", err)
	}
	if _, err := s.tools(user, conversation.ID, func(Event) {})[0].(tool.InvokableTool).InvokableRun(ctx, `{}`); !errors.Is(err, ErrForbidden) {
		t.Fatalf("退出后仍能调用工具: %v", err)
	}
}

func TestSplitPrivateKeyLogsAreRedacted(t *testing.T) {
	parts := []string{"before", "-----BEGIN PRIVATE KEY-----", "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo=", "-----END PRIVATE KEY-----", "after"}
	text := redact(strings.Join(parts, "\n"))
	if strings.Contains(text, parts[2]) {
		t.Fatal("完整分行私钥泄露")
	}
	text = redact(strings.Join(parts[1:3], "\n"))
	if strings.Contains(text, parts[2]) {
		t.Fatal("不完整私钥泄露")
	}
	text = redact(parts[2])
	if strings.Contains(text, parts[2]) {
		t.Fatal("截断窗口内的私钥主体泄露")
	}
}
