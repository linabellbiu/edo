package dnsmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	digitaloceandns "github.com/libdns/digitalocean"
	"github.com/libdns/libdns"
	"golang.org/x/net/idna"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"zrt/internal/model"
	"zrt/internal/secret"
)

var (
	ErrInvalidProviderAccount = errors.New("DNS 厂商账号信息无效")
	ErrInvalidProviderConfig  = errors.New("DNS 厂商凭据配置无效")
	ErrProviderAccountExists  = errors.New("DNS 厂商账号名称已存在")
	ErrProviderAccountMissing = errors.New("DNS 厂商账号不存在")
	ErrProviderAccountInUse   = errors.New("DNS 厂商账号仍被域名使用，不能删除")
	ErrProviderAccountOff     = errors.New("DNS 厂商账号已停用")
	ErrInvalidDomain          = errors.New("域名信息无效")
	ErrDomainExists           = errors.New("该厂商账号下已存在此域名")
	ErrDomainNotFound         = errors.New("域名不存在")
	ErrDomainDisabled         = errors.New("域名已停用")
	ErrInvalidRecord          = errors.New("DNS 解析记录格式无效")
	ErrRecordExists           = errors.New("相同 DNS 解析记录已存在")
	ErrRecordNotFound         = errors.New("DNS 解析记录不存在或已变化，请刷新后重试")
	ErrRecordReadOnly         = errors.New("SOA 与根域名 NS 记录只能在 DNS 厂商控制台维护")
	ErrRecordIdentityChange   = errors.New("修改主机记录或记录类型时，请先删除旧记录再创建新记录")
	ErrProviderRecordSetLimit = errors.New("当前 DNS 厂商无法通过 ZRT 安全管理此多值记录集，请在厂商控制台处理")
	ErrProviderRequest        = errors.New("DNS 厂商接口调用失败")
)

var accountNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_. -]{1,127}$`)
var providerSecretPattern = regexp.MustCompile(`(?i)(authorization|x-auth-key|api[_-]?key|accesskeyid|secretid|signature|token)(\s*[:=]\s*)([^&\s,}\]]+)`)
var bearerSecretPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,}\]]+`)

var writableRecordTypes = map[string]struct{}{
	"A": {}, "AAAA": {}, "CAA": {}, "CNAME": {}, "HTTPS": {}, "MX": {}, "NAPTR": {},
	"NS": {}, "PTR": {}, "SRV": {}, "SSHFP": {}, "SVCB": {}, "TLSA": {}, "TXT": {}, "URI": {},
}

const providerRequestTimeout = 30 * time.Second

type ProviderAccountInput struct {
	Name              string
	Provider          model.DNSProvider
	Config            map[string]string
	ClearSecretFields []string
}

type DomainInput struct {
	AccountID   string
	Name        string
	Description string
}

type RecordInput struct {
	Name  string
	Type  string
	Value string
	TTL   int64
}

type Record struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	FQDN     string `json:"fqdn"`
	Type     string `json:"type"`
	Value    string `json:"value"`
	TTL      int64  `json:"ttl"`
	ReadOnly bool   `json:"read_only"`
}

type recordMutationStrategy uint8

const (
	replaceRecordSet recordMutationStrategy = iota
	replaceByProviderID
	replacePrecisely
	replaceSingleRecordSet
)

type Service struct {
	db       *gorm.DB
	secrets  *secret.Manager
	registry *Registry
	locks    sync.Map
}

func NewService(db *gorm.DB, secrets *secret.Manager, registry *Registry) *Service {
	return &Service{db: db, secrets: secrets, registry: registry}
}

func (s *Service) ProviderCatalog() []ProviderDefinition { return s.registry.Catalog() }

func (s *Service) ListProviderAccounts(ctx context.Context) ([]model.DNSProviderAccount, error) {
	var accounts []model.DNSProviderAccount
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&accounts).Error; err != nil {
		return nil, fmt.Errorf("查询 DNS 厂商账号失败: %w", err)
	}
	return accounts, nil
}

func (s *Service) FindProviderAccount(ctx context.Context, id string) (*model.DNSProviderAccount, error) {
	var account model.DNSProviderAccount
	if err := s.db.WithContext(ctx).First(&account, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProviderAccountMissing
		}
		return nil, fmt.Errorf("查询 DNS 厂商账号失败: %w", err)
	}
	return &account, nil
}

