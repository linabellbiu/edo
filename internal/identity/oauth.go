package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"

	"zrt/internal/account"
	"zrt/internal/model"
)

const oauthStateTTL = 10 * time.Minute
const oauthStartWindow = time.Minute
const oauthStartLimit = 30

type oauthState struct {
	ProviderID string `json:"provider_id"`
	Verifier   string `json:"verifier"`
	ReturnTo   string `json:"return_to"`
}

func (s *Service) StartOAuth(ctx context.Context, providerID, returnTo, clientIP string) (string, error) {
	provider, err := s.getActive(ctx, providerID)
	if err != nil {
		return "", err
	}
	if provider.Type == TypeLDAP {
		return "", ErrInvalidProvider
	}
	allowed, err := s.allowOAuthStart(ctx, provider.ID, clientIP)
	if err != nil {
		return "", fmt.Errorf("%w：登录限流暂时不可用: %v", ErrExternalLogin, err)
	}
	if !allowed {
		return "", account.ErrTooManyAttempts
	}
	clientSecret, err := s.secrets.Decrypt(provider.ClientSecretCiphertext, []byte("identity-provider:"+provider.ID+":client-secret"))
	if err != nil || clientSecret == "" {
		return "", fmt.Errorf("%w：读取客户端密钥失败: %v", ErrExternalLogin, err)
	}
	state, err := randomToken(32)
	if err != nil {
		return "", ErrExternalLogin
	}
	verifier, err := randomToken(48)
	if err != nil {
		return "", ErrExternalLogin
	}
	transaction := oauthState{ProviderID: provider.ID, Verifier: verifier, ReturnTo: safeReturnTo(returnTo)}
	value, err := json.Marshal(transaction)
	if err != nil {
		return "", ErrExternalLogin
	}
	if err := s.cache.Client().Set(ctx, s.oauthStateKey(state), value, oauthStateTTL).Err(); err != nil {
		return "", fmt.Errorf("保存 OAuth 登录状态失败: %w", err)
	}
	challenge := sha256.Sum256([]byte(verifier))
	if provider.Type == TypeFeishu {
		parsed, _ := url.Parse(provider.AuthorizationURL)
		query := parsed.Query()
		query.Set("app_id", provider.ClientID)
		query.Set("redirect_uri", provider.RedirectURL)
		query.Set("response_type", "code")
		query.Set("state", state)
		query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
		query.Set("code_challenge_method", "S256")
		if provider.Scopes != "" {
			query.Set("scope", provider.Scopes)
		}
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	config := oauthConfig(provider, clientSecret)
	return config.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), nil
}

