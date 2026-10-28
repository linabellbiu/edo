package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/pipeline"
	"edo/internal/repository"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestNativeCapabilitiesAndCatalogDoNotInventFeaturesOrExposeCredentials(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	row := model.GitRepository{ID: newID(), Name: "助手仓库", Provider: model.GitProviderGitHub, CloneURL: "https://example.invalid/app.git", DefaultBranch: "main", CredentialCiphertext: "never-send-this", DepartmentID: user.DepartmentID, ProjectID: database.DefaultProjectID(user.DepartmentID), IsActive: true}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	output, err := diagnosticTool(t, s, user, "delivery_catalog").InvokableRun(ctx, `{"resource":"repositories"}`)
	if err != nil || !strings.Contains(output, row.ID) || strings.Contains(output, "never-send-this") || strings.Contains(output, "clone_url") {
		t.Fatalf("目录字段不正确: %s %v", output, err)
	}
	output, err = diagnosticTool(t, s, user, "edo_capabilities").InvokableRun(ctx, `{"resource":"invented_feature"}`)
	if err != nil || !strings.Contains(output, "目前还没有这个功能") || !strings.Contains(output, "false") {
		t.Fatalf("未知能力未明确拒绝: %s %v", output, err)
	}
	for _, name := range []string{"runtime_control", "yaml_apply", "arbitrary_commands", "external_knowledge", "kubernetes_workload_create", "delete", "k3s_uninstall", "kubernetes.k3s.uninstall"} {
		output, err = diagnosticTool(t, s, user, "edo_capabilities").InvokableRun(ctx, `{"resource":"`+name+`"}`)
		if err != nil || !strings.Contains(output, `\"supported\":false`) {
			t.Fatalf("禁止能力错误开放: %s %v", output, err)
		}
	}
	for _, input := range []string{`{"project_id":"other","resource":"repositories"}`, `{"workflow":{"name":"test","command":"rm"}}`} {
		if _, err := diagnosticTool(t, s, user, "prepare_workflow").InvokableRun(ctx, input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("未拒绝模型注入参数: %v", err)
		}
	}
}

func TestNativePlanningStepLimitIsBoundedAndKeepsSavedValues(t *testing.T) {
	if defaultSettings().MaxSteps != 12 {
		t.Fatal("新配置没有为多步规划预留步骤")
	}
	for _, steps := range []int{6, 12, 20} {
		value, err := normalizeSettings(SettingsInput{BaseURL: "https://example.invalid/v1", Model: "test", MaxSteps: steps})
		if err != nil || value.MaxSteps != steps {
			t.Fatalf("步骤设置被覆盖: %+v %v", value, err)
		}
	}
	if _, err := normalizeSettings(SettingsInput{BaseURL: "https://example.invalid/v1", MaxSteps: 21}); err == nil {
		t.Fatal("未限制单次对话步骤")
	}
}