func (s *Service) CreateProviderAccount(ctx context.Context, actorID string, input ProviderAccountInput) (*model.DNSProviderAccount, error) {
	input.Name = strings.TrimSpace(input.Name)
	if !accountNamePattern.MatchString(input.Name) {
		return nil, ErrInvalidProviderAccount
	}
	publicConfig, secretConfig, secretFields, err := s.registry.SplitConfig(input.Provider, input.Config, nil, nil, input.ClearSecretFields)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	publicJSON, secretCiphertext, secretFieldsJSON, err := s.encodeAccountConfig(id, publicConfig, secretConfig, secretFields)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	account := &model.DNSProviderAccount{
		ID: id, Name: input.Name, Provider: input.Provider, PublicConfig: publicJSON,
		SecretCiphertext: secretCiphertext, ConfiguredSecretFields: secretFieldsJSON,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(account).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrProviderAccountExists
		}
		return nil, fmt.Errorf("创建 DNS 厂商账号失败: %w", err)
	}
	return account, nil
}

func (s *Service) UpdateProviderAccount(ctx context.Context, id string, input ProviderAccountInput) (*model.DNSProviderAccount, error) {
	existing, err := s.FindProviderAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	input.Name = strings.TrimSpace(input.Name)
	if !accountNamePattern.MatchString(input.Name) || (input.Provider != "" && input.Provider != existing.Provider) {
		return nil, ErrInvalidProviderAccount
	}
	existingPublic, existingSecrets, err := s.decodeAccountConfig(existing)
	if err != nil {
		return nil, err
	}
	publicConfig, secretConfig, secretFields, err := s.registry.SplitConfig(
		existing.Provider, input.Config, existingPublic, existingSecrets, input.ClearSecretFields,
	)
	if err != nil {
		return nil, err
	}
	publicJSON, secretCiphertext, secretFieldsJSON, err := s.encodeAccountConfig(id, publicConfig, secretConfig, secretFields)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"name": input.Name, "public_config": publicJSON, "secret_ciphertext": secretCiphertext,
		"configured_secret_fields": secretFieldsJSON, "updated_at": now,
	}
	if err := s.db.WithContext(ctx).Model(existing).Updates(updates).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrProviderAccountExists
		}
		return nil, fmt.Errorf("更新 DNS 厂商账号失败: %w", err)
	}
	existing.Name, existing.PublicConfig, existing.SecretCiphertext = input.Name, publicJSON, secretCiphertext
	existing.ConfiguredSecretFields, existing.UpdatedAt = secretFieldsJSON, now
	return existing, nil
}

func (s *Service) SetProviderAccountActive(ctx context.Context, id string, active bool) error {
	result := s.db.WithContext(ctx).Model(&model.DNSProviderAccount{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改 DNS 厂商账号状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrProviderAccountMissing
	}
	return nil
}

func (s *Service) DeleteProviderAccount(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.DNSProviderAccount
		if err := tx.First(&account, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProviderAccountMissing
			}
			return fmt.Errorf("查询待删除 DNS 厂商账号失败: %w", err)
		}
		var count int64
		if err := tx.Model(&model.DNSDomain{}).Where("account_id = ?", id).Count(&count).Error; err != nil {
			return fmt.Errorf("检查 DNS 厂商账号使用状态失败: %w", err)
		}
		if count > 0 {
			return ErrProviderAccountInUse
		}
		if err := tx.Delete(&account).Error; err != nil {
			return fmt.Errorf("删除 DNS 厂商账号失败: %w", err)
		}
		return nil
	})
}

func (s *Service) PublicConfig(account *model.DNSProviderAccount) (map[string]string, []string, error) {
	publicConfig := map[string]string{}
	if len(account.PublicConfig) > 0 {
		if err := json.Unmarshal(account.PublicConfig, &publicConfig); err != nil {
			return nil, nil, fmt.Errorf("解析 DNS 厂商公开配置失败: %w", err)
		}
	}
	secretFields := []string{}
	if len(account.ConfiguredSecretFields) > 0 {
		if err := json.Unmarshal(account.ConfiguredSecretFields, &secretFields); err != nil {
			return nil, nil, fmt.Errorf("解析 DNS 厂商凭据状态失败: %w", err)
		}
	}
	return publicConfig, secretFields, nil
}

