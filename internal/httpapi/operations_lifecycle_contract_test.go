package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	dnsmanager "edo/internal/dns"
	"edo/internal/model"
	"edo/internal/monitor"
	"edo/internal/notification"
	"edo/internal/scheduler"
	"edo/internal/secret"
)

type operationsContractHTTP struct{ calls atomic.Int32 }

func (transport *operationsContractHTTP) Do(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, errors.New("测试不允许向外部地址发起请求")
}

type operationsContractFixture struct {
	router http.Handler
	cookie *http.Cookie
	db     *gorm.DB
}

func newOperationsContractFixture(t *testing.T, configure ...func(*Dependencies, *gorm.DB)) *operationsContractFixture {
	t.Helper()
	transport := &operationsContractHTTP{}
	fixture := &operationsContractFixture{}
	router, closeTest := newAuthTestRouter(t, func(deps *Dependencies, db *gorm.DB) {
		manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
		if err != nil {
			t.Fatal(err)
		}
		fixture.db = db
		deps.DNS = dnsmanager.NewService(db, manager, dnsmanager.NewRegistry())
		deps.Notifications = notification.NewService(db, manager, transport, 4)
		deps.Monitors = monitor.NewService(db, manager, deps.Notifications, transport, 4, deps.Logger)
		deps.Scheduler = scheduler.NewService(db, deps.Notifications, 4, deps.Logger)
		for _, option := range configure {
			option(deps, db)
		}
	})
	t.Cleanup(closeTest)
	t.Cleanup(func() {
		if transport.calls.Load() != 0 {
			t.Errorf("配置或入队接口不应同步调用外部服务: calls=%d", transport.calls.Load())
		}
	})
	fixture.router = router
	fixture.cookie = loginForDepartmentTest(t, router, "admin")
	return fixture
}

func (fixture *operationsContractFixture) request(t *testing.T, method, path string, payload any, status int, code ...string) *httptest.ResponseRecorder {
	t.Helper()
	response := performJSONRequest(t, fixture.router, method, path, payload, fixture.cookie)
	if response.Code != status {
		t.Fatalf("%s %s 状态错误: got=%d want=%d body=%s", method, path, response.Code, status, response.Body.String())
	}
	if len(code) != 0 {
		var body errorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != code[0] || body.Message == "" {
			t.Fatalf("%s %s 错误契约错误: body=%s err=%v", method, path, response.Body.String(), err)
		}
	}
	return response
}

func operationsObject(t *testing.T, response *httptest.ResponseRecorder, name string) map[string]any {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	if err := json.Unmarshal(body[name], &item); err != nil || item == nil {
		t.Fatalf("响应缺少对象 %s: body=%s err=%v", name, response.Body.String(), err)
	}
	return item
}

func operationsID(t *testing.T, response *httptest.ResponseRecorder, name string) string {
	t.Helper()
	id, _ := operationsObject(t, response, name)["id"].(string)
	if id == "" {
		t.Fatalf("响应对象 %s 没有ID", name)
	}
	return id
}

func assertOperationsRedacted(t *testing.T, response *httptest.ResponseRecorder, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(response.Body.String(), value) {
			t.Fatal("接口响应包含不应回显的测试密钥或连接信息")
		}
	}
}

func assertOperationsNoJobs(t *testing.T, db *gorm.DB) {
	t.Helper()
	var jobs, events int64
	if err := db.Model(&model.Job{}).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.OutboxEvent{}).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 0 || events != 0 {
		t.Fatalf("配置修改意外创建后台任务: jobs=%d events=%d", jobs, events)
	}
}

