package dnsmanager

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libdns/libdns"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"zrt/internal/model"
	"zrt/internal/secret"
)

type fakeDNSProvider struct {
	mu             sync.Mutex
	records        []libdns.Record
	err            error
	appendErr      error
	appendFailures int
}

func (p *fakeDNSProvider) GetRecords(context.Context, string) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	return append([]libdns.Record(nil), p.records...), nil
}

func (p *fakeDNSProvider) AppendRecords(_ context.Context, _ string, records []libdns.Record) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	if p.appendFailures > 0 {
		p.appendFailures--
		return nil, p.appendErr
	}
	p.records = append(p.records, records...)
	return records, nil
}

func (p *fakeDNSProvider) SetRecords(_ context.Context, _ string, records []libdns.Record) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	groups := map[string]struct{}{}
	for _, item := range records {
		rr := item.RR()
		groups[rr.Name+"\x00"+rr.Type] = struct{}{}
	}
	kept := make([]libdns.Record, 0, len(p.records)+len(records))
	for _, item := range p.records {
		rr := item.RR()
		if _, replace := groups[rr.Name+"\x00"+rr.Type]; !replace {
			kept = append(kept, item)
		}
	}
	p.records = append(kept, records...)
	return records, nil
}

func (p *fakeDNSProvider) DeleteRecords(_ context.Context, _ string, records []libdns.Record) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	deleted := make([]libdns.Record, 0, len(records))
	for _, target := range records {
		kept := p.records[:0]
		for _, item := range p.records {
			if sameRR(item.RR(), target.RR()) {
				deleted = append(deleted, item)
				continue
			}
			kept = append(kept, item)
		}
		p.records = kept
	}
	return deleted, nil
}

func TestProviderCatalogContainsCommonVendors(t *testing.T) {
	catalog := NewRegistry().Catalog()
	if len(catalog) != 14 {
		t.Fatalf("DNS 厂商数量错误: %d", len(catalog))
	}
	wanted := map[model.DNSProvider]bool{
		model.DNSProviderCloudflare: false, model.DNSProviderAliDNS: false,
		model.DNSProviderTencentCloud: false, model.DNSProviderRoute53: false,
	}
	for _, provider := range catalog {
		if _, ok := wanted[provider.Code]; ok {
			wanted[provider.Code] = true
		}
	}
	for provider, found := range wanted {
		if !found {
			t.Fatalf("常用 DNS 厂商未注册: %s", provider)
		}
	}
}

func TestProviderCredentialsAreEncryptedAndDomainPreventsDeletion(t *testing.T) {
	service, db, _ := newDNSTestService(t)
	account, err := service.CreateProviderAccount(context.Background(), "admin", ProviderAccountInput{
		Name: "测试 DNS", Provider: "test", Config: map[string]string{"token": "very-secret-token"},
	})
	if err != nil {
		t.Fatalf("创建 DNS 厂商账号失败: %v", err)
	}
	if account.SecretCiphertext == "" || account.SecretCiphertext == "very-secret-token" || string(account.PublicConfig) == "very-secret-token" {
		t.Fatal("DNS 厂商凭据未加密")
	}
	var stored model.DNSProviderAccount
	if err := db.First(&stored, "id = ?", account.ID).Error; err != nil {
		t.Fatalf("读取 DNS 厂商账号失败: %v", err)
	}
	if string(stored.PublicConfig) != `{}` {
		t.Fatalf("公开配置中不应包含凭据: %s", stored.PublicConfig)
	}
	domain, err := service.CreateDomain(context.Background(), "admin", DomainInput{AccountID: account.ID, Name: "Example.COM."})
	if err != nil || domain.Name != "example.com" {
		t.Fatalf("创建或规范化域名失败: domain=%+v err=%v", domain, err)
	}
	if err := service.DeleteProviderAccount(context.Background(), account.ID); !errors.Is(err, ErrProviderAccountInUse) {
		t.Fatalf("删除使用中的 DNS 厂商账号未被拒绝: %v", err)
	}
	if err := service.DeleteDomain(context.Background(), domain.ID); err != nil {
		t.Fatalf("删除域名引用失败: %v", err)
	}
	if err := service.DeleteProviderAccount(context.Background(), account.ID); err != nil {
		t.Fatalf("删除未使用的 DNS 厂商账号失败: %v", err)
	}
}