func (s *Service) allowOAuthStart(ctx context.Context, providerID, clientIP string) (bool, error) {
	digest := sha256.Sum256([]byte(providerID + "\x00" + clientIP))
	key := s.cache.Key("oauth_start", hex.EncodeToString(digest[:]))
	script := redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return current
`)
	count, err := script.Run(ctx, s.cache.Client(), []string{key}, oauthStartWindow.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return count <= oauthStartLimit, nil
}

func (s *Service) CompleteOAuth(ctx context.Context, providerID, state, code, clientIP string) (account.LoginResult, string, error) {
	if len(state) < 32 || len(state) > 128 || len(code) > 4096 {
		return account.LoginResult{}, "/", ErrInvalidState
	}
	value, err := s.cache.Client().GetDel(ctx, s.oauthStateKey(state)).Bytes()
	if errors.Is(err, redis.Nil) {
		return account.LoginResult{}, "/", ErrInvalidState
	}
	if err != nil {
		return account.LoginResult{}, "/", fmt.Errorf("读取 OAuth 登录状态失败: %w", err)
	}
	var transaction oauthState
	if err := json.Unmarshal(value, &transaction); err != nil {
		return account.LoginResult{}, "/", ErrInvalidState
	}
	if code == "" {
		return account.LoginResult{}, transaction.ReturnTo, ErrExternalLogin
	}
	provider, err := s.getActive(ctx, providerID)
	if err != nil || provider.Type == TypeLDAP || transaction.ProviderID != provider.ID {
		return account.LoginResult{}, transaction.ReturnTo, ErrProviderDisabled
	}
	clientSecret, err := s.secrets.Decrypt(provider.ClientSecretCiphertext, []byte("identity-provider:"+provider.ID+":client-secret"))
	if err != nil {
		return account.LoginResult{}, transaction.ReturnTo, fmt.Errorf("%w：读取客户端密钥失败: %v", ErrExternalLogin, err)
	}
	var token *oauth2.Token
	if provider.Type == TypeFeishu {
		token, err = s.exchangeFeishu(ctx, provider.TokenURL, provider.ClientID, clientSecret, provider.RedirectURL, code, transaction.Verifier)
	} else {
		config := oauthConfig(provider, clientSecret)
		exchangeCtx := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
		token, err = config.Exchange(exchangeCtx, code, oauth2.VerifierOption(transaction.Verifier))
	}
	if err != nil {
		return account.LoginResult{}, transaction.ReturnTo, fmt.Errorf("%w：换取访问令牌失败: %v", ErrExternalLogin, err)
	}
	if token == nil || token.AccessToken == "" {
		return account.LoginResult{}, transaction.ReturnTo, ErrExternalLogin
	}
	claims, err := s.fetchUserInfo(ctx, provider.UserInfoURL, token.AccessToken)
	if err != nil {
		return account.LoginResult{}, transaction.ReturnTo, fmt.Errorf("%w：读取外部用户信息失败: %v", ErrExternalLogin, err)
	}
	profile, err := profileFromClaims(provider.SubjectField, provider.UsernameField, provider.NicknameField, provider.EmailField, provider.EmailVerifiedField, claims)
	if err != nil {
		return account.LoginResult{}, transaction.ReturnTo, err
	}
	user, err := s.resolveUser(ctx, provider, profile)
	if err != nil {
		return account.LoginResult{}, transaction.ReturnTo, err
	}
	result, err := s.login.CreateSession(ctx, user, clientIP, provider.Type)
	return result, transaction.ReturnTo, err
}

func oauthConfig(provider *model.IdentityProvider, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: provider.ClientID, ClientSecret: clientSecret, RedirectURL: provider.RedirectURL,
		Scopes: strings.Fields(provider.Scopes),
		Endpoint: oauth2.Endpoint{
			AuthURL: provider.AuthorizationURL, TokenURL: provider.TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

func (s *Service) exchangeFeishu(ctx context.Context, tokenURL, clientID, clientSecret, redirectURL, code, verifier string) (*oauth2.Token, error) {
	payload, _ := json.Marshal(map[string]string{
		"grant_type": "authorization_code", "client_id": clientID, "client_secret": clientSecret,
		"code": code, "redirect_uri": redirectURL, "code_verifier": verifier,
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var body struct {
		Code         int    `json:"code"`
		Message      string `json:"msg"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		Data         *struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			TokenType    string `json:"token_type"`
			ExpiresIn    int64  `json:"expires_in"`
		} `json:"data"`
	}
	if err := decodeLimitedJSON(response.Body, &body); err != nil || response.StatusCode < 200 || response.StatusCode >= 300 || body.Code != 0 {
		return nil, ErrExternalLogin
	}
	if body.Data != nil {
		body.AccessToken, body.RefreshToken, body.TokenType, body.ExpiresIn = body.Data.AccessToken, body.Data.RefreshToken, body.Data.TokenType, body.Data.ExpiresIn
	}
	if body.AccessToken == "" {
		return nil, ErrExternalLogin
	}
	token := &oauth2.Token{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken, TokenType: body.TokenType}
	if body.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return token, nil
}

func (s *Service) fetchUserInfo(ctx context.Context, endpoint, accessToken string) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ZRT")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, ErrExternalLogin
	}
	var claims map[string]any
	if err := decodeLimitedJSON(response.Body, &claims); err != nil {
		return nil, err
	}
	if code, exists := claims["code"]; exists && stringify(code) != "0" {
		return nil, ErrExternalLogin
	}
	if data, ok := claims["data"].(map[string]any); ok {
		claims = data
	}
	return claims, nil
}

func profileFromClaims(subjectField, usernameField, nicknameField, emailField, emailVerifiedField string, claims map[string]any) (Profile, error) {
	subject := stringify(claimValue(claims, subjectField))
	username := stringify(claimValue(claims, usernameField))
	nickname := stringify(claimValue(claims, nicknameField))
	email := strings.ToLower(strings.TrimSpace(stringify(claimValue(claims, emailField))))
	if subject == "" {
		return Profile{}, ErrExternalLogin
	}
	if email != "" && emailVerifiedField != "" && !claimBool(claimValue(claims, emailVerifiedField)) {
		return Profile{}, ErrUnverifiedEmail
	}
	if username == "" {
		username = email
	}
	if username == "" {
		username = nickname
	}
	if username == "" {
		digest := sha256.Sum256([]byte(subject))
		username = "user-" + hex.EncodeToString(digest[:4])
	}
	return Profile{Subject: subject, Username: username, Nickname: nickname, Email: email}, nil
}

func claimValue(claims map[string]any, path string) any {
	if path == "" {
		return nil
	}
	var current any = claims
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[part]
	}
	return current
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func claimBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(typed)
		return parsed
	default:
		return false
	}
}

func decodeLimitedJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 1024*1024))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func safeReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") {
		return "/"
	}
	return value
}

func (s *Service) oauthStateKey(state string) string {
	digest := sha256.Sum256([]byte(state))
	return s.cache.Key("oauth_state", hex.EncodeToString(digest[:]))
}
