package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

var ErrUnavailable = errors.New("密钥加密功能尚未配置")

type Manager struct {
	aead cipher.AEAD
}

func New(encodedKey string) (*Manager, error) {
	encodedKey = strings.TrimSpace(encodedKey)
	if encodedKey == "" {
		return &Manager{}, nil
	}
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encodedKey)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("EDO_SECRETS_KEY 必须是 32 字节随机密钥的 Base64 编码")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化密钥加密器失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 加密器失败: %w", err)
	}
	return &Manager{aead: aead}, nil
}

func (m *Manager) Available() bool { return m != nil && m.aead != nil }

func (m *Manager) Encrypt(plaintext string, associatedData []byte) (string, error) {
	if !m.Available() {
		return "", ErrUnavailable
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成密钥加密随机数失败: %w", err)
	}
	ciphertext := m.aead.Seal(nil, nonce, []byte(plaintext), associatedData)
	value := append(nonce, ciphertext...)
	return "v1:" + base64.RawURLEncoding.EncodeToString(value), nil
}

func (m *Manager) Decrypt(encoded string, associatedData []byte) (string, error) {
	if !m.Available() {
		return "", ErrUnavailable
	}
	version, payload, ok := strings.Cut(encoded, ":")
	if !ok || version != "v1" {
		return "", errors.New("不支持的密文版本")
	}
	value, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(value) < m.aead.NonceSize()+m.aead.Overhead() {
		return "", errors.New("密文格式无效")
	}
	nonce := value[:m.aead.NonceSize()]
	ciphertext := value[m.aead.NonceSize():]
	plaintext, err := m.aead.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return "", errors.New("密文校验失败")
	}
	return string(plaintext), nil
}
