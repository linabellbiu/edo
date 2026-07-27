package repository

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/credential"
	"zrt/internal/database"
	"zrt/internal/model"
	"zrt/internal/secret"
)

func TestRepositorySecretsAreEncryptedAndURLsAreValidated(t *testing.T) {
	service, db := newRepositoryTestService(t)
	token := "provider-access-token"
	repo, webhookSecret, err := service.Create(context.Background(), "admin", Input{
		Name: "production-api", Provider: model.GitProviderGitHub,
		CloneURL: "https://github.example.com/team/api.git", AuthType: model.GitAuthToken,
		Credential: &token, WebhookEnabled: true,
	})
	if err != nil {
		t.Fatalf("创建代码仓库失败: %v", err)
	}
	if webhookSecret == "" || repo.CredentialCiphertext == token || repo.WebhookSecretCiphertext == webhookSecret {
		t.Fatal("代码仓库密钥未加密或未生成")
	}
	var stored model.GitRepository
	if err := db.First(&stored, "id = ?", repo.ID).Error; err != nil {
		t.Fatalf("读取代码仓库失败: %v", err)
	}
	if stored.CredentialCiphertext == token || stored.WebhookSecretCiphertext == webhookSecret {
		t.Fatal("数据库中出现明文密钥")
	}

	credentialURL := "https://user:secret@example.com/team/api.git"
	_, _, err = service.Create(context.Background(), "admin", Input{
		Name: "bad-credentials", Provider: model.GitProviderGeneric,
		CloneURL: credentialURL, AuthType: model.GitAuthNone,
	})
	if !errors.Is(err, ErrInvalidRepository) {
		t.Fatalf("URL 中的明文凭据未被拒绝: %v", err)
	}
	_, _, err = service.Create(context.Background(), "admin", Input{
		Name: "bad-http", Provider: model.GitProviderGeneric,
		CloneURL: "http://git.example.com/team/api.git", AuthType: model.GitAuthNone,
	})
	if !errors.Is(err, ErrInsecureRepository) {
		t.Fatalf("未确认的不安全 HTTP 仓库未被拒绝: %v", err)
	}
}

func TestRepositoryNameSupportsChinese(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	service.git = staticRefLister{result: RefResult{Branches: []GitRef{{Name: "main"}}}}
	input := Input{
		Name: "我的", Provider: model.GitProviderGitea,
		CloneURL: "https://git.example.com/team/project.git", AuthType: model.GitAuthNone,
	}
	if _, err := service.TestInput(context.Background(), "admin", input); err != nil {
		t.Fatalf("连接测试不应拒绝中文仓库名称: %v", err)
	}
	repo, _, err := service.Create(context.Background(), "admin", input)
	if err != nil {
		t.Fatalf("创建仓库不应拒绝中文名称: %v", err)
	}
	if repo.Name != "我的" {
		t.Fatalf("中文仓库名称未正确保存: %+v", repo)
	}

	input.Name = "错误/名称"
	if _, err := service.TestInput(context.Background(), "admin", input); !errors.Is(err, ErrInvalidRepositoryName) {
		t.Fatalf("连接测试应返回明确的仓库名称错误: %v", err)
	}
	if _, _, err := service.Create(context.Background(), "admin", input); !errors.Is(err, ErrInvalidRepositoryName) {
		t.Fatalf("创建仓库应返回相同的名称错误: %v", err)
	}
}

