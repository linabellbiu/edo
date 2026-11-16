package diagnosticerror

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDiagnosticPreservesCauseWithoutExposingCredentialRepresentations(t *testing.T) {
	summary := errors.New("读取目标失败，请检查连接")
	credential := "private/token with \"quoted\" value"
	encoded, _ := json.Marshal(credential)
	variants := []string{credential, url.QueryEscape(credential), url.PathEscape(credential), base64.StdEncoding.EncodeToString([]byte(credential)), string(encoded[1 : len(encoded)-1])}
	detail := "dial tcp 192.0.2.10:6443: connection refused\n" + strings.Join(variants, "\n") + "\nAuthorization: Bearer unrelated-credential\nhttps://user:other-password@example.invalid\n"
	err := New(summary, detail, credential)
	if !errors.Is(err, summary) || err.Error() != summary.Error() || errors.Unwrap(err) != summary {
		t.Fatalf("安全提示或错误身份改变: %v", err)
	}
	var diagnostic interface{ Diagnostic() string }
	if !errors.As(err, &diagnostic) {
		t.Fatal("底层诊断没有传递")
	}
	actual := diagnostic.Diagnostic()
	if !strings.Contains(actual, "dial tcp 192.0.2.10:6443: connection refused") {
		t.Fatalf("真实网络诊断被摘要取代: %s", actual)
	}
	for _, value := range append(variants, "unrelated-credential", "other-password") {
		if strings.Contains(actual, value) {
			t.Fatalf("凭据表示未脱敏: %q", value)
		}
	}
}

func TestDiagnosticRedactsBeforeTruncationAndKeepsUTF8(t *testing.T) {
	summary := errors.New("执行失败")
	secret := strings.Repeat("私密", MaximumBytes)
	err := New(summary, "输出："+secret+"\n"+strings.Repeat("错误详情", MaximumBytes), secret)
	detail := err.(interface{ Diagnostic() string }).Diagnostic()
	if len(detail) > MaximumBytes || !utf8.ValidString(detail) || strings.Contains(detail, "私密") || !strings.Contains(detail, "已截断") {
		t.Fatal("诊断容量、UTF8或先脱敏后截断边界错误")
	}
	if New(nil, "unused") != nil || New(summary, "") != summary {
		t.Fatal("空错误或空诊断语义改变")
	}
}