func TestOperationsDNSAccountAndDomainHTTPLifecycle(t *testing.T) {
	fixture := newOperationsContractFixture(t)
	fixture.request(t, http.MethodGet, "/api/v1/dns/providers", nil, http.StatusOK)
	fixture.request(t, http.MethodPost, "/api/v1/dns/accounts", map[string]any{
		"name": "测试DNS", "provider": "cloudflare", "config": map[string]string{"unknown_field": "ignored"},
	}, http.StatusBadRequest, "invalid_dns_provider_config")
	const token = "synthetic-dns-contract-token"
	accountPayload := map[string]any{
		"name": "测试DNS", "provider": "cloudflare", "config": map[string]string{"api_token": token},
	}
	created := fixture.request(t, http.MethodPost, "/api/v1/dns/accounts", accountPayload, http.StatusCreated)
	accountID := operationsID(t, created, "account")
	assertOperationsRedacted(t, created, token)
	fixture.request(t, http.MethodPost, "/api/v1/dns/accounts", accountPayload, http.StatusConflict, "dns_account_exists")
	var stored model.DNSProviderAccount
	if err := fixture.db.Where(&model.DNSProviderAccount{ID: accountID}).First(&stored).Error; err != nil || stored.SecretCiphertext == "" ||
		strings.Contains(stored.SecretCiphertext, token) || strings.Contains(string(stored.PublicConfig), token) {
		t.Fatalf("DNS 密钥没有加密保存: err=%v", err)
	}
	updated := fixture.request(t, http.MethodPut, "/api/v1/dns/accounts/"+accountID, map[string]any{
		"name": "已修改DNS", "provider": "cloudflare", "config": map[string]string{"api_token": ""},
	}, http.StatusOK)
	assertOperationsRedacted(t, updated, token)
	listed := fixture.request(t, http.MethodGet, "/api/v1/dns/accounts", nil, http.StatusOK)
	assertOperationsRedacted(t, listed, token)
	domainPayload := map[string]any{"account_id": accountID, "name": "Example.COM.", "description": "HTTP契约域名"}
	domain := fixture.request(t, http.MethodPost, "/api/v1/dns/domains", domainPayload, http.StatusCreated)
	domainID := operationsID(t, domain, "domain")
	if operationsObject(t, domain, "domain")["name"] != "example.com" {
		t.Fatal("域名没有规范化")
	}
	fixture.request(t, http.MethodPost, "/api/v1/dns/domains", domainPayload, http.StatusConflict, "domain_exists")
	fixture.request(t, http.MethodDelete, "/api/v1/dns/accounts/"+accountID, nil, http.StatusConflict, "dns_account_in_use")
	fixture.request(t, http.MethodPut, "/api/v1/dns/domains/"+domainID, map[string]any{
		"account_id": accountID, "name": "example.org", "description": "修改说明",
	}, http.StatusOK)
	fixture.request(t, http.MethodGet, "/api/v1/dns/domains", nil, http.StatusOK)
	fixture.request(t, http.MethodPatch, "/api/v1/dns/domains/"+domainID+"/status", map[string]bool{"active": false}, http.StatusNoContent)
	fixture.request(t, http.MethodGet, "/api/v1/dns/domains/"+domainID+"/records", nil, http.StatusConflict, "domain_disabled")
	fixture.request(t, http.MethodPost, "/api/v1/dns/domains/"+domainID+"/test", nil, http.StatusConflict, "domain_disabled")
	fixture.request(t, http.MethodPatch, "/api/v1/dns/domains/"+domainID+"/status", map[string]bool{"active": true}, http.StatusNoContent)
	fixture.request(t, http.MethodPatch, "/api/v1/dns/accounts/"+accountID+"/status", map[string]bool{"active": false}, http.StatusNoContent)
	fixture.request(t, http.MethodGet, "/api/v1/dns/domains/"+domainID+"/records", nil, http.StatusConflict, "dns_account_disabled")
	fixture.request(t, http.MethodPatch, "/api/v1/dns/accounts/"+accountID+"/status", map[string]bool{"active": true}, http.StatusNoContent)
	fixture.request(t, http.MethodPost, "/api/v1/dns/domains/"+domainID+"/records", map[string]any{
		"name": "", "type": "A", "value": "192.0.2.10", "ttl": 300,
	}, http.StatusBadRequest, "invalid_dns_record")
	fixture.request(t, http.MethodGet, "/api/v1/dns/domains/missing/records", nil, http.StatusNotFound, "domain_not_found")
	fixture.request(t, http.MethodDelete, "/api/v1/dns/domains/"+domainID, nil, http.StatusNoContent)
	fixture.request(t, http.MethodDelete, "/api/v1/dns/domains/"+domainID, nil, http.StatusNotFound, "domain_not_found")
	fixture.request(t, http.MethodDelete, "/api/v1/dns/accounts/"+accountID, nil, http.StatusNoContent)
	fixture.request(t, http.MethodDelete, "/api/v1/dns/accounts/"+accountID, nil, http.StatusNotFound, "dns_account_not_found")
	assertOperationsNoJobs(t, fixture.db)
}

