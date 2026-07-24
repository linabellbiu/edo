package dnsmanager

import (
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"

	alidns "github.com/libdns/alidns"
	azuredns "github.com/libdns/azure"
	cloudflaredns "github.com/libdns/cloudflare"
	digitaloceandns "github.com/libdns/digitalocean"
	gandidns "github.com/libdns/gandi"
	godaddydns "github.com/libdns/godaddy"
	googleclouddns "github.com/libdns/googleclouddns"
	hetznerdns "github.com/libdns/hetzner"
	huaweiclouddns "github.com/libdns/huaweicloud"
	"github.com/libdns/libdns"
	namecheapdns "github.com/libdns/namecheap"
	powerdnsprovider "github.com/libdns/powerdns"
	rfc2136provider "github.com/libdns/rfc2136"
	route53provider "github.com/libdns/route53"
	tencentclouddns "github.com/libdns/tencentcloud"

	"zrt/internal/model"
)

var ErrUnsupportedProvider = errors.New("不支持该 DNS 厂商")

type RecordProvider interface {
	libdns.RecordGetter
	libdns.RecordAppender
	libdns.RecordSetter
	libdns.RecordDeleter
}

type FieldDefinition struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
	Multiline   bool   `json:"multiline,omitempty"`
}

type ProviderDefinition struct {
	Code        model.DNSProvider `json:"code"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Fields      []FieldDefinition `json:"fields"`
}

type providerFactory func(map[string]string) RecordProvider

type Registry struct {
	definitions map[model.DNSProvider]ProviderDefinition
	factories   map[model.DNSProvider]providerFactory
	order       []model.DNSProvider
}

func NewRegistry() *Registry {
	r := &Registry{
		definitions: make(map[model.DNSProvider]ProviderDefinition),
		factories:   make(map[model.DNSProvider]providerFactory),
	}
	r.register(ProviderDefinition{
		Code: model.DNSProviderCloudflare, Name: "Cloudflare", Description: "使用最小权限 API Token 管理 Cloudflare DNS Zone。",
		Fields: []FieldDefinition{
			{Key: "api_token", Label: "DNS API Token", Secret: true, Required: true, Placeholder: "需要 Zone:Read 与 DNS:Edit 权限"},
			{Key: "zone_token", Label: "Zone 查询 Token", Secret: true, Help: "可选；使用独立的 Zone:Read Token。"},
		},
	}, func(c map[string]string) RecordProvider {
		return &cloudflaredns.Provider{APIToken: c["api_token"], ZoneToken: c["zone_token"]}
	})
	r.register(ProviderDefinition{
		Code: model.DNSProviderAliDNS, Name: "阿里云 DNS", Description: "支持阿里云 RAM AccessKey 与临时 STS 凭据。",
		Fields: []FieldDefinition{
			{Key: "access_key_id", Label: "AccessKey ID", Required: true},
			{Key: "access_key_secret", Label: "AccessKey Secret", Secret: true, Required: true},
			{Key: "security_token", Label: "STS Security Token", Secret: true, Help: "使用临时 RAM 凭据时填写。"},
		},
	}, func(c map[string]string) RecordProvider {
		return &alidns.Provider{CredentialInfo: alidns.CredentialInfo{
			AccessKeyID: c["access_key_id"], AccessKeySecret: c["access_key_secret"], SecurityToken: c["security_token"],
		}}
	})
	r.register(ProviderDefinition{
		Code: model.DNSProviderTencentCloud, Name: "腾讯云 DNSPod", Description: "通过腾讯云 API 3.0 管理 DNSPod 公网域名解析。",
		Fields: []FieldDefinition{
			{Key: "secret_id", Label: "Secret ID", Required: true},
			{Key: "secret_key", Label: "Secret Key", Secret: true, Required: true},
			{Key: "session_token", Label: "临时 Token", Secret: true},
			{Key: "region", Label: "区域", Default: "ap-guangzhou", Placeholder: "ap-guangzhou"},
		},
	}, func(c map[string]string) RecordProvider {
		return &tencentclouddns.Provider{SecretId: c["secret_id"], SecretKey: c["secret_key"], SessionToken: c["session_token"], Region: c["region"]}
	})
	r.register(ProviderDefinition{
		Code: model.DNSProviderRoute53, Name: "AWS Route 53", Description: "管理 AWS Route 53 公有托管区域的普通记录集。",
		Fields: []FieldDefinition{
			{Key: "access_key_id", Label: "Access Key ID", Required: true},
			{Key: "secret_access_key", Label: "Secret Access Key", Secret: true, Required: true},
			{Key: "session_token", Label: "Session Token", Secret: true},
			{Key: "region", Label: "区域", Default: "us-east-1"},
			{Key: "hosted_zone_id", Label: "Hosted Zone ID", Help: "可选；同名 Zone 较多时建议明确指定。"},
		},
	}, func(c map[string]string) RecordProvider {
		return &route53provider.Provider{
			Region: c["region"], AccessKeyId: c["access_key_id"], SecretAccessKey: c["secret_access_key"],
			SessionToken: c["session_token"], HostedZoneID: c["hosted_zone_id"], MaxRetries: 1,
		}
	})
	r.register(ProviderDefinition{
		Code: model.DNSProviderHuaweiCloud, Name: "华为云 DNS", Description: "管理华为云 DNS 公网 Zone 与记录集。",
		Fields: []FieldDefinition{
			{Key: "access_key_id", Label: "Access Key ID", Required: true},
			{Key: "secret_access_key", Label: "Secret Access Key", Secret: true, Required: true},
			{Key: "region_id", Label: "区域", Default: "cn-south-1"},
		},
	}, func(c map[string]string) RecordProvider {
		return &huaweiclouddns.Provider{AccessKeyId: c["access_key_id"], SecretAccessKey: c["secret_access_key"], RegionId: c["region_id"]}
	})
	r.register(ProviderDefinition{
		Code: model.DNSProviderAzure, Name: "Azure DNS", Description: "使用 Microsoft Entra 服务主体管理 Azure DNS Zone。",
		Fields: []FieldDefinition{
			{Key: "subscription_id", Label: "Subscription ID", Required: true},
			{Key: "resource_group_name", Label: "资源组", Required: true},
			{Key: "tenant_id", Label: "Tenant ID", Required: true},
			{Key: "client_id", Label: "Client ID", Required: true},
			{Key: "client_secret", Label: "Client Secret", Secret: true, Required: true},
		},
	}, func(c map[string]string) RecordProvider {
		return &azuredns.Provider{
			SubscriptionId: c["subscription_id"], ResourceGroupName: c["resource_group_name"],
			TenantId: c["tenant_id"], ClientId: c["client_id"], ClientSecret: c["client_secret"],
		}
	})
	r.register(ProviderDefinition{
		Code: model.DNSProviderGoogleCloud, Name: "Google Cloud DNS", Description: "使用服务账号管理 Google Cloud DNS 托管区域。",
		Fields: []FieldDefinition{
			{Key: "project", Label: "GCP Project ID", Required: true},
			{Key: "service_account_json", Label: "服务账号 JSON", Secret: true, Required: true, Multiline: true},
		},
	}, func(c map[string]string) RecordProvider {
		return &googleclouddns.Provider{Project: c["project"], ServiceAccountJSON: c["service_account_json"]}
	})
	r.register(ProviderDefinition{
		Code: model.DNSProviderDigitalOcean, Name: "DigitalOcean", Description: "管理 DigitalOcean Networking 中的域名记录。",
		Fields: []FieldDefinition{{Key: "api_token", Label: "Personal Access Token", Secret: true, Required: true}},
	}, func(c map[string]string) RecordProvider { return &digitaloceandns.Provider{APIToken: c["api_token"]} })
	r.register(ProviderDefinition{
		Code: model.DNSProviderGandi, Name: "Gandi LiveDNS", Description: "使用 Personal Access Token 管理 Gandi LiveDNS。",
		Fields: []FieldDefinition{{Key: "bearer_token", Label: "Personal Access Token", Secret: true, Required: true}},
	}, func(c map[string]string) RecordProvider { return &gandidns.Provider{BearerToken: c["bearer_token"]} })
	r.register(ProviderDefinition{
		Code: model.DNSProviderGoDaddy, Name: "GoDaddy", Description: "通过 Domains API 管理 GoDaddy DNS 记录。",
		Fields: []FieldDefinition{{Key: "api_token", Label: "API Key 与 Secret", Secret: true, Required: true, Placeholder: "key:secret"}},
	}, func(c map[string]string) RecordProvider { return &godaddydns.Provider{APIToken: c["api_token"]} })
	r.register(ProviderDefinition{
		Code: model.DNSProviderNamecheap, Name: "Namecheap", Description: "通过已加入白名单的固定出口 IP 管理 Namecheap Host Records。",
		Fields: []FieldDefinition{
			{Key: "user", Label: "API 用户名", Required: true},
			{Key: "api_key", Label: "API Key", Secret: true, Required: true},
			{Key: "client_ip", Label: "白名单出口 IPv4", Required: true},
		},
	}, func(c map[string]string) RecordProvider {
		return &namecheapdns.Provider{APIKey: c["api_key"], User: c["user"], ClientIP: c["client_ip"]}
	})
	r.register(ProviderDefinition{
		Code: model.DNSProviderHetzner, Name: "Hetzner DNS", Description: "使用 Hetzner DNS API Token 管理 Zone。",
		Fields: []FieldDefinition{{Key: "api_token", Label: "Auth API Token", Secret: true, Required: true}},
	}, func(c map[string]string) RecordProvider { return &hetznerdns.Provider{AuthAPIToken: c["api_token"]} })
	r.register(ProviderDefinition{
		Code: model.DNSProviderPowerDNS, Name: "PowerDNS", Description: "连接自托管 PowerDNS Authoritative HTTP API。",
		Fields: []FieldDefinition{
			{Key: "server_url", Label: "Server URL", Required: true, Placeholder: "https://dns-api.example.com"},
			{Key: "server_id", Label: "Server ID", Default: "localhost"},
			{Key: "api_token", Label: "API Token", Secret: true, Required: true},
		},
	}, func(c map[string]string) RecordProvider {
		return &powerdnsprovider.Provider{ServerURL: c["server_url"], ServerID: c["server_id"], APIToken: c["api_token"]}
	})
	r.register(ProviderDefinition{
		Code: model.DNSProviderRFC2136, Name: "RFC 2136 / TSIG", Description: "连接支持动态更新与 AXFR 的标准权威 DNS 服务。",
		Fields: []FieldDefinition{
			{Key: "server", Label: "DNS 服务器", Required: true, Placeholder: "dns.example.com:53"},
			{Key: "key_name", Label: "TSIG Key Name", Required: true},
			{Key: "key_alg", Label: "TSIG 算法", Default: "hmac-sha256"},
			{Key: "key", Label: "TSIG Secret", Secret: true, Required: true},
		},
	}, func(c map[string]string) RecordProvider {
		return &rfc2136provider.Provider{Server: c["server"], KeyName: c["key_name"], KeyAlg: c["key_alg"], Key: c["key"]}
	})
	return r
}

func (r *Registry) register(definition ProviderDefinition, factory providerFactory) {
	r.definitions[definition.Code] = definition
	r.factories[definition.Code] = factory
	r.order = append(r.order, definition.Code)
}

func (r *Registry) Catalog() []ProviderDefinition {
	result := make([]ProviderDefinition, 0, len(r.order))
	for _, code := range r.order {
		definition := r.definitions[code]
		definition.Fields = append([]FieldDefinition(nil), definition.Fields...)
		result = append(result, definition)
	}
	return result
}

func (r *Registry) Build(provider model.DNSProvider, config map[string]string) (RecordProvider, error) {
	factory, ok := r.factories[provider]
	if !ok {
		return nil, ErrUnsupportedProvider
	}
	return factory(config), nil
}

func (r *Registry) SplitConfig(
	provider model.DNSProvider,
	incoming, existingPublic, existingSecrets map[string]string,
	clearSecretFields []string,
) (map[string]string, map[string]string, []string, error) {
	definition, ok := r.definitions[provider]
	if !ok {
		return nil, nil, nil, ErrUnsupportedProvider
	}
	allowed := make(map[string]FieldDefinition, len(definition.Fields))
	for _, field := range definition.Fields {
		allowed[field.Key] = field
	}
	for key := range incoming {
		if _, ok := allowed[key]; !ok {
			return nil, nil, nil, ErrInvalidProviderConfig
		}
	}
	clear := make(map[string]struct{}, len(clearSecretFields))
	for _, key := range clearSecretFields {
		field, ok := allowed[key]
		if !ok || !field.Secret || field.Required {
			return nil, nil, nil, ErrInvalidProviderConfig
		}
		clear[key] = struct{}{}
	}
	publicConfig := cloneConfig(existingPublic)
	secretConfig := cloneConfig(existingSecrets)
	totalLength := 0
	for _, field := range definition.Fields {
		value, supplied := incoming[field.Key]
		value = strings.TrimSpace(value)
		if len(value) > 64*1024 {
			return nil, nil, nil, ErrInvalidProviderConfig
		}
		if field.Secret {
			if _, shouldClear := clear[field.Key]; shouldClear {
				delete(secretConfig, field.Key)
			} else if supplied && value != "" {
				secretConfig[field.Key] = value
			}
		} else if supplied {
			if value == "" {
				delete(publicConfig, field.Key)
			} else {
				publicConfig[field.Key] = value
			}
		}
		if field.Default != "" {
			target := publicConfig
			if field.Secret {
				target = secretConfig
			}
			if target[field.Key] == "" {
				target[field.Key] = field.Default
			}
		}
		configured := publicConfig[field.Key]
		if field.Secret {
			configured = secretConfig[field.Key]
		}
		if field.Required && configured == "" {
			return nil, nil, nil, ErrInvalidProviderConfig
		}
		totalLength += len(configured)
	}
	if totalLength > 128*1024 || !validProviderConfig(provider, publicConfig, secretConfig) {
		return nil, nil, nil, ErrInvalidProviderConfig
	}
	configuredSecretFields := make([]string, 0, len(secretConfig))
	for key, value := range secretConfig {
		if value != "" {
			configuredSecretFields = append(configuredSecretFields, key)
		}
	}
	sort.Strings(configuredSecretFields)
	return publicConfig, secretConfig, configuredSecretFields, nil
}

func validProviderConfig(provider model.DNSProvider, publicConfig, secretConfig map[string]string) bool {
	merged := cloneConfig(publicConfig)
	for key, value := range secretConfig {
		merged[key] = value
	}
	switch provider {
	case model.DNSProviderPowerDNS:
		parsed, err := url.Parse(merged["server_url"])
		return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
	case model.DNSProviderRFC2136:
		_, _, err := net.SplitHostPort(merged["server"])
		return err == nil
	case model.DNSProviderNamecheap:
		ip := net.ParseIP(merged["client_ip"])
		return ip != nil && ip.To4() != nil
	case model.DNSProviderGoDaddy:
		parts := strings.SplitN(merged["api_token"], ":", 2)
		return len(parts) == 2 && parts[0] != "" && parts[1] != ""
	default:
		return true
	}
}

func cloneConfig(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

// 第三方驱动升级时在编译阶段暴露接口不兼容。
var (
	_ RecordProvider = (*cloudflaredns.Provider)(nil)
	_ RecordProvider = (*alidns.Provider)(nil)
	_ RecordProvider = (*tencentclouddns.Provider)(nil)
	_ RecordProvider = (*route53provider.Provider)(nil)
	_ RecordProvider = (*huaweiclouddns.Provider)(nil)
	_ RecordProvider = (*azuredns.Provider)(nil)
	_ RecordProvider = (*googleclouddns.Provider)(nil)
	_ RecordProvider = (*digitaloceandns.Provider)(nil)
	_ RecordProvider = (*gandidns.Provider)(nil)
	_ RecordProvider = (*godaddydns.Provider)(nil)
	_ RecordProvider = (*namecheapdns.Provider)(nil)
	_ RecordProvider = (*hetznerdns.Provider)(nil)
	_ RecordProvider = (*powerdnsprovider.Provider)(nil)
	_ RecordProvider = (*rfc2136provider.Provider)(nil)
)
