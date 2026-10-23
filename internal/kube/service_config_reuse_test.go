package kube

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func restConfigReuseFixture(t testing.TB) *clientcmdapi.Config {
	t.Helper()
	config, err := clientcmd.Load([]byte(safeKubeconfig))
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func restConfigReuseData(t testing.TB, config *clientcmdapi.Config) []byte {
	t.Helper()
	data, err := clientcmd.Write(*config)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSafeRESTConfigReusesParsedConfigWithoutChangingContextAndTLS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*clientcmdapi.Config)
	}{
		{name: "单上下文"},
		{name: "多上下文保留当前生产配置", change: func(config *clientcmdapi.Config) {
			addRESTConfigReuseContext(config)
		}},
		{name: "当前上下文选择非首个集群和用户", change: func(config *clientcmdapi.Config) {
			addRESTConfigReuseContext(config)
			config.CurrentContext = "staging"
		}},
		{name: "内联证书和TLS服务名", change: func(config *clientcmdapi.Config) {
			cluster := config.Clusters["production"]
			cluster.CertificateAuthorityData = []byte("synthetic-ca-data")
			cluster.TLSServerName, cluster.DisableCompression = "api.internal.example.com", true
			user := config.AuthInfos["edo"]
			user.Token = ""
			user.ClientCertificateData, user.ClientKeyData = []byte("synthetic-cert-data"), []byte("synthetic-key-data")
		}},
		{name: "基本认证", change: func(config *clientcmdapi.Config) {
			user := config.AuthInfos["edo"]
			user.Token, user.Username, user.Password = "", "test-user", "synthetic-password"
		}},
		{name: "保留缺失用户引用的匿名连接语义", change: func(config *clientcmdapi.Config) {
			config.Contexts["production"].AuthInfo = "missing"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := restConfigReuseFixture(t)
			if tc.change != nil {
				tc.change(config)
			}
			data := restConfigReuseData(t, config)
			want, err := clientcmd.RESTConfigFromKubeConfig(data)
			if err != nil {
				t.Fatalf("官方构造器拒绝测试配置: %v", err)
			}
			got, err := safeRESTConfig(data)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("复用已解析配置后，集群、认证或TLS结果与官方字节入口不同")
			}
			if tc.name == "当前上下文选择非首个集群和用户" && (got.Host != "https://staging.example.com/api" || got.BearerToken != "staging-test-token") {
				t.Fatal("没有保留当前上下文选择")
			}
		})
	}
}

func addRESTConfigReuseContext(config *clientcmdapi.Config) {
	config.Clusters["staging"] = &clientcmdapi.Cluster{Server: "https://staging.example.com/api"}
	config.AuthInfos["staging-user"] = &clientcmdapi.AuthInfo{Token: "staging-test-token"}
	config.Contexts["staging"] = &clientcmdapi.Context{Cluster: "staging", AuthInfo: "staging-user", Namespace: "staging"}
}

