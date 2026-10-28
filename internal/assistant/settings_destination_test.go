package assistant

import (
	"context"
	"errors"
	"testing"

	"edo/internal/model"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestAssistantKeyCannotFollowChangedDestinationImplicitly(t *testing.T) {
	s, _, actor, ctx := assistantFixture(t)
	view := saveTestSettings(t, s, actor, ctx, "synthetic-old-key")
	calls := 0
	var receivedKey, receivedURL string
	s.newModel = func(_ context.Context, settings model.AssistantSettings, key string) (einomodel.BaseChatModel, error) {
		calls++
		receivedKey, receivedURL = key, settings.BaseURL
		return &fakeModel{respond: func(context.Context, []*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage("连接正常", nil), nil
		}}, nil
	}
	input := SettingsInput{BaseURL: "https://changed.invalid/v1", Model: "test-model", ExpectedVersion: view.Version}
	if _, err := s.TestSettings(ctx, input); !errors.Is(err, ErrAPIKeyRebindRequired) {
		t.Fatalf("修改测试目的地未拒绝旧密钥复用: err=%v", err)
	}
	if _, err := s.SaveSettings(ctx, actor.ID, input); !errors.Is(err, ErrAPIKeyRebindRequired) {
		t.Fatalf("保存新目的地未要求重绑: err=%v", err)
	}
	unchanged, err := s.Settings(ctx)
	if err != nil || unchanged.BaseURL != view.BaseURL || unchanged.Version != view.Version || calls != 0 {
		t.Fatalf("拒绝重绑时配置或调用数改变: err=%v calls=%d", err, calls)
	}
	if _, err := s.TestSettings(ctx, SettingsInput{Model: "another-model"}); err != nil || calls != 1 || receivedKey != "synthetic-old-key" || receivedURL != view.BaseURL {
		t.Fatalf("同目的地只改模型未保留授权: err=%v calls=%d", err, calls)
	}
	input.APIKey = "synthetic-rebound-key"
	if _, err := s.TestSettings(ctx, input); err != nil || calls != 2 || receivedKey != input.APIKey || receivedURL != input.BaseURL {
		t.Fatalf("显式新密钥不能测试新目的地: err=%v calls=%d", err, calls)
	}
	input.APIKey, input.ClearAPIKey = "", true
	cleared, err := s.SaveSettings(ctx, actor.ID, input)
	if err != nil || cleared.HasAPIKey || cleared.BaseURL != input.BaseURL {
		t.Fatalf("明确清除密钥不能保存新目的地草稿: err=%v", err)
	}
}
