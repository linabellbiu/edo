package secret

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestEncryptDecryptWithAssociatedData(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	manager, err := New(key)
	if err != nil {
		t.Fatalf("初始化密钥管理器失败: %v", err)
	}
	ciphertext, err := manager.Encrypt("sensitive-token", []byte("repository:1"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	plaintext, err := manager.Decrypt(ciphertext, []byte("repository:1"))
	if err != nil || plaintext != "sensitive-token" {
		t.Fatalf("解密结果错误: value=%q err=%v", plaintext, err)
	}
	if _, err := manager.Decrypt(ciphertext, []byte("repository:2")); err == nil {
		t.Fatal("关联数据不匹配时仍能解密")
	}
}

func TestUnavailableManagerRejectsSecrets(t *testing.T) {
	manager, err := New("")
	if err != nil {
		t.Fatalf("空配置初始化失败: %v", err)
	}
	if _, err := manager.Encrypt("token", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("未配置密钥时应拒绝加密: %v", err)
	}
}
