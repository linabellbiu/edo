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

	"edo/internal/model"
	"edo/internal/task"
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
	SourceBranch string `json:"source_branch,omitempty"`
	TargetBranch string `json:"target_branch,omitempty"`
	Action       string `json:"action,omitempty"`
	Message      string `json:"message,omitempty"`
}

type webhookPayload struct {
	Ref         string `json:"ref"`
	After       string `json:"after"`
	CheckoutSHA string `json:"checkout_sha"`
	Action      string `json:"action"`
	Number      int64  `json:"number"`
	IID         int64  `json:"iid"`
	ObjectKind  string `json:"object_kind"`
	HeadCommit  *struct {
		Message string `json:"message"`
	} `json:"head_commit"`
	Commits []struct {
		Message string `json:"message"`
	} `json:"commits"`
	PullRequest *struct {
		Number         int64  `json:"number"`
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		Head           struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	ObjectAttributes *struct {
		Action         string `json:"action"`
		IID            int64  `json:"iid"`
		SourceBranch   string `json:"source_branch"`
		TargetBranch   string `json:"target_branch"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		LastCommit     struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
}

type normalizedWebhookEvent struct {
	EventType    string
	Ref          string
	CommitSHA    string
	SourceBranch string
	TargetBranch string
	Action       string
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
	event, err := normalizeWebhookEvent(repository.Provider, eventName, payload)
	if err != nil {
		return WebhookResult{}, ErrUnsupportedEvent
	}
	if allZeroSHA(event.CommitSHA) {
		event.CommitSHA = ""
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
		EventType: event.EventType, Ref: truncateString(event.Ref, 512), CommitSHA: truncateString(event.CommitSHA, 64),
		Message:     truncateString(webhookCommitMessage(payload), 255),
		PayloadHash: hex.EncodeToString(payloadHash[:]), Status: model.WebhookReceived, ReceivedAt: now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(delivery).Error; err != nil {
			return err
		}
		job, err := task.NewService(tx, s.defaultMaxAttempts).Create(ctx, task.CreateInput{
			DepartmentID: repository.DepartmentID,
			Kind:         "repository.webhook", Subject: "edo.task.repository.webhook",
			Payload: WebhookTaskPayload{
				DeliveryID: delivery.ID, RepositoryID: repository.ID,
				EventType: event.EventType, Ref: delivery.Ref, CommitSHA: delivery.CommitSHA,
				SourceBranch: truncateString(event.SourceBranch, 255), TargetBranch: truncateString(event.TargetBranch, 255),
				Action: truncateString(event.Action, 32), Message: delivery.Message,
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

func normalizeWebhookEvent(provider model.GitProvider, eventName string, payload webhookPayload) (normalizedWebhookEvent, error) {
	if supportedPushEvent(provider, eventName) {
		eventType := ""
		switch {
		case strings.HasPrefix(payload.Ref, "refs/heads/"):
			eventType = "branch_push"
		case strings.HasPrefix(payload.Ref, "refs/tags/"):
			eventType = "tag_push"
		default:
			return normalizedWebhookEvent{}, ErrUnsupportedEvent
		}
		commitSHA := strings.TrimSpace(payload.After)
		if commitSHA == "" {
			commitSHA = strings.TrimSpace(payload.CheckoutSHA)
		}
		return normalizedWebhookEvent{EventType: eventType, Ref: strings.TrimSpace(payload.Ref), CommitSHA: commitSHA}, nil
	}
	if !supportedPullRequestEvent(provider, eventName) {
		return normalizedWebhookEvent{}, ErrUnsupportedEvent
	}

	if provider == model.GitProviderGitLab {
		if payload.ObjectAttributes == nil {
			return normalizedWebhookEvent{}, ErrUnsupportedEvent
		}
		ref, ok := pullRequestHeadRef(provider, payload.ObjectAttributes.IID)
		if !ok {
			return normalizedWebhookEvent{}, ErrUnsupportedEvent
		}
		action := normalizeWebhookPullRequestAction(payload.ObjectAttributes.Action, false)
		commitSHA := strings.TrimSpace(payload.ObjectAttributes.LastCommit.ID)
		if action == "merged" && strings.TrimSpace(payload.ObjectAttributes.MergeCommitSHA) != "" {
			commitSHA = strings.TrimSpace(payload.ObjectAttributes.MergeCommitSHA)
		}
		event := normalizedWebhookEvent{
			EventType: "pull_request", Ref: ref,
			CommitSHA:    commitSHA,
			SourceBranch: strings.TrimSpace(payload.ObjectAttributes.SourceBranch),
			TargetBranch: strings.TrimSpace(payload.ObjectAttributes.TargetBranch),
			Action:       action,
		}
		if event.CommitSHA == "" || event.SourceBranch == "" || event.TargetBranch == "" {
			return normalizedWebhookEvent{}, ErrUnsupportedEvent
		}
		return event, nil
	}

	if payload.PullRequest == nil {
		return normalizedWebhookEvent{}, ErrUnsupportedEvent
	}
	action := normalizeWebhookPullRequestAction(payload.Action, payload.PullRequest.Merged)
	commitSHA := strings.TrimSpace(payload.PullRequest.Head.SHA)
	if action == "merged" && strings.TrimSpace(payload.PullRequest.MergeCommitSHA) != "" {
		commitSHA = strings.TrimSpace(payload.PullRequest.MergeCommitSHA)
	}
	sourceBranch := strings.TrimSpace(payload.PullRequest.Head.Ref)
	targetBranch := strings.TrimSpace(payload.PullRequest.Base.Ref)
	if provider == model.GitProviderGeneric {
		ref := strings.TrimSpace(payload.Ref)
		if !isPullRequestRef(ref) || commitSHA == "" || sourceBranch == "" || targetBranch == "" {
			return normalizedWebhookEvent{}, ErrUnsupportedEvent
		}
		return normalizedWebhookEvent{
			EventType: "pull_request", Ref: ref, CommitSHA: commitSHA,
			SourceBranch: sourceBranch, TargetBranch: targetBranch,
			Action: action,
		}, nil
	}
	number := payload.Number
	if number == 0 {
		number = payload.PullRequest.Number
	}
	if number == 0 {
		number = payload.IID
	}
	ref, ok := pullRequestHeadRef(provider, number)
	if !ok || commitSHA == "" || sourceBranch == "" || targetBranch == "" {
		return normalizedWebhookEvent{}, ErrUnsupportedEvent
	}
	return normalizedWebhookEvent{
		EventType: "pull_request", Ref: ref, CommitSHA: commitSHA,
		SourceBranch: sourceBranch, TargetBranch: targetBranch,
		Action: action,
	}, nil
}

func normalizeWebhookPullRequestAction(action string, merged bool) string {
	if merged {
		return "merged"
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "open", "opened", "reopen", "reopened":
		return "opened"
	case "update", "updated", "synchronize", "synchronized":
		return "updated"
	case "merge", "merged":
		return "merged"
	default:
		return ""
	}
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
		return verifyHMACHeader(headers.Get("X-EDO-Signature-256"), "sha256=", body, secret)
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
		return headers.Get("X-EDO-Event")
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
		return headers.Get("X-EDO-Delivery")
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
