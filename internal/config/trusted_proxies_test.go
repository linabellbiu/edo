package config

import (
	"slices"
	"strings"
	"testing"
)

func TestLoadTrustedProxyOrigins(t *testing.T) {
	t.Setenv("EDO_SERVER_TRUSTED_PROXIES", "")
	cfg, err := Load()
	if err != nil || len(cfg.Server.TrustedProxies) != 0 {
		t.Fatalf("默认不得信任任何转发来源: proxies=%v err=%v", cfg.Server.TrustedProxies, err)
	}
	t.Setenv("EDO_SERVER_TRUSTED_PROXIES", " 127.0.0.1 , 192.0.2.0/28,2001:db8::1, 2001:db8:1::/64 ")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"127.0.0.1", "192.0.2.0/28", "2001:db8::1", "2001:db8:1::/64"}; !slices.Equal(cfg.Server.TrustedProxies, want) {
		t.Fatalf("可信来源必须保留明确地址并去除周围空白: got=%v want=%v", cfg.Server.TrustedProxies, want)
	}
}

func TestLoadRejectsInvalidTrustedProxyOrigins(t *testing.T) {
	for _, value := range []string{"0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96", "::ffff:0.0.0.0", "0.0.0.0", "::", "proxy.example.test", "127.0.0.1:8080", "192.0.2.1/33", "fe80::1%eth0", "192.0.2.1,,192.0.2.2", " "} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("EDO_SERVER_TRUSTED_PROXIES", value)
			_, err := Load()
			if err == nil || err.Error() != "可信代理设置无效，请填写明确的 IP 或 CIDR" {
				t.Fatalf("无效或全网信任的来源必须在启动前失败并返回安全提示: err=%v", err)
			}
			if strings.TrimSpace(value) != "" && strings.Contains(err.Error(), value) {
				t.Fatal("对外错误不得回显配置值")
			}
		})
	}
}
