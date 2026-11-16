package logredact

import (
	"regexp"
	"strings"
)

// Text 复用已有诊断脱敏规则；调用方仍须提供本次操作实际使用的秘密值。
var sensitiveAssignment = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|passwd|secret|authorization|cookie|credential)([\s"']*[:=][\s"']*|\s+)([^\s,;"']+)`)
var sensitiveQuotedAssignment = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|passwd|secret|authorization|cookie|credential)([\s"']*[:=]\s*)("(?:[^"\\]|\\.)*"|'[^']*')`)
var bearerPattern = regexp.MustCompile(`(?i)bearer\s+[^\s,"']+`)
var keyPattern = regexp.MustCompile(`\b(sk-[a-zA-Z0-9_-]{8,}|gh[pousr]_[a-zA-Z0-9_]{10,}|AKIA[A-Z0-9]{16})\b`)
var privateKeyPattern = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
var incompletePrivateKeyPattern = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*`)
var privateKeyBodyPattern = regexp.MustCompile(`(?m)^[A-Za-z0-9+/]{32,}={0,2}[\t ]*$`)
var urlCredentials = regexp.MustCompile(`(?i)((?:https?|postgres(?:ql)?|mysql|rediss?|nats|amqps?|mongodb(?:\+srv)?|ssh)://)[^\s/@]+:[^\s/@]+@`)
var mysqlDSNCredentials = regexp.MustCompile(`(?i)\b[^:\s/@]+:[^@\s]+@(tcp|unix)\(`)

func Text(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[已隐藏]")
		}
	}
	value = privateKeyPattern.ReplaceAllString(value, "[私钥已隐藏]")
	value = incompletePrivateKeyPattern.ReplaceAllString(value, "[不完整私钥已隐藏]")
	value = privateKeyBodyPattern.ReplaceAllString(value, "[编码数据已隐藏]")
	value = bearerPattern.ReplaceAllString(value, "Bearer [已隐藏]")
	value = sensitiveQuotedAssignment.ReplaceAllString(value, "$1$2[已隐藏]")
	value = sensitiveAssignment.ReplaceAllString(value, "$1$2[已隐藏]")
	value = keyPattern.ReplaceAllString(value, "[密钥已隐藏]")
	value = urlCredentials.ReplaceAllString(value, "${1}[已隐藏]@")
	return mysqlDSNCredentials.ReplaceAllString(value, "[已隐藏]@$1(")
}
