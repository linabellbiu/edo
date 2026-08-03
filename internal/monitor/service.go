package monitor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/notification"
	"edo/internal/secret"
	"edo/internal/task"
)

var (
	ErrInvalidRule        = errors.New("监控规则配置无效")
	ErrRuleExists         = errors.New("监控规则名称已存在")
	ErrRuleNotFound       = errors.New("监控规则不存在")
	ErrInvalidTaskPayload = errors.New("监控任务参数无效")
)

var ruleNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{1,127}$`)

type Input struct {
	Name                  string
	Endpoint              *string
	Method                string
	ExpectedStatusMin     int
	ExpectedStatusMax     int
	TimeoutSeconds        int
	IntervalSeconds       int
	FailureThreshold      int
	RecoveryThreshold     int
	NotificationChannelID string
	AllowHTTP             bool
}

type View struct {
	ID                    string              `json:"id"`
	Name                  string              `json:"name"`
	Endpoint              string              `json:"endpoint"`
	Method                string              `json:"method"`
	ExpectedStatusMin     int                 `json:"expected_status_min"`
	ExpectedStatusMax     int                 `json:"expected_status_max"`
	TimeoutSeconds        int                 `json:"timeout_seconds"`
	IntervalSeconds       int                 `json:"interval_seconds"`
	FailureThreshold      int                 `json:"failure_threshold"`
	RecoveryThreshold     int                 `json:"recovery_threshold"`
	NotificationChannelID string              `json:"notification_channel_id"`
	Status                model.MonitorStatus `json:"status"`
	ConsecutiveFailures   int                 `json:"consecutive_failures"`
	ConsecutiveSuccesses  int                 `json:"consecutive_successes"`
	IsActive              bool                `json:"is_active"`
	NextRunAt             time.Time           `json:"next_run_at"`
	LastRunAt             *time.Time          `json:"last_run_at,omitempty"`
	LastChangedAt         *time.Time          `json:"last_changed_at,omitempty"`
	LastJobID             string              `json:"last_job_id"`
	CreatedBy             string              `json:"created_by"`
	CreatedAt             time.Time           `json:"created_at"`
	UpdatedAt             time.Time           `json:"updated_at"`
}

type TaskPayload struct {
	RuleID      string    `json:"rule_id"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Service struct {
	db          *gorm.DB
	secrets     *secret.Manager
	notifier    *notification.Service
	httpClient  HTTPDoer
	maxAttempts int
	logger      *slog.Logger
}

func NewService(
	db *gorm.DB,
	secrets *secret.Manager,
	notifier *notification.Service,
	httpClient HTTPDoer,
	maxAttempts int,
	logger *slog.Logger,
) *Service {
	if httpClient == nil {
		httpClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("监控探针不允许重定向")
		}}
	}
	return &Service{
		db: db, secrets: secrets, notifier: notifier, httpClient: httpClient,
		maxAttempts: maxAttempts, logger: logger,
	}
}

func (s *Service) List(ctx context.Context) ([]View, error) {
	var rules []model.MonitorRule
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("查询监控规则失败: %w", err)
	}
	result := make([]View, 0, len(rules))
	for index := range rules {
		result = append(result, toView(&rules[index]))
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, actorID string, input Input) (*View, error) {
	id := uuid.NewString()
	normalized, endpointCiphertext, endpointDisplay, err := s.normalize(id, nil, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	rule := &model.MonitorRule{
		ID: id, Name: normalized.Name, EndpointCiphertext: endpointCiphertext, EndpointDisplay: endpointDisplay,
		Method: normalized.Method, ExpectedStatusMin: normalized.ExpectedStatusMin,
		ExpectedStatusMax: normalized.ExpectedStatusMax, TimeoutSeconds: normalized.TimeoutSeconds,
		IntervalSeconds: normalized.IntervalSeconds, FailureThreshold: normalized.FailureThreshold,
		RecoveryThreshold: normalized.RecoveryThreshold, NotificationChannelID: normalized.NotificationChannelID,
		Status: model.MonitorUnknown, IsActive: true, NextRunAt: now,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrRuleExists
		}
		return nil, fmt.Errorf("创建监控规则失败: %w", err)
	}
	view := toView(rule)
	return &view, nil
}