func TestRecordLifecycleAndReadOnlyProtection(t *testing.T) {
	service, _, provider := newDNSTestService(t)
	provider.records = []libdns.Record{
		libdns.RR{Name: "example.com.", Type: "SOA", Data: "ns.example.com. hostmaster.example.com. 1 3600 600 86400 300", TTL: 300 * time.Second},
		libdns.RR{Name: "example.com.", Type: "NS", Data: "ns.example.com.", TTL: 300 * time.Second},
		libdns.RR{Name: "www.example.com.", Type: "A", Data: "192.0.2.10", TTL: 300 * time.Second},
	}
	account, err := service.CreateProviderAccount(context.Background(), "admin", ProviderAccountInput{
		Name: "测试 DNS", Provider: "test", Config: map[string]string{"token": "very-secret-token"},
	})
	if err != nil {
		t.Fatalf("创建 DNS 厂商账号失败: %v", err)
	}
	domain, err := service.CreateDomain(context.Background(), "admin", DomainInput{AccountID: account.ID, Name: "example.com"})
	if err != nil {
		t.Fatalf("创建域名失败: %v", err)
	}
	records, err := service.ListRecords(context.Background(), domain.ID)
	if err != nil || len(records) != 3 || records[0].Name != "@" {
		t.Fatalf("读取 DNS 记录失败: records=%+v err=%v", records, err)
	}
	for _, record := range records[:2] {
		if !record.ReadOnly {
			t.Fatalf("权威基础记录未标记只读: %+v", record)
		}
		if err := service.DeleteRecord(context.Background(), domain.ID, record.ID); !errors.Is(err, ErrRecordReadOnly) {
			t.Fatalf("删除只读记录未被拒绝: %v", err)
		}
	}
	created, err := service.CreateRecord(context.Background(), domain.ID, RecordInput{Name: "api", Type: "A", Value: "192.0.2.20", TTL: 300})
	if err != nil || created.Name != "api" {
		t.Fatalf("创建 DNS 记录失败: record=%+v err=%v", created, err)
	}
	if _, err := service.CreateRecord(context.Background(), domain.ID, RecordInput{Name: "api", Type: "A", Value: "192.0.2.20", TTL: 300}); !errors.Is(err, ErrRecordExists) {
		t.Fatalf("重复 DNS 记录未被拒绝: %v", err)
	}
	updated, err := service.UpdateRecord(context.Background(), domain.ID, created.ID, RecordInput{Name: "api", Type: "A", Value: "192.0.2.21", TTL: 600})
	if err != nil || updated.Value != "192.0.2.21" || updated.ID == created.ID {
		t.Fatalf("更新 DNS 记录失败: record=%+v err=%v", updated, err)
	}
	if _, err := service.UpdateRecord(context.Background(), domain.ID, updated.ID, RecordInput{Name: "new-api", Type: "A", Value: "192.0.2.21", TTL: 600}); !errors.Is(err, ErrRecordIdentityChange) {
		t.Fatalf("直接修改记录标识未被拒绝: %v", err)
	}
	if err := service.DeleteRecord(context.Background(), domain.ID, updated.ID); err != nil {
		t.Fatalf("删除 DNS 记录失败: %v", err)
	}
}

func TestProviderErrorIsWrappedForSafeHTTPMapping(t *testing.T) {
	service, _, provider := newDNSTestService(t)
	account, _ := service.CreateProviderAccount(context.Background(), "admin", ProviderAccountInput{
		Name: "测试 DNS", Provider: "test", Config: map[string]string{"token": "very-secret-token"},
	})
	domain, _ := service.CreateDomain(context.Background(), "admin", DomainInput{AccountID: account.ID, Name: "example.com"})
	provider.err = errors.New("upstream address and internal diagnostic")
	_, err := service.ListRecords(context.Background(), domain.ID)
	if !errors.Is(err, ErrProviderRequest) {
		t.Fatalf("厂商错误未包装为稳定错误: %v", err)
	}
	provider.err = errors.New("request failed: Authorization: Bearer leaked-token Signature=leaked-signature")
	_, err = service.ListRecords(context.Background(), domain.ID)
	if err == nil || strings.Contains(err.Error(), "leaked-token") || strings.Contains(err.Error(), "leaked-signature") {
		t.Fatalf("厂商错误中的凭据未脱敏: %v", err)
	}
}

