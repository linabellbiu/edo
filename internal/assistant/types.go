package assistant

import (
	"context"
	"log/slog"
	"time"

	"edo/internal/access"
	"edo/internal/audit"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/pipeline"
	"edo/internal/secret"
	"gorm.io/gorm"
)

type Dependencies struct {
	Database   *gorm.DB
	Secrets    *secret.Manager
	Access     *access.Service
	Pipeline   *pipeline.Service
	Kubernetes *kube.Service
	Audits     *audit.Service
	Logger     *slog.Logger
}

type SettingsView struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
	HasAPIKey      bool   `json:"has_api_key"`
	Version        uint64 `json:"version"`
	MaxSteps       int    `json:"max_steps"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type SettingsInput struct {
	Enabled         bool   `json:"enabled"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	APIKey          string `json:"api_key"`
	ClearAPIKey     bool   `json:"clear_api_key"`
	ExpectedVersion uint64 `json:"expected_version"`
	MaxSteps        int    `json:"max_steps"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
}

type TestResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Model   string `json:"model"`
}

type StatusView struct {
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Reason     string `json:"reason"`
}

type ConversationView struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MessageView struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type ActionView struct {
	ID         string        `json:"id"`
	Kind       string        `json:"kind"`
	Title      string        `json:"title"`
	Summary    string        `json:"summary"`
	Status     string        `json:"status"`
	TargetName string        `json:"target_name"`
	ExpiresAt  time.Time     `json:"expires_at"`
	Result     *ActionResult `json:"result,omitempty"`
}

type ActionResult struct {
	PipelineRunID string `json:"pipeline_run_id,omitempty"`
	Message       string `json:"message,omitempty"`
}

type DetailView struct {
	Conversation ConversationView `json:"conversation"`
	Messages     []MessageView    `json:"messages"`
	Actions      []ActionView     `json:"actions"`
}

type Event struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ToolName string       `json:"tool_name,omitempty"`
	Action   *ActionView  `json:"action,omitempty"`
	Message  *MessageView `json:"message,omitempty"`
	Error    string       `json:"error,omitempty"`
}

// serviceContract 让接线与实现的签名变化在编译时立即暴露。
type serviceContract interface {
	Settings(context.Context) (SettingsView, error)
	SaveSettings(context.Context, string, SettingsInput) (SettingsView, error)
	TestSettings(context.Context, SettingsInput) (TestResult, error)
	Status(context.Context) (StatusView, error)
	ListConversations(context.Context, *model.User) ([]ConversationView, error)
	CreateConversation(context.Context, *model.User, string) (ConversationView, error)
	Conversation(context.Context, *model.User, string) (DetailView, error)
	Chat(context.Context, *model.User, string, string, func(Event)) error
	ConfirmAction(context.Context, *model.User, string) (ActionView, error)
	RejectAction(context.Context, *model.User, string) (ActionView, error)
}
