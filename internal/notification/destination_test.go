package notification

import (
	"context"
	"errors"
	"testing"

	"edo/internal/model"
)

func TestNotificationDestinationChangeRequiresExplicitToken(t *testing.T) {
	client := &fakeHTTPClient{statusCode: 204}
	s, db := newNotificationTestService(t, client, 1)
	endpoint, token := "https://original.invalid/hooks/edo", "synthetic-original-token"
	channel, err := s.CreateChannel(context.Background(), "test-actor", ChannelInput{Name: "test-destination", Type: model.NotificationChannelWebhook, Endpoint: &endpoint, Token: &token})
	if err != nil {
		t.Fatal(err)
	}
	var before model.NotificationChannel
	if err := db.First(&before, "uuid = ?", channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	changed := "https://changed.invalid/hooks/edo"
	input := ChannelInput{Name: "test-destination", Type: model.NotificationChannelWebhook, Endpoint: &changed}
	if _, err := s.UpdateChannel(context.Background(), channel.ID, input); !errors.Is(err, ErrTokenRebindRequired) {
		t.Fatalf("更换通知地址隐式复用旧令牌: err=%v", err)
	}
	var after model.NotificationChannel
	if err := db.First(&after, "uuid = ?", channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if before.EndpointCiphertext != after.EndpointCiphertext || before.TokenCiphertext != after.TokenCiphertext || client.request != nil {
		t.Fatal("被拒绝的重绑修改了配置或发送了通知")
	}
	input.Endpoint, input.Name = &endpoint, "test-renamed"
	if _, err := s.UpdateChannel(context.Background(), channel.ID, input); err != nil {
		t.Fatalf("同地址改名不应要求密钥: %v", err)
	}
	input.Endpoint = &changed
	newToken := "synthetic-rebound-token"
	input.Token = &newToken
	if _, err := s.UpdateChannel(context.Background(), channel.ID, input); err != nil {
		t.Fatalf("显式重绑失败: %v", err)
	}
	cleared, another := "", "https://another.invalid/hooks/edo"
	input.Endpoint, input.Token = &another, &cleared
	view, err := s.UpdateChannel(context.Background(), channel.ID, input)
	if err != nil || view.HasToken {
		t.Fatalf("明确清除令牌不能更换地址: %v", err)
	}
}