func (s *Service) ListDomains(ctx context.Context) ([]model.DNSDomain, error) {
	var domains []model.DNSDomain
	if err := s.db.WithContext(ctx).Preload("Account").Order("name ASC").Find(&domains).Error; err != nil {
		return nil, fmt.Errorf("查询域名列表失败: %w", err)
	}
	return domains, nil
}

func (s *Service) FindDomain(ctx context.Context, id string) (*model.DNSDomain, error) {
	var domain model.DNSDomain
	if err := s.db.WithContext(ctx).Preload("Account").First(&domain, "dns_domains.id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDomainNotFound
		}
		return nil, fmt.Errorf("查询域名失败: %w", err)
	}
	return &domain, nil
}

func (s *Service) CreateDomain(ctx context.Context, actorID string, input DomainInput) (*model.DNSDomain, error) {
	account, err := s.FindProviderAccount(ctx, strings.TrimSpace(input.AccountID))
	if err != nil {
		return nil, err
	}
	if !account.IsActive {
		return nil, ErrProviderAccountOff
	}
	name, description, err := normalizeDomainInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	domain := &model.DNSDomain{
		ID: uuid.NewString(), AccountID: account.ID, Name: name, Description: description,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now, Account: *account,
	}
	if err := s.db.WithContext(ctx).Create(domain).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrDomainExists
		}
		return nil, fmt.Errorf("创建域名失败: %w", err)
	}
	return domain, nil
}

func (s *Service) UpdateDomain(ctx context.Context, id string, input DomainInput) (*model.DNSDomain, error) {
	existing, err := s.FindDomain(ctx, id)
	if err != nil {
		return nil, err
	}
	account, err := s.FindProviderAccount(ctx, strings.TrimSpace(input.AccountID))
	if err != nil {
		return nil, err
	}
	if !account.IsActive {
		return nil, ErrProviderAccountOff
	}
	name, description, err := normalizeDomainInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	updates := map[string]any{"account_id": account.ID, "name": name, "description": description, "updated_at": now}
	if err := s.db.WithContext(ctx).Model(existing).Updates(updates).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrDomainExists
		}
		return nil, fmt.Errorf("更新域名失败: %w", err)
	}
	existing.AccountID, existing.Name, existing.Description = account.ID, name, description
	existing.Account, existing.UpdatedAt = *account, now
	return existing, nil
}

