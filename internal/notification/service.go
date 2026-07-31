package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/task"
)

var (
	ErrInvalidChannel       = errors.New("通知渠道配置无效")
	ErrChannelExists        = errors.New("通知渠道名称已存在")
	ErrChannelNotFound      = errors.New("通知渠道不存在")
	ErrInvalidNotification  = errors.New("通知内容无效")
	ErrNotificationNotFound = errors.New("通知记录不存在")
	ErrInvalidTaskPayload   = errors.New("通知任务参数无效")
)

var channelNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{1,127}$`)

type ChannelInput struct {
	Name      string
	Type      model.NotificationChannelType
	Endpoint  *string
	Token     *string
	AllowHTTP bool
}

type ChannelView struct {
	ID        string                        `json:"id"`
	Name      string                        `json:"name"`
	Type      model.NotificationChannelType `json:"type"`
	HasToken  bool                          `json:"has_token"`
	IsActive  bool                          `json:"is_active"`
	CreatedBy string                        `json:"created_by"`
	CreatedAt time.Time                     `json:"created_at"`
	UpdatedAt time.Time                     `json:"updated_at"`
}

type EnqueueInput struct {
	ChannelID string
	Title     string
	Message   string
	Severity  model.NotificationSeverity
	Source    string
	SourceID  string
	DedupeKey string
}

type TaskPayload struct {
	NotificationID string `json:"notification_id"`
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type DispatchError struct {
	retryable bool
	err       error
}

func (e *DispatchError) Error() string { return e.err.Error() }
func (e *DispatchError) Unwrap() error { return e.err }
func IsRetryable(err error) bool {
	var dispatchError *DispatchError
	return errors.As(err, &dispatchError) && dispatchError.retryable
}

func IsDispatchError(err error) bool {
	var dispatchError *DispatchError
	return errors.As(err, &dispatchError)
}

type Service struct {
	db          *gorm.DB
	secrets     *secret.Manager
	httpClient  HTTPDoer
	maxAttempts int
}

func NewService(db *gorm.DB, secrets *secret.Manager, httpClient HTTPDoer, maxAttempts int) *Service {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("通知 Webhook 不允许重定向")
			},
		}
	}
	return &Service{
		db: db, secrets: secrets,
		httpClient: httpClient, maxAttempts: maxAttempts,
	}
}

func (s *Service) ListChannels(ctx context.Context) ([]ChannelView, error) {
	var channels []model.NotificationChannel
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("查询通知渠道失败: %w", err)
	}
	result := make([]ChannelView, 0, len(channels))
	for index := range channels {
		result = append(result, channelView(&channels[index]))
	}
	return result, nil
}

func (s *Service) CreateChannel(ctx context.Context, actorID string, input ChannelInput) (*ChannelView, error) {
	id := uuid.NewString()
	name, channelType, endpointCiphertext, tokenCiphertext, err := s.normalizeChannel(id, nil, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	channel := &model.NotificationChannel{
		ID: id, Name: name, Type: channelType, EndpointCiphertext: endpointCiphertext,
		TokenCiphertext: tokenCiphertext, IsActive: true, CreatedBy: actorID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(channel).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrChannelExists
		}
		return nil, fmt.Errorf("创建通知渠道失败: %w", err)
	}
	view := channelView(channel)
	return &view, nil
}

func (s *Service) UpdateChannel(ctx context.Context, id string, input ChannelInput) (*ChannelView, error) {
	var channel model.NotificationChannel
	if err := s.db.WithContext(ctx).First(&channel, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrChannelNotFound
		}
		return nil, fmt.Errorf("查询通知渠道失败: %w", err)
	}
	name, channelType, endpointCiphertext, tokenCiphertext, err := s.normalizeChannel(id, &channel, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&channel).Updates(map[string]any{
		"name": name, "type": channelType, "endpoint_ciphertext": endpointCiphertext,
		"token_ciphertext": tokenCiphertext, "updated_at": now,
	}).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrChannelExists
		}
		return nil, fmt.Errorf("更新通知渠道失败: %w", err)
	}
	channel.Name, channel.Type = name, channelType
	channel.EndpointCiphertext, channel.TokenCiphertext, channel.UpdatedAt = endpointCiphertext, tokenCiphertext, now
	view := channelView(&channel)
	return &view, nil
}

func (s *Service) SetChannelActive(ctx context.Context, id string, active bool) error {
	result := s.db.WithContext(ctx).Model(&model.NotificationChannel{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改通知渠道状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrChannelNotFound
	}
	return nil
}

func (s *Service) List(ctx context.Context, limit int) ([]model.Notification, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var notifications []model.Notification
	if err := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit).Find(&notifications).Error; err != nil {
		return nil, fmt.Errorf("查询通知记录失败: %w", err)
	}
	return notifications, nil
}

func (s *Service) Enqueue(ctx context.Context, input EnqueueInput) (*model.Notification, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Message = strings.TrimSpace(input.Message)
	input.Source = strings.TrimSpace(input.Source)
	input.SourceID = strings.TrimSpace(input.SourceID)
	if input.ChannelID == "" || input.Title == "" || input.Message == "" ||
		utf8.RuneCountInString(input.Title) > 255 || utf8.RuneCountInString(input.Message) > 8192 ||
		!validSeverity(input.Severity) || utf8.RuneCountInString(input.Source) > 64 ||
		utf8.RuneCountInString(input.SourceID) > 128 || utf8.RuneCountInString(input.DedupeKey) > 191 {
		return nil, ErrInvalidNotification
	}
	var channel model.NotificationChannel
	if err := s.db.WithContext(ctx).First(&channel, "id = ? AND is_active = ?", input.ChannelID, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrChannelNotFound
		}
		return nil, fmt.Errorf("查询通知渠道失败: %w", err)
	}
	if input.DedupeKey != "" {
		var existing model.Notification
		if err := s.db.WithContext(ctx).Where("dedupe_key = ?", input.DedupeKey).First(&existing).Error; err == nil {
			return &existing, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("检查通知去重键失败: %w", err)
		}
	}
	now := time.Now().UTC()
	notification := &model.Notification{
		ID: uuid.NewString(), ChannelID: input.ChannelID, Title: input.Title, Message: input.Message,
		Severity: input.Severity, Source: input.Source, SourceID: input.SourceID,
		Status: model.NotificationQueued, CreatedAt: now, UpdatedAt: now,
	}
	if input.DedupeKey != "" {
		notification.DedupeKey = &input.DedupeKey
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(notification).Error; err != nil {
			return err
		}
		job, err := task.NewService(tx, s.maxAttempts).Create(ctx, task.CreateInput{
			Kind: "notification.dispatch", Subject: "edo.task.notification.dispatch",
			Payload:        TaskPayload{NotificationID: notification.ID},
			IdempotencyKey: "notification:" + notification.ID, Idempotent: true,
		})
		if err != nil {
			return err
		}
		notification.JobID = job.ID
		return tx.Model(notification).Update("job_id", job.ID).Error
	})
	if err != nil {
		if input.DedupeKey != "" && errors.Is(err, gorm.ErrDuplicatedKey) {
			var existing model.Notification
			if findErr := s.db.WithContext(ctx).Where("dedupe_key = ?", input.DedupeKey).First(&existing).Error; findErr == nil {
				return &existing, nil
			}
		}
		return nil, fmt.Errorf("创建通知任务失败: %w", err)
	}
	return notification, nil
}

func (s *Service) Dispatch(ctx context.Context, notificationID, jobID string) error {
	var item model.Notification
	if err := s.db.WithContext(ctx).First(&item, "id = ?", notificationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotificationNotFound
		}
		return fmt.Errorf("查询通知记录失败: %w", err)
	}
	if item.Status == model.NotificationSucceeded {
		return nil
	}
	var channel model.NotificationChannel
	if err := s.db.WithContext(ctx).First(&channel, "id = ? AND is_active = ?", item.ChannelID, true).Error; err != nil {
		return s.failDispatch(ctx, &item, jobID, false, "通知渠道不可用", err)
	}
	endpoint, err := s.secrets.Decrypt(channel.EndpointCiphertext, channelEndpointAAD(channel.ID))
	if err != nil {
		return s.failDispatch(ctx, &item, jobID, false, "通知渠道配置不可用", err)
	}
	token := ""
	if channel.TokenCiphertext != "" {
		token, err = s.secrets.Decrypt(channel.TokenCiphertext, channelTokenAAD(channel.ID))
		if err != nil {
			return s.failDispatch(ctx, &item, jobID, false, "通知渠道配置不可用", err)
		}
	}
	payload, err := json.Marshal(map[string]any{
		"id": item.ID, "title": item.Title, "message": item.Message,
		"severity": item.Severity, "source": item.Source, "source_id": item.SourceID,
		"created_at": item.CreatedAt,
	})
	if err != nil {
		return s.failDispatch(ctx, &item, jobID, false, "通知内容格式无效", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return s.failDispatch(ctx, &item, jobID, false, "通知渠道配置无效", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "edo-notifier")
	request.Header.Set("Idempotency-Key", item.ID)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return s.failDispatch(ctx, &item, jobID, true, "通知发送暂时失败", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
		return s.failDispatch(ctx, &item, jobID, retryable, "通知渠道拒绝了本次发送", fmt.Errorf("Webhook HTTP 状态码 %d", response.StatusCode))
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&model.Notification{}).Where("id = ?", item.ID).
		Updates(map[string]any{
			"status": model.NotificationSucceeded, "sent_at": now, "updated_at": now,
			"error_message": "",
		}).Error; err != nil {
		return fmt.Errorf("记录通知发送成功状态失败: %w", err)
	}
	return nil
}

func (s *Service) failDispatch(
	ctx context.Context,
	item *model.Notification,
	jobID string,
	retryable bool,
	publicMessage string,
	cause error,
) error {
	attempt, maxAttempts := item.Attempts+1, s.maxAttempts
	var job model.Job
	if err := s.db.WithContext(ctx).Select("attempt", "max_attempts").First(&job, "id = ?", jobID).Error; err == nil {
		attempt, maxAttempts = job.Attempt, job.MaxAttempts
	}
	status := model.NotificationQueued
	if !retryable || attempt >= maxAttempts {
		status = model.NotificationFailed
	}
	now := time.Now().UTC()
	updateContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.db.WithContext(updateContext).Model(&model.Notification{}).Where("id = ?", item.ID).
		Updates(map[string]any{
			"status": status, "attempts": attempt, "error_message": publicMessage, "updated_at": now,
		}).Error; err != nil {
		return &DispatchError{retryable: false, err: fmt.Errorf("记录通知失败状态失败: %v；发送错误: %w", err, cause)}
	}
	return &DispatchError{retryable: retryable, err: cause}
}

func (s *Service) normalizeChannel(
	id string,
	existing *model.NotificationChannel,
	input ChannelInput,
) (string, model.NotificationChannelType, string, string, error) {
	name := strings.TrimSpace(input.Name)
	if !channelNamePattern.MatchString(name) || input.Type != model.NotificationChannelWebhook {
		return "", "", "", "", ErrInvalidChannel
	}
	endpointCiphertext := ""
	tokenCiphertext := ""
	if existing != nil {
		endpointCiphertext = existing.EndpointCiphertext
		tokenCiphertext = existing.TokenCiphertext
	}
	if input.Endpoint != nil {
		endpoint := strings.TrimSpace(*input.Endpoint)
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
			(parsed.Scheme != "https" && !(input.AllowHTTP && parsed.Scheme == "http")) {
			return "", "", "", "", ErrInvalidChannel
		}
		endpointCiphertext, err = s.secrets.Encrypt(endpoint, channelEndpointAAD(id))
		if err != nil {
			return "", "", "", "", err
		}
	}
	if endpointCiphertext == "" {
		return "", "", "", "", ErrInvalidChannel
	}
	if input.Token != nil {
		token := strings.TrimSpace(*input.Token)
		if utf8.RuneCountInString(token) > 4096 {
			return "", "", "", "", ErrInvalidChannel
		}
		if token == "" {
			tokenCiphertext = ""
		} else {
			var err error
			tokenCiphertext, err = s.secrets.Encrypt(token, channelTokenAAD(id))
			if err != nil {
				return "", "", "", "", err
			}
		}
	}
	return name, input.Type, endpointCiphertext, tokenCiphertext, nil
}

func channelView(channel *model.NotificationChannel) ChannelView {
	return ChannelView{
		ID: channel.ID, Name: channel.Name, Type: channel.Type,
		HasToken: channel.TokenCiphertext != "", IsActive: channel.IsActive,
		CreatedBy: channel.CreatedBy, CreatedAt: channel.CreatedAt, UpdatedAt: channel.UpdatedAt,
	}
}

func validSeverity(value model.NotificationSeverity) bool {
	return value == model.NotificationInfo || value == model.NotificationWarning || value == model.NotificationCritical
}

func channelEndpointAAD(id string) []byte { return []byte("notification_channel:" + id + ":endpoint") }
func channelTokenAAD(id string) []byte    { return []byte("notification_channel:" + id + ":token") }