func TestUnsafeMultiValueRecordSetMutationsAreRejected(t *testing.T) {
	service, _, provider := newDNSTestServiceForProvider(t, model.DNSProviderAzure)
	provider.records = []libdns.Record{
		libdns.RR{Name: "www", Type: "A", Data: "192.0.2.10", TTL: 300 * time.Second},
		libdns.RR{Name: "www", Type: "A", Data: "192.0.2.11", TTL: 300 * time.Second},
	}
	account, err := service.CreateProviderAccount(context.Background(), "admin", ProviderAccountInput{
		Name: "Azure DNS", Provider: model.DNSProviderAzure, Config: map[string]string{"token": "very-secret-token"},
	})
	if err != nil {
		t.Fatalf("创建测试 DNS 厂商账号失败: %v", err)
	}
	domain, err := service.CreateDomain(context.Background(), "admin", DomainInput{AccountID: account.ID, Name: "example.com"})
	if err != nil {
		t.Fatalf("创建测试域名失败: %v", err)
	}
	firstID := recordID(canonicalRR(provider.records[0].RR(), domain.Name))
	if _, err := service.CreateRecord(context.Background(), domain.ID, RecordInput{Name: "www", Type: "A", Value: "192.0.2.12", TTL: 300}); !errors.Is(err, ErrProviderRecordSetLimit) {
		t.Fatalf("不安全的多值记录新增未被拒绝: %v", err)
	}
	if _, err := service.UpdateRecord(context.Background(), domain.ID, firstID, RecordInput{Name: "www", Type: "A", Value: "192.0.2.12", TTL: 300}); !errors.Is(err, ErrProviderRecordSetLimit) {
		t.Fatalf("不安全的多值记录修改未被拒绝: %v", err)
	}
	if err := service.DeleteRecord(context.Background(), domain.ID, firstID); !errors.Is(err, ErrProviderRecordSetLimit) {
		t.Fatalf("不安全的多值记录删除未被拒绝: %v", err)
	}
	if len(provider.records) != 2 {
		t.Fatalf("拒绝操作后记录集被意外修改: %+v", provider.records)
	}
}

func TestPreciseReplacementRestoresOldRecordWhenCreateFails(t *testing.T) {
	service, _, provider := newDNSTestServiceForProvider(t, model.DNSProviderGandi)
	provider.records = []libdns.Record{
		libdns.RR{Name: "api", Type: "A", Data: "192.0.2.10", TTL: 300 * time.Second},
	}
	provider.appendErr = errors.New("temporary create failure")
	provider.appendFailures = 1
	account, err := service.CreateProviderAccount(context.Background(), "admin", ProviderAccountInput{
		Name: "Gandi DNS", Provider: model.DNSProviderGandi, Config: map[string]string{"token": "very-secret-token"},
	})
	if err != nil {
		t.Fatalf("创建测试 DNS 厂商账号失败: %v", err)
	}
	domain, err := service.CreateDomain(context.Background(), "admin", DomainInput{AccountID: account.ID, Name: "example.com"})
	if err != nil {
		t.Fatalf("创建测试域名失败: %v", err)
	}
	recordID := recordID(canonicalRR(provider.records[0].RR(), domain.Name))
	if _, err := service.UpdateRecord(context.Background(), domain.ID, recordID, RecordInput{Name: "api", Type: "A", Value: "192.0.2.11", TTL: 300}); !errors.Is(err, ErrProviderRequest) {
		t.Fatalf("精确替换失败未返回稳定厂商错误: %v", err)
	}
	if len(provider.records) != 1 || provider.records[0].RR().Data != "192.0.2.10" {
		t.Fatalf("新记录创建失败后未恢复旧记录: %+v", provider.records)
	}
}

func TestEmptyTXTRecordIsRejected(t *testing.T) {
	if _, err := normalizeRecord(RecordInput{Name: "@", Type: "TXT", Value: "", TTL: 300}, "example.com"); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("空 TXT 记录未被拒绝: %v", err)
	}
}

func newDNSTestService(t *testing.T) (*Service, *gorm.DB, *fakeDNSProvider) {
	return newDNSTestServiceForProvider(t, "test")
}

func newDNSTestServiceForProvider(t *testing.T, providerCode model.DNSProvider) (*Service, *gorm.DB, *fakeDNSProvider) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.DNSProviderAccount{}, &model.DNSDomain{}); err != nil {
		t.Fatalf("初始化 DNS 测试表失败: %v", err)
	}
	manager, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("初始化测试密钥失败: %v", err)
	}
	provider := &fakeDNSProvider{}
	registry := &Registry{definitions: map[model.DNSProvider]ProviderDefinition{}, factories: map[model.DNSProvider]providerFactory{}}
	registry.register(ProviderDefinition{
		Code: providerCode, Name: "测试 DNS", Fields: []FieldDefinition{{Key: "token", Label: "Token", Secret: true, Required: true}},
	}, func(map[string]string) RecordProvider { return provider })
	return NewService(db, manager, registry), db, provider
}