func (s *Service) SetDomainActive(ctx context.Context, id string, active bool) error {
	result := s.db.WithContext(ctx).Model(&model.DNSDomain{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改域名状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrDomainNotFound
	}
	return nil
}

func (s *Service) DeleteDomain(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Delete(&model.DNSDomain{}, "id = ?", id)
	if result.Error != nil {
		return fmt.Errorf("删除域名失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrDomainNotFound
	}
	return nil
}

func (s *Service) ListRecords(ctx context.Context, domainID string) ([]Record, error) {
	lock := s.domainLock(domainID)
	lock.Lock()
	defer lock.Unlock()
	domain, provider, zone, err := s.domainProvider(ctx, domainID)
	if err != nil {
		return nil, err
	}
	providerCtx, cancel := context.WithTimeout(ctx, providerRequestTimeout)
	defer cancel()
	records, err := provider.GetRecords(providerCtx, zone)
	if err != nil {
		return nil, providerFailure(domain.Account.Provider, "list_records", err)
	}
	return toRecords(domain.Name, records), nil
}

func (s *Service) CreateRecord(ctx context.Context, domainID string, input RecordInput) (*Record, error) {
	lock := s.domainLock(domainID)
	lock.Lock()
	defer lock.Unlock()
	domain, provider, zone, err := s.domainProvider(ctx, domainID)
	if err != nil {
		return nil, err
	}
	record, err := normalizeRecord(input, domain.Name)
	if err != nil {
		return nil, err
	}
	if isReadOnlyRecord(record.RR()) {
		return nil, ErrRecordReadOnly
	}
	providerCtx, cancel := context.WithTimeout(ctx, providerRequestTimeout)
	defer cancel()
	existing, err := provider.GetRecords(providerCtx, zone)
	if err != nil {
		return nil, providerFailure(domain.Account.Provider, "read_before_create", err)
	}
	newRR := canonicalRR(record.RR(), domain.Name)
	groupSize := 0
	for _, item := range existing {
		rr := canonicalRR(item.RR(), domain.Name)
		if rr.Name == newRR.Name && rr.Type == newRR.Type {
			groupSize++
		}
		if sameRecordValue(rr, newRR) {
			return nil, ErrRecordExists
		}
	}
	if groupSize > 0 && mutationStrategy(domain.Account.Provider, newRR.Type) == replaceSingleRecordSet {
		return nil, ErrProviderRecordSetLimit
	}
	created, err := provider.AppendRecords(providerCtx, zone, []libdns.Record{record})
	if err != nil {
		return nil, providerFailure(domain.Account.Provider, "create_record", err)
	}
	if len(created) > 0 {
		result := toRecord(domain.Name, created[0].RR())
		return &result, nil
	}
	result := toRecord(domain.Name, record.RR())
	return &result, nil
}

func (s *Service) UpdateRecord(ctx context.Context, domainID, recordID string, input RecordInput) (*Record, error) {
	lock := s.domainLock(domainID)
	lock.Lock()
	defer lock.Unlock()
	domain, provider, zone, err := s.domainProvider(ctx, domainID)
	if err != nil {
		return nil, err
	}
	desired, err := normalizeRecord(input, domain.Name)
	if err != nil {
		return nil, err
	}
	providerCtx, cancel := context.WithTimeout(ctx, providerRequestTimeout)
	defer cancel()
	existing, err := provider.GetRecords(providerCtx, zone)
	if err != nil {
		return nil, providerFailure(domain.Account.Provider, "read_before_update", err)
	}
	targetIndex := findRecordIndex(existing, recordID, domain.Name)
	if targetIndex < 0 {
		return nil, ErrRecordNotFound
	}
	target := canonicalRR(existing[targetIndex].RR(), domain.Name)
	if isReadOnlyRecord(target) || isReadOnlyRecord(canonicalRR(desired.RR(), domain.Name)) {
		return nil, ErrRecordReadOnly
	}
	if target.Name != desired.RR().Name || target.Type != desired.RR().Type {
		return nil, ErrRecordIdentityChange
	}
	group := make([]libdns.Record, 0, len(existing))
	for index, item := range existing {
		rr := canonicalRR(item.RR(), domain.Name)
		if rr.Name != target.Name || rr.Type != target.Type {
			continue
		}
		if index == targetIndex {
			group = append(group, desired)
			continue
		}
		if sameRecordValue(rr, desired.RR()) {
			return nil, ErrRecordExists
		}
		rr.TTL = desired.RR().TTL
		parsed, parseErr := rr.Parse()
		if parseErr != nil {
			return nil, fmt.Errorf("规范化 DNS 记录集失败: %w", parseErr)
		}
		group = append(group, parsed)
	}
	var updated []libdns.Record
	switch mutationStrategy(domain.Account.Provider, target.Type) {
	case replaceByProviderID:
		identified, ok := recordWithProviderID(domain.Account.Provider, existing[targetIndex], desired)
		if !ok {
			return nil, ErrProviderRecordSetLimit
		}
		updated, err = provider.SetRecords(providerCtx, zone, []libdns.Record{identified})
	case replacePrecisely:
		updated, err = replaceRecordPrecisely(providerCtx, domain.Name, zone, provider, existing[targetIndex], desired)
	case replaceSingleRecordSet:
		if len(group) != 1 {
			return nil, ErrProviderRecordSetLimit
		}
		updated, err = provider.SetRecords(providerCtx, zone, []libdns.Record{desired})
	default:
		updated, err = provider.SetRecords(providerCtx, zone, group)
	}
	if err != nil {
		return nil, providerFailure(domain.Account.Provider, "update_record", err)
	}
	for _, item := range updated {
		if sameRecordValue(canonicalRR(item.RR(), domain.Name), desired.RR()) {
			result := toRecord(domain.Name, item.RR())
			return &result, nil
		}
	}
	result := toRecord(domain.Name, desired.RR())
	return &result, nil
}

func (s *Service) DeleteRecord(ctx context.Context, domainID, recordID string) error {
	lock := s.domainLock(domainID)
	lock.Lock()
	defer lock.Unlock()
	domain, provider, zone, err := s.domainProvider(ctx, domainID)
	if err != nil {
		return err
	}
	providerCtx, cancel := context.WithTimeout(ctx, providerRequestTimeout)
	defer cancel()
	existing, err := provider.GetRecords(providerCtx, zone)
	if err != nil {
		return providerFailure(domain.Account.Provider, "read_before_delete", err)
	}
	targetIndex := findRecordIndex(existing, recordID, domain.Name)
	if targetIndex < 0 {
		return ErrRecordNotFound
	}
	target := canonicalRR(existing[targetIndex].RR(), domain.Name)
	if isReadOnlyRecord(target) {
		return ErrRecordReadOnly
	}
	if mutationStrategy(domain.Account.Provider, target.Type) == replaceSingleRecordSet && recordGroupSize(existing, target, domain.Name) != 1 {
		return ErrProviderRecordSetLimit
	}
	if _, err := provider.DeleteRecords(providerCtx, zone, []libdns.Record{existing[targetIndex]}); err != nil {
		return providerFailure(domain.Account.Provider, "delete_record", err)
	}
	return nil
}

func (s *Service) domainProvider(ctx context.Context, domainID string) (*model.DNSDomain, RecordProvider, string, error) {
	domain, err := s.FindDomain(ctx, domainID)
	if err != nil {
		return nil, nil, "", err
	}
	if !domain.IsActive {
		return nil, nil, "", ErrDomainDisabled
	}
	if !domain.Account.IsActive {
		return nil, nil, "", ErrProviderAccountOff
	}
	publicConfig, secretConfig, err := s.decodeAccountConfig(&domain.Account)
	if err != nil {
		return nil, nil, "", err
	}
	for key, value := range secretConfig {
		publicConfig[key] = value
	}
	provider, err := s.registry.Build(domain.Account.Provider, publicConfig)
	if err != nil {
		return nil, nil, "", err
	}
	return domain, provider, domain.Name + ".", nil
}

func (s *Service) encodeAccountConfig(
	id string,
	publicConfig, secretConfig map[string]string,
	secretFields []string,
) (datatypes.JSON, string, datatypes.JSON, error) {
	publicJSON, err := json.Marshal(publicConfig)
	if err != nil {
		return nil, "", nil, fmt.Errorf("序列化 DNS 厂商公开配置失败: %w", err)
	}
	secretJSON, err := json.Marshal(secretConfig)
	if err != nil {
		return nil, "", nil, fmt.Errorf("序列化 DNS 厂商凭据失败: %w", err)
	}
	ciphertext, err := s.secrets.Encrypt(string(secretJSON), accountCredentialAAD(id))
	if err != nil {
		return nil, "", nil, fmt.Errorf("加密 DNS 厂商凭据失败: %w", err)
	}
	secretFieldsJSON, err := json.Marshal(secretFields)
	if err != nil {
		return nil, "", nil, fmt.Errorf("序列化 DNS 厂商凭据状态失败: %w", err)
	}
	return datatypes.JSON(publicJSON), ciphertext, datatypes.JSON(secretFieldsJSON), nil
}

func (s *Service) decodeAccountConfig(account *model.DNSProviderAccount) (map[string]string, map[string]string, error) {
	publicConfig := map[string]string{}
	if err := json.Unmarshal(account.PublicConfig, &publicConfig); err != nil {
		return nil, nil, fmt.Errorf("解析 DNS 厂商公开配置失败: %w", err)
	}
	plaintext, err := s.secrets.Decrypt(account.SecretCiphertext, accountCredentialAAD(account.ID))
	if err != nil {
		return nil, nil, fmt.Errorf("解密 DNS 厂商凭据失败: %w", err)
	}
	secretConfig := map[string]string{}
	if err := json.Unmarshal([]byte(plaintext), &secretConfig); err != nil {
		return nil, nil, fmt.Errorf("解析 DNS 厂商凭据失败: %w", err)
	}
	return publicConfig, secretConfig, nil
}

func (s *Service) domainLock(domainID string) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(domainID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func normalizeDomainInput(input DomainInput) (string, string, error) {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.Name), "."))
	ascii, err := idna.Lookup.ToASCII(name)
	if err != nil || !validDomainName(ascii) {
		return "", "", ErrInvalidDomain
	}
	description := strings.TrimSpace(input.Description)
	if utf8.RuneCountInString(description) > 512 {
		return "", "", ErrInvalidDomain
	}
	return ascii, description, nil
}