func TestRepositoryCanBeUpdatedAndDeletedWhenUnused(t *testing.T) {
	service, db := newRepositoryTestService(t)
	repo, _, err := service.Create(context.Background(), "admin", Input{
		Name: "待修改仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/old.git", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("创建待修改仓库失败: %v", err)
	}
	updated, webhookSecret, err := service.Update(context.Background(), "admin", repo.ID, Input{
		Name: "已修改仓库", Provider: model.GitProviderGitea,
		CloneURL: "https://git.example.com/team/new.git", DefaultBranch: "develop",
		AuthType: model.GitAuthNone, WebhookEnabled: true,
	})
	if err != nil {
		t.Fatalf("修改代码仓库失败: %v", err)
	}
	if updated.Name != "已修改仓库" || updated.Provider != model.GitProviderGitea ||
		updated.CloneURL != "https://git.example.com/team/new.git" || updated.DefaultBranch != "develop" || webhookSecret == "" {
		t.Fatalf("修改后的代码仓库内容不正确: %+v", updated)
	}

	delivery := model.GitWebhookDelivery{
		ID: "delivery-delete", RepositoryID: repo.ID, DeliveryID: "provider-delete", EventType: "push",
		Ref: "refs/heads/develop", PayloadHash: "hash", Status: model.WebhookProcessed, ReceivedAt: time.Now().UTC(),
	}
	if err := db.Create(&delivery).Error; err != nil {
		t.Fatalf("创建 Webhook 投递记录失败: %v", err)
	}
	now := time.Now().UTC()
	application := model.Application{
		ID: "application-delete", Name: "引用仓库的应用", RepositoryID: repo.ID, Branch: "develop",
		SyncStatus: model.ApplicationSyncIdle, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&application).Error; err != nil {
		t.Fatalf("创建仓库引用失败: %v", err)
	}
	if err := service.Delete(context.Background(), repo.ID); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("使用中的代码仓库未阻止删除: %v", err)
	}
	if err := db.Delete(&application).Error; err != nil {
		t.Fatalf("清理仓库引用失败: %v", err)
	}
	if err := service.Delete(context.Background(), repo.ID); err != nil {
		t.Fatalf("删除未使用的代码仓库失败: %v", err)
	}
	var deliveryCount int64
	if err := db.Model(&model.GitWebhookDelivery{}).Where("repository_id = ?", repo.ID).Count(&deliveryCount).Error; err != nil || deliveryCount != 0 {
		t.Fatalf("删除仓库后仍有 Webhook 投递记录: count=%d err=%v", deliveryCount, err)
	}
	if err := service.Delete(context.Background(), repo.ID); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("重复删除仓库未返回不存在: %v", err)
	}
}

func TestRepositoryUsesOwnedCredentialAndWebhookCanBeRevealed(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	token := "saved-provider-token"
	saved, err := service.credentials.Create(context.Background(), "user-a", credential.Input{
		Name: "GitHub 生产令牌", Provider: model.GitProviderGitHub, AuthType: model.GitAuthToken, Secret: &token,
	})
	if err != nil {
		t.Fatalf("创建个人令牌失败: %v", err)
	}
	repo, generated, err := service.Create(context.Background(), "user-a", Input{
		Name: "owned-token-repository", Provider: model.GitProviderGitHub,
		CloneURL: "https://github.com/example/project.git", AuthType: model.GitAuthToken,
		CredentialID: &saved.ID, WebhookEnabled: true,
	})
	if err != nil {
		t.Fatalf("引用个人令牌创建仓库失败: %v", err)
	}
	if repo.CredentialID == nil || *repo.CredentialID != saved.ID || repo.CredentialCiphertext != "" {
		t.Fatalf("仓库未正确引用个人令牌: %+v", repo)
	}
	revealed, err := service.RevealWebhookSecret(context.Background(), repo.ID)
	if err != nil || revealed != generated {
		t.Fatalf("Webhook 密钥无法重复读取: generated=%q revealed=%q err=%v", generated, revealed, err)
	}
	_, _, err = service.Create(context.Background(), "user-b", Input{
		Name: "foreign-token-repository", Provider: model.GitProviderGitHub,
		CloneURL: "https://github.com/example/other.git", AuthType: model.GitAuthToken, CredentialID: &saved.ID,
	})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("其他用户引用非本人令牌未被拒绝: %v", err)
	}
}

