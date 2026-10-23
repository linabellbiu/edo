package assistant

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"edo/internal/audit"
	"edo/internal/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newID() string { return uuid.NewString() }

func defaultSettings() model.AssistantSettings {
	return model.AssistantSettings{ID: settingsID, BaseURL: "https://api.openai.com/v1", MaxSteps: 6, TimeoutSeconds: 90}
}

func (s *Service) settings(ctx context.Context) (model.AssistantSettings, error) {
	row := defaultSettings()
	err := s.deps.Database.WithContext(ctx).First(&row, "uuid = ?", settingsID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultSettings(), nil
	}
	if err != nil {
		return row, s.failure(ctx, "assistant_settings_read", err, ErrStorage)
	}
	return row, nil
}

func settingsView(row model.AssistantSettings) SettingsView {
	return SettingsView{Enabled: row.Enabled, BaseURL: row.BaseURL, Model: row.Model, HasAPIKey: row.APIKeyCiphertext != "", Version: row.Version, MaxSteps: row.MaxSteps, TimeoutSeconds: row.TimeoutSeconds}
}

func (s *Service) Settings(ctx context.Context) (SettingsView, error) {
	row, err := s.settings(ctx)
	return settingsView(row), err
}

func (s *Service) Status(ctx context.Context) (StatusView, error) {
	row, err := s.settings(ctx)
	if err != nil {
		return StatusView{}, err
	}
	view := StatusView{Enabled: row.Enabled, Configured: row.APIKeyCiphertext != "" && row.Model != "" && row.BaseURL != "" && s.deps.Secrets.Available()}
	if !row.Enabled {
		view.Reason = ErrDisabled.Error()
	} else if !view.Configured {
		view.Reason = ErrUnavailable.Error()
	}
	return view, nil
}

func normalizeSettings(input SettingsInput) (SettingsInput, error) {
	input.BaseURL, input.Model, input.APIKey = strings.TrimSpace(input.BaseURL), strings.TrimSpace(input.Model), strings.TrimSpace(input.APIKey)
	if input.BaseURL == "" {
		input.BaseURL = defaultSettings().BaseURL
	}
	u, err := url.Parse(input.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(input.BaseURL) > 1024 || len(input.Model) > 128 || strings.ContainsAny(input.Model, "\r\n") || len(input.APIKey) > 8192 || strings.ContainsAny(input.APIKey, "\r\n") || (input.ClearAPIKey && input.APIKey != "") {
		return input, ErrInvalidInput
	}
	input.BaseURL = strings.TrimRight(input.BaseURL, "/")
	if input.MaxSteps == 0 {
		input.MaxSteps = 6
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 90
	}
	if input.MaxSteps < 1 || input.MaxSteps > 10 || input.TimeoutSeconds < 10 || input.TimeoutSeconds > 180 || (input.Enabled && input.Model == "") {
		return input, ErrInvalidInput
	}
	return input, nil
}

func (s *Service) configuredInput(ctx context.Context, input SettingsInput) (model.AssistantSettings, string, error) {
	input, err := normalizeSettings(input)
	if err != nil {
		return model.AssistantSettings{}, "", s.failure(ctx, "assistant_settings_validate", err, err)
	}
	row, err := s.settings(ctx)
	if err != nil {
		return row, "", err
	}
	row.Enabled, row.BaseURL, row.Model, row.MaxSteps, row.TimeoutSeconds = input.Enabled, input.BaseURL, input.Model, input.MaxSteps, input.TimeoutSeconds
	key := input.APIKey
	if input.ClearAPIKey {
		row.APIKeyCiphertext = ""
	} else if key != "" {
		row.APIKeyCiphertext, err = s.deps.Secrets.Encrypt(key, []byte("assistant:global:api_key"))
		if err != nil {
			return row, "", s.failure(ctx, "assistant_key_encrypt", err, ErrUnavailable)
		}
	}
	if key == "" && row.APIKeyCiphertext != "" {
		key, err = s.deps.Secrets.Decrypt(row.APIKeyCiphertext, []byte("assistant:global:api_key"))
		if err != nil {
			return row, "", s.failure(ctx, "assistant_key_decrypt", err, ErrUnavailable)
		}
	}
	return row, key, nil
}

func (s *Service) SaveSettings(ctx context.Context, actorID string, input SettingsInput) (SettingsView, error) {
	var actor model.User
	if err := s.deps.Database.WithContext(ctx).First(&actor, "uuid = ? AND is_active = ?", actorID, true).Error; err != nil {
		return SettingsView{}, s.failure(ctx, "assistant_settings_actor", err, ErrForbidden)
	}
	if s.deps.Access == nil {
		return SettingsView{}, s.failure(ctx, "assistant_settings_permission", ErrForbidden, ErrForbidden)
	}
	allowed, err := s.deps.Access.HasPermission(ctx, &actor, "config.update")
	if err != nil || !allowed {
		return SettingsView{}, s.failure(ctx, "assistant_settings_permission", err, ErrForbidden)
	}
	row, _, err := s.configuredInput(ctx, input)
	if err != nil {
		return SettingsView{}, err
	}
	if row.Version != input.ExpectedVersion {
		return SettingsView{}, s.failure(ctx, "assistant_settings_version", ErrConflict, ErrConflict)
	}
	row.Version, row.UpdatedBy, row.UpdatedAt = row.Version+1, actorID, time.Now().UTC()
	err = s.deps.Database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if input.ExpectedVersion == 0 {
			if err := tx.Create(&row).Error; err != nil {
				return s.failure(ctx, "assistant_settings_create", err, ErrConflict)
			}
		} else {
			result := tx.Model(&model.AssistantSettings{}).Where("uuid = ? AND version = ?", settingsID, input.ExpectedVersion).Select("*").Updates(&row)
			if result.Error != nil {
				return s.failure(ctx, "assistant_settings_save", result.Error, ErrStorage)
			}
			if result.RowsAffected != 1 {
				return s.failure(ctx, "assistant_settings_save", ErrConflict, ErrConflict)
			}
		}
		clone := *s
		clone.deps.Audits = audit.NewService(tx)
		return clone.audit(ctx, actorID, "assistant.settings.update", settingsID, model.AuditSucceeded, map[string]any{"enabled": row.Enabled, "has_api_key": row.APIKeyCiphertext != ""})
	})
	if err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrStorage) {
			return SettingsView{}, err
		}
		return SettingsView{}, s.failure(ctx, "assistant_settings_transaction", err, ErrStorage)
	}
	return settingsView(row), nil
}

func (s *Service) TestSettings(ctx context.Context, input SettingsInput) (TestResult, error) {
	row, key, err := s.configuredInput(ctx, input)
	if err != nil {
		return TestResult{}, err
	}
	if key == "" || row.Model == "" {
		return TestResult{}, s.failure(ctx, "assistant_model_test", ErrUnavailable, ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	provider, err := s.newModel(ctx, row, key)
	if err == nil {
		_, err = provider.Generate(ctx, []*schema.Message{schema.UserMessage("请仅回复：连接正常")})
	}
	if err != nil {
		return TestResult{}, s.modelFailure(ctx, "assistant_model_test", err, key)
	}
	return TestResult{Success: true, Message: "模型连接正常", Model: row.Model}, nil
}
