package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"zrt/internal/cache"
)

var ErrSessionNotFound = errors.New("登录会话不存在或已过期")

type Session struct {
	UserID      string    `json:"user_id"`
	AuthVersion uint64    `json:"auth_version"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type SessionStore struct {
	redis *cache.Redis
	ttl   time.Duration
}

func NewSessionStore(redisClient *cache.Redis, ttl time.Duration) *SessionStore {
	return &SessionStore{redis: redisClient, ttl: ttl}
}

func (s *SessionStore) Create(ctx context.Context, userID string, authVersion uint64) (string, Session, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", Session{}, fmt.Errorf("生成登录会话凭据失败: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	now := time.Now().UTC()
	session := Session{UserID: userID, AuthVersion: authVersion, IssuedAt: now, ExpiresAt: now.Add(s.ttl)}
	value, err := json.Marshal(session)
	if err != nil {
		return "", Session{}, fmt.Errorf("序列化登录会话失败: %w", err)
	}
	if err := s.redis.Client().Set(ctx, s.key(token), value, s.ttl).Err(); err != nil {
		return "", Session{}, fmt.Errorf("保存登录会话失败: %w", err)
	}
	return token, session, nil
}

func (s *SessionStore) Get(ctx context.Context, token string) (Session, error) {
	if len(token) < 40 || len(token) > 128 {
		return Session{}, ErrSessionNotFound
	}
	value, err := s.redis.Client().Get(ctx, s.key(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("读取登录会话失败: %w", err)
	}
	var session Session
	if err := json.Unmarshal(value, &session); err != nil {
		_ = s.redis.Client().Del(ctx, s.key(token)).Err()
		return Session{}, fmt.Errorf("登录会话数据损坏: %w", err)
	}
	if session.ExpiresAt.Before(time.Now().UTC()) {
		_ = s.redis.Client().Del(ctx, s.key(token)).Err()
		return Session{}, ErrSessionNotFound
	}
	return session, nil
}

func (s *SessionStore) Delete(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.redis.Client().Del(ctx, s.key(token)).Err(); err != nil {
		return fmt.Errorf("删除登录会话失败: %w", err)
	}
	return nil
}

func (s *SessionStore) key(token string) string {
	digest := sha256.Sum256([]byte(token))
	return s.redis.Key("session", hex.EncodeToString(digest[:]))
}