func (s *Service) Update(ctx context.Context, id string, input Input) (*View, error) {
	var rule model.MonitorRule
	if err := s.db.WithContext(ctx).First(&rule, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRuleNotFound
		}
		return nil, fmt.Errorf("查询监控规则失败: %w", err)
	}
	normalized, endpointCiphertext, endpointDisplay, err := s.normalize(id, &rule, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&rule).Updates(map[string]any{
		"name": normalized.Name, "endpoint_ciphertext": endpointCiphertext, "endpoint_display": endpointDisplay,
		"method": normalized.Method, "expected_status_min": normalized.ExpectedStatusMin,
		"expected_status_max": normalized.ExpectedStatusMax, "timeout_seconds": normalized.TimeoutSeconds,
		"interval_seconds": normalized.IntervalSeconds, "failure_threshold": normalized.FailureThreshold,
		"recovery_threshold": normalized.RecoveryThreshold, "notification_channel_id": normalized.NotificationChannelID,
		"next_run_at": now, "status": model.MonitorUnknown, "consecutive_failures": 0,
		"consecutive_successes": 0, "updated_at": now,
	}).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrRuleExists
		}
		return nil, fmt.Errorf("更新监控规则失败: %w", err)
	}
	rule.Name, rule.EndpointCiphertext, rule.EndpointDisplay = normalized.Name, endpointCiphertext, endpointDisplay
	rule.Method, rule.ExpectedStatusMin, rule.ExpectedStatusMax = normalized.Method, normalized.ExpectedStatusMin, normalized.ExpectedStatusMax
	rule.TimeoutSeconds, rule.IntervalSeconds = normalized.TimeoutSeconds, normalized.IntervalSeconds
	rule.FailureThreshold, rule.RecoveryThreshold = normalized.FailureThreshold, normalized.RecoveryThreshold
	rule.NotificationChannelID, rule.NextRunAt, rule.UpdatedAt = normalized.NotificationChannelID, now, now
	rule.Status, rule.ConsecutiveFailures, rule.ConsecutiveSuccesses = model.MonitorUnknown, 0, 0
	view := toView(&rule)
	return &view, nil
}

func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	updates := map[string]any{"is_active": active, "updated_at": time.Now().UTC()}
	if active {
		updates["next_run_at"] = time.Now().UTC()
	}
	result := s.db.WithContext(ctx).Model(&model.MonitorRule{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("修改监控规则状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrRuleNotFound
	}
	return nil
}

func (s *Service) ListChecks(ctx context.Context, ruleID string, limit int) ([]model.MonitorCheck, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var checks []model.MonitorCheck
	if err := s.db.WithContext(ctx).Where("rule_id = ?", ruleID).Order("checked_at DESC").Limit(limit).Find(&checks).Error; err != nil {
		return nil, fmt.Errorf("查询监控记录失败: %w", err)
	}
	return checks, nil
}

func (s *Service) EnqueueDue(ctx context.Context, now time.Time) error {
	var rules []model.MonitorRule
	if err := s.db.WithContext(ctx).Where("is_active = ? AND next_run_at <= ?", true, now.UTC()).
		Order("next_run_at ASC").Limit(100).Find(&rules).Error; err != nil {
		return fmt.Errorf("查询到期监控规则失败: %w", err)
	}
	for index := range rules {
		if err := s.enqueueOne(ctx, &rules[index], now.UTC()); err != nil {
			s.logger.Error("投递到期监控任务失败", "operation", "monitor_enqueue", "rule_id", rules[index].ID, "err", err)
		}
	}
	return nil
}

func (s *Service) Execute(ctx context.Context, payload TaskPayload, jobID string) error {
	if payload.RuleID == "" || payload.ScheduledAt.IsZero() || jobID == "" {
		return ErrInvalidTaskPayload
	}
	var existing model.MonitorCheck
	if err := s.db.WithContext(ctx).Where("job_id = ?", jobID).First(&existing).Error; err == nil {
		return s.deliverPendingAlert(ctx, &existing)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("检查监控任务幂等状态失败: %w", err)
	}
	var rule model.MonitorRule
	if err := s.db.WithContext(ctx).First(&rule, "id = ?", payload.RuleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRuleNotFound
		}
		return fmt.Errorf("查询监控规则失败: %w", err)
	}
	if !rule.IsActive {
		return nil
	}
	endpoint, err := s.secrets.Decrypt(rule.EndpointCiphertext, endpointAAD(rule.ID))
	if err != nil {
		return fmt.Errorf("解密监控目标失败: %w", err)
	}
	requestContext, cancel := context.WithTimeout(ctx, time.Duration(rule.TimeoutSeconds)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, rule.Method, endpoint, nil)
	if err != nil {
		return ErrInvalidRule
	}
	request.Header.Set("User-Agent", "edo-monitor")
	started := time.Now()
	response, requestErr := s.httpClient.Do(request)
	latency := time.Since(started).Milliseconds()
	statusCode := 0
	healthy := false
	publicError := ""
	if requestErr != nil {
		publicError = "无法连接监控目标"
		s.logger.Warn("监控探针请求失败", "operation", "monitor_probe", "rule_id", rule.ID, "err", requestErr)
	} else {
		statusCode = response.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		if closeErr := response.Body.Close(); closeErr != nil {
			s.logger.Warn("关闭监控探针响应失败", "operation", "monitor_probe_close", "rule_id", rule.ID, "err", closeErr)
		}
		healthy = statusCode >= rule.ExpectedStatusMin && statusCode <= rule.ExpectedStatusMax
		if !healthy {
			publicError = "监控目标返回了非预期状态"
		}
	}
	check, err := s.recordResult(ctx, &rule, jobID, healthy, statusCode, latency, publicError)
	if err != nil {
		return err
	}
	return s.deliverPendingAlert(ctx, check)
}

