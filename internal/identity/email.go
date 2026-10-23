package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/mail"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	smtp "github.com/wneessen/go-mail"

	"edo/internal/account"
	"edo/internal/model"
)

const emailCodeTTL = 5 * time.Minute

var (
	ErrInvalidEmail     = errors.New("请输入有效的邮箱地址")
	ErrInvalidEmailCode = errors.New("验证码错误或已过期，请重新获取")
	ErrEmailUnavailable = errors.New("邮件发送暂时不可用，请稍后重试")
	ErrEmailRateLimited = errors.New("验证码发送过于频繁，请稍后重试")
)

type EmailSettings struct {
	Host           string   `json:"host"`
	Port           int      `json:"port"`
	Username       string   `json:"username"`
	Password       *string  `json:"password,omitempty"`
	From           string   `json:"from"`
	TLSMode        string   `json:"tls_mode"`
	AllowedDomains []string `json:"allowed_domains"`
}

type EmailSettingsView struct {
	EmailSettings
	HasPassword bool `json:"has_password"`
}

// 邮件连接配置复用登录方式的加密载荷，避免增加明文密码或第二套配置存储。
func (s *Service) emailSettings(provider *model.IdentityProvider) (EmailSettings, error) {
	value, err := s.secrets.Decrypt(provider.ClientSecretCiphertext, []byte("identity-provider:"+provider.ID+":client-secret"))
	if err != nil {
		return EmailSettings{}, fmt.Errorf("读取邮件登录配置失败: %w", err)
	}
	var settings EmailSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return EmailSettings{}, fmt.Errorf("解析邮件登录配置失败: %w", err)
	}
	return settings, nil
}

func (s *Service) normalizeEmail(input *EmailSettings, current *model.IdentityProvider) (string, error) {
	if input == nil {
		return "", fmt.Errorf("%w：请填写邮件服务器配置", ErrInvalidProvider)
	}
	settings := *input
	settings.Host = strings.TrimSpace(settings.Host)
	settings.Username = strings.TrimSpace(settings.Username)
	settings.From = strings.TrimSpace(settings.From)
	if settings.TLSMode != "tls" && settings.TLSMode != "starttls" {
		return "", fmt.Errorf("%w：邮件连接必须使用 TLS 或 STARTTLS", ErrInvalidProvider)
	}
	if settings.Host == "" || len(settings.Host) > 253 || strings.ContainsAny(settings.Host, "/@\r\n\t ") ||
		(strings.Contains(settings.Host, ":") && net.ParseIP(settings.Host) == nil) || settings.Port < 1 || settings.Port > 65535 ||
		len(settings.Username) > 320 || len(settings.AllowedDomains) > 50 {
		return "", ErrInvalidProvider
	}
	if _, err := normalizeEmailAddress(settings.From); err != nil {
		return "", fmt.Errorf("%w：发件邮箱无效", ErrInvalidProvider)
	}
	if current != nil && (settings.Password == nil || *settings.Password == "") {
		previous, err := s.emailSettings(current)
		if err != nil {
			return "", err
		}
		settings.Password = previous.Password
	}
	if settings.Username != "" && (settings.Password == nil || *settings.Password == "") {
		return "", fmt.Errorf("%w：请填写邮件服务器密码或授权码", ErrInvalidProvider)
	}
	if settings.Password != nil && len(*settings.Password) > 4096 {
		return "", ErrInvalidProvider
	}
	settings.AllowedDomains = append([]string{}, settings.AllowedDomains...)
	for i, domain := range settings.AllowedDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if _, err := normalizeEmailAddress("edo@" + domain); err != nil || !strings.Contains(domain, ".") {
			return "", fmt.Errorf("%w：邮箱域名无效", ErrInvalidProvider)
		}
		settings.AllowedDomains[i] = domain
	}
	encoded, err := json.Marshal(settings)
	return string(encoded), err
}

func normalizeEmailAddress(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", ErrInvalidEmail
	}
	return value, nil
}

func emailDomainAllowed(settings EmailSettings, address string) bool {
	if len(settings.AllowedDomains) == 0 {
		return true
	}
	domain := address[strings.LastIndex(address, "@")+1:]
	for _, allowed := range settings.AllowedDomains {
		if domain == allowed {
			return true
		}
	}
	return false
}

