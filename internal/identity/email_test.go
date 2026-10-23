package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"edo/internal/model"
)

func emailTestInput() ProviderInput {
	password := "smtp-test-password"
	return ProviderInput{Type: TypeEmail, Name: "email", DisplayName: "邮箱验证码", IsActive: true, AutoCreate: true,
		Email: &EmailSettings{Host: "smtp.example.com", Port: 465, Username: "mailer", Password: &password, From: "noreply@example.com", TLSMode: "tls", AllowedDomains: []string{"example.com"}}}
}

func TestEmailCodeCreatesSessionWithoutMergingLocalUserAndIsSingleUse(t *testing.T) {
	s, db, closeTest := newIdentityTestService(t)
	defer closeTest()
	ctx := context.Background()
	local, err := s.accounts.CreateUser(ctx, "alice", "本地用户", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	var delivered string
	s.sendEmail = func(_ context.Context, settings EmailSettings, address, code string) error {
		if address != "alice@example.com" || settings.Password == nil || *settings.Password != "smtp-test-password" {
			t.Fatal("发件参数不正确")
		}
		delivered = code
		return nil
	}
	view, err := s.Create(ctx, emailTestInput(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "smtp-test-password") || strings.Contains(string(encoded), `"password"`) || !view.Email.HasPassword {
		t.Fatalf("邮件密码泄露: %s", encoded)
	}
	if err := s.SendEmailCode(ctx, view.ID, " Alice@Example.com ", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 6 {
		t.Fatal("未投递六位验证码")
	}
	if err := s.SendEmailCode(ctx, view.ID, "alice@example.com", "192.0.2.2"); !errors.Is(err, ErrEmailRateLimited) {
		t.Fatalf("发送冷却未生效: %v", err)
	}
	keys, _ := s.cache.Client().Keys(ctx, s.cache.Key("email_login", "code", "*")).Result()
	for _, key := range keys {
		values, _ := s.cache.Client().HGetAll(ctx, key).Result()
		if values["digest"] == delivered || len(values["digest"]) != 64 {
			t.Fatal("验证码未摘要存储")
		}
	}
	var wg sync.WaitGroup
	var success int
	var mu sync.Mutex
	for range 6 {
		wg.Go(func() {
			result, err := s.LoginEmail(ctx, view.ID, "alice@example.com", delivered, "192.0.2.1")
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
				if result.Token == "" || result.User.ID == local.ID || result.User.IsSuperuser {
					t.Error("会话或身份边界错误")
				}
			} else if !errors.Is(err, ErrInvalidEmailCode) {
				t.Errorf("登录失败: %v", err)
			}
		})
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("验证码重复使用: successes=%d", success)
	}
	var count int64
	db.Model(&model.ExternalIdentity{}).Where("provider_id = ?", view.ID).Count(&count)
	if count != 1 {
		t.Fatalf("身份绑定数量不正确: %d", count)
	}
}

func TestEmailCodeAttemptLimitExpiryAndProviderDisable(t *testing.T) {
	s, _, closeTest := newIdentityTestService(t)
	defer closeTest()
	ctx := context.Background()
	var delivered string
	s.sendEmail = func(_ context.Context, _ EmailSettings, _, code string) error { delivered = code; return nil }
	view, err := s.Create(ctx, emailTestInput(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	send := func(address string) {
		t.Helper()
		if err := s.SendEmailCode(ctx, view.ID, address, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	send("attempts@example.com")
	wrong := "000000"
	if delivered == wrong {
		wrong = "999999"
	}
	for range 5 {
		if _, err := s.LoginEmail(ctx, view.ID, "attempts@example.com", wrong, "192.0.2.99"); !errors.Is(err, ErrInvalidEmailCode) {
			t.Fatal(err)
		}
	}
	if _, err := s.LoginEmail(ctx, view.ID, "attempts@example.com", delivered, "192.0.2.1"); !errors.Is(err, ErrInvalidEmailCode) {
		t.Fatal("超过校验次数后仍然有效")
	}
	send("expired@example.com")
	provider, _ := s.getActive(ctx, view.ID)
	key := s.emailCodeKey(provider, "expired@example.com")
	ttl, err := s.cache.Client().TTL(ctx, key).Result()
	if err != nil || ttl > 5*time.Minute || ttl < 4*time.Minute {
		t.Fatalf("验证码过期时间无效: %v %v", ttl, err)
	}
	s.cache.Client().ExpireAt(ctx, key, time.Unix(1, 0))
	if _, err := s.LoginEmail(ctx, view.ID, "expired@example.com", delivered, "192.0.2.1"); !errors.Is(err, ErrInvalidEmailCode) {
		t.Fatal("过期验证码仍然有效")
	}
	send("disabled@example.com")
	if err := s.SetActive(ctx, view.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginEmail(ctx, view.ID, "disabled@example.com", delivered, "192.0.2.1"); !errors.Is(err, ErrProviderDisabled) {
		t.Fatal("停用后仍可登录")
	}
	if err := s.SendEmailCode(ctx, view.ID, "disabled@example.com", "192.0.2.1"); !errors.Is(err, ErrProviderDisabled) {
		t.Fatal("停用后仍可发码")
	}
}

func TestEmailSettingsPreservePasswordAndRejectInsecureTransport(t *testing.T) {
	s, db, closeTest := newIdentityTestService(t)
	defer closeTest()
	ctx := context.Background()
	input := emailTestInput()
	var delivered string
	s.sendEmail = func(_ context.Context, _ EmailSettings, _, code string) error { delivered = code; return nil }
	view, err := s.Create(ctx, input, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendEmailCode(ctx, view.ID, "config@example.com", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	input.Email.Password = nil
	input.Email.Host = "mail.example.com"
	updated, err := s.Update(ctx, view.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Email.HasPassword || updated.Email.Host != "mail.example.com" {
		t.Fatal("更新邮件配置失败")
	}
	if _, err := s.LoginEmail(ctx, view.ID, "config@example.com", delivered, "192.0.2.1"); !errors.Is(err, ErrInvalidEmailCode) {
		t.Fatal("配置更新后旧验证码仍可使用")
	}
	var stored model.IdentityProvider
	db.First(&stored, "id = ?", view.ID)
	settings, err := s.emailSettings(&stored)
	if err != nil || settings.Password == nil || *settings.Password != "smtp-test-password" {
		t.Fatal("未保留原邮件密码")
	}
	input.Email.TLSMode = "none"
	if _, err := s.Update(ctx, view.ID, input); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("允许不加密 SMTP: %v", err)
	}
	if _, err := s.StartOAuth(ctx, view.ID, "/", "192.0.2.1"); !errors.Is(err, ErrInvalidProvider) {
		t.Fatal("邮箱进入 OAuth 流程")
	}
}

func TestEmailDomainProvisioningAndDeliveryFailure(t *testing.T) {
	s, _, closeTest := newIdentityTestService(t)
	defer closeTest()
	ctx := context.Background()
	calls := 0
	s.sendEmail = func(context.Context, EmailSettings, string, string) error {
		calls++
		return errors.New("邮件服务器不可用")
	}
	input := emailTestInput()
	view, err := s.Create(ctx, input, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendEmailCode(ctx, view.ID, "alice@other.example", "192.0.2.1"); err != nil || calls != 0 {
		t.Fatal("向未允许域名发信")
	}
	if err := s.SendEmailCode(ctx, view.ID, "alice@example.com", "192.0.2.1"); !errors.Is(err, ErrEmailUnavailable) || calls != 1 {
		t.Fatalf("未处理发送失败: %v", err)
	}
	keys, _ := s.cache.Client().Keys(ctx, s.cache.Key("email_login", "code", "*")).Result()
	if len(keys) != 0 {
		t.Fatal("发送失败后仍保留验证码")
	}
	input.AutoCreate = false
	if _, err := s.Update(ctx, view.ID, input); err != nil {
		t.Fatal(err)
	}
	if err := s.SendEmailCode(ctx, view.ID, "unknown@example.com", "192.0.2.1"); err != nil || calls != 1 {
		t.Fatal("向未绑定用户发送验证码")
	}
	for _, address := range []string{"name <alice@example.com>", "alice@example.com\r\nBcc:other@example.com", "not-an-email"} {
		if _, err := normalizeEmailAddress(address); !errors.Is(err, ErrInvalidEmail) {
			t.Fatalf("接受无效邮箱: %q", address)
		}
	}
}
