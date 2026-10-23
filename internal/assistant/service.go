package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"edo/internal/audit"
	"edo/internal/database"
	"edo/internal/model"
	"gorm.io/gorm"
)

var (
	ErrUnavailable   = errors.New("助手尚未配置，请联系管理员设置模型和 API Key")
	ErrDisabled      = errors.New("助手尚未启用")
	ErrForbidden     = errors.New("没有执行此操作的权限")
	ErrNotFound      = errors.New("会话或操作不存在")
	ErrConflict      = errors.New("配置已被修改，请刷新后重试")
	ErrInvalidInput  = errors.New("输入内容无效，请检查后重试")
	ErrBusy          = errors.New("当前会话正在回复，请稍后重试")
	ErrActionExpired = errors.New("操作确认已过期，请重新生成")
	ErrActionState   = errors.New("操作已处理或目标状态已变化，请重新查询")
	ErrModelRequest  = errors.New("模型服务请求失败，请检查配置或稍后重试")
	ErrStorage       = errors.New("助手数据暂时无法保存，请稍后重试")
	ErrCanceled      = errors.New("回复已中止")
	ErrOutputLimit   = errors.New("本次回复已达到长度上限，请缩小问题范围")
)

const settingsID = "global"

type Service struct {
	deps     Dependencies
	logger   *slog.Logger
	newModel modelFactory
}

type sessionAuthorizationKey struct{}

// WithSessionAuthorization 把已验证的登录会话复查回调传给长连接和工具调用。
func WithSessionAuthorization(ctx context.Context, authorized func() bool) context.Context {
	return context.WithValue(ctx, sessionAuthorizationKey{}, authorized)
}

var _ serviceContract = (*Service)(nil)

func NewService(deps Dependencies) *Service {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{deps: deps, logger: logger, newModel: newOpenAIModel}
}

func (s *Service) failure(ctx context.Context, operation string, cause, public error, attrs ...any) error {
	attrs = append([]any{"operation", operation, "err", cause}, attrs...)
	s.logger.ErrorContext(ctx, "助手操作失败", attrs...)
	return public
}

// 每个子调用重新读取身份与项目，避免长连接沿用已撤销的权限。
func (s *Service) authorize(ctx context.Context, user *model.User, permission string) (database.DepartmentScope, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if authorized, exists := ctx.Value(sessionAuthorizationKey{}).(func() bool); exists && (authorized == nil || !authorized()) {
		return scope, s.failure(ctx, "assistant_session", ErrForbidden, ErrForbidden)
	}
	if user == nil || !ok || scope.UserID != user.ID || scope.ProjectID == "" || scope.DepartmentID == "" || s.deps.Access == nil {
		return scope, s.failure(ctx, "assistant_authorize", ErrForbidden, ErrForbidden)
	}
	var current model.User
	if err := s.deps.Database.WithContext(ctx).First(&current, "id = ?", user.ID).Error; err != nil {
		return scope, s.failure(ctx, "assistant_identity", err, ErrForbidden, "actor_id", user.ID)
	}
	if !current.IsActive || current.AuthVersion != user.AuthVersion {
		return scope, s.failure(ctx, "assistant_identity", ErrForbidden, ErrForbidden, "actor_id", user.ID)
	}
	project, err := s.deps.Access.ResolveProject(ctx, &current, scope.ProjectID)
	if err != nil || !project.IsActive || project.DepartmentID != scope.DepartmentID {
		return scope, s.failure(ctx, "assistant_project", err, ErrForbidden, "actor_id", user.ID, "project_id", scope.ProjectID)
	}
	allowed, err := s.deps.Access.HasPermission(ctx, &current, permission)
	if err != nil || !allowed {
		return scope, s.failure(ctx, "assistant_permission", err, ErrForbidden, "actor_id", user.ID, "project_id", scope.ProjectID, "permission", permission)
	}
	return scope, nil
}

func (s *Service) owned(ctx context.Context, user *model.User) *gorm.DB {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	return s.deps.Database.WithContext(ctx).Where("user_id = ? AND project_id = ? AND department_id = ?", user.ID, scope.ProjectID, scope.DepartmentID)
}

func (s *Service) audit(ctx context.Context, actorID, operation, targetID string, result model.AuditResult, metadata map[string]any) error {
	if s.deps.Audits == nil {
		return s.failure(ctx, "assistant_audit", errors.New("审计服务未配置"), ErrStorage)
	}
	if err := s.deps.Audits.Record(ctx, audit.RecordInput{ActorUserID: actorID, Action: operation, ResourceType: "assistant", ResourceID: targetID, Result: result, Metadata: metadata}); err != nil {
		return s.failure(ctx, "assistant_audit", err, ErrStorage, "actor_id", actorID, "target_id", targetID)
	}
	return nil
}

