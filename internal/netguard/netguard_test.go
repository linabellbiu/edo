package netguard

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestValidateHostRejectsLiteralPrivateAddresses(t *testing.T) {
	cases := []struct {
		host    string
		blocked bool
	}{
		{host: "169.254.169.254", blocked: true}, // 云平台元数据地址
		{host: "127.0.0.1", blocked: true},
		{host: "10.0.0.5", blocked: true},
		{host: "192.168.1.1", blocked: true},
		{host: "172.16.0.1", blocked: true},
		{host: "0.0.0.0", blocked: true},
		{host: "::1", blocked: true},
		{host: "fe80::1", blocked: true},
		{host: "localhost", blocked: true},
		{host: "LOCALHOST", blocked: true},
		{host: "example.com", blocked: false},
		{host: "8.8.8.8", blocked: false},
		{host: "2001:4860:4860::8888", blocked: false},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			err := ValidateHost(tc.host)
			if tc.blocked && !errors.Is(err, ErrPrivateAddress) {
				t.Fatalf("应当拒绝 %s，实际 err=%v", tc.host, err)
			}
			if !tc.blocked && err != nil {
				t.Fatalf("不应拒绝 %s，实际 err=%v", tc.host, err)
			}
		})
	}
}

func TestDialContextRejectsPrivateLiteralBeforeDialing(t *testing.T) {
	dial := DialContext(&net.Dialer{})
	_, err := dial(context.Background(), "tcp", "169.254.169.254:80")
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("应当拒绝元数据地址，实际 err=%v", err)
	}
}

func TestDialContextRejectsLoopbackLiteral(t *testing.T) {
	dial := DialContext(nil)
	_, err := dial(context.Background(), "tcp", "127.0.0.1:65535")
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("应当拒绝环回地址，实际 err=%v", err)
	}
}

func TestDialContextAllowsPublicLiteralAndAttemptsDial(t *testing.T) {
	// 不依赖外部网络：外部"必然失败"的地址在部分网络环境下会被透明代理接通，结论不稳定。
	// 这里用公网 IP 字面量配合一个必然拒绝的超时，只断言它没有被地址校验拦截，而是进入了真实拨号。
	// 0.0.0.0 之外的公网段在测试环境中同样可能接通，所以只要求错误不是 ErrPrivateAddress。
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	dial := DialContext(&net.Dialer{Timeout: 200 * time.Millisecond})
	_, err := dial(ctx, "tcp", "8.8.8.8:1")
	if errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("公网地址不应被当作私有地址拒绝: %v", err)
	}
}
