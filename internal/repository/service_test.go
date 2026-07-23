package repository

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/config"
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
	return NewService(db, secretManager, NewGitClient(config.Git{Command: "git", Timeout: time.Second}), 4), db
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