func TestNativeWorkflowConfirmationIsOwnedIdempotentAndCreatesOnlyDraft(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	saveTestSettings(t, s, user, ctx, "test-only-key")
	repo := model.GitRepository{ID: newID(), Name: "助手仓库", CloneURL: "https://example.invalid/app.git", IsActive: true, DepartmentID: user.DepartmentID, ProjectID: database.DefaultProjectID(user.DepartmentID)}
	if err := db.WithContext(ctx).Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	s.deps.Pipeline = pipeline.NewService(db, repository.NewService(db, s.deps.Secrets, credential.NewService(db, s.deps.Secrets), nil, 4), s.deps.Secrets)
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	input := operationInput{Kind: "workflow_create", Workflow: &pipeline.AssistantWorkflowInput{ApplicationName: "from_assistant", RepositoryID: repo.ID, Name: "新流水线", Preset: "blank", Branch: "main", Events: []string{"manual"}}}
	action, err := s.proposeOperation(ctx, user, conversation.ID, input)
	if err != nil || action.Preview == nil {
		t.Fatalf("缺少真实预览: %+v %v", action, err)
	}
	var count int64
	db.Model(&model.Application{}).Count(&count)
	if count != 0 {
		t.Fatal("未确认先创建了应用")
	}
	other, otherCtx := ordinaryAssistantUser(t, s, db, user, ctx)
	if _, err := s.ConfirmAction(otherCtx, other, action.ID); err == nil {
		t.Fatal("另一个用户确认了操作")
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result, err := s.ConfirmAction(ctx, user, action.ID); err == nil && result.Status == "succeeded" {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("确认次数错误: %d", accepted.Load())
	}
	var workflows []model.ReleaseWorkflow
	if err := db.Find(&workflows).Error; err != nil || len(workflows) != 1 || workflows[0].IsActive {
		t.Fatalf("草稿创建状态错误: %+v %v", workflows, err)
	}
	db.Model(&model.PipelineRun{}).Count(&count)
	if count != 0 {
		t.Fatal("创建时发起了部署")
	}
	// 配置变化与过期记录都不能再次执行。
	input.Workflow.ApplicationName = "second_app"
	action, err = s.proposeOperation(ctx, user, conversation.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&repo).Update("default_branch", "changed").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmAction(ctx, user, action.ID); !errors.Is(err, ErrActionState) {
		t.Fatalf("仓库变化未使旧确认失效: %v", err)
	}
	if err := db.Model(&model.AssistantAction{}).Where("uuid = ?", action.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmAction(ctx, user, action.ID); !errors.Is(err, ErrActionExpired) {
		t.Fatalf("过期确认未拒绝: %v", err)
	}
}

func TestNativeEinoPlansFromCatalogWithoutWritingConfiguration(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	saveTestSettings(t, s, user, ctx, "fake-planning-key")
	repo := model.GitRepository{ID: newID(), Name: "demo", CloneURL: "https://example.invalid/demo.git", IsActive: true, DepartmentID: user.DepartmentID, ProjectID: database.DefaultProjectID(user.DepartmentID)}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	s.deps.Pipeline = pipeline.NewService(db, repository.NewService(db, s.deps.Secrets, credential.NewService(db, s.deps.Secrets), nil, 4), s.deps.Secrets)
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
		return &fakeModel{respond: func(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
			calls++
			steps := []schema.FunctionCall{{Name: "edo_capabilities", Arguments: `{}`}, {Name: "delivery_catalog", Arguments: `{"resource":"repositories"}`}, {Name: "prepare_workflow", Arguments: `{"workflow":{"application_name":"eino_app","repository_id":"` + repo.ID + `","name":"发布草稿","preset":"blank","branch":"main","events":["manual"]}}`}}
			if calls <= len(steps) {
				return schema.AssistantMessage("", []schema.ToolCall{{ID: newID(), Type: "function", Function: steps[calls-1]}}), nil
			}
			found := false
			for _, message := range messages {
				if message.Role == schema.Tool && strings.Contains(message.Content, "workflow_create") {
					found = true
				}
			}
			if !found {
				t.Error("Eino没有收到真实计划")
			}
			return schema.AssistantMessage("已生成草稿计划，请查看并确认。目前尚未创建、启用或部署。", nil), nil
		}}, nil
	}
	if err := s.Chat(ctx, user, conversation.ID, "给demo仓库创建main分支的空白流水线", nil); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Conversation(ctx, user, conversation.ID)
	if err != nil || len(detail.Actions) != 1 || detail.Actions[0].Kind != "workflow_create" || calls != 4 {
		t.Fatalf("规划流程不完整: %+v %v", detail, err)
	}
	var count int64
	db.Model(&model.Application{}).Count(&count)
	if count != 0 {
		t.Fatal("模型绕过确认直接创建配置")
	}
}

type yamlCheckReader struct {
	diagnosticKube
	checks int
	fail   error
}

func (r *yamlCheckReader) ValidateResource(context.Context, string, string, string, string, map[string]any) error {
	r.checks++
	return r.fail
}

func TestNativeYAMLPreviewFormatIdentityDryRunAndNoApply(t *testing.T) {
	for _, text := range []string{"a: 1\na: 2", "a: [", "a: &a [*a]", "a: 1\n---\nb: 2", strings.Repeat("a", 17*1024)} {
		if _, _, err := checkedYAML(text); err == nil {
			t.Fatalf("无效YAML未拒绝: %.40s", text)
		}
	}
	s, db, user, ctx := assistantFixture(t)
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	cluster := model.KubernetesCluster{ID: newID(), Name: "测试集群", IsActive: true, DepartmentID: user.DepartmentID, ProjectID: database.DefaultProjectID(user.DepartmentID)}
	if err := db.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	reader := &yamlCheckReader{diagnosticKube: diagnosticKube{object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "app", "namespace": "test", "uid": "uid-1", "resourceVersion": "12"}}}}
	s.deps.Kubernetes = reader
	input := toolInput{ClusterID: cluster.ID, Resource: "configmaps", Namespace: "test", Name: "app", UID: "uid-1", YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: test\n  uid: uid-1\n  resourceVersion: '12'\ndata:\n  logLevel: debug\n"}
	tool := &assistantTool{service: s, user: user, conversationID: conversation.ID, emit: func(Event) {}}
	value, err := tool.yamlPreview(ctx, input)
	if err != nil || reader.checks != 1 {
		t.Fatalf("未完成dry-run: %+v %v", value, err)
	}
	view := value.(ActionView)
	if view.Status != "draft" || view.Preview == nil {
		t.Fatal("预览生成了可执行操作")
	}
	if _, err := s.ConfirmAction(ctx, user, view.ID); err == nil {
		t.Fatal("YAML预览允许确认执行")
	}
	input.YAML = strings.Replace(input.YAML, "'12'", "'11'", 1)
	if _, err := tool.yamlPreview(ctx, input); !errors.Is(err, ErrActionState) || reader.checks != 1 {
		t.Fatalf("旧版本未拦截: %v", err)
	}
	input.Resource = "secrets"
	if _, err := tool.yamlPreview(ctx, input); err == nil {
		t.Fatal("允许Secret预览")
	}
}

