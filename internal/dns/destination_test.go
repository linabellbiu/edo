package dnsmanager

import (
	"context"
	"errors"
	"testing"

	"edo/internal/model"
)

func TestPowerDNSDestinationChangeRequiresNonemptyExplicitToken(t *testing.T) {
	s, db, _ := newDNSTestService(t)
	s.registry = NewRegistry()
	ctx := context.Background()
	account, err := s.CreateProviderAccount(ctx, "test-actor", ProviderAccountInput{
		Name: "test-powerdns", Provider: model.DNSProviderPowerDNS,
		Config: map[string]string{"server_url": "https://original.invalid", "api_token": "synthetic-original-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 以数据库实际 JSON 表示作为基线，兼容 MySQL 的规范化存储。
	account, err = s.FindProviderAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []*string{nil, new(string)} {
		config := map[string]string{"server_url": "https://changed.invalid"}
		if token != nil {
			config["api_token"] = *token
		}
		if _, err := s.UpdateProviderAccount(ctx, account.ID, ProviderAccountInput{Name: account.Name, Config: config}); !errors.Is(err, ErrTokenRebindRequired) {
			t.Fatalf("未提供非空新令牌仍允许改变目的地: err=%v", err)
		}
	}
	var unchanged model.DNSProviderAccount
	if err := db.First(&unchanged, "uuid = ?", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if string(unchanged.PublicConfig) != string(account.PublicConfig) || unchanged.SecretCiphertext != account.SecretCiphertext {
		t.Fatal("拒绝目的地变更仍修改了账户")
	}
	renamed, err := s.UpdateProviderAccount(ctx, account.ID, ProviderAccountInput{Name: "test-renamed", Config: map[string]string{"server_url": "https://original.invalid/"}})
	if err != nil {
		t.Fatalf("同目的地改名不应要求令牌: %v", err)
	}
	_, secrets, err := s.decodeAccountConfig(renamed)
	if err != nil || secrets["api_token"] != "synthetic-original-token" {
		t.Fatalf("同目的地改名未保留密钥: %v", err)
	}
	rebound, err := s.UpdateProviderAccount(ctx, account.ID, ProviderAccountInput{Name: renamed.Name, Config: map[string]string{
		"server_url": "https://changed.invalid", "api_token": "synthetic-rebound-token",
	}})
	if err != nil {
		t.Fatalf("显式新令牌重绑失败: %v", err)
	}
	public, secrets, err := s.decodeAccountConfig(rebound)
	if err != nil || public["server_url"] != "https://changed.invalid" || secrets["api_token"] != "synthetic-rebound-token" {
		t.Fatalf("新目的地未绑定明确提交的密钥: %v", err)
	}
}
