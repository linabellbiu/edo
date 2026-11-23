// Package netguard 提供出站网络请求的目标地址校验，防止服务端被诱导访问内网或元数据地址（SSRF）。
//
// EDO 有多处功能需要连接用户提供的地址：监控探针、Webhook 通知、应用数据库连接测试、
// Git 仓库地址。这些目标默认不受信任，必须在保存时校验，并在真正建立连接前再校验一次解析结果，
// 防止先注册一个解析到公网的域名、通过校验后再把 DNS 改到内网（DNS rebinding）。
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// ErrPrivateAddress 表示目标地址解析到了私网、环回、链路本地或其他非公网地址。
var ErrPrivateAddress = errors.New("目标地址指向私有网络或本机，已拒绝访问")

// ValidateHost 校验主机名或 IP 字面量是否可能指向内网地址。
// 只做字符串形态的快速判断（IP 字面量、localhost），不做 DNS 解析；
// 用于表单保存阶段的即时拒绝，解析阶段的校验见 DialContext。
func ValidateHost(host string) error {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]")
	if host == "" {
		return ErrPrivateAddress
	}
	if strings.EqualFold(host, "localhost") {
		return ErrPrivateAddress
	}
	if ip := net.ParseIP(host); ip != nil {
		return validateIP(ip)
	}
	return nil
}

// validateIP 判断一个已解析的 IP 是否属于禁止访问的范围：环回、私有、链路本地（含 IPv6 ULA）、
// 未指定地址，以及云平台普遍使用的元数据地址段 169.254.0.0/16。
func validateIP(ip net.IP) error {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return ErrPrivateAddress
	}
	return nil
}

// DialContext 包装标准的 net.Dialer，在真正建立连接前校验 DNS 解析出的每一个地址。
// 用作 http.Transport.DialContext，拦截保存时校验通过、但发送时域名已被改向内网的场景。
// 所有解析结果都必须通过校验；通过后按解析顺序逐个尝试建连，保留多地址回退能力。
func DialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("解析连接目标失败: %w", err)
		}
		var ips []net.IP
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			ips, err = net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("解析目标地址失败: %w", err)
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("目标地址无法解析: %s", host)
		}
		for _, ip := range ips {
			if err := validateIP(ip); err != nil {
				return nil, err
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