func TestNativeKnowledgeHistoryRechecksPermissionsAndProjectScope(t *testing.T) {
	s, db, admin, adminCtx := assistantFixture(t)
	user, ctx := ordinaryAssistantUser(t, s, db, admin, adminCtx)
	project := database.DefaultProjectID(user.DepartmentID)
	for _, row := range []model.AssistantMessage{
		{ID: newID(), UserID: user.ID, ProjectID: project, DepartmentID: user.DepartmentID, Role: "assistant", Status: "completed", Content: "数据库故障：本人记录", RequiredPermissions: `["pipeline_run.read"]`},
		{ID: newID(), UserID: admin.ID, ProjectID: project, DepartmentID: user.DepartmentID, Role: "assistant", Status: "completed", Content: "数据库故障：他人记录", RequiredPermissions: `[]`},
		{ID: newID(), UserID: user.ID, ProjectID: "other", DepartmentID: user.DepartmentID, Role: "assistant", Status: "completed", Content: "数据库故障：其他项目", RequiredPermissions: `[]`},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	tool := diagnosticTool(t, s, user, "knowledge_search")
	output, err := tool.InvokableRun(ctx, `{"query":"数据库故障","resource":"history"}`)
	if err != nil || !strings.Contains(output, "本人记录") || strings.Contains(output, "他人记录") || strings.Contains(output, "其他项目") {
		t.Fatalf("历史检索越界: %s %v", output, err)
	}
	if err := s.deps.Access.SetUserPermissions(adminCtx, user.ID, access.UserPermissionOverrides{Allow: []string{"assistant.read", "assistant.execute"}}); err != nil {
		t.Fatal(err)
	}
	output, err = tool.InvokableRun(ctx, `{"query":"数据库故障","resource":"history"}`)
	if err != nil || strings.Contains(output, "本人记录") {
		t.Fatalf("撤权后泄露历史: %s %v", output, err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatal(err)
	}
}
