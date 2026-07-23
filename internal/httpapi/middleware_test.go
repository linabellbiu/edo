package httpapi

import "testing"

func TestValidRequestID(t *testing.T) {
	for _, value := range []string{"abc", "request-123", "request_123"} {
		if !validRequestID(value) {
			t.Fatalf("合法请求 ID 被拒绝: %s", value)
		}
	}
	for _, value := range []string{"", "token?secret", "含中文"} {
		if validRequestID(value) {
			t.Fatalf("非法请求 ID 被接受: %s", value)
		}
	}
}