func (fixture *operationsContractFixture) createChannel(t *testing.T, name string) string {
	t.Helper()
	response := fixture.request(t, http.MethodPost, "/api/v1/notification-channels", map[string]any{
		"name": name, "type": "webhook", "endpoint": "https://notify.example.invalid/hook?secret=synthetic-hook-query",
		"token": "synthetic-notify-contract-token",
	}, http.StatusCreated)
	assertOperationsRedacted(t, response, "synthetic-notify-contract-token", "synthetic-hook-query", "notify.example.invalid")
	return operationsID(t, response, "channel")
}

func TestOperationsNotificationChannelHTTPQueuesWithoutDispatch(t *testing.T) {
	fixture := newOperationsContractFixture(t)
	fixture.request(t, http.MethodPost, "/api/v1/notification-channels", map[string]any{
		"name": "invalid-notify", "type": "webhook", "endpoint": "http://notify.example.invalid/hook",
	}, http.StatusBadRequest, "invalid_notification_channel")
	channelID := fixture.createChannel(t, "ops-notify")
	fixture.request(t, http.MethodPost, "/api/v1/notification-channels", map[string]any{
		"name": "ops-notify", "type": "webhook", "endpoint": "https://notify.example.invalid/hook",
	}, http.StatusConflict, "notification_channel_exists")
	updated := fixture.request(t, http.MethodPut, "/api/v1/notification-channels/"+channelID, map[string]any{
		"name": "ops-notify-renamed", "type": "webhook",
	}, http.StatusOK)
	if operationsObject(t, updated, "channel")["has_token"] != true {
		t.Fatal("未提供密钥时不应清除已保存的通知密钥")
	}
	listed := fixture.request(t, http.MethodGet, "/api/v1/notification-channels", nil, http.StatusOK)
	assertOperationsRedacted(t, listed, "synthetic-notify-contract-token", "synthetic-hook-query", "notify.example.invalid")
	queued := fixture.request(t, http.MethodPost, "/api/v1/notification-channels/"+channelID+"/test", nil, http.StatusAccepted)
	notificationID := operationsID(t, queued, "notification")
	item := operationsObject(t, queued, "notification")
	if item["status"] != string(model.NotificationQueued) || item["job_id"] == "" {
		t.Fatal("通知测试没有创建真实排队记录")
	}
	var stored model.Notification
	if err := fixture.db.Where(&model.Notification{ID: notificationID}).First(&stored).Error; err != nil || stored.Attempts != 0 || stored.SentAt != nil {
		t.Fatalf("测试接口不应同步发送通知: err=%v", err)
	}
	var jobs, events int64
	if err := fixture.db.Model(&model.Job{}).Where(&model.Job{ID: stored.JobID, Kind: "notification.dispatch"}).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.OutboxEvent{}).Where(&model.OutboxEvent{AggregateID: stored.JobID}).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || events != 1 {
		t.Fatalf("通知与任务/Outbox没有原子创建: jobs=%d events=%d", jobs, events)
	}
	fixture.request(t, http.MethodGet, "/api/v1/notifications?limit=999", nil, http.StatusOK)
	fixture.request(t, http.MethodPatch, "/api/v1/notification-channels/"+channelID+"/status", map[string]bool{"active": false}, http.StatusNoContent)
	fixture.request(t, http.MethodPost, "/api/v1/notification-channels/"+channelID+"/test", nil, http.StatusNotFound, "notification_channel_not_found")
	fixture.request(t, http.MethodPatch, "/api/v1/notification-channels/"+channelID+"/status", map[string]bool{"active": true}, http.StatusNoContent)
	fixture.request(t, http.MethodPut, "/api/v1/notification-channels/missing", map[string]any{
		"name": "missing-notify", "type": "webhook",
	}, http.StatusNotFound, "notification_channel_not_found")
}

