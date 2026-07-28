package repository

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/model"
	"zrt/internal/task"
)

type WebhookResult struct {
	Delivery  *model.GitWebhookDelivery
	Duplicate bool
}

type WebhookTaskPayload struct {
	DeliveryID   string `json:"delivery_id"`
	RepositoryID string `json:"repository_id"`
	EventType    string `json:"event_type"`
	Ref          string `json:"ref"`
	CommitSHA    string `json:"commit_sha"`
	Message      string `json:"message,omitempty"`
}

type webhookPayload struct {
	Ref         string `json:"ref"`
	After       string `json:"after"`
	CheckoutSHA string `json:"checkout_sha"`
	Action      string `json:"action"`
	ObjectKind  string `json:"object_kind"`
	HeadCommit  *struct {
		Message string `json:"message"`
	} `json:"head_commit"`
	Commits []struct {
		Message string `json:"message"`
	} `json:"commits"`
	PullRequest *struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	ObjectAttributes *struct {
		Action       string `json:"action"`
		TargetBranch string `json:"target_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
}

func (s *Service) HandleWebhook(
	ctx context.Context,
	repositoryID string,
	headers http.Header,
	body []byte,
) (WebhookResult, error) {
	if s.webhookGate == nil {
		return WebhookResult{}, ErrExternalWebhookDisabled
	}
	enabled, err := s.webhookGate.ExternalGitWebhookEnabled(ctx)
	if err != nil {
		return WebhookResult{}, fmt.Errorf("%w: %v", ErrWebhookUnavailable, err)
	}
	if !enabled {
		return WebhookResult{}, ErrExternalWebhookDisabled
	}
	repository, err := s.Find(ctx, repositoryID)
	if err != nil {
		return WebhookResult{}, err
	}
	if !repository.IsActive || !repository.WebhookEnabled || repository.WebhookSecretCiphertext == "" {
		return WebhookResult{}, ErrWebhookDisabled
	}
	webhookSecret, err := s.secrets.Decrypt(repository.WebhookSecretCiphertext, webhookAAD(repository.ID))
	if err != nil {
		return WebhookResult{}, fmt.Errorf("解密 Webhook 密钥失败: %w", err)
	}
	if err := verifyWebhook(*repository, headers, body, webhookSecret); err != nil {
		return WebhookResult{}, err
	}

	eventName := providerEventName(repository.Provider, headers)
	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return WebhookResult{}, ErrUnsupportedEvent
	}
	eventType := ""
	commitSHA := ""
	if supportedPushEvent(repository.Provider, eventName) {
		switch {
		case strings.HasPrefix(payload.Ref, "refs/heads/"):
			eventType = "branch_push"
		case strings.HasPrefix(payload.Ref, "refs/tags/"):
			eventType = "tag_push"
		default:
			return WebhookResult{}, ErrUnsupportedEvent
		}
		commitSHA = payload.After
		if commitSHA == "" {
			commitSHA = payload.CheckoutSHA
		}
	} else if supportedPullRequestEvent(repository.Provider, eventName) {
		eventType = "pull_request"
		if payload.PullRequest != nil {
			payload.Ref = "refs/heads/" + strings.TrimSpace(payload.PullRequest.Base.Ref)
			commitSHA = payload.PullRequest.Head.SHA
		} else if payload.ObjectAttributes != nil {
			payload.Ref = "refs/heads/" + strings.TrimSpace(payload.ObjectAttributes.TargetBranch)
			commitSHA = payload.ObjectAttributes.LastCommit.ID
		}
		if payload.Ref == "refs/heads/" || commitSHA == "" {
			return WebhookResult{}, ErrUnsupportedEvent
		}
	} else {
		return WebhookResult{}, ErrUnsupportedEvent
	}
	if allZeroSHA(commitSHA) {
		commitSHA = ""
	}
	payloadHash := sha256.Sum256(body)
	deliveryID := providerDeliveryID(repository.Provider, headers)
	if deliveryID == "" {
		deliveryID = hex.EncodeToString(payloadHash[:])
	}
	if len(deliveryID) > 128 {
		digest := sha256.Sum256([]byte(deliveryID))
		deliveryID = hex.EncodeToString(digest[:])
	}

	var existing model.GitWebhookDelivery
	err = s.db.WithContext(ctx).Where("repository_id = ? AND delivery_id = ?", repository.ID, deliveryID).First(&existing).Error
	if err == nil {
		return WebhookResult{Delivery: &existing, Duplicate: true}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return WebhookResult{}, fmt.Errorf("查询 Webhook 投递记录失败: %w", err)
	}

	now := time.Now().UTC()
	delivery := &model.GitWebhookDelivery{
		ID: uuid.NewString(), RepositoryID: repository.ID, DeliveryID: deliveryID,
		EventType: eventType, Ref: truncateString(payload.Ref, 512), CommitSHA: truncateString(commitSHA, 64),
		Message:     truncateString(webhookCommitMessage(payload), 255),
		PayloadHash: hex.EncodeToString(payloadHash[:]), Status: model.WebhookReceived, ReceivedAt: now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(delivery).Error; err != nil {
			return err
		}
		job, err := task.NewService(tx, s.defaultMaxAttempts).Create(ctx, task.CreateInput{
			Kind: "repository.webhook", Subject: "zrt.task.repository.webhook",
			Payload: WebhookTaskPayload{
				DeliveryID: delivery.ID, RepositoryID: repository.ID,
				EventType: eventType, Ref: delivery.Ref, CommitSHA: delivery.CommitSHA, Message: delivery.Message,
			},
			IdempotencyKey: webhookIdempotencyKey(repository.ID, deliveryID), Idempotent: true,
		})
		if err != nil {
			return err
		}
		delivery.JobID = job.ID
		return tx.Model(delivery).Update("job_id", job.ID).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			if findErr := s.db.WithContext(ctx).Where("repository_id = ? AND delivery_id = ?", repository.ID, deliveryID).First(&existing).Error; findErr == nil {
				return WebhookResult{Delivery: &existing, Duplicate: true}, nil
			}
		}
		return WebhookResult{}, fmt.Errorf("保存 Webhook 投递任务失败: %w", err)
	}
	return WebhookResult{Delivery: delivery}, nil
}

func (s *Service) ProcessWebhookTask(ctx context.Context, payload json.RawMessage) error {
	var input WebhookTaskPayload
	if err := json.Unmarshal(payload, &input); err != nil || input.DeliveryID == "" || input.RepositoryID == "" {
		return ErrInvalidTaskPayload
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.GitWebhookDelivery{}).
		Where("id = ? AND repository_id = ? AND status = ?", input.DeliveryID, input.RepositoryID, model.WebhookReceived).
		Updates(map[string]any{"status": model.WebhookProcessed, "processed_at": now})
	if result.Error != nil {
		return fmt.Errorf("更新 Webhook 处理状态失败: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var delivery model.GitWebhookDelivery
	if err := s.db.WithContext(ctx).First(&delivery, "id = ? AND repository_id = ?", input.DeliveryID, input.RepositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInvalidTaskPayload
		}
		return fmt.Errorf("查询 Webhook 处理状态失败: %w", err)
	}
	if delivery.Status == model.WebhookProcessed {
		return nil
	}
	return ErrInvalidTaskPayload
}

func verifyWebhook(repository model.GitRepository, headers http.Header, body []byte, secret string) error {
	switch repository.Provider {
	case model.GitProviderGitHub:
		return verifyHMACHeader(headers.Get("X-Hub-Signature-256"), "sha256=", body, secret)
	case model.GitProviderGitea:
		return verifyHMACHeader(headers.Get("X-Gitea-Signature"), "", body, secret)
	case model.GitProviderGitLab:
		return verifyToken(headers.Get("X-Gitlab-Token"), secret)
	case model.GitProviderGitee:
		return verifyToken(headers.Get("X-Gitee-Token"), secret)
	case model.GitProviderGeneric:
		return verifyHMACHeader(headers.Get("X-ZRT-Signature-256"), "sha256=", body, secret)
	default:
		return ErrInvalidSignature
	}
}

func verifyHMACHeader(value, prefix string, body []byte, secret string) error {
	if prefix != "" && !strings.HasPrefix(value, prefix) {
		return ErrInvalidSignature
	}
	value = strings.TrimPrefix(value, prefix)
	provided, err := hex.DecodeString(value)
	if err != nil {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

func verifyToken(value, secret string) error {
	if !hmac.Equal([]byte(value), []byte(secret)) {
		return ErrInvalidSignature
	}
	return nil
}

func providerEventName(provider model.GitProvider, headers http.Header) string {
	switch provider {
	case model.GitProviderGitHub:
		return headers.Get("X-GitHub-Event")
	case model.GitProviderGitLab:
		return headers.Get("X-Gitlab-Event")
	case model.GitProviderGitea:
		return headers.Get("X-Gitea-Event")
	case model.GitProviderGitee:
		return headers.Get("X-Gitee-Event")
	default:
		return headers.Get("X-ZRT-Event")
	}
}

func supportedPushEvent(provider model.GitProvider, event string) bool {
	event = strings.ToLower(strings.TrimSpace(event))
	switch provider {
	case model.GitProviderGitHub, model.GitProviderGitea, model.GitProviderGeneric:
		return event == "push"
	case model.GitProviderGitLab:
		return event == "push hook" || event == "tag push hook"
	case model.GitProviderGitee:
		return event == "push hook" || event == "push_hooks" || event == "push"
	default:
		return false
	}
}

func supportedPullRequestEvent(provider model.GitProvider, event string) bool {
	event = strings.ToLower(strings.TrimSpace(event))
	switch provider {
	case model.GitProviderGitHub, model.GitProviderGitea, model.GitProviderGeneric:
		return event == "pull_request"
	case model.GitProviderGitLab:
		return event == "merge request hook"
	case model.GitProviderGitee:
		return event == "merge request hook" || event == "merge_request_hooks" || event == "pull_request"
	default:
		return false
	}
}

func providerDeliveryID(provider model.GitProvider, headers http.Header) string {
	switch provider {
	case model.GitProviderGitHub:
		return headers.Get("X-GitHub-Delivery")
	case model.GitProviderGitLab:
		return headers.Get("X-Gitlab-Event-UUID")
	case model.GitProviderGitea:
		return headers.Get("X-Gitea-Delivery")
	case model.GitProviderGitee:
		return headers.Get("X-Gitee-Delivery")
	default:
		return headers.Get("X-ZRT-Delivery")
	}
}

func webhookCommitMessage(payload webhookPayload) string {
	if payload.HeadCommit != nil {
		return strings.TrimSpace(payload.HeadCommit.Message)
	}
	if len(payload.Commits) > 0 {
		return strings.TrimSpace(payload.Commits[0].Message)
	}
	return ""
}

func webhookIdempotencyKey(repositoryID, deliveryID string) string {
	digest := sha256.Sum256([]byte(repositoryID + "\x00" + deliveryID))
	return "webhook:" + hex.EncodeToString(digest[:])
}

func allZeroSHA(value string) bool {
	return value != "" && strings.Trim(value, "0") == ""
}
