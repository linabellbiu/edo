package identity

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	ldap "github.com/go-ldap/ldap/v3"

	"zrt/internal/account"
	"zrt/internal/model"
)

func (s *Service) LoginLDAP(ctx context.Context, providerID, username, password, clientIP string) (account.LoginResult, error) {
	provider, err := s.getActive(ctx, providerID)
	if err != nil {
		return account.LoginResult{}, err
	}
	if provider.Type != TypeLDAP {
		return account.LoginResult{}, ErrInvalidProvider
	}
	username = strings.TrimSpace(username)
	identity := provider.Name + ":" + strings.ToLower(username)
	blocked, _, err := s.limiter.Blocked(ctx, identity, clientIP)
	if err != nil {
		return account.LoginResult{}, account.ErrLoginUnavailable
	}
	if blocked {
		return account.LoginResult{}, account.ErrTooManyAttempts
	}
	if username == "" || password == "" {
		return account.LoginResult{}, s.ldapFailure(ctx, identity, clientIP)
	}
	profile, err := s.authenticateLDAP(ctx, provider, username, password)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			return account.LoginResult{}, s.ldapFailure(ctx, identity, clientIP)
		}
		return account.LoginResult{}, err
	}
	user, err := s.resolveUser(ctx, provider, profile)
	if err != nil {
		return account.LoginResult{}, err
	}
	result, err := s.login.CreateSession(ctx, user, clientIP, TypeLDAP)
	if err != nil {
		return account.LoginResult{}, err
	}
	_ = s.limiter.Reset(ctx, identity, clientIP)
	return result, nil
}

func (s *Service) authenticateLDAP(ctx context.Context, provider *model.IdentityProvider, username, password string) (Profile, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, ErrExternalLogin
	}
	parsed, err := url.Parse(provider.LDAPURL)
	if err != nil {
		return Profile{}, ErrExternalLogin
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname()}
	connection, err := ldap.DialURL(
		provider.LDAPURL,
		ldap.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}),
		ldap.DialWithTLSConfig(tlsConfig),
	)
	if err != nil {
		return Profile{}, fmt.Errorf("%w：连接 LDAP 失败: %v", ErrExternalLogin, err)
	}
	defer connection.Close()
	connection.SetTimeout(10 * time.Second)
	if provider.LDAPStartTLS && parsed.Scheme == "ldap" {
		if err := connection.StartTLS(tlsConfig); err != nil {
			return Profile{}, fmt.Errorf("%w：建立 LDAP 加密连接失败: %v", ErrExternalLogin, err)
		}
	}
	if provider.LDAPBindDN != "" {
		bindPassword, err := s.secrets.Decrypt(provider.LDAPBindPasswordCiphertext, []byte("identity-provider:"+provider.ID+":ldap-bind-password"))
		if err != nil {
			return Profile{}, ErrExternalLogin
		}
		if err := connection.Bind(provider.LDAPBindDN, bindPassword); err != nil {
			return Profile{}, fmt.Errorf("%w：LDAP 服务账号绑定失败: %v", ErrExternalLogin, err)
		}
	}
	filter := strings.ReplaceAll(provider.LDAPUserFilter, "{username}", ldap.EscapeFilter(username))
	attributes := uniqueNonEmpty([]string{
		provider.LDAPUsernameAttribute, provider.LDAPNicknameAttribute, provider.LDAPEmailAttribute, "entryUUID", "objectGUID",
	})
	search := ldap.NewSearchRequest(
		provider.LDAPBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, 10, false, filter, attributes, nil,
	)
	result, err := connection.Search(search)
	if err != nil {
		return Profile{}, fmt.Errorf("%w：LDAP 用户查询失败: %v", ErrExternalLogin, err)
	}
	if len(result.Entries) != 1 {
		return Profile{}, ErrInvalidCredentials
	}
	entry := result.Entries[0]
	if err := connection.Bind(entry.DN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return Profile{}, ErrInvalidCredentials
		}
		return Profile{}, fmt.Errorf("%w：LDAP 用户绑定失败: %v", ErrExternalLogin, err)
	}
	subject := strings.TrimSpace(entry.GetAttributeValue("entryUUID"))
	if subject == "" {
		if raw := entry.GetRawAttributeValue("objectGUID"); len(raw) > 0 {
			subject = hex.EncodeToString(raw)
		}
	}
	if subject == "" {
		subject = strings.ToLower(strings.TrimSpace(entry.DN))
	}
	remoteUsername := strings.TrimSpace(entry.GetAttributeValue(provider.LDAPUsernameAttribute))
	if remoteUsername == "" {
		remoteUsername = username
	}
	return Profile{
		Subject: subject, Username: remoteUsername,
		Nickname: strings.TrimSpace(entry.GetAttributeValue(provider.LDAPNicknameAttribute)),
		Email:    strings.ToLower(strings.TrimSpace(entry.GetAttributeValue(provider.LDAPEmailAttribute))),
	}, nil
}

func (s *Service) ldapFailure(ctx context.Context, identity, clientIP string) error {
	if err := s.limiter.RecordFailure(ctx, identity, clientIP); err != nil {
		return account.ErrLoginUnavailable
	}
	return ErrInvalidCredentials
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