func TestOperationsMonitorRuleHTTPValidatesAndStaysIdle(t *testing.T) {
	fixture := newOperationsContractFixture(t)
	payload := map[string]any{
		"name": "ops-monitor", "endpoint": "https://monitor.example.invalid/health?token=synthetic-monitor-query",
		"method": "GET", "expected_status_min": 200, "expected_status_max": 299,
		"timeout_seconds": 3, "interval_seconds": 60, "failure_threshold": 2, "recovery_threshold": 2,
	}
	for _, method := range []string{"POST", "DELETE"} {
		payload["method"] = method
		fixture.request(t, http.MethodPost, "/api/v1/monitor-rules", payload, http.StatusBadRequest, "invalid_monitor_rule")
	}
	payload["method"] = "GET"
	payload["expected_status_min"] = 400
	fixture.request(t, http.MethodPost, "/api/v1/monitor-rules", payload, http.StatusBadRequest, "invalid_monitor_rule")
	payload["expected_status_min"] = 200
	created := fixture.request(t, http.MethodPost, "/api/v1/monitor-rules", payload, http.StatusCreated)
	ruleID := operationsID(t, created, "rule")
	assertOperationsRedacted(t, created, "synthetic-monitor-query")
	fixture.request(t, http.MethodPost, "/api/v1/monitor-rules", payload, http.StatusConflict, "monitor_rule_exists")
	listed := fixture.request(t, http.MethodGet, "/api/v1/monitor-rules", nil, http.StatusOK)
	assertOperationsRedacted(t, listed, "synthetic-monitor-query")
	payload["name"] = "ops-monitor-renamed"
	payload["method"] = "HEAD"
	delete(payload, "endpoint")
	fixture.request(t, http.MethodPut, "/api/v1/monitor-rules/"+ruleID, payload, http.StatusOK)
	fixture.request(t, http.MethodPatch, "/api/v1/monitor-rules/"+ruleID+"/status", map[string]bool{"active": false}, http.StatusNoContent)
	fixture.request(t, http.MethodPatch, "/api/v1/monitor-rules/"+ruleID+"/status", map[string]bool{"active": true}, http.StatusNoContent)
	fixture.request(t, http.MethodGet, "/api/v1/monitor-rules/"+ruleID+"/checks", nil, http.StatusOK)
	fixture.request(t, http.MethodPut, "/api/v1/monitor-rules/missing", payload, http.StatusNotFound, "monitor_rule_not_found")
	var rule model.MonitorRule
	if err := fixture.db.Where(&model.MonitorRule{ID: ruleID}).First(&rule).Error; err != nil || rule.Status != model.MonitorUnknown || rule.LastRunAt != nil || rule.EndpointCiphertext == "" {
		t.Fatalf("保存监控配置不应立即探测: err=%v", err)
	}
	assertOperationsNoJobs(t, fixture.db)
}