func TestWebhookProvidersSignatureAndDeduplication(t *testing.T) {
	providers := []struct {
		provider       model.GitProvider
		eventHeader    string
		eventValue     string
		deliveryHeader string
		signature      func(http.Header, []byte, string)
	}{
		{model.GitProviderGitHub, "X-GitHub-Event", "push", "X-GitHub-Delivery", func(header http.Header, body []byte, secret string) {
			header.Set("X-Hub-Signature-256", "sha256="+sign(body, secret))
		}},
		{model.GitProviderGitLab, "X-Gitlab-Event", "Push Hook", "X-Gitlab-Event-UUID", func(header http.Header, _ []byte, secret string) {
			header.Set("X-Gitlab-Token", secret)
		}},
		{model.GitProviderGitea, "X-Gitea-Event", "push", "X-Gitea-Delivery", func(header http.Header, body []byte, secret string) {
			header.Set("X-Gitea-Signature", sign(body, secret))
		}},
		{model.GitProviderGitee, "X-Gitee-Event", "Push Hook", "X-Gitee-Delivery", func(header http.Header, _ []byte, secret string) {
			header.Set("X-Gitee-Token", secret)
		}},
		{model.GitProviderGeneric, "X-ZRT-Event", "push", "X-ZRT-Delivery", func(header http.Header, body []byte, secret string) {
			header.Set("X-ZRT-Signature-256", "sha256="+sign(body, secret))
		}},
	}
	body := []byte(`{"ref":"refs/heads/main","after":"0123456789012345678901234567890123456789","head_commit":{"message":"release"}}`)

	for _, tt := range providers {
		t.Run(string(tt.provider), func(t *testing.T) {
			service, db := newRepositoryTestService(t)
			repo, webhookSecret, err := service.Create(context.Background(), "admin", Input{
				Name: "repo-" + string(tt.provider), Provider: tt.provider,
				CloneURL: "https://git.example.com/team/api.git", AuthType: model.GitAuthNone,
				WebhookEnabled: true,
			})
			if err != nil {
				t.Fatalf("创建 Webhook 仓库失败: %v", err)
			}
			headers := make(http.Header)
			headers.Set(tt.eventHeader, tt.eventValue)
			headers.Set(tt.deliveryHeader, "delivery-1")
			tt.signature(headers, body, webhookSecret)

			result, err := service.HandleWebhook(context.Background(), repo.ID, headers, body)
			if err != nil {
				t.Fatalf("处理 %s Webhook 失败: %v", tt.provider, err)
			}
			if result.Duplicate || result.Delivery.JobID == "" || result.Delivery.EventType != "branch_push" {
				t.Fatalf("Webhook 结果错误: %+v", result)
			}
			duplicate, err := service.HandleWebhook(context.Background(), repo.ID, headers, body)
			if err != nil || !duplicate.Duplicate || duplicate.Delivery.ID != result.Delivery.ID {
				t.Fatalf("Webhook 去重失败: result=%+v err=%v", duplicate, err)
			}
			var jobCount int64
			if err := db.Model(&model.Job{}).Where("id = ?", result.Delivery.JobID).Count(&jobCount).Error; err != nil || jobCount != 1 {
				t.Fatalf("Webhook 任务数量错误: count=%d err=%v", jobCount, err)
			}

			badHeaders := headers.Clone()
			switch tt.provider {
			case model.GitProviderGitHub:
				badHeaders.Set("X-Hub-Signature-256", "sha256=00")
			case model.GitProviderGitLab:
				badHeaders.Set("X-Gitlab-Token", "bad")
			case model.GitProviderGitea:
				badHeaders.Set("X-Gitea-Signature", "00")
			case model.GitProviderGitee:
				badHeaders.Set("X-Gitee-Token", "bad")
			default:
				badHeaders.Set("X-ZRT-Signature-256", "sha256=00")
			}
			if _, err := service.HandleWebhook(context.Background(), repo.ID, badHeaders, body); !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("无效签名未被拒绝: %v", err)
			}
		})
	}
}

func TestGenericWebhookNormalizesCommonGitEvents(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	repo, webhookSecret, err := service.Create(context.Background(), "admin", Input{
		Name: "generic-events", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/events.git", AuthType: model.GitAuthNone,
		WebhookEnabled: true,
	})
	if err != nil {
		t.Fatalf("创建通用 Webhook 仓库失败: %v", err)
	}

	tests := []struct {
		name       string
		event      string
		body       string
		wantType   string
		wantRef    string
		wantCommit string
	}{
		{
			name: "branch push", event: "push",
			body:     `{"ref":"refs/heads/main","after":"1111111111111111111111111111111111111111"}`,
			wantType: "branch_push", wantRef: "refs/heads/main", wantCommit: "1111111111111111111111111111111111111111",
		},
		{
			name: "tag push", event: "push",
			body:     `{"ref":"refs/tags/v1.2.3","after":"2222222222222222222222222222222222222222"}`,
			wantType: "tag_push", wantRef: "refs/tags/v1.2.3", wantCommit: "2222222222222222222222222222222222222222",
		},
		{
			name: "pull request", event: "pull_request",
			body:     `{"pull_request":{"head":{"sha":"3333333333333333333333333333333333333333"},"base":{"ref":"release"}}}`,
			wantType: "pull_request", wantRef: "refs/heads/release", wantCommit: "3333333333333333333333333333333333333333",
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			headers := make(http.Header)
			headers.Set("X-ZRT-Event", tt.event)
			headers.Set("X-ZRT-Delivery", fmt.Sprintf("common-event-%d", index))
			headers.Set("X-ZRT-Signature-256", "sha256="+sign(body, webhookSecret))

			result, err := service.HandleWebhook(context.Background(), repo.ID, headers, body)
			if err != nil {
				t.Fatalf("处理通用 Webhook 事件失败: %v", err)
			}
			if result.Delivery.EventType != tt.wantType || result.Delivery.Ref != tt.wantRef || result.Delivery.CommitSHA != tt.wantCommit {
				t.Fatalf("事件归一化结果错误: %+v", result.Delivery)
			}
		})
	}
}