func (s *Service) Run(ctx context.Context, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = 15 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := s.EnqueueDue(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.logger.Error("监控任务扫描失败", "operation", "monitor_scan", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) enqueueOne(ctx context.Context, rule *model.MonitorRule, now time.Time) error {
	scheduledAt := rule.NextRunAt.UTC()
	nextRunAt := now.Add(time.Duration(rule.IntervalSeconds) * time.Second)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.MonitorRule{}).
			Where("id = ? AND is_active = ? AND next_run_at = ?", rule.ID, true, rule.NextRunAt).
			Updates(map[string]any{"next_run_at": nextRunAt, "last_run_at": scheduledAt, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		job, err := task.NewService(tx, s.maxAttempts).Create(ctx, task.CreateInput{
			DepartmentID: rule.DepartmentID,
			Kind:         "monitor.check", Subject: "edo.task.monitor.check", Idempotent: true,
			IdempotencyKey: "monitor:" + rule.ID + ":" + scheduledAt.Format(time.RFC3339),
			Payload:        TaskPayload{RuleID: rule.ID, ScheduledAt: scheduledAt},
		})
		if err != nil {
			return err
		}
		return tx.Model(&model.MonitorRule{}).Where("id = ?", rule.ID).Update("last_job_id", job.ID).Error
	})
}

func (s *Service) recordResult(
	ctx context.Context,
	rule *model.MonitorRule,
	jobID string,
	healthy bool,
	statusCode int,
	latency int64,
	publicError string,
) (*model.MonitorCheck, error) {
	now := time.Now().UTC()
	checkStatus := model.MonitorUnhealthy
	if healthy {
		checkStatus = model.MonitorHealthy
	}
	check := &model.MonitorCheck{
		ID: uuid.NewString(), RuleID: rule.ID, JobID: jobID, Status: checkStatus,
		StatusCode: statusCode, LatencyMS: latency, ErrorMessage: publicError, CheckedAt: now,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.MonitorRule
		if err := tx.First(&current, "id = ?", rule.ID).Error; err != nil {
			return err
		}
		updates := map[string]any{"updated_at": now}
		if healthy {
			current.ConsecutiveSuccesses++
			current.ConsecutiveFailures = 0
			updates["consecutive_successes"] = current.ConsecutiveSuccesses
			updates["consecutive_failures"] = 0
			if current.Status == model.MonitorUnhealthy && current.ConsecutiveSuccesses >= current.RecoveryThreshold {
				current.Status = model.MonitorHealthy
				check.AlertType = "recovery"
				updates["status"] = current.Status
				updates["last_changed_at"] = now
			} else if current.Status == model.MonitorUnknown && current.ConsecutiveSuccesses >= current.RecoveryThreshold {
				current.Status = model.MonitorHealthy
				updates["status"] = current.Status
				updates["last_changed_at"] = now
			}
		} else {
			current.ConsecutiveFailures++
			current.ConsecutiveSuccesses = 0
			updates["consecutive_failures"] = current.ConsecutiveFailures
			updates["consecutive_successes"] = 0
			if current.Status != model.MonitorUnhealthy && current.ConsecutiveFailures >= current.FailureThreshold {
				current.Status = model.MonitorUnhealthy
				check.AlertType = "failure"
				updates["status"] = current.Status
				updates["last_changed_at"] = now
			}
		}
		if current.NotificationChannelID == "" {
			check.AlertType = ""
		}
		if err := tx.Create(check).Error; err != nil {
			return err
		}
		return tx.Model(&model.MonitorRule{}).Where("id = ?", current.ID).Updates(updates).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			var existing model.MonitorCheck
			if findErr := s.db.WithContext(ctx).Where("job_id = ?", jobID).First(&existing).Error; findErr == nil {
				return &existing, nil
			}
		}
		return nil, fmt.Errorf("记录监控结果失败: %w", err)
	}
	return check, nil
}