func TestOperationsScheduleHTTPValidatesTimezoneAndStaysIdle(t *testing.T) {
	fixture := newOperationsContractFixture(t)
	channelID := fixture.createChannel(t, "schedule-notify")
	payload := map[string]any{
		"name": "ops-schedule", "cron_expression": "*/5 * * * *", "action": "notification",
		"payload": map[string]any{"channel_id": channelID, "title": "定时测试", "message": "等待定时触发", "severity": "info"},
	}
	for _, expression := range []string{"0 */5 * * * *", "@every 1m", "invalid"} {
		payload["cron_expression"] = expression
		fixture.request(t, http.MethodPost, "/api/v1/schedules", payload, http.StatusBadRequest, "invalid_schedule")
	}
	payload["cron_expression"] = "*/5 * * * *"
	payload["timezone"] = "No/SuchTimezone"
	fixture.request(t, http.MethodPost, "/api/v1/schedules", payload, http.StatusBadRequest, "invalid_schedule")
	delete(payload, "timezone")
	created := fixture.request(t, http.MethodPost, "/api/v1/schedules", payload, http.StatusCreated)
	scheduleID := operationsID(t, created, "schedule")
	item := operationsObject(t, created, "schedule")
	if item["timezone"] != "Asia/Shanghai" {
		t.Fatal("定时任务默认时区应为上海")
	}
	fixture.request(t, http.MethodPost, "/api/v1/schedules", payload, http.StatusConflict, "schedule_exists")
	payload["name"], payload["cron_expression"] = "ops-schedule-renamed", "*/10 * * * *"
	fixture.request(t, http.MethodPut, "/api/v1/schedules/"+scheduleID, payload, http.StatusOK)
	fixture.request(t, http.MethodPatch, "/api/v1/schedules/"+scheduleID+"/status", map[string]bool{"active": false}, http.StatusNoContent)
	fixture.request(t, http.MethodPatch, "/api/v1/schedules/"+scheduleID+"/status", map[string]bool{"active": true}, http.StatusNoContent)
	fixture.request(t, http.MethodGet, "/api/v1/schedules", nil, http.StatusOK)
	fixture.request(t, http.MethodPut, "/api/v1/schedules/missing", payload, http.StatusNotFound, "schedule_not_found")
	var stored model.ScheduledTask
	if err := fixture.db.Where(&model.ScheduledTask{ID: scheduleID}).First(&stored).Error; err != nil || stored.LastRunAt != nil ||
		stored.LastJobID != "" || !stored.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("保存或重新启用定时任务不应立即执行: err=%v", err)
	}
	assertOperationsNoJobs(t, fixture.db)
}

func TestOperationsConfigurationHTTPKeepsSecretsAndVersionBoundaries(t *testing.T) {
	fixture := newOperationsContractFixture(t)
	const initial, replacement = "synthetic-config-contract-secret", "synthetic-config-contract-rotated"
	payload := map[string]any{"namespace": "ops-contract", "environment": "production", "key": "API_TOKEN", "value": initial, "is_secret": true}
	created := fixture.request(t, http.MethodPost, "/api/v1/configurations", payload, http.StatusCreated)
	id := operationsID(t, created, "configuration")
	assertOperationsRedacted(t, created, initial)
	fixture.request(t, http.MethodPost, "/api/v1/configurations", payload, http.StatusConflict, "configuration_exists")
	listed := fixture.request(t, http.MethodGet, "/api/v1/configurations?namespace=ops-contract&environment=production", nil, http.StatusOK)
	assertOperationsRedacted(t, listed, initial)
	update := map[string]any{"value": replacement, "is_secret": true, "expected_version": 99}
	fixture.request(t, http.MethodPut, "/api/v1/configurations/"+id, update, http.StatusConflict, "configuration_version_conflict")
	var stored model.Configuration
	if err := fixture.db.Where(&model.Configuration{ID: id}).First(&stored).Error; err != nil || stored.Version != 1 {
		t.Fatalf("版本冲突仍改写了配置: err=%v", err)
	}
	update["expected_version"] = 1
	updated := fixture.request(t, http.MethodPut, "/api/v1/configurations/"+id, update, http.StatusOK)
	assertOperationsRedacted(t, updated, initial, replacement)
	if operationsObject(t, updated, "configuration")["version"] != float64(2) {
		t.Fatal("配置更新后版本没有递增")
	}
	fixture.request(t, http.MethodPatch, "/api/v1/configurations/"+id+"/status", map[string]any{
		"active": false, "expected_version": 1,
	}, http.StatusConflict, "configuration_version_conflict")
	fixture.request(t, http.MethodPatch, "/api/v1/configurations/"+id+"/status", map[string]any{
		"active": false, "expected_version": 2,
	}, http.StatusOK)
	revisions := fixture.request(t, http.MethodGet, "/api/v1/configurations/"+id+"/revisions", nil, http.StatusOK)
	assertOperationsRedacted(t, revisions, initial, replacement)
	fixture.request(t, http.MethodPut, "/api/v1/configurations/missing", update, http.StatusNotFound, "configuration_not_found")
	assertOperationsNoJobs(t, fixture.db)
}
