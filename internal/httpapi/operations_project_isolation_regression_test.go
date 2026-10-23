package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/monitor"
	"edo/internal/scheduler"
)

func operationsRegressionProject(t *testing.T, fixture *operationsContractFixture) string {
	t.Helper()
	project := model.Project{
		ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, Name: "隔离复现项目",
		IsActive: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := fixture.db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	return project.ID
}

func TestOperationsRegressionMonitorChecksRespectSelectedProject(t *testing.T) {
	fixture := newOperationsContractFixture(t)
	otherProject := operationsRegressionProject(t, fixture)
	testProjectScopes.Store(fixture.cookie.Value, otherProject)
	created := fixture.request(t, http.MethodPost, "/api/v1/monitor-rules", map[string]any{
		"name": "other-project-rule", "endpoint": "https://monitor.example.invalid/health",
		"method": "GET", "expected_status_min": 200, "expected_status_max": 299,
		"timeout_seconds": 3, "interval_seconds": 60, "failure_threshold": 2, "recovery_threshold": 2,
	}, http.StatusCreated)
	ruleID := operationsID(t, created, "rule")
	check := model.MonitorCheck{
		ID: uuid.NewString(), RuleID: ruleID, JobID: "other-project-check", Status: model.MonitorUnhealthy,
		ErrorMessage: "other-project-private-monitor-result", CheckedAt: time.Now().UTC(),
	}
	if err := fixture.db.Create(&check).Error; err != nil {
		t.Fatal(err)
	}
	testProjectScopes.Store(fixture.cookie.Value, database.DefaultProjectID(database.DefaultDepartmentID))
	role := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "monitor-probe-reader", []string{"monitor.read", "repository.read"})
	createUserForHTTPTest(t, fixture.router, fixture.cookie, "monitor-probe-reader", "", []string{role})
	reader := loginForDepartmentTest(t, fixture.router, "monitor-probe-reader")
	for _, id := range []string{ruleID, uuid.NewString()} {
		response := performJSONRequest(t, fixture.router, http.MethodGet, "/api/v1/monitor-rules/"+id+"/checks", nil, reader)
		var failure errorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != http.StatusNotFound || failure.Code != "monitor_rule_not_found" {
			t.Fatalf("范围外与不存在的监控规则均应返回404: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
		}
		assertOperationsRedacted(t, response, check.ErrorMessage, check.ID, ruleID)
	}
	allowed := fixture.request(t, http.MethodGet, "/api/v1/monitor-rules/"+ruleID+"/checks?project_id="+otherProject, nil, http.StatusOK)
	if !strings.Contains(allowed.Body.String(), check.ID) {
		t.Fatal("本项目的监控记录没有正常返回")
	}
	noMonitorRole := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "monitor-regression-denied", []string{"repository.read"})
	createUserForHTTPTest(t, fixture.router, fixture.cookie, "monitor-regression-denied", "", []string{noMonitorRole})
	deniedUser := loginForDepartmentTest(t, fixture.router, "monitor-regression-denied")
	denied := performJSONRequest(t, fixture.router, http.MethodGet, "/api/v1/monitor-rules/"+ruleID+"/checks", nil, deniedUser)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺少监控权限的请求应保持403: status=%d body=%s", denied.Code, denied.Body.String())
	}
}