func (s *Service) SendEmailCode(ctx context.Context, providerID, address, clientIP string) error {
	provider, err := s.getActive(ctx, providerID)
	if err != nil {
		return err
	}
	if provider.Type != TypeEmail {
		return ErrInvalidProvider
	}
	address, err = normalizeEmailAddress(address)
	if err != nil {
		return err
	}
	settings, err := s.emailSettings(provider)
	if err != nil {
		return err
	}
	// 邮箱、来源地址和全局发送量一起限制，且不受可关闭的密码失败锁定开关影响。
	limits := redis.NewScript(`
for i = 1, #KEYS do
  local limit = tonumber(ARGV[(i-1)*2+1])
  if tonumber(redis.call('GET', KEYS[i]) or '0') >= limit then return 0 end
end
for i = 1, #KEYS do
  if redis.call('INCR', KEYS[i]) == 1 then redis.call('PEXPIRE', KEYS[i], ARGV[(i-1)*2+2]) end
end
return 1`)
	allowed, err := limits.Run(ctx, s.cache.Client(), []string{
		s.emailKey("cooldown", provider.ID, address), s.emailKey("address", provider.ID, address),
		s.emailKey("source", provider.ID, clientIP), s.emailKey("total", provider.ID, ""),
	}, 1, time.Minute.Milliseconds(), 5, time.Hour.Milliseconds(), 20, time.Hour.Milliseconds(), 200, time.Hour.Milliseconds()).Int()
	if err != nil {
		return fmt.Errorf("读取邮件发送限流失败: %w", err)
	}
	if allowed != 1 {
		return ErrEmailRateLimited
	}
	if !emailDomainAllowed(settings, address) {
		return nil
	}
	if !provider.AutoCreate {
		var count int64
		if err := s.db.WithContext(ctx).Model(&model.ExternalIdentity{}).Where("provider_id = ? AND subject = ?", provider.ID, address).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
	}
	number, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return fmt.Errorf("生成邮箱验证码失败: %w", err)
	}
	code := fmt.Sprintf("%06d", number.Int64())
	key := s.emailCodeKey(provider, address)
	digest := emailCodeDigest(provider, address, code)
	if _, err := s.cache.Client().TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, key, "digest", digest, "attempts", 0)
		pipe.Expire(ctx, key, emailCodeTTL)
		return nil
	}); err != nil {
		return fmt.Errorf("保存邮箱验证码失败: %w", err)
	}
	if err := s.sendEmail(ctx, settings, address, code); err != nil {
		_ = s.cache.Client().Del(ctx, key).Err()
		return fmt.Errorf("%w: %w", ErrEmailUnavailable, err)
	}
	return nil
}

func (s *Service) LoginEmail(ctx context.Context, providerID, address, code, clientIP string) (account.LoginResult, error) {
	provider, err := s.getActive(ctx, providerID)
	if err != nil {
		return account.LoginResult{}, err
	}
	if provider.Type != TypeEmail {
		return account.LoginResult{}, ErrInvalidProvider
	}
	address, err = normalizeEmailAddress(address)
	if err != nil {
		return account.LoginResult{}, err
	}
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return account.LoginResult{}, ErrInvalidEmailCode
	}
	settings, err := s.emailSettings(provider)
	if err != nil {
		return account.LoginResult{}, err
	}
	if !emailDomainAllowed(settings, address) {
		return account.LoginResult{}, ErrInvalidEmailCode
	}
	verify := redis.NewScript(`
local digest = redis.call('HGET', KEYS[1], 'digest')
if not digest then return 0 end
if redis.call('HINCRBY', KEYS[1], 'attempts', 1) > 5 then redis.call('DEL', KEYS[1]); return 0 end
if digest ~= ARGV[1] then
  if tonumber(redis.call('HGET', KEYS[1], 'attempts')) >= 5 then redis.call('DEL', KEYS[1]) end
  return 0
end
redis.call('DEL', KEYS[1])
return 1`)
	valid, err := verify.Run(ctx, s.cache.Client(), []string{s.emailCodeKey(provider, address)}, emailCodeDigest(provider, address, code)).Int()
	if err != nil {
		return account.LoginResult{}, fmt.Errorf("校验邮箱验证码失败: %w", err)
	}
	if valid != 1 {
		return account.LoginResult{}, ErrInvalidEmailCode
	}
	user, err := s.resolveUser(ctx, provider, Profile{Subject: address, Username: strings.SplitN(address, "@", 2)[0], Email: address})
	if err != nil {
		return account.LoginResult{}, err
	}
	return s.login.CreateSession(ctx, user, clientIP, TypeEmail)
}

func (s *Service) emailKey(kind, providerID, value string) string {
	digest := sha256.Sum256([]byte(providerID + "\x00" + value))
	return s.cache.Key("email_login", kind, hex.EncodeToString(digest[:]))
}

func (s *Service) emailCodeKey(provider *model.IdentityProvider, address string) string {
	return s.emailKey("code", provider.ID, provider.ClientSecretCiphertext+"\x00"+address)
}

func emailCodeDigest(provider *model.IdentityProvider, address, code string) string {
	// 加密配置作为服务端盐值，不让仅能读取 Redis 的人离线枚举六位验证码。
	digest := sha256.Sum256([]byte(provider.ClientSecretCiphertext + "\x00" + address + "\x00" + code))
	return hex.EncodeToString(digest[:])
}

func sendLoginEmail(ctx context.Context, settings EmailSettings, address, code string) error {
	options := []smtp.Option{smtp.WithPort(settings.Port), smtp.WithTimeout(8 * time.Second), smtp.WithTLSPolicy(smtp.TLSMandatory), smtp.WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: settings.Host})}
	if settings.TLSMode == "tls" {
		options = append(options, smtp.WithSSL())
	}
	if settings.Username != "" {
		options = append(options, smtp.WithSMTPAuth(smtp.SMTPAuthAutoDiscover), smtp.WithUsername(settings.Username), smtp.WithPassword(*settings.Password))
	}
	client, err := smtp.NewClient(settings.Host, options...)
	if err != nil {
		return err
	}
	message := smtp.NewMsg()
	if err := message.From(settings.From); err != nil {
		return err
	}
	if err := message.To(address); err != nil {
		return err
	}
	message.Subject("EDO 登录验证码")
	message.SetBodyString(smtp.TypeTextPlain, "你的 EDO 登录验证码是："+code+"。验证码 5 分钟内有效，请勿向他人透露。如非本人操作，请忽略此邮件。")
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return client.DialAndSendWithContext(ctx, message)
}