func validDomainName(name string) bool {
	if name == "" || len(name) > 253 || strings.Contains(name, "..") {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func normalizeRecord(input RecordInput, zone string) (libdns.Record, error) {
	recordType := strings.ToUpper(strings.TrimSpace(input.Type))
	if _, ok := writableRecordTypes[recordType]; !ok || (input.TTL != 1 && (input.TTL < 30 || input.TTL > 604800)) {
		return nil, ErrInvalidRecord
	}
	name := strings.ToLower(strings.TrimSpace(input.Name))
	if strings.HasSuffix(name, ".") {
		fqdn := strings.ToLower(strings.TrimSuffix(name, "."))
		if fqdn != zone && !strings.HasSuffix(fqdn, "."+zone) {
			return nil, ErrInvalidRecord
		}
		name = libdns.RelativeName(fqdn, zone)
	}
	if !validRecordName(name) {
		return nil, ErrInvalidRecord
	}
	value := input.Value
	if recordType != "TXT" {
		value = strings.TrimSpace(value)
	}
	if len(value) > 4096 || value == "" || strings.ContainsRune(value, '\x00') {
		return nil, ErrInvalidRecord
	}
	rr := libdns.RR{Name: name, Type: recordType, Data: value, TTL: time.Duration(input.TTL) * time.Second}
	record, err := rr.Parse()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	return record, nil
}

func validRecordName(name string) bool {
	if name == "@" {
		return true
	}
	if name == "" || len(name) > 253 || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for index, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
				char == '-' || char == '_' || (char == '*' && index == 0 && label == "*") {
				continue
			}
			return false
		}
	}
	return true
}