func TestSafeRESTConfigReuseKeepsSecurityRejectionsEquivalent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*clientcmdapi.Config)
		want   error
	}{
		{name: "CA文件引用", change: func(c *clientcmdapi.Config) { c.Clusters["production"].CertificateAuthority = "/nonexistent/ca.pem" }, want: ErrUnsafeKubeconfig},
		{name: "代理", change: func(c *clientcmdapi.Config) { c.Clusters["production"].ProxyURL = "http://proxy.example.com" }, want: ErrUnsafeKubeconfig},
		{name: "跳过证书校验", change: func(c *clientcmdapi.Config) { c.Clusters["production"].InsecureSkipTLSVerify = true }, want: ErrUnsafeKubeconfig},
		{name: "外部命令", change: func(c *clientcmdapi.Config) {
			c.AuthInfos["edo"].Exec = &clientcmdapi.ExecConfig{Command: "forbidden-command", APIVersion: "client.authentication.k8s.io/v1"}
		}, want: ErrUnsafeKubeconfig},
		{name: "外部认证插件", change: func(c *clientcmdapi.Config) {
			c.AuthInfos["edo"].AuthProvider = &clientcmdapi.AuthProviderConfig{Name: "forbidden-provider"}
		}, want: ErrUnsafeKubeconfig},
		{name: "客户端证书文件", change: func(c *clientcmdapi.Config) { c.AuthInfos["edo"].ClientCertificate = "/nonexistent/cert.pem" }, want: ErrUnsafeKubeconfig},
		{name: "客户端私钥文件", change: func(c *clientcmdapi.Config) { c.AuthInfos["edo"].ClientKey = "/nonexistent/key.pem" }, want: ErrUnsafeKubeconfig},
		{name: "令牌文件", change: func(c *clientcmdapi.Config) { c.AuthInfos["edo"].TokenFile = "/nonexistent/token" }, want: ErrUnsafeKubeconfig},
		{name: "身份模拟用户", change: func(c *clientcmdapi.Config) { c.AuthInfos["edo"].Impersonate = "other-user" }, want: ErrUnsafeKubeconfig},
		{name: "身份模拟组", change: func(c *clientcmdapi.Config) { c.AuthInfos["edo"].ImpersonateGroups = []string{"other-group"} }, want: ErrUnsafeKubeconfig},
		{name: "身份模拟附加字段", change: func(c *clientcmdapi.Config) {
			c.AuthInfos["edo"].ImpersonateUserExtra = map[string][]string{"scope": {"other"}}
		}, want: ErrUnsafeKubeconfig},
		{name: "未使用集群也禁止代理", change: func(c *clientcmdapi.Config) {
			c.Clusters["unused"] = &clientcmdapi.Cluster{Server: "https://unused.example.com", ProxyURL: "http://proxy.example.com"}
		}, want: ErrUnsafeKubeconfig},
		{name: "未使用用户也禁止外部命令", change: func(c *clientcmdapi.Config) {
			c.AuthInfos["unused"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{Command: "forbidden-command"}}
		}, want: ErrUnsafeKubeconfig},
		{name: "非法URL转义", change: func(c *clientcmdapi.Config) { c.Clusters["production"].Server = "https://api.example.com/%invalid" }, want: ErrInvalidCluster},
		{name: "非HTTPS地址", change: func(c *clientcmdapi.Config) { c.Clusters["production"].Server = "http://api.example.com" }, want: ErrInvalidCluster},
		{name: "空主机", change: func(c *clientcmdapi.Config) { c.Clusters["production"].Server = "https:///api" }, want: ErrInvalidCluster},
		{name: "URL内嵌认证", change: func(c *clientcmdapi.Config) {
			c.Clusters["production"].Server = "https://test-user:synthetic-password@api.example.com"
		}, want: ErrInvalidCluster},
		{name: "URL查询参数", change: func(c *clientcmdapi.Config) { c.Clusters["production"].Server = "https://api.example.com?query=value" }, want: ErrInvalidCluster},
		{name: "URL片段", change: func(c *clientcmdapi.Config) { c.Clusters["production"].Server = "https://api.example.com#fragment" }, want: ErrInvalidCluster},
		{name: "当前上下文不存在", change: func(c *clientcmdapi.Config) { c.CurrentContext = "missing" }, want: ErrInvalidCluster},
		{name: "缺少当前上下文", change: func(c *clientcmdapi.Config) { c.CurrentContext = "" }, want: ErrInvalidCluster},
		{name: "引用集群不存在", change: func(c *clientcmdapi.Config) { c.Contexts["production"].Cluster = "missing" }, want: ErrInvalidCluster},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := restConfigReuseFixture(t)
			tc.change(config)
			data := restConfigReuseData(t, config)
			oldConfig, oldErr := originalSafeRESTConfig(data)
			got, err := safeRESTConfig(data)
			if oldConfig != nil || got != nil || !errors.Is(oldErr, tc.want) || !errors.Is(err, tc.want) {
				t.Fatalf("安全拒绝语义变化: previous=%v current=%v want=%v", oldErr, err, tc.want)
			}
		})
	}
	for _, data := range []string{"invalid: [", "apiVersion: unknown/v1\nkind: Unknown\n"} {
		if config, err := safeRESTConfig([]byte(data)); config != nil || !errors.Is(err, ErrInvalidCluster) {
			t.Fatal("无效kubeconfig未返回稳定错误")
		}
	}
}

// 保留优化前实现作为基准；校验与构造两部分均计入，避免低估旧版开销。
func originalSafeRESTConfig(data []byte) (*rest.Config, error) {
	rawConfig, err := clientcmd.Load(data)
	if err != nil {
		return nil, ErrInvalidCluster
	}
	for _, cluster := range rawConfig.Clusters {
		if cluster == nil || cluster.CertificateAuthority != "" || cluster.ProxyURL != "" || cluster.InsecureSkipTLSVerify {
			return nil, ErrUnsafeKubeconfig
		}
		parsed, err := url.Parse(cluster.Server)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, ErrInvalidCluster
		}
	}
	for _, authInfo := range rawConfig.AuthInfos {
		if authInfo == nil || authInfo.Exec != nil || authInfo.AuthProvider != nil ||
			authInfo.ClientCertificate != "" || authInfo.ClientKey != "" || authInfo.TokenFile != "" ||
			authInfo.Impersonate != "" || len(authInfo.ImpersonateGroups) > 0 || len(authInfo.ImpersonateUserExtra) > 0 {
			return nil, ErrUnsafeKubeconfig
		}
	}
	result, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return nil, ErrInvalidCluster
	}
	return result, nil
}

var restConfigReuseBenchmarkResult *rest.Config

func BenchmarkSafeRESTConfigParsedReuse(b *testing.B) {
	for _, count := range []int{1, 32} {
		config := restConfigReuseFixture(b)
		for index := 1; index < count; index++ {
			name := fmt.Sprintf("context-%02d", index)
			config.Clusters[name] = &clientcmdapi.Cluster{Server: fmt.Sprintf("https://api-%d.example.com", index), CertificateAuthorityData: []byte(strings.Repeat("c", 1024))}
			config.AuthInfos[name] = &clientcmdapi.AuthInfo{Token: fmt.Sprintf("synthetic-token-%d", index)}
			config.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
			config.CurrentContext = name
		}
		data := restConfigReuseData(b, config)
		b.Run(fmt.Sprintf("%d个上下文", count), func(b *testing.B) {
			for _, implementation := range []struct {
				name string
				read func([]byte) (*rest.Config, error)
			}{{"原双解析", originalSafeRESTConfig}, {"复用已解析配置", safeRESTConfig}} {
				b.Run(implementation.name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						result, err := implementation.read(data)
						if err != nil {
							b.Fatal(err)
						}
						restConfigReuseBenchmarkResult = result
					}
				})
			}
		})
	}
}
