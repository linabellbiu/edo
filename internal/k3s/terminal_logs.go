package k3s

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"

	"edo/internal/sshclient"
)

// 脱敏属于业务边界，沿用已有凭据分类与 Go 标准库；不引入日志终端或执行器依赖。
var terminalANSI = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\))")
var terminalSecretField = regexp.MustCompile(`(?i)(?:[a-z0-9_.-]*(?:token|secret|password|passwd|credential|private.?key|access.?key|api[._-]?key|authorization|datastore.endpoint)[a-z0-9_.-]*|client-key-data|client-certificate-data)["'\\]*\s*(?:[:=]|\s+)\s*`)
var terminalK3sToken = regexp.MustCompile(`K10[[:xdigit:]]{64}::[^\s"'<>]+`)
var terminalJWT = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

type installationLogRedactor struct {
	values     []string
	pem        bool
	yamlIndent int
}

func newInstallationLogRedactor(values []string) *installationLogRedactor {
	unique := map[string]bool{}
	for _, value := range values {
		if value == "" {
			continue
		}
		unique[value] = true
		if encoded, err := json.Marshal(value); err == nil && len(encoded) > 2 {
			unique[string(encoded[1:len(encoded)-1])] = true
		}
		unique[url.QueryEscape(value)] = true
		for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' }) {
			if part != "" {
				unique[part] = true
			}
		}
	}
	r := &installationLogRedactor{yamlIndent: -1}
	for value := range unique {
		r.values = append(r.values, value)
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

func (r *installationLogRedactor) line(value string) string {
	value = terminalANSI.ReplaceAllString(value, "")
	value = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && c != '\t' {
			return -1
		}
		return c
	}, value)
	if strings.Contains(value, "-----BEGIN ") {
		r.pem = true
	}
	if r.pem {
		if strings.Contains(value, "-----END ") {
			r.pem = false
		}
		return "[已隐藏凭据内容]"
	}
	prefix := ""
	if stamp, rest, ok := strings.Cut(value, " "); ok {
		if _, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			prefix, value = stamp+" ", rest
		}
	}
	indent := len(value) - len(strings.TrimLeft(value, " \t"))
	if r.yamlIndent >= 0 {
		if strings.TrimSpace(value) == "" || indent > r.yamlIndent {
			return ""
		}
		r.yamlIndent = -1
	}
	if index := terminalSecretField.FindStringIndex(value); index != nil {
		// 整个敏感字段尾部隐藏，兼容 JSON、k3d 调试结构体、环境变量和 YAML 块。
		tail := strings.TrimSpace(value[index[1]:])
		if tail == "|" || tail == ">" || tail == "|-" || tail == ">-" || tail == "" {
			r.yamlIndent = indent
		}
		value = value[:index[1]] + "[已隐藏]"
	}
	value = authorizationValue.ReplaceAllString(value, "[已隐藏认证信息]")
	value = authenticatedURL.ReplaceAllStringFunc(value, func(v string) string { scheme, _, _ := strings.Cut(v, "://"); return scheme + "://[已隐藏]@" })
	value = terminalK3sToken.ReplaceAllString(value, "[已隐藏令牌]")
	value = terminalJWT.ReplaceAllString(value, "[已隐藏令牌]")
	for _, secret := range r.values {
		value = strings.ReplaceAll(value, secret, "[已隐藏]")
	}
	return prefix + value
}

func installationLogSecrets(input Input, bundle sshclient.Bundle, extra ...string) []string {
	values := append([]string{bundle.PrivateKey, bundle.Passphrase, bundle.Password, bundle.SudoPassword, input.ServerToken}, extra...)
	var walk func(any, bool)
	walk = func(value any, secret bool) {
		switch data := value.(type) {
		case string:
			if secret {
				values = append(values, data)
			}
		case map[string]any:
			for key, item := range data {
				walk(item, secret || IsSecretOption(key))
			}
		case []any:
			for _, item := range data {
				walk(item, secret)
			}
		case []string:
			if secret {
				values = append(values, data...)
			}
		}
	}
	walk(input.Installer, false)
	walk(input.ServerOptions, false)
	walk(input.AgentOptions, false)
	var overrides map[string]any
	if yaml.Unmarshal([]byte(input.Overrides), &overrides) == nil {
		walk(overrides, false)
	}
	return values
}