func (s *Service) deliverPendingAlert(ctx context.Context, check *model.MonitorCheck) error {
	if check.AlertType == "" || check.NotificationID != "" {
		return nil
	}
	var rule model.MonitorRule
	if err := s.db.WithContext(ctx).First(&rule, "id = ?", check.RuleID).Error; err != nil {
		return fmt.Errorf("查询监控告警规则失败: %w", err)
	}
	title := "监控告警：" + rule.Name
	message := "监控目标连续探测失败，状态已变为异常。"
	severity := model.NotificationCritical
	if check.AlertType == "recovery" {
		title = "监控恢复：" + rule.Name
		message = "监控目标已连续探测成功，状态已恢复正常。"
		severity = model.NotificationInfo
	}
	item, err := s.notifier.Enqueue(ctx, notification.EnqueueInput{
		ChannelID: rule.NotificationChannelID, Title: title, Message: message,
		Severity: severity, Source: "monitor", SourceID: check.ID,
		DedupeKey: "monitor:" + check.ID + ":" + check.AlertType,
	})
	if err != nil {
		return fmt.Errorf("创建监控告警通知失败: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&model.MonitorCheck{}).Where("id = ? AND notification_id = ''", check.ID).
		Update("notification_id", item.ID).Error; err != nil {
		return fmt.Errorf("关联监控告警通知失败: %w", err)
	}
	return nil
}

func (s *Service) normalize(id string, existing *model.MonitorRule, input Input) (Input, string, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Method = strings.ToUpper(strings.TrimSpace(input.Method))
	input.NotificationChannelID = strings.TrimSpace(input.NotificationChannelID)
	if !ruleNamePattern.MatchString(input.Name) || (input.Method != http.MethodGet && input.Method != http.MethodHead) ||
		input.ExpectedStatusMin < 100 || input.ExpectedStatusMax > 599 || input.ExpectedStatusMin > input.ExpectedStatusMax ||
		input.TimeoutSeconds < 1 || input.TimeoutSeconds > 60 || input.IntervalSeconds < 30 || input.IntervalSeconds > 86400 ||
		input.FailureThreshold < 1 || input.FailureThreshold > 10 || input.RecoveryThreshold < 1 || input.RecoveryThreshold > 10 {
		return Input{}, "", "", ErrInvalidRule
	}
	if input.NotificationChannelID != "" {
		var count int64
		if err := s.db.Model(&model.NotificationChannel{}).Where("id = ? AND is_active = ?", input.NotificationChannelID, true).Count(&count).Error; err != nil || count != 1 {
			return Input{}, "", "", ErrInvalidRule
		}
	}
	endpointCiphertext, endpointDisplay := "", ""
	if existing != nil {
		endpointCiphertext, endpointDisplay = existing.EndpointCiphertext, existing.EndpointDisplay
	}
	if input.Endpoint != nil {
		endpoint := strings.TrimSpace(*input.Endpoint)
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
			(parsed.Scheme != "https" && !(input.AllowHTTP && parsed.Scheme == "http")) || len(endpoint) > 4096 {
			return Input{}, "", "", ErrInvalidRule
		}
		endpointCiphertext, err = s.secrets.Encrypt(endpoint, endpointAAD(id))
		if err != nil {
			return Input{}, "", "", err
		}
		parsed.RawQuery = ""
		endpointDisplay = parsed.String()
	}
	if endpointCiphertext == "" {
		return Input{}, "", "", ErrInvalidRule
	}
	return input, endpointCiphertext, endpointDisplay, nil
}

func toView(rule *model.MonitorRule) View {
	return View{
		ID: rule.ID, Name: rule.Name, Endpoint: rule.EndpointDisplay, Method: rule.Method,
		ExpectedStatusMin: rule.ExpectedStatusMin, ExpectedStatusMax: rule.ExpectedStatusMax,
		TimeoutSeconds: rule.TimeoutSeconds, IntervalSeconds: rule.IntervalSeconds,
		FailureThreshold: rule.FailureThreshold, RecoveryThreshold: rule.RecoveryThreshold,
		NotificationChannelID: rule.NotificationChannelID, Status: rule.Status,
		ConsecutiveFailures: rule.ConsecutiveFailures, ConsecutiveSuccesses: rule.ConsecutiveSuccesses,
		IsActive: rule.IsActive, NextRunAt: rule.NextRunAt, LastRunAt: rule.LastRunAt,
		LastChangedAt: rule.LastChangedAt, LastJobID: rule.LastJobID, CreatedBy: rule.CreatedBy,
		CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
	}
}

func endpointAAD(id string) []byte { return []byte("monitor_rule:" + id + ":endpoint") }