func safeText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	return strings.ToValidUTF8(value, "")
}

func conversationView(row model.AssistantConversation) ConversationView {
	return ConversationView{ID: row.ID, Title: row.Title, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func messageView(row model.AssistantMessage) MessageView {
	return MessageView{ID: row.ID, Role: row.Role, Content: row.Content, Status: row.Status, CreatedAt: row.CreatedAt}
}

func (s *Service) authorizeMessage(ctx context.Context, user *model.User, row model.AssistantMessage) error {
	if row.RequiredPermissions == "" {
		return nil
	}
	var permissions []string
	if err := json.Unmarshal([]byte(row.RequiredPermissions), &permissions); err != nil {
		return s.failure(ctx, "assistant_message_permissions", err, ErrForbidden, "message_id", row.ID)
	}
	for _, permission := range permissions {
		if _, err := s.authorize(ctx, user, permission); err != nil {
			return err
		}
		if usage, ok := ctx.Value(permissionUsageKey{}).(*permissionUsage); ok {
			usage.mu.Lock()
			usage.permissions[permission] = true
			usage.mu.Unlock()
		}
	}
	return nil
}

func (s *Service) ListConversations(ctx context.Context, user *model.User) ([]ConversationView, error) {
	if _, err := s.authorize(ctx, user, "assistant.read"); err != nil {
		return nil, err
	}
	var rows []model.AssistantConversation
	if err := s.owned(ctx, user).Order("updated_at DESC, id DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, s.failure(ctx, "assistant_list", err, ErrStorage)
	}
	views := make([]ConversationView, 0, len(rows))
	for _, row := range rows {
		views = append(views, conversationView(row))
	}
	return views, nil
}

func (s *Service) CreateConversation(ctx context.Context, user *model.User, title string) (ConversationView, error) {
	scope, err := s.authorize(ctx, user, "assistant.execute")
	if err != nil {
		return ConversationView{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新的会话"
	}
	if len([]rune(title)) > 64 {
		return ConversationView{}, s.failure(ctx, "assistant_create", ErrInvalidInput, ErrInvalidInput)
	}
	now := time.Now().UTC()
	row := model.AssistantConversation{ID: newID(), UserID: user.ID, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Title: title, CreatedAt: now, UpdatedAt: now}
	if err := s.deps.Database.WithContext(ctx).Create(&row).Error; err != nil {
		return ConversationView{}, s.failure(ctx, "assistant_create", err, ErrStorage)
	}
	return conversationView(row), nil
}

func (s *Service) conversation(ctx context.Context, user *model.User, id string) (model.AssistantConversation, error) {
	var row model.AssistantConversation
	if err := s.owned(ctx, user).First(&row, "id = ?", id).Error; err != nil {
		public := ErrStorage
		if errors.Is(err, gorm.ErrRecordNotFound) {
			public = ErrNotFound
		}
		return row, s.failure(ctx, "assistant_conversation", err, public, "conversation_id", id)
	}
	return row, nil
}

func (s *Service) Conversation(ctx context.Context, user *model.User, id string) (DetailView, error) {
	if _, err := s.authorize(ctx, user, "assistant.read"); err != nil {
		return DetailView{}, err
	}
	row, err := s.conversation(ctx, user, id)
	if err != nil {
		return DetailView{}, err
	}
	var messages []model.AssistantMessage
	var actions []model.AssistantAction
	if err := s.owned(ctx, user).Where("conversation_id = ?", id).Order("created_at DESC, id DESC").Limit(200).Find(&messages).Error; err != nil {
		return DetailView{}, s.failure(ctx, "assistant_messages", err, ErrStorage)
	}
	if err := s.owned(ctx, user).Where("conversation_id = ?", id).Order("created_at ASC, id ASC").Limit(100).Find(&actions).Error; err != nil {
		return DetailView{}, s.failure(ctx, "assistant_actions", err, ErrStorage)
	}
	view := DetailView{Conversation: conversationView(row), Messages: []MessageView{}, Actions: []ActionView{}}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messageView(messages[i])
		if err := s.authorizeMessage(ctx, user, messages[i]); err != nil {
			message.Content, message.Status = "当前无权查看此回复涉及的业务数据", "restricted"
		}
		view.Messages = append(view.Messages, message)
	}
	if len(actions) > 0 {
		if _, err := s.authorize(ctx, user, "pipeline_run.read"); err == nil {
			for _, action := range actions {
				view.Actions = append(view.Actions, actionView(action))
			}
		}
	}
	return view, nil
}