func toRecords(zone string, records []libdns.Record) []Record {
	result := make([]Record, 0, len(records))
	for _, item := range records {
		result = append(result, toRecord(zone, item.RR()))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			if result[i].Name == "@" {
				return true
			}
			if result[j].Name == "@" {
				return false
			}
			return result[i].Name < result[j].Name
		}
		if result[i].Type != result[j].Type {
			return result[i].Type < result[j].Type
		}
		return result[i].Value < result[j].Value
	})
	return result
}

func toRecord(zone string, rr libdns.RR) Record {
	rr = canonicalRR(rr, zone)
	return Record{
		ID: recordID(rr), Name: rr.Name, FQDN: strings.TrimSuffix(libdns.AbsoluteName(rr.Name, zone), "."),
		Type: rr.Type, Value: rr.Data, TTL: int64(rr.TTL / time.Second), ReadOnly: isReadOnlyRecord(rr),
	}
}

func findRecordIndex(records []libdns.Record, id, zone string) int {
	for index, item := range records {
		if recordID(canonicalRR(item.RR(), zone)) == id {
			return index
		}
	}
	return -1
}

func recordID(rr libdns.RR) string {
	hash := sha256.Sum256([]byte(rr.Name + "\x00" + rr.Type + "\x00" + rr.Data + "\x00" + strconv.FormatInt(int64(rr.TTL), 10)))
	return hex.EncodeToString(hash[:16])
}

func canonicalRR(rr libdns.RR, zone string) libdns.RR {
	name := strings.ToLower(strings.TrimSpace(rr.Name))
	zone = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))
	if name == "" || strings.TrimSuffix(name, ".") == zone {
		name = "@"
	} else if strings.HasSuffix(name, ".") {
		name = libdns.RelativeName(name, zone)
	}
	rr.Name = name
	rr.Type = strings.ToUpper(strings.TrimSpace(rr.Type))
	return rr
}

func sameRR(left, right libdns.RR) bool {
	return left.Name == right.Name && left.Type == right.Type && left.Data == right.Data && left.TTL == right.TTL
}

func sameRecordValue(left, right libdns.RR) bool {
	return left.Name == right.Name && left.Type == right.Type && left.Data == right.Data
}

