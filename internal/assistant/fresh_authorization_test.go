package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"edo/internal/access"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/pipeline"
	"edo/internal/repository"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
)

type freshAssistantFixture struct {
	service *Service
	db      *gorm.DB
	user    *model.User
	ctx     context.Context
	roleID  string
}

func newFreshAssistantFixture(t *testing.T, permissions ...string) *freshAssistantFixture {
	t.Helper()
	s, db, admin, adminCtx := assistantFixture(t)
	saveTestSettings(t, s, admin, adminCtx, "isolated-fake-model-key")
	user := &model.User{ID: newID(), Username: "assistant-fresh-member", DepartmentID: admin.DepartmentID, IsActive: true, AuthVersion: 1}
	role := model.Role{ID: newID(), Name: "assistant-fresh-role", DisplayName: "助手项目测试角色"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	for _, permission := range append([]string{"assistant.read", "assistant.execute"}, permissions...) {
		if err := db.Create(&model.RolePermission{RoleID: role.ID, Permission: permission}).Error; err != nil {
			t.Fatal(err)
		}
	}
	projectID := database.DefaultProjectID(user.DepartmentID)
	if err := s.deps.Access.SetProjectMember(adminCtx, admin, projectID, user.ID, role.ID); err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: projectID})
	// 模拟已建立且基础助手权限仍有效的会话；资源动作须由每个子调用独立复核。
	ctx = WithSessionAuthorization(ctx, func() bool { return true })
	return &freshAssistantFixture{service: s, db: db, user: user, ctx: ctx, roleID: role.ID}
}

func (f *freshAssistantFixture) revokeWithoutReload(t *testing.T, permission, change string) {
	t.Helper()
	// 只修改本用例隔离数据库，确定性保留跨实例策略通知到达前的旧 Casbin。
	if change == "role_action_removed" {
		result := f.db.Where("role_id = ? AND permission = ?", f.roleID, permission).Delete(&model.RolePermission{})
		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf("撤销测试角色动作失败: rows=%d err=%v", result.RowsAffected, result.Error)
		}
	} else {
		if err := f.db.Create(&model.UserPermission{UserID: f.user.ID, Permission: permission, Effect: model.PermissionDeny}).Error; err != nil {
			t.Fatal(err)
		}
	}
	stale, err := f.service.deps.Access.HasPermission(f.ctx, f.user, permission)
	if err != nil || !stale {
		t.Fatalf("夹具未保留旧策略允许: permission=%s allowed=%v err=%v", permission, stale, err)
	}
	scope, _ := database.DepartmentScopeFromContext(f.ctx)
	fresh, err := access.FreshProjectPermissions(f.db, f.ctx, f.user.ID, scope.ProjectID)
	if err != nil || fresh.Permissions[permission] || !fresh.Permissions["assistant.read"] || !fresh.Permissions["assistant.execute"] {
		t.Fatalf("夹具必须仅撤销资源动作并保留助手权限: fresh=%+v err=%v", fresh, err)
	}
}

func TestAssistantNativeToolRejectsRevokedResourceBeforePolicyReload(t *testing.T) {
	for _, change := range []string{"role_action_removed", "personal_deny_added"} {
		t.Run(change, func(t *testing.T) {
			f := newFreshAssistantFixture(t, "repository.read")
			scope, _ := database.DepartmentScopeFromContext(f.ctx)
			repo := model.GitRepository{ID: newID(), Name: "当前项目仓库", CloneURL: "https://example.invalid/current.git", DepartmentID: scope.DepartmentID, ProjectID: scope.ProjectID, IsActive: true}
			if err := f.db.Create(&repo).Error; err != nil {
				t.Fatal(err)
			}
			tool := diagnosticTool(t, f.service, f.user, "delivery_catalog")
			if output, err := tool.InvokableRun(f.ctx, `{"resource":"repositories"}`); err != nil || !strings.Contains(output, repo.ID) {
				t.Fatalf("当前有效权限未返回真实项目仓库: output=%s err=%v", output, err)
			}
			f.revokeWithoutReload(t, "repository.read", change)
			if output, err := tool.InvokableRun(f.ctx, `{"resource":"repositories"}`); !errors.Is(err, ErrForbidden) || output != "" {
				t.Fatalf("资源动作已撤销，原生工具仍返回数据: output=%s err=%v", output, err)
			}
		})
	}
}

