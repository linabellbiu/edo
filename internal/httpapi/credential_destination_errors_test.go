package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"edo/internal/assistant"
	dnsmanager "edo/internal/dns"
	"edo/internal/notification"
	"github.com/gin-gonic/gin"
)

func TestCredentialDestinationErrorsAreSafeAndActionable(t *testing.T) {
	for _, item := range []struct {
		name, code string
		err        error
		write      func(*gin.Context, *slog.Logger, error)
	}{
		{"assistant", "assistant_key_rebind_required", assistant.ErrAPIKeyRebindRequired, func(c *gin.Context, logger *slog.Logger, err error) {
			(assistantHandler{logger: logger}).fail(c, "test_assistant_destination", err)
		}},
		{"notification", "notification_token_rebind_required", notification.ErrTokenRebindRequired, func(c *gin.Context, logger *slog.Logger, err error) {
			(notificationHandler{logger: logger}).writeError(c, "test_notification_destination", err)
		}},
		{"dns", "dns_token_rebind_required", dnsmanager.ErrTokenRebindRequired, func(c *gin.Context, logger *slog.Logger, err error) {
			(dnsHandler{logger: logger}).writeServiceError(c, "test_dns_destination", err)
		}},
	} {
		t.Run(item.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			router := gin.New()
			router.PUT("/settings/:id", func(c *gin.Context) { item.write(c, logger, errors.Join(item.err, errors.New("fixture diagnostic"))) })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/settings/fixture-target", nil))
			var body errorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusBadRequest || body.Code != item.code || body.Message != item.err.Error() || strings.Contains(response.Body.String(), "fixture diagnostic") {
				t.Fatalf("目的地重绑错误未安全返回: status=%d code=%s", response.Code, body.Code)
			}
			if !strings.Contains(logs.String(), "fixture-target") || !strings.Contains(logs.String(), "fixture diagnostic") {
				t.Fatal("拒绝日志缺少目标或内部诊断")
			}
		})
	}
}