func recordGroupSize(records []libdns.Record, target libdns.RR, zone string) int {
	size := 0
	for _, item := range records {
		rr := canonicalRR(item.RR(), zone)
		if rr.Name == target.Name && rr.Type == target.Type {
			size++
		}
	}
	return size
}

func mutationStrategy(provider model.DNSProvider, recordType string) recordMutationStrategy {
	switch provider {
	case model.DNSProviderDigitalOcean:
		return replaceByProviderID
	case model.DNSProviderAliDNS, model.DNSProviderTencentCloud, model.DNSProviderGandi:
		return replacePrecisely
	case model.DNSProviderCloudflare:
		if recordType == "SRV" || recordType == "HTTPS" || recordType == "SVCB" {
			return replaceSingleRecordSet
		}
		return replacePrecisely
	case model.DNSProviderAzure, model.DNSProviderHuaweiCloud, model.DNSProviderGoDaddy, model.DNSProviderHetzner:
		return replaceSingleRecordSet
	default:
		return replaceRecordSet
	}
}

func recordWithProviderID(provider model.DNSProvider, current, desired libdns.Record) (libdns.Record, bool) {
	switch provider {
	case model.DNSProviderDigitalOcean:
		existing, ok := current.(digitaloceandns.DNS)
		if !ok || existing.ID == "" {
			return nil, false
		}
		return digitaloceandns.DNS{Record: desired.RR(), ID: existing.ID}, true
	default:
		return nil, false
	}
}

func replaceRecordPrecisely(
	ctx context.Context,
	domain, zone string,
	provider RecordProvider,
	current, desired libdns.Record,
) ([]libdns.Record, error) {
	mutationCtx := ctx
	if _, err := provider.DeleteRecords(mutationCtx, zone, []libdns.Record{current}); err != nil {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerRequestTimeout)
		defer cancel()
		records, inspectErr := provider.GetRecords(recoveryCtx, zone)
		if inspectErr != nil {
			return nil, fmt.Errorf("删除旧记录结果不确定: mutation_err=%v inspect_err=%v", err, inspectErr)
		}
		if containsRecordValue(records, current.RR(), domain) {
			return nil, err
		}
		mutationCtx = recoveryCtx
	}

	created, err := provider.AppendRecords(mutationCtx, zone, []libdns.Record{desired})
	if err == nil {
		return created, nil
	}

	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerRequestTimeout)
	defer cancel()
	records, inspectErr := provider.GetRecords(recoveryCtx, zone)
	if inspectErr != nil {
		return nil, fmt.Errorf("创建新记录结果不确定: mutation_err=%v inspect_err=%v", err, inspectErr)
	}
	if containsRecordValue(records, desired.RR(), domain) {
		return []libdns.Record{desired}, nil
	}
	if containsRecordValue(records, current.RR(), domain) {
		return nil, err
	}
	if _, restoreErr := provider.AppendRecords(recoveryCtx, zone, []libdns.Record{current}); restoreErr != nil {
		return nil, fmt.Errorf("替换记录失败且补偿恢复失败: mutation_err=%v restore_err=%v", err, restoreErr)
	}
	return nil, err
}

func containsRecordValue(records []libdns.Record, wanted libdns.RR, zone string) bool {
	wanted = canonicalRR(wanted, zone)
	for _, item := range records {
		if sameRecordValue(canonicalRR(item.RR(), zone), wanted) {
			return true
		}
	}
	return false
}

func isReadOnlyRecord(rr libdns.RR) bool {
	return rr.Type == "SOA" || (rr.Type == "NS" && (rr.Name == "" || rr.Name == "@"))
}

func accountCredentialAAD(id string) []byte {
	return []byte("dns_provider_account:" + id + ":credentials")
}

func providerFailure(provider model.DNSProvider, operation string, err error) error {
	diagnostic := bearerSecretPattern.ReplaceAllString(err.Error(), "$1[REDACTED]")
	diagnostic = providerSecretPattern.ReplaceAllString(diagnostic, "$1$2[REDACTED]")
	if len(diagnostic) > 2048 {
		diagnostic = diagnostic[:2048]
	}
	return fmt.Errorf("%w: provider=%s operation=%s err=%s", ErrProviderRequest, provider, operation, diagnostic)
}
