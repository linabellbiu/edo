package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/account"
	"zrt/internal/auth"
	"zrt/internal/cache"
	"zrt/internal/model"
	"zrt/internal/secret"
)

const (
	TypeLDAP         = "ldap"
	TypeGenericOAuth = "generic_oauth"
	TypeFeishu       = "feishu"
	TypeGoogle       = "google"
	TypeGitHub       = "github"
	TypeGitLab       = "gitlab"
)

var (
	ErrInvalidProvider      = errors.New("登录方式配置无效")
	ErrProviderNotFound     = errors.New("登录方式不存在")
	ErrProviderDisabled     = errors.New("登录方式未启用")
	ErrInvalidCredentials   = errors.New("用户名或密码错误")
	ErrProvisioningDisabled = errors.New("该账户尚未绑定，请联系管理员")
	ErrExternalLogin        = errors.New("外部登录失败，请重试")
	ErrInvalidState         = errors.New("登录请求已失效，请重新开始")
	ErrUnverifiedEmail      = errors.New("外部账户邮箱尚未验证")
	providerNamePattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,63}$`)
)

type ProviderInput struct {
	Type          string `json:"type"`
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	IsActive      bool   `json:"is_active"`
	AutoCreate    bool   `json:"auto_create"`
	DefaultRoleID string `json:"default_role_id"`

	ClientID           string  `json:"client_id"`
	ClientSecret       *string `json:"client_secret"`
	AuthorizationURL   string  `json:"authorization_url"`
	TokenURL           string  `json:"token_url"`
	UserInfoURL        string  `json:"user_info_url"`
	RedirectURL        string  `json:"redirect_url"`
	Scopes             string  `json:"scopes"`
	SubjectField       string  `json:"subject_field"`
	UsernameField      string  `json:"username_field"`
	NicknameField      string  `json:"nickname_field"`
	EmailField         string  `json:"email_field"`
	EmailVerifiedField string  `json:"email_verified_field"`

	LDAPURL               string  `json:"ldap_url"`
	LDAPBaseDN            string  `json:"ldap_base_dn"`
	LDAPBindDN            string  `json:"ldap_bind_dn"`
	LDAPBindPassword      *string `json:"ldap_bind_password"`
	LDAPUserFilter        string  `json:"ldap_user_filter"`
	LDAPUsernameAttribute string  `json:"ldap_username_attribute"`
	LDAPNicknameAttribute string  `json:"ldap_nickname_attribute"`
	LDAPEmailAttribute    string  `json:"ldap_email_attribute"`
	LDAPStartTLS          bool    `json:"ldap_start_tls"`
	AllowInsecure         bool    `json:"allow_insecure"`
}

type ProviderView struct {
	model.IdentityProvider
	HasClientSecret bool `json:"has_client_secret"`
	HasBindPassword bool `json:"has_bind_password"`
}

type PublicProvider struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

type Profile struct {
	Subject  string
	Username string
	Nickname string
	Email    string
}

type Service struct {
	db         *gorm.DB
	cache      *cache.Redis
	secrets    *secret.Manager
	accounts   *account.Service
	login      *account.LoginService
	limiter    *auth.LoginRateLimiter
	httpClient *http.Client
}

func NewService(db *gorm.DB, redis *cache.Redis, secrets *secret.Manager, accounts *account.Service, login *account.LoginService, limiter *auth.LoginRateLimiter) *Service {
	return &Service{
		db: db, cache: redis, secrets: secrets, accounts: accounts, login: login, limiter: limiter,
		httpClient: &http.Client{
			Timeout:       12 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (s *Service) List(ctx context.Context) ([]ProviderView, error) {
	var providers []model.IdentityProvider
	if err := s.db.WithContext(ctx).Order("display_name ASC").Find(&providers).Error; err != nil {
		return nil, fmt.Errorf("查询登录方式失败: %w", err)
	}
	result := make([]ProviderView, 0, len(providers))
	for _, provider := range providers {
		result = append(result, providerView(provider))
	}
	return result, nil
}

func (s *Service) ListPublic(ctx context.Context) ([]PublicProvider, error) {
	var providers []model.IdentityProvider
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Order("display_name ASC").Find(&providers).Error; err != nil {
		return nil, fmt.Errorf("查询可用登录方式失败: %w", err)
	}
	result := make([]PublicProvider, 0, len(providers))
	for _, provider := range providers {
		result = append(result, PublicProvider{ID: provider.ID, Type: provider.Type, DisplayName: provider.DisplayName})
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, input ProviderInput, createdBy string) (*ProviderView, error) {
	provider, clientSecret, bindPassword, err := s.normalize(ctx, input, nil)
	if err != nil {
		return nil, err
	}
	provider.ID = uuid.NewString()
	provider.CreatedBy = createdBy
	now := time.Now().UTC()
	provider.CreatedAt, provider.UpdatedAt = now, now
	if err := s.encryptSecrets(provider, clientSecret, bindPassword); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Create(provider).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, fmt.Errorf("%w：标识已存在", ErrInvalidProvider)
		}
		return nil, fmt.Errorf("创建登录方式失败: %w", err)
	}
	view := providerView(*provider)
	return &view, nil
}

func (s *Service) Update(ctx context.Context, id string, input ProviderInput) (*ProviderView, error) {
	var current model.IdentityProvider
	if err := s.db.WithContext(ctx).First(&current, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProviderNotFound
		}
		return nil, fmt.Errorf("读取登录方式失败: %w", err)
	}
	provider, clientSecret, bindPassword, err := s.normalize(ctx, input, &current)
	if err != nil {
		return nil, err
	}
	provider.ID = current.ID
	provider.CreatedBy = current.CreatedBy
	provider.CreatedAt = current.CreatedAt
	provider.UpdatedAt = time.Now().UTC()
	provider.ClientSecretCiphertext = current.ClientSecretCiphertext
	provider.LDAPBindPasswordCiphertext = current.LDAPBindPasswordCiphertext
	if err := s.encryptSecrets(provider, clientSecret, bindPassword); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Save(provider).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, fmt.Errorf("%w：标识已存在", ErrInvalidProvider)
		}
		return nil, fmt.Errorf("更新登录方式失败: %w", err)
	}
	view := providerView(*provider)
	return &view, nil
}

func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	result := s.db.WithContext(ctx).Model(&model.IdentityProvider{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改登录方式状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrProviderNotFound
	}
	return nil
}

func (s *Service) getActive(ctx context.Context, id string) (*model.IdentityProvider, error) {
	var provider model.IdentityProvider
	if err := s.db.WithContext(ctx).First(&provider, "id = ? OR name = ?", id, strings.ToLower(strings.TrimSpace(id))).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProviderNotFound
		}
		return nil, fmt.Errorf("读取登录方式失败: %w", err)
	}
	if !provider.IsActive {
		return nil, ErrProviderDisabled
	}
	return &provider, nil
}

func (s *Service) normalize(ctx context.Context, input ProviderInput, current *model.IdentityProvider) (*model.IdentityProvider, *string, *string, error) {
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	input.Name = strings.ToLower(strings.TrimSpace(input.Name))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.DefaultRoleID = strings.TrimSpace(input.DefaultRoleID)
	if !isKnownType(input.Type) || !providerNamePattern.MatchString(input.Name) || input.DisplayName == "" || utf8.RuneCountInString(input.DisplayName) > 64 {
		return nil, nil, nil, ErrInvalidProvider
	}
	if current != nil && (input.Type != current.Type || input.Name != current.Name) {
		return nil, nil, nil, fmt.Errorf("%w：创建后不能修改类型或内部标识", ErrInvalidProvider)
	}
	if input.DefaultRoleID != "" {
		var count int64
		if err := s.db.WithContext(ctx).Model(&model.Role{}).Where("id = ?", input.DefaultRoleID).Count(&count).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("检查默认角色失败: %w", err)
		}
		if count != 1 {
			return nil, nil, nil, fmt.Errorf("%w：默认角色不存在", ErrInvalidProvider)
		}
	}
	applyPreset(&input)
	if (input.ClientSecret != nil && len(*input.ClientSecret) > 4096) || (input.LDAPBindPassword != nil && len(*input.LDAPBindPassword) > 4096) {
		return nil, nil, nil, fmt.Errorf("%w：密钥内容过长", ErrInvalidProvider)
	}
	provider := &model.IdentityProvider{
		Type: input.Type, Name: input.Name, DisplayName: input.DisplayName, IsActive: input.IsActive,
		AutoCreate: input.AutoCreate, DefaultRoleID: input.DefaultRoleID, ClientID: strings.TrimSpace(input.ClientID),
		AuthorizationURL: strings.TrimSpace(input.AuthorizationURL), TokenURL: strings.TrimSpace(input.TokenURL),
		UserInfoURL: strings.TrimSpace(input.UserInfoURL), RedirectURL: strings.TrimSpace(input.RedirectURL),
		Scopes: normalizeScopes(input.Scopes), SubjectField: strings.TrimSpace(input.SubjectField),
		UsernameField: strings.TrimSpace(input.UsernameField), NicknameField: strings.TrimSpace(input.NicknameField),
		EmailField: strings.TrimSpace(input.EmailField), EmailVerifiedField: strings.TrimSpace(input.EmailVerifiedField),
		LDAPURL: strings.TrimSpace(input.LDAPURL), LDAPBaseDN: strings.TrimSpace(input.LDAPBaseDN),
		LDAPBindDN: strings.TrimSpace(input.LDAPBindDN), LDAPUserFilter: strings.TrimSpace(input.LDAPUserFilter),
		LDAPUsernameAttribute: strings.TrimSpace(input.LDAPUsernameAttribute),
		LDAPNicknameAttribute: strings.TrimSpace(input.LDAPNicknameAttribute), LDAPEmailAttribute: strings.TrimSpace(input.LDAPEmailAttribute),
		LDAPStartTLS: input.LDAPStartTLS, AllowInsecure: input.AllowInsecure,
	}
	if input.Type == TypeLDAP {
		if err := validateLDAP(provider); err != nil {
			return nil, nil, nil, err
		}
		if provider.LDAPBindDN != "" && (input.LDAPBindPassword == nil || *input.LDAPBindPassword == "") && (current == nil || current.LDAPBindPasswordCiphertext == "") {
			return nil, nil, nil, fmt.Errorf("%w：服务账号密码不能为空", ErrInvalidProvider)
		}
	} else {
		if err := validateOAuth(provider); err != nil {
			return nil, nil, nil, err
		}
		if (input.ClientSecret == nil || *input.ClientSecret == "") && (current == nil || current.ClientSecretCiphertext == "") {
			return nil, nil, nil, fmt.Errorf("%w：客户端密钥不能为空", ErrInvalidProvider)
		}
	}
	return provider, input.ClientSecret, input.LDAPBindPassword, nil
}

func (s *Service) encryptSecrets(provider *model.IdentityProvider, clientSecret, bindPassword *string) error {
	if clientSecret != nil && *clientSecret != "" {
		value, err := s.secrets.Encrypt(*clientSecret, []byte("identity-provider:"+provider.ID+":client-secret"))
		if err != nil {
			return fmt.Errorf("加密客户端密钥失败: %w", err)
		}
		provider.ClientSecretCiphertext = value
	}
	if bindPassword != nil && *bindPassword != "" {
		value, err := s.secrets.Encrypt(*bindPassword, []byte("identity-provider:"+provider.ID+":ldap-bind-password"))
		if err != nil {
			return fmt.Errorf("加密 LDAP 服务账号密码失败: %w", err)
		}
		provider.LDAPBindPasswordCiphertext = value
	}
	return nil
}

func (s *Service) resolveUser(ctx context.Context, provider *model.IdentityProvider, profile Profile) (*model.User, error) {
	profile.Subject = strings.TrimSpace(profile.Subject)
	if profile.Subject == "" || len(profile.Subject) > 512 {
		return nil, ErrExternalLogin
	}
	var user model.User
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identity model.ExternalIdentity
		err := tx.First(&identity, "provider_id = ? AND subject = ?", provider.ID, profile.Subject).Error
		now := time.Now().UTC()
		if err == nil {
			if err := tx.First(&user, "id = ?", identity.UserID).Error; err != nil {
				return err
			}
			return tx.Model(&identity).Updates(map[string]any{
				"remote_username": profile.Username, "email": profile.Email, "last_login_at": now, "updated_at": now,
			}).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if !provider.AutoCreate {
			return ErrProvisioningDisabled
		}
		username := availableUsername(tx, profile.Username, provider.ID, profile.Subject)
		nickname := strings.TrimSpace(profile.Nickname)
		if nickname == "" {
			nickname = username
		}
		nickname = truncateRunes(nickname, 64)
		randomPassword := make([]byte, 32)
		if _, err := rand.Read(randomPassword); err != nil {
			return err
		}
		passwordHash, err := auth.HashPassword(hex.EncodeToString(randomPassword))
		if err != nil {
			return err
		}
		user = model.User{
			ID: uuid.NewString(), Username: username, Nickname: nickname, PasswordHash: passwordHash,
			IsActive: true, IsSuperuser: false, AuthVersion: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		identity = model.ExternalIdentity{
			ID: uuid.NewString(), ProviderID: provider.ID, Subject: profile.Subject, UserID: user.ID,
			RemoteUsername: profile.Username, Email: profile.Email, LastLoginAt: &now, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&identity).Error; err != nil {
			return err
		}
		if provider.DefaultRoleID != "" {
			return tx.Create(&model.UserRole{UserID: user.ID, RoleID: provider.DefaultRoleID, CreatedAt: now}).Error
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrProvisioningDisabled) {
			return nil, err
		}
		return nil, fmt.Errorf("绑定外部身份失败: %w", err)
	}
	if !user.IsActive {
		return nil, account.ErrAccountDisabled
	}
	return &user, nil
}

func providerView(provider model.IdentityProvider) ProviderView {
	return ProviderView{IdentityProvider: provider, HasClientSecret: provider.ClientSecretCiphertext != "", HasBindPassword: provider.LDAPBindPasswordCiphertext != ""}
}

func isKnownType(value string) bool {
	switch value {
	case TypeLDAP, TypeGenericOAuth, TypeFeishu, TypeGoogle, TypeGitHub, TypeGitLab:
		return true
	default:
		return false
	}
}

func validateOAuth(provider *model.IdentityProvider) error {
	if provider.ClientID == "" || provider.SubjectField == "" || provider.UsernameField == "" ||
		len(provider.ClientID) > 255 || len(provider.Scopes) > 512 ||
		!withinLimit(64, provider.SubjectField, provider.UsernameField, provider.NicknameField, provider.EmailField, provider.EmailVerifiedField) ||
		!withinLimit(1024, provider.AuthorizationURL, provider.TokenURL, provider.UserInfoURL, provider.RedirectURL) {
		return ErrInvalidProvider
	}
	for _, value := range []string{provider.AuthorizationURL, provider.TokenURL, provider.UserInfoURL, provider.RedirectURL} {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(provider.AllowInsecure && parsed.Scheme == "http")) {
			return fmt.Errorf("%w：授权、回调和用户信息地址必须使用 HTTPS", ErrInvalidProvider)
		}
	}
	return nil
}

func validateLDAP(provider *model.IdentityProvider) error {
	parsed, err := url.Parse(provider.LDAPURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ldap" && parsed.Scheme != "ldaps") || provider.LDAPBaseDN == "" ||
		!strings.Contains(provider.LDAPUserFilter, "{username}") || provider.LDAPUsernameAttribute == "" ||
		!withinLimit(1024, provider.LDAPURL) || !withinLimit(512, provider.LDAPBaseDN, provider.LDAPBindDN, provider.LDAPUserFilter) ||
		!withinLimit(64, provider.LDAPUsernameAttribute, provider.LDAPNicknameAttribute, provider.LDAPEmailAttribute) {
		return ErrInvalidProvider
	}
	if parsed.Scheme == "ldap" && !provider.LDAPStartTLS && !provider.AllowInsecure {
		return fmt.Errorf("%w：普通 LDAP 必须启用 StartTLS；如确需明文连接，请明确允许不安全连接", ErrInvalidProvider)
	}
	return nil
}

func withinLimit(limit int, values ...string) bool {
	for _, value := range values {
		if len(value) > limit {
			return false
		}
	}
	return true
}

func applyPreset(input *ProviderInput) {
	set := func(target *string, value string) {
		if strings.TrimSpace(*target) == "" {
			*target = value
		}
	}
	switch input.Type {
	case TypeGoogle:
		set(&input.AuthorizationURL, "https://accounts.google.com/o/oauth2/v2/auth")
		set(&input.TokenURL, "https://oauth2.googleapis.com/token")
		set(&input.UserInfoURL, "https://openidconnect.googleapis.com/v1/userinfo")
		set(&input.Scopes, "openid profile email")
		set(&input.SubjectField, "sub")
		set(&input.UsernameField, "email")
		set(&input.NicknameField, "name")
		set(&input.EmailField, "email")
		set(&input.EmailVerifiedField, "email_verified")
	case TypeGitHub:
		set(&input.AuthorizationURL, "https://github.com/login/oauth/authorize")
		set(&input.TokenURL, "https://github.com/login/oauth/access_token")
		set(&input.UserInfoURL, "https://api.github.com/user")
		set(&input.Scopes, "read:user user:email")
		set(&input.SubjectField, "id")
		set(&input.UsernameField, "login")
		set(&input.NicknameField, "name")
		set(&input.EmailField, "email")
	case TypeGitLab:
		set(&input.AuthorizationURL, "https://gitlab.com/oauth/authorize")
		set(&input.TokenURL, "https://gitlab.com/oauth/token")
		set(&input.UserInfoURL, "https://gitlab.com/api/v4/user")
		set(&input.Scopes, "read_user")
		set(&input.SubjectField, "id")
		set(&input.UsernameField, "username")
		set(&input.NicknameField, "name")
		set(&input.EmailField, "email")
	case TypeFeishu:
		set(&input.AuthorizationURL, "https://accounts.feishu.cn/open-apis/authen/v1/authorize")
		set(&input.TokenURL, "https://open.feishu.cn/open-apis/authen/v2/oauth/token")
		set(&input.UserInfoURL, "https://open.feishu.cn/open-apis/authen/v1/user_info")
		set(&input.SubjectField, "open_id")
		set(&input.UsernameField, "email")
		set(&input.NicknameField, "name")
		set(&input.EmailField, "email")
	case TypeGenericOAuth:
		set(&input.SubjectField, "sub")
		set(&input.UsernameField, "preferred_username")
		set(&input.NicknameField, "name")
		set(&input.EmailField, "email")
	case TypeLDAP:
		set(&input.LDAPUserFilter, "(uid={username})")
		set(&input.LDAPUsernameAttribute, "uid")
		set(&input.LDAPNicknameAttribute, "displayName")
		set(&input.LDAPEmailAttribute, "mail")
	}
}

func normalizeScopes(value string) string {
	parts := strings.Fields(strings.ReplaceAll(value, ",", " "))
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func availableUsername(tx *gorm.DB, remote, providerID, subject string) string {
	base := normalizeUsername(remote)
	if base == "" {
		base = "user"
	}
	var count int64
	tx.Model(&model.User{}).Where("username = ?", base).Count(&count)
	if count == 0 {
		return base
	}
	digest := sha256.Sum256([]byte(providerID + "\x00" + subject))
	suffix := "-" + hex.EncodeToString(digest[:3])
	if len(base)+len(suffix) > 32 {
		base = base[:32-len(suffix)]
	}
	return base + suffix
}

func normalizeUsername(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r), r == '@':
			b.WriteByte('-')
		}
	}
	result := strings.Trim(b.String(), "-._")
	if result == "" || result[0] < 'a' || result[0] > 'z' {
		result = "user-" + result
	}
	if len(result) < 3 {
		result += "-id"
	}
	if len(result) > 32 {
		result = strings.TrimRight(result[:32], "-._")
	}
	return result
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