func TestOperationsRegressionBackgroundJobsKeepSavedProject(t *testing.T) {
	for _, kind := range []string{"monitor.check", "scheduler.execute"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newOperationsContractFixture(t)
			projectID := operationsRegressionProject(t, fixture)
			testProjectScopes.Store(fixture.cookie.Value, projectID)
			now := time.Now().UTC()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if kind == "monitor.check" {
				created := fixture.request(t, http.MethodPost, "/api/v1/monitor-rules", map[string]any{
					"name": "custom-project-rule", "endpoint": "https://monitor.example.invalid/health",
					"method": "GET", "expected_status_min": 200, "expected_status_max": 299,
					"timeout_seconds": 3, "interval_seconds": 60, "failure_threshold": 2, "recovery_threshold": 2,
				}, http.StatusCreated)
				id := operationsID(t, created, "rule")
				if err := fixture.db.Model(&model.MonitorRule{}).Where(&model.MonitorRule{ID: id}).Update("next_run_at", now.Add(-time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
				if err := monitor.NewService(fixture.db, nil, nil, nil, 4, logger).EnqueueDue(context.Background(), now); err != nil {
					t.Fatal(err)
				}
			} else {
				channelID := fixture.createChannel(t, "custom-project-notify")
				created := fixture.request(t, http.MethodPost, "/api/v1/schedules", map[string]any{
					"name": "custom-project-schedule", "cron_expression": "*/5 * * * *", "action": "notification",
					"payload": map[string]any{"channel_id": channelID, "title": "定时测试", "message": "等待定时触发", "severity": "info"},
				}, http.StatusCreated)
				id := operationsID(t, created, "schedule")
				if err := fixture.db.Model(&model.ScheduledTask{}).Where(&model.ScheduledTask{ID: id}).Update("next_run_at", now.Add(-time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
				if err := scheduler.NewService(fixture.db, nil, 4, logger).EnqueueDue(context.Background(), now); err != nil {
					t.Fatal(err)
				}
			}
			var job model.Job
			if err := fixture.db.Where("kind = ?", kind).First(&job).Error; err != nil {
				t.Fatal(err)
			}
			if job.ProjectID != projectID {
				t.Fatalf("后台任务没有继承来源的项目: kind=%s got=%s want=%s", kind, job.ProjectID, projectID)
			}
			var event model.OutboxEvent
			if err := fixture.db.Where(&model.OutboxEvent{AggregateID: job.ID}).First(&event).Error; err != nil {
				t.Fatal(err)
			}
			if event.ProjectID != projectID {
				t.Fatal("Outbox 没有继承来源的项目")
			}
		})
	}
}

func TestOperationsRegressionWebhookDeliveriesRespectProjectMembership(t *testing.T) {
	fixture := newOperationsContractFixture(t)
	project := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Webhook隔离项目"}, http.StatusCreated)
	projectID := operationsID(t, project, "project")
	repository := fixture.request(t, http.MethodPost, "/api/v1/repositories?project_id="+projectID, map[string]any{
		"name": "private-webhook-repository", "provider": "generic", "auth_type": "none", "clone_url": "https://example.invalid/private.git",
	}, http.StatusCreated)
	repositoryID := operationsID(t, repository, "repository")
	delivery := model.GitWebhookDelivery{
		ID: uuid.NewString(), RepositoryID: repositoryID, DeliveryID: uuid.NewString(), EventType: "push",
		Ref: "refs/heads/private-branch", CommitSHA: strings.Repeat("a", 40), Message: "other-project-private-webhook-result",
		PayloadHash: strings.Repeat("b", 64), Status: model.WebhookProcessed, ReceivedAt: time.Now().UTC(),
	}
	if err := fixture.db.Create(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	role := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "webhook-probe-reader", []string{"repository.read"})
	createUserForHTTPTest(t, fixture.router, fixture.cookie, "webhook-probe-reader", "", []string{role})
	reader := loginForDepartmentTest(t, fixture.router, "webhook-probe-reader")
	for _, id := range []string{repositoryID, uuid.NewString()} {
		response := performJSONRequest(t, fixture.router, http.MethodGet, "/api/v1/repositories/"+id+"/webhook-deliveries", nil, reader)
		var failure errorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != http.StatusNotFound || failure.Code != "repository_not_found" {
			t.Fatalf("范围外与不存在的仓库均应返回404: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
		}
		assertOperationsRedacted(t, response, delivery.Message, delivery.ID, repositoryID)
	}
	allowed := fixture.request(t, http.MethodGet, "/api/v1/repositories/"+repositoryID+"/webhook-deliveries?project_id="+projectID, nil, http.StatusOK)
	if !strings.Contains(allowed.Body.String(), delivery.ID) {
		t.Fatal("本项目的Webhook记录没有正常返回")
	}
	noRepositoryRole := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "webhook-regression-denied", []string{"application.read"})
	createUserForHTTPTest(t, fixture.router, fixture.cookie, "webhook-regression-denied", "", []string{noRepositoryRole})
	deniedUser := loginForDepartmentTest(t, fixture.router, "webhook-regression-denied")
	denied := performJSONRequest(t, fixture.router, http.MethodGet, "/api/v1/repositories/"+repositoryID+"/webhook-deliveries", nil, deniedUser)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺少仓库权限的请求应保持403: status=%d body=%s", denied.Code, denied.Body.String())
	}
}
