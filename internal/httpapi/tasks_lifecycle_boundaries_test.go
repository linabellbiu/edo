package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/task"
)

func TestOperationsTaskHTTPEffectivePermissionsAndProjectIsolation(t *testing.T) {
	fixture := newOperationsContractFixture(t, func(deps *Dependencies, db *gorm.DB) {
		deps.Tasks = task.NewService(db, 4)
	})
	createProject := func(name string) string {
		t.Helper()
		response := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]string{"name": name}, http.StatusCreated)
		return operationsID(t, response, "project")
	}
	projectA, projectB := createProject("任务权限项目A"), createProject("任务权限项目B")
	readerRole := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "ops-task-reader", []string{"task.read"})
	executorRole := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "ops-task-executor", []string{"task.read", "task.execute"})
	otherRole := createRoleForHTTPTest(t, fixture.router, fixture.cookie, "ops-task-unrelated", []string{"repository.read"})
	readerID := createUserForHTTPTest(t, fixture.router, fixture.cookie, "ops-task-reader", "", nil)
	executorID := createUserForHTTPTest(t, fixture.router, fixture.cookie, "ops-task-executor", "", nil)
	otherID := createUserForHTTPTest(t, fixture.router, fixture.cookie, "ops-task-unrelated", "", nil)
	assign := func(project, user, role string) {
		t.Helper()
		fixture.request(t, http.MethodPut, "/api/v1/projects/"+project+"/members/"+user, map[string]string{"role_id": role}, http.StatusNoContent)
	}
	assign(projectA, readerID, readerRole)
	assign(projectA, executorID, executorRole)
	assign(projectB, executorID, executorRole)
	assign(projectA, otherID, otherRole)
	reader := loginForDepartmentTest(t, fixture.router, "ops-task-reader")
	executor := loginForDepartmentTest(t, fixture.router, "ops-task-executor")
	other := loginForDepartmentTest(t, fixture.router, "ops-task-unrelated")
	for _, cookie := range []*http.Cookie{reader, executor, other} {
		testProjectScopes.Store(cookie.Value, projectA)
	}
	createJob := func(project string, state model.JobStatus) *model.Job {
		t.Helper()
		job, err := task.NewService(fixture.db, 4).Create(context.Background(), task.CreateInput{
			ProjectID: project, DepartmentID: database.DefaultDepartmentID,
			Kind: "notification.dispatch", Subject: "edo.task.notification.dispatch", Idempotent: true,
			Payload: map[string]string{"private_payload": "synthetic-project-task-payload"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Model(job).Update("status", state).Error; err != nil {
			t.Fatal(err)
		}
		return job
	}
	pendingA, failedA := createJob(projectA, model.JobPending), createJob(projectA, model.JobFailed)
	pendingB, failedB := createJob(projectB, model.JobPending), createJob(projectB, model.JobFailed)
	checks := []struct {
		name, method, path string
		cookie             *http.Cookie
		status             int
	}{
		{"未登录列表", http.MethodGet, "/api/v1/tasks", nil, http.StatusUnauthorized},
		{"无任务查看权限", http.MethodGet, "/api/v1/tasks", other, http.StatusForbidden},
		{"无执行权限取消", http.MethodPost, "/api/v1/tasks/" + pendingA.ID + "/cancel", reader, http.StatusForbidden},
		{"无执行权限重试", http.MethodPost, "/api/v1/tasks/" + failedA.ID + "/retry", reader, http.StatusForbidden},
		{"非成员项目列表", http.MethodGet, "/api/v1/tasks?project_id=" + projectB, reader, http.StatusForbidden},
		{"跨项目取消", http.MethodPost, "/api/v1/tasks/" + pendingB.ID + "/cancel", executor, http.StatusNotFound},
		{"跨项目重试", http.MethodPost, "/api/v1/tasks/" + failedB.ID + "/retry", executor, http.StatusNotFound},
		{"切换项目后不能取消原项目", http.MethodPost, "/api/v1/tasks/" + pendingA.ID + "/cancel?project_id=" + projectB, executor, http.StatusNotFound},
		{"切换项目后不能重试原项目", http.MethodPost, "/api/v1/tasks/" + failedA.ID + "/retry?project_id=" + projectB, executor, http.StatusNotFound},
	}
	for _, item := range checks {
		t.Run(item.name, func(t *testing.T) {
			response := performJSONRequest(t, fixture.router, item.method, item.path, nil, item.cookie)
			if response.Code != item.status {
				t.Fatalf("任务授权边界错误: got=%d want=%d body=%s", response.Code, item.status, response.Body.String())
			}
		})
	}
	for _, cookie := range []*http.Cookie{reader, executor} {
		response := performJSONRequest(t, fixture.router, http.MethodGet, "/api/v1/tasks", nil, cookie)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), pendingA.ID) || !strings.Contains(response.Body.String(), failedA.ID) ||
			strings.Contains(response.Body.String(), pendingB.ID) || strings.Contains(response.Body.String(), failedB.ID) {
			t.Fatalf("任务列表没有按当前项目隔离: status=%d body=%s", response.Code, response.Body.String())
		}
		assertOperationsRedacted(t, response, "private_payload", "synthetic-project-task-payload")
	}
	var jobsBefore, eventsBefore int64
	if err := fixture.db.Model(&model.Job{}).Count(&jobsBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.OutboxEvent{}).Count(&eventsBefore).Error; err != nil {
		t.Fatal(err)
	}
	if jobsBefore != 4 || eventsBefore != 4 {
		t.Fatal("被拒绝的任务操作仍写入了任务或Outbox")
	}
	for _, expected := range []*model.Job{pendingA, pendingB, failedA, failedB} {
		var saved model.Job
		if err := fixture.db.Where(&model.Job{ID: expected.ID}).First(&saved).Error; err != nil || saved.Status != expected.Status {
			t.Fatalf("被拒绝的操作改写了任务状态: id=%s err=%v", expected.ID, err)
		}
	}
	cancel := performJSONRequest(t, fixture.router, http.MethodPost, "/api/v1/tasks/"+pendingA.ID+"/cancel", nil, executor)
	if cancel.Code != http.StatusNoContent {
		t.Fatalf("拥有执行权限不能取消本项目任务: status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	retry := performJSONRequest(t, fixture.router, http.MethodPost, "/api/v1/tasks/"+failedA.ID+"/retry", nil, executor)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("拥有执行权限不能重试本项目幂等失败任务: status=%d body=%s", retry.Code, retry.Body.String())
	}
	var response struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var saved model.Job
	if err := fixture.db.Where(&model.Job{ID: response.TaskID}).First(&saved).Error; err != nil || saved.ProjectID != projectA || saved.Status != model.JobPending {
		t.Fatalf("重试任务未继承当前项目或被同步执行: err=%v", err)
	}
}

func TestOperationsTaskHTTPListsCancelsAndRetriesWithoutExecution(t *testing.T) {
	fixture := newOperationsContractFixture(t, func(deps *Dependencies, db *gorm.DB) {
		deps.Tasks = task.NewService(db, 4)
	})
	createJob := func(status model.JobStatus, idempotent bool, order int) *model.Job {
		t.Helper()
		job, err := task.NewService(fixture.db, 4).Create(context.Background(), task.CreateInput{
			ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), DepartmentID: database.DefaultDepartmentID,
			Kind: "notification.dispatch", Subject: "edo.task.notification.dispatch", Idempotent: idempotent,
			Payload: map[string]string{"private_payload": "synthetic-task-private-payload"},
		})
		if err != nil {
			t.Fatal(err)
		}
		updates := map[string]any{"status": status, "created_at": time.Now().UTC().Add(time.Duration(order) * time.Second)}
		if status == model.JobFailed {
			updates["attempt"] = 4
		}
		if err := fixture.db.Model(job).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
		return job
	}
	pending := createJob(model.JobPending, true, 0)
	running := createJob(model.JobRunning, true, 1)
	succeeded := createJob(model.JobSucceeded, true, 2)
	failed := createJob(model.JobFailed, true, 3)
	unsafe := createJob(model.JobFailed, false, 4)
	listed := fixture.request(t, http.MethodGet, "/api/v1/tasks?status=failed&limit=1", nil, http.StatusOK)
	var listing struct {
		Tasks []task.View `json:"tasks"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listing); err != nil || len(listing.Tasks) != 1 || listing.Tasks[0].ID != unsafe.ID {
		t.Fatalf("任务状态筛选、倒序或数量限制错误: body=%s err=%v", listed.Body.String(), err)
	}
	assertOperationsRedacted(t, listed, "private_payload", "synthetic-task-private-payload")
	fixture.request(t, http.MethodPost, "/api/v1/tasks/"+pending.ID+"/cancel", nil, http.StatusNoContent)
	var canceled model.Job
	if err := fixture.db.Where(&model.Job{ID: pending.ID}).First(&canceled).Error; err != nil || canceled.Status != model.JobCanceled || canceled.FinishedAt == nil || canceled.ErrorCode != "canceled_by_user" {
		t.Fatalf("取消待执行任务没有保存终态: err=%v", err)
	}
	for _, job := range []*model.Job{pending, running, succeeded} {
		fixture.request(t, http.MethodPost, "/api/v1/tasks/"+job.ID+"/cancel", nil, http.StatusConflict, "invalid_task_state")
		fixture.request(t, http.MethodPost, "/api/v1/tasks/"+job.ID+"/retry", nil, http.StatusConflict, "invalid_task_state")
	}
	fixture.request(t, http.MethodPost, "/api/v1/tasks/missing/cancel", nil, http.StatusNotFound, "task_not_found")
	fixture.request(t, http.MethodPost, "/api/v1/tasks/missing/retry", nil, http.StatusNotFound, "task_not_found")
	fixture.request(t, http.MethodPost, "/api/v1/tasks/"+unsafe.ID+"/retry", nil, http.StatusConflict, "task_not_retryable")
	retried := fixture.request(t, http.MethodPost, "/api/v1/tasks/"+failed.ID+"/retry", nil, http.StatusAccepted)
	var retryResponse struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(retried.Body.Bytes(), &retryResponse); err != nil || retryResponse.TaskID == "" || retryResponse.TaskID == failed.ID {
		t.Fatalf("重试没有创建独立任务ID: body=%s err=%v", retried.Body.String(), err)
	}
	var retryJob, original model.Job
	if err := fixture.db.Where(&model.Job{ID: retryResponse.TaskID}).First(&retryJob).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where(&model.Job{ID: failed.ID}).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if retryJob.Status != model.JobPending || retryJob.Attempt != 0 || retryJob.StartedAt != nil || retryJob.FinishedAt != nil ||
		retryJob.ProjectID != failed.ProjectID || retryJob.DepartmentID != failed.DepartmentID || !sameTaskJSON(t, retryJob.Payload, failed.Payload) ||
		original.Status != model.JobFailed || original.Attempt != 4 {
		t.Fatal("重试改写了来源任务、丢失范围/参数，或在HTTP请求中执行了任务")
	}
	var event model.OutboxEvent
	if err := fixture.db.Where(&model.OutboxEvent{AggregateID: retryJob.ID}).First(&event).Error; err != nil || event.PublishedAt != nil || event.PublishAttempts != 0 || event.ProjectID != retryJob.ProjectID {
		t.Fatalf("重试没有创建待发布的同项目Outbox事件: err=%v", err)
	}
	var envelope task.Message
	if err := json.Unmarshal(event.Payload, &envelope); err != nil || envelope.JobID != retryJob.ID || !sameTaskJSON(t, envelope.Payload, retryJob.Payload) {
		t.Fatalf("重试事件与任务参数不一致: err=%v", err)
	}
	var jobs, events int64
	if err := fixture.db.Model(&model.Job{}).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.OutboxEvent{}).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 6 || events != 6 {
		t.Fatalf("被拒绝的取消/重试产生了额外副作用: jobs=%d events=%d", jobs, events)
	}
}

// MySQL 会规范化 JSON 的空格和键顺序，任务参数应按内容比较。
func sameTaskJSON(t *testing.T, left, right []byte) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(left, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(right, &b); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(a, b)
}