func TestAssistantConfirmRejectsRevokedExecutionBeforePolicyReload(t *testing.T) {
	for _, change := range []string{"current_grant", "role_action_removed", "personal_deny_added"} {
		t.Run(change, func(t *testing.T) {
			f := newFreshAssistantFixture(t, "pipeline_run.read", "pipeline_run.execute")
			scope, _ := database.DepartmentScopeFromContext(f.ctx)
			app := model.Application{ID: newID(), Name: "assistant_fresh_app", RepositoryID: newID(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, CreatedBy: f.user.ID, IsActive: true}
			if err := f.db.Create(&model.GitRepository{ID: app.RepositoryID, Name: "assistant_fresh_repo", CloneURL: "https://example.invalid/app.git", ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, IsActive: true}).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Omit("Repository", "Repositories", "Workflows").Create(&app).Error; err != nil {
				t.Fatal(err)
			}
			run := model.PipelineRun{ID: newID(), ApplicationID: app.ID, ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, Status: model.PipelineRunDetected, Stage: "detected", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), CreatedBy: f.user.ID}
			if err := f.db.Omit("Application", "Repositories").Create(&run).Error; err != nil {
				t.Fatal(err)
			}
			conversation, err := f.service.CreateConversation(f.ctx, f.user, "")
			if err != nil {
				t.Fatal(err)
			}
			action, err := f.service.propose(f.ctx, f.user, conversation.ID, "pipeline_stop", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if change != "current_grant" {
				f.revokeWithoutReload(t, "pipeline_run.execute", change)
			}
			view, err := f.service.ConfirmAction(f.ctx, f.user, action.ID)
			var storedAction model.AssistantAction
			if queryErr := f.db.First(&storedAction, "uuid = ?", action.ID).Error; queryErr != nil {
				t.Fatal(queryErr)
			}
			if queryErr := f.db.First(&run, "uuid = ?", run.ID).Error; queryErr != nil {
				t.Fatal(queryErr)
			}
			if change == "current_grant" {
				if err != nil || view.Status != "succeeded" || run.Status != model.PipelineRunCanceled {
					t.Fatalf("有效项目成员的确认未正常执行: view=%+v run=%s err=%v", view, run.Status, err)
				}
			} else if !errors.Is(err, ErrForbidden) || storedAction.Status != "pending" || run.Status != model.PipelineRunDetected {
				t.Fatalf("已撤权的确认仍领取操作或停止运行: action=%s run=%s err=%v", storedAction.Status, run.Status, err)
			}
		})
	}
}

func TestAssistantNativeConfirmationRejectsRevokedWriteBeforePolicyReload(t *testing.T) {
	for _, change := range []string{"current_grant", "role_action_removed", "personal_deny_added"} {
		t.Run(change, func(t *testing.T) {
			input := operationInput{Kind: "workflow_create", Workflow: &pipeline.AssistantWorkflowInput{ApplicationName: "fresh_workflow_app", RepositoryID: newID(), Name: "新建草稿", Preset: "blank", Branch: "main", Events: []string{"manual"}}}
			permissions := operationPermissions(input)
			f := newFreshAssistantFixture(t, permissions[1:]...)
			f.service.deps.Pipeline = pipeline.NewService(f.db, repository.NewService(f.db, f.service.deps.Secrets, credential.NewService(f.db, f.service.deps.Secrets), nil, 4), f.service.deps.Secrets)
			scope, _ := database.DepartmentScopeFromContext(f.ctx)
			repo := model.GitRepository{ID: input.Workflow.RepositoryID, Name: "助手仓库", CloneURL: "https://example.invalid/app.git", IsActive: true, DepartmentID: scope.DepartmentID, ProjectID: scope.ProjectID}
			if err := f.db.Create(&repo).Error; err != nil {
				t.Fatal(err)
			}
			conversation, err := f.service.CreateConversation(f.ctx, f.user, "")
			if err != nil {
				t.Fatal(err)
			}
			action, err := f.service.proposeOperation(f.ctx, f.user, conversation.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if change != "current_grant" {
				f.revokeWithoutReload(t, "workflow.create", change)
			}
			view, err := f.service.ConfirmAction(f.ctx, f.user, action.ID)
			var count int64
			if queryErr := f.db.Model(&model.ReleaseWorkflow{}).Where("project_id = ?", scope.ProjectID).Count(&count).Error; queryErr != nil {
				t.Fatal(queryErr)
			}
			var stored model.AssistantAction
			if queryErr := f.db.First(&stored, "uuid = ?", action.ID).Error; queryErr != nil {
				t.Fatal(queryErr)
			}
			if change == "current_grant" {
				if err != nil || view.Status != "succeeded" || count != 1 {
					t.Fatalf("有效权限未创建真实草稿: count=%d view=%+v err=%v", count, view, err)
				}
			} else if !errors.Is(err, ErrForbidden) || count != 0 || stored.Status != "pending" {
				t.Fatalf("已撤权的原生确认仍写入草稿: action=%s count=%d err=%v", stored.Status, count, err)
			}
		})
	}
}

func TestAssistantChatSuppressesOutputAfterResourceRevokedBeforePolicyReload(t *testing.T) {
	for _, change := range []string{"role_action_removed", "personal_deny_added"} {
		t.Run(change, func(t *testing.T) {
			f := newFreshAssistantFixture(t, "pipeline_run.read")
			conversation, err := f.service.CreateConversation(f.ctx, f.user, "")
			if err != nil {
				t.Fatal(err)
			}
			var calls, deltas atomic.Int32
			f.service.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
				return &fakeModel{respond: func(context.Context, []*schema.Message) (*schema.Message, error) {
					if calls.Add(1) == 1 {
						return schema.AssistantMessage("", []schema.ToolCall{{ID: "read-before-revocation", Type: "function", Function: schema.FunctionCall{Name: "list_pipeline_runs", Arguments: `{}`}}}), nil
					}
					f.revokeWithoutReload(t, "pipeline_run.read", change)
					return schema.AssistantMessage("撤权后不得继续输出的项目数据", nil), nil
				}}, nil
			}
			err = f.service.Chat(f.ctx, f.user, conversation.ID, "查询当前项目流水线", func(event Event) {
				if event.Type == "delta" {
					deltas.Add(1)
				}
			})
			if !errors.Is(err, ErrForbidden) || deltas.Load() != 0 || calls.Load() != 2 {
				t.Fatalf("助手仍输出已撤权资源: calls=%d deltas=%d err=%v", calls.Load(), deltas.Load(), err)
			}
		})
	}
}

func TestAssistantFreshAuthorizationStorageFailureIsSafeAndLogged(t *testing.T) {
	f := newFreshAssistantFixture(t, "repository.read")
	var output bytes.Buffer
	f.service.logger = slog.New(slog.NewJSONHandler(&output, nil))
	cause := errors.New("isolated database diagnostic marker")
	if err := f.db.Callback().Query().Before("gorm:query").Register("assistant-fresh-storage-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "role_permissions" {
			tx.AddError(cause)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.authorize(f.ctx, f.user, "repository.read"); !errors.Is(err, ErrForbidden) || strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("当前权限读取失败未安全拒绝: err=%v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("缺少安全诊断日志: log=%s err=%v", output.String(), err)
	}
	scope, _ := database.DepartmentScopeFromContext(f.ctx)
	diagnostic, _ := entry["err"].(string)
	if entry["operation"] != "assistant_permission" || entry["actor_id"] != f.user.ID || entry["project_id"] != scope.ProjectID || entry["department_id"] != scope.DepartmentID || entry["permission"] != "repository.read" || !strings.Contains(diagnostic, cause.Error()) {
		t.Fatalf("错误日志缺少操作上下文与原始诊断: %v", entry)
	}
}