func TestWebhookRequiresGlobalFeatureGate(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	repo, _, err := service.Create(context.Background(), "admin", Input{
		Name: "gated-webhook", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/gated.git", AuthType: model.GitAuthNone,
		WebhookEnabled: true,
	})
	if err != nil {
		t.Fatalf("创建测试仓库失败: %v", err)
	}

	service.webhookGate = staticWebhookGate{}
	if _, err := service.HandleWebhook(context.Background(), repo.ID, http.Header{}, []byte(`{}`)); !errors.Is(err, ErrExternalWebhookDisabled) {
		t.Fatalf("全局开关关闭时 Webhook 未被拒绝: %v", err)
	}
	service.webhookGate = staticWebhookGate{err: errors.New("database unavailable")}
	if _, err := service.HandleWebhook(context.Background(), repo.ID, http.Header{}, []byte(`{}`)); !errors.Is(err, ErrWebhookUnavailable) {
		t.Fatalf("全局开关读取失败时 Webhook 未安全失败: %v", err)
	}
}

func TestProcessWebhookTaskIsIdempotent(t *testing.T) {
	service, db := newRepositoryTestService(t)
	now := time.Now().UTC()
	delivery := model.GitWebhookDelivery{
		ID: "delivery-id", RepositoryID: "repository-id", DeliveryID: "provider-id",
		EventType: "branch_push", Ref: "refs/heads/main", PayloadHash: hex.EncodeToString(make([]byte, 32)),
		Status: model.WebhookReceived, ReceivedAt: now,
	}
	if err := db.Create(&delivery).Error; err != nil {
		t.Fatalf("创建投递记录失败: %v", err)
	}
	payload, _ := json.Marshal(WebhookTaskPayload{DeliveryID: delivery.ID, RepositoryID: delivery.RepositoryID})
	for range 2 {
		if err := service.ProcessWebhookTask(context.Background(), payload); err != nil {
			t.Fatalf("幂等处理 Webhook 任务失败: %v", err)
		}
	}
}

func TestRepositoryInputCanBeTestedWithoutSaving(t *testing.T) {
	service, db := newRepositoryTestService(t)
	service.git = staticRefLister{result: RefResult{
		Branches: []GitRef{{Name: "main", SHA: "0123456789012345678901234567890123456789"}},
		Tags:     []GitRef{{Name: "v1.0.0", SHA: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}},
	}}

	result, err := service.TestInput(context.Background(), "admin", Input{
		Name: "临时仓库", Provider: model.GitProviderGeneric, CloneURL: "https://git.example.com/team/api.git", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("测试未保存仓库失败: %v", err)
	}
	if len(result.Branches) != 1 || result.Branches[0].Name != "main" || len(result.Tags) != 1 || result.Tags[0].Name != "v1.0.0" {
		t.Fatalf("未正确读取未保存仓库的引用: %+v", result)
	}
	var count int64
	if err := db.Model(&model.GitRepository{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("测试仓库连接不应写入数据库: count=%d err=%v", count, err)
	}

	if _, err := service.TestInput(context.Background(), "admin", Input{
		Name: "令牌仓库", Provider: model.GitProviderGitHub, CloneURL: "https://github.example.com/team/api.git", AuthType: model.GitAuthToken,
	}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("缺少令牌的仓库测试未被拒绝: %v", err)
	}
}

func newRepositoryTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开仓库测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移仓库测试数据库失败: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化仓库测试密钥失败: %v", err)
	}
	credentialService := credential.NewService(db, secretManager)
	return NewService(
		db, secretManager, credentialService, NewGitClient(config.Git{Timeout: time.Second}), 4,
		WithWebhookGate(staticWebhookGate{enabled: true}),
	), db
}

type staticWebhookGate struct {
	enabled bool
	err     error
}

func (s staticWebhookGate) ExternalGitWebhookEnabled(context.Context) (bool, error) {
	return s.enabled, s.err
}

type staticRefLister struct {
	result RefResult
	err    error
}

func (s staticRefLister) ListRefs(context.Context, model.GitRepository, string) (RefResult, error) {
	return s.result, s.err
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
