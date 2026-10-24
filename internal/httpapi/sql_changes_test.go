package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sqlchange"
	"edo/internal/task"
	"github.com/alicebob/miniredis/v2"
	"github.com/bsm/redislock"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const sqlChangeHTTPPassword = "sql-http-synthetic-password"

type sqlChangeHTTPRunner struct {
	checks   atomic.Int32
	executes atomic.Int32
	passed   bool
}

func (r *sqlChangeHTTPRunner) Check(context.Context, string, string) (sqlchange.CheckResult, error) {
	r.checks.Add(1)
	return sqlchange.CheckResult{Passed: r.passed, Summary: "HTTP 测试检查结果", Findings: json.RawMessage(`[]`)}, nil
}

func (r *sqlChangeHTTPRunner) Execute(_ context.Context, _ model.ApplicationDatabase, password, _ string, _ string) error {
	r.executes.Add(1)
	if password != sqlChangeHTTPPassword {
		return fmt.Errorf("测试目标凭据不匹配")
	}
	return nil
}

type sqlChangeHTTPFixture struct {
	router   http.Handler
	db       *gorm.DB
	deps     Dependencies
	service  *sqlchange.Service
	runner   *sqlChangeHTTPRunner
	admin    *http.Cookie
	creator  *model.User
	reviewer *model.User
	writer   *http.Cookie
	review   *http.Cookie
	app      model.Application
	target   model.ApplicationDatabase
	run      model.PipelineRun
}

func newSQLChangeHTTPFixture(t *testing.T) *sqlChangeHTTPFixture {
	t.Helper()
	f := &sqlChangeHTTPFixture{runner: &sqlChangeHTTPRunner{passed: true}}
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))))
	if err != nil {
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, db *gorm.DB) {
		if err := db.AutoMigrate(&model.SQLChange{}, &model.SQLChangeRevision{}, &model.SQLChangeReview{}, &model.SQLChangeExecution{}); err != nil {
			t.Fatal(err)
		}
		f.service = sqlchange.New(db, manager, deps.Access, deps.Logger)
		f.service.ConfigureRunner(f.runner)
		f.service.ConfigureLocks(redislock.New(redisClient), "edo.sql-http.lock")
		deps.SQLChanges = f.service
		f.db, f.deps = db, *deps
	})
	t.Cleanup(cleanup)
	f.router = router
	f.admin = loginForDepartmentTest(t, router, "admin")
	f.creator, f.writer = f.user(t, "sql-http-author", "sql_change.read", "sql_change.create", "sql_change.update", "sql_change.delete", "sql_change.check", "sql_change.review", "sql_change.execute", "sql_change.skip", "application_database.execute")
	f.reviewer, f.review = f.user(t, "sql-http-reviewer", "sql_change.read", "sql_change.review", "sql_change.skip")
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	now := time.Now().UTC()
	repo := model.GitRepository{ID: uuid.NewString(), Name: "sql-http-repository", CloneURL: "https://git.example.invalid/sql-http.git", Provider: model.GitProviderGeneric, AuthType: model.GitAuthNone, DefaultBranch: "main", IsActive: true, ProjectID: projectID, DepartmentID: database.DefaultDepartmentID, CreatedBy: f.creator.ID, CreatedAt: now, UpdatedAt: now}
	f.app = model.Application{ID: uuid.NewString(), Name: "sql_http_app", RepositoryID: repo.ID, IsActive: true, ProjectID: projectID, DepartmentID: database.DefaultDepartmentID, CreatedBy: f.creator.ID, CreatedAt: now, UpdatedAt: now}
	env := model.Environment{ID: uuid.NewString(), Name: "HTTP SQL 测试环境", IsActive: true, ProjectID: projectID, DepartmentID: database.DefaultDepartmentID, CreatedBy: f.creator.ID, CreatedAt: now, UpdatedAt: now}
	f.run = model.PipelineRun{ID: uuid.NewString(), ProjectID: projectID, DepartmentID: database.DefaultDepartmentID, ApplicationID: f.app.ID, WorkflowID: uuid.NewString(), Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), Status: model.PipelineRunSucceeded, CreatedBy: f.creator.ID, CreatedAt: now, UpdatedAt: now}
	f.target = model.ApplicationDatabase{ID: uuid.NewString(), Name: "HTTP SQL 测试目标", ProjectID: projectID, DepartmentID: database.DefaultDepartmentID, ApplicationID: f.app.ID, EnvironmentID: env.ID, Driver: "postgres", Host: "sql-http.example.invalid", Port: 5432, Database: "sql_http", Username: "sql_http", TLSMode: "disable", Fingerprint: strings.Repeat("f", 64), IsActive: true, Version: 1, CreatedBy: f.creator.ID, UpdatedBy: f.creator.ID, CreatedAt: now, UpdatedAt: now}
	f.target.Ciphertext, err = manager.Encrypt(sqlChangeHTTPPassword, []byte("application-database:"+f.target.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []any{&repo, &f.app, &env, &f.run, &f.target} {
		if err := f.db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *sqlChangeHTTPFixture) user(t *testing.T, name string, codes ...string) (*model.User, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	user, err := f.deps.Accounts.CreateUser(ctx, name, name, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	role, err := f.deps.Access.CreateRole(ctx, access.RoleInput{Name: name + "-role", DisplayName: "SQL 接口测试角色", Permissions: append([]string{"project.read"}, codes...)})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := f.deps.Accounts.FindByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Access.SetProjectMember(ctx, admin, database.DefaultProjectID(user.DepartmentID), user.ID, role.ID); err != nil {
		t.Fatal(err)
	}
	token, _, err := f.deps.Sessions.Create(ctx, user.ID, user.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	return user, &http.Cookie{Name: f.deps.AuthConfig.CookieName, Value: token}
}

func (f *sqlChangeHTTPFixture) input(name string) sqlchange.Input {
	return sqlchange.Input{Name: name, ApplicationID: f.app.ID, Branch: "main", PipelineRunID: f.run.ID, DatabaseID: f.target.ID, ReviewerID: f.reviewer.ID, SQL: "ALTER TABLE acceptance_sql ADD COLUMN note TEXT;"}
}

func (f *sqlChangeHTTPFixture) request(t *testing.T, method, path string, payload any, cookie *http.Cookie, want int) *httptest.ResponseRecorder {
	t.Helper()
	response := performJSONRequest(t, f.router, method, "/api/v1/sql-changes"+path, payload, cookie)
	if response.Code != want {
		t.Fatalf("%s SQL 工单接口状态错误: got=%d want=%d body=%s", method, response.Code, want, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("SQL 工单响应没有禁止缓存")
	}
	if strings.Contains(response.Body.String(), sqlChangeHTTPPassword) || strings.Contains(response.Body.String(), f.target.Ciphertext) || (path != "/options" && strings.Contains(response.Body.String(), f.target.Host)) {
		t.Fatal("SQL 工单接口泄露目标凭据或非确认页连接地址")
	}
	return response
}

func sqlChangeHTTPDetail(t *testing.T, response *httptest.ResponseRecorder) sqlchange.DetailResult {
	t.Helper()
	var result sqlchange.DetailResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Change.ID == "" || result.Revision.ID == "" {
		t.Fatalf("SQL 工单详情契约错误: err=%v body=%s", err, response.Body.String())
	}
	return result
}

func (f *sqlChangeHTTPFixture) create(t *testing.T, name string) sqlchange.DetailResult {
	t.Helper()
	return sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "", f.input(name), f.writer, http.StatusOK))
}

func (f *sqlChangeHTTPFixture) consume(t *testing.T, kind string, change sqlchange.DetailResult, wantErr ...error) task.Message {
	t.Helper()
	var jobs []model.Job
	if err := f.db.Where("kind = ?", "sql_change."+kind).Order("id DESC").Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		var payload struct {
			ChangeID   string `json:"change_id"`
			RevisionID string `json:"revision_id"`
		}
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ChangeID != change.Change.ID || payload.RevisionID != change.Revision.ID {
			continue
		}
		message := task.Message{Version: 1, JobID: job.ID, Kind: job.Kind, MaxAttempts: job.MaxAttempts, Payload: json.RawMessage(job.Payload)}
		var err error
		if kind == "check" {
			err = f.service.HandleCheck(context.Background(), message)
		} else {
			err = f.service.HandleExecute(context.Background(), message)
		}
		var expected error
		if len(wantErr) != 0 {
			expected = wantErr[0]
		}
		if !errors.Is(err, expected) {
			t.Fatalf("消费 SQL 测试任务结果错误: kind=%s err=%v want=%v", kind, err, expected)
		}
		return message
	}
	t.Fatalf("没有找到当前修订的 SQL %s 任务", kind)
	return task.Message{}
}

func (f *sqlChangeHTTPFixture) checked(t *testing.T, change sqlchange.DetailResult, wantErr ...error) sqlchange.DetailResult {
	t.Helper()
	f.consume(t, "check", change, wantErr...)
	return sqlChangeHTTPDetail(t, f.request(t, http.MethodGet, "/"+change.Change.ID, nil, f.writer, http.StatusOK))
}

func TestSQLChangeHTTPErrorContract(t *testing.T) {
	for _, test := range []struct {
		err  error
		want int
	}{
		{sqlchange.ErrScope, 400}, {sqlchange.ErrForbidden, 403}, {sqlchange.ErrNotFound, 404}, {sqlchange.ErrInvalid, 400},
		{sqlchange.ErrConflict, 409}, {sqlchange.ErrImmutable, 409}, {sqlchange.ErrState, 409},
		{sqlchange.ErrUnavailable, 503}, {sqlchange.ErrStorage, 503}, {sqlchange.ErrCheckFailed, 409},
		{sqlchange.ErrExecutionFailed, 409}, {sqlchange.ErrExecutionUncertain, 409},
	} {
		status, code, message := sqlChangeHTTPError(fmt.Errorf("%w: SQL/password/internal-address", test.err))
		if status != test.want || code == "" || message != test.err.Error() {
			t.Fatalf("错误分类泄露内部原因或状态错误: got=%d code=%s message=%s", status, code, message)
		}
	}
	status, _, message := sqlChangeHTTPError(fmt.Errorf("SQL/password/internal-address"))
	if status != 500 || strings.Contains(message, "password") || strings.Contains(message, "internal-address") {
		t.Fatal("未知错误没有使用安全中文提示")
	}
}

func TestSQLChangeHTTPLifecycleReviewDigestAndExecution(t *testing.T) {
	f := newSQLChangeHTTPFixture(t)
	created := f.create(t, "SQL 生命周期")
	if created.Change.Phase != "before" || created.Change.RepositoryID != f.app.RepositoryID || created.Change.EnvironmentID != f.target.EnvironmentID || created.SQL != f.input("").SQL || len(created.Change.Digest) != 64 {
		t.Fatal("工单默认位置、关联信息或 SQL 摘要不正确")
	}
	if f.runner.checks.Load() != 0 || f.runner.executes.Load() != 0 {
		t.Fatal("保存接口同步检查或执行了 SQL")
	}
	list := f.request(t, http.MethodGet, "?search=生命周期&page=1&page_size=10", nil, f.writer, 200)
	var listed sqlchange.ListResult
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || listed.Total != 1 || len(listed.Items) != 1 || strings.Contains(list.Body.String(), created.SQL) {
		t.Fatal("SQL 列表分页或 SQL 正文保护错误")
	}
	f.request(t, http.MethodPost, "/"+created.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: created.Change.Version, ConfirmDigest: created.Change.Digest}, f.writer, 409)
	checked := f.checked(t, created)
	f.request(t, http.MethodPost, "/"+created.Change.ID+"/reviews", sqlchange.ActionInput{ExpectedVersion: checked.Change.Version, Decision: "approve"}, f.writer, 403)
	_, other := f.user(t, "sql-http-other-reviewer", "sql_change.review", "sql_change.skip")
	f.request(t, http.MethodPost, "/"+created.Change.ID+"/reviews", sqlchange.ActionInput{ExpectedVersion: checked.Change.Version, Decision: "approve"}, other, 403)
	approved := sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "/"+created.Change.ID+"/reviews", sqlchange.ActionInput{ExpectedVersion: checked.Change.Version, Decision: "approve"}, f.review, 200))
	if f.runner.executes.Load() != 0 {
		t.Fatal("审核通过后自动执行了 SQL")
	}
	f.request(t, http.MethodPost, "/"+created.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: approved.Change.Version, ConfirmDigest: strings.Repeat("0", 64)}, f.writer, 409)
	queued := sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "/"+created.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: approved.Change.Version, ConfirmDigest: approved.Change.Digest}, f.writer, 202))
	if f.runner.executes.Load() != 0 {
		t.Fatal("执行接口没有立即入队而同步执行 SQL")
	}
	f.request(t, http.MethodPost, "/"+created.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: queued.Change.Version, ConfirmDigest: queued.Change.Digest}, f.writer, 409)
	whileQueued := f.input("排队期间不能修改")
	whileQueued.ExpectedVersion = queued.Change.Version
	f.request(t, http.MethodPut, "/"+created.Change.ID, whileQueued, f.writer, 409)
	message := f.consume(t, "execute", queued)
	finished := sqlChangeHTTPDetail(t, f.request(t, http.MethodGet, "/"+created.Change.ID, nil, f.writer, 200))
	if finished.Change.Status != "succeeded" || len(finished.Executions) != 1 || finished.Executions[0].Status != "succeeded" || f.runner.executes.Load() != 1 {
		t.Fatal("后台真实执行状态没有反映到工单")
	}
	f.request(t, http.MethodPost, "/"+created.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: finished.Change.Version, ConfirmDigest: finished.Change.Digest}, f.writer, 409)
	if err := f.service.HandleExecute(context.Background(), message); err != nil || f.runner.executes.Load() != 1 {
		t.Fatalf("消息重投重复执行 SQL: calls=%d err=%v", f.runner.executes.Load(), err)
	}
	input := f.input("成功后不允许修改")
	input.ExpectedVersion = finished.Change.Version
	f.request(t, http.MethodPut, "/"+created.Change.ID, input, f.writer, 409)
	f.request(t, http.MethodDelete, fmt.Sprintf("/%s?version=%d", created.Change.ID, finished.Change.Version), nil, f.writer, 409)
	var audits []model.AuditLog
	if err := f.db.Where("resource_type = ?", "sql_change").Find(&audits).Error; err != nil || len(audits) == 0 {
		t.Fatal("SQL 工单写操作没有审计")
	}
	for _, audit := range audits {
		if strings.Contains(string(audit.Metadata), created.SQL) || strings.Contains(string(audit.Metadata), sqlChangeHTTPPassword) || strings.Contains(string(audit.Metadata), "ALTER TABLE") {
			t.Fatal("SQL 正文或凭据进入操作审计")
		}
	}
}

func TestSQLChangeHTTPModificationRequiresNewCheckAndReview(t *testing.T) {
	f := newSQLChangeHTTPFixture(t)
	first := f.checked(t, f.create(t, "SQL 修改重审"))
	approved := sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "/"+first.Change.ID+"/reviews", sqlchange.ActionInput{ExpectedVersion: first.Change.Version, Decision: "approve"}, f.review, 200))
	input := f.input("修改后的 SQL")
	input.ExpectedVersion, input.SQL = approved.Change.Version, "ALTER TABLE acceptance_sql ADD COLUMN remark TEXT;"
	updated := sqlChangeHTTPDetail(t, f.request(t, http.MethodPut, "/"+first.Change.ID, input, f.writer, 200))
	if updated.Revision.ID == first.Revision.ID || updated.Change.Digest == first.Change.Digest || updated.Change.Version <= approved.Change.Version || updated.Revision.CheckStatus == "passed" {
		t.Fatal("SQL 修改没有生成新修订并作废旧检查")
	}
	f.request(t, http.MethodPut, "/"+first.Change.ID, input, f.writer, 409)
	f.request(t, http.MethodPost, "/"+first.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: updated.Change.Version, ConfirmDigest: updated.Change.Digest}, f.writer, 409)
	updated = f.checked(t, updated)
	f.request(t, http.MethodPost, "/"+first.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: updated.Change.Version, ConfirmDigest: updated.Change.Digest}, f.writer, 409)
	approved = sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "/"+first.Change.ID+"/reviews", sqlchange.ActionInput{ExpectedVersion: updated.Change.Version, Decision: "approve"}, f.review, 200))
	if len(approved.Reviews) < 2 {
		t.Fatal("修改后的重新审核覆盖了原审核历史")
	}
	f.request(t, http.MethodDelete, fmt.Sprintf("/%s?version=%d", first.Change.ID, approved.Change.Version), nil, f.writer, 204)
	f.request(t, http.MethodGet, "/"+first.Change.ID, nil, f.writer, 404)
}

func TestSQLChangeHTTPSkipRequiresAssignedReviewerAndReason(t *testing.T) {
	f := newSQLChangeHTTPFixture(t)
	change := f.create(t, "SQL 人工跳过")
	path := "/" + change.Change.ID + "/skip"
	f.request(t, http.MethodPost, path, sqlchange.ActionInput{ExpectedVersion: change.Change.Version, Reason: "明确跳过"}, f.writer, 403)
	f.request(t, http.MethodPost, path, sqlchange.ActionInput{ExpectedVersion: change.Change.Version}, f.review, 400)
	skipped := sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, path, sqlchange.ActionInput{ExpectedVersion: change.Change.Version, Reason: "此发布无需调整此目标"}, f.review, 200))
	if skipped.Change.Status != "skipped" || len(skipped.Executions) != 1 || skipped.Executions[0].Status != "skipped" || skipped.Executions[0].Reason == "" || f.runner.executes.Load() != 0 {
		t.Fatal("人工跳过没有独立记录或被伪装为 SQL 成功")
	}
	f.request(t, http.MethodPost, path, sqlchange.ActionInput{ExpectedVersion: skipped.Change.Version, Reason: "重复跳过"}, f.review, 409)
	f.request(t, http.MethodPost, "/"+change.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: skipped.Change.Version, ConfirmDigest: skipped.Change.Digest}, f.writer, 409)
}

func TestSQLChangeHTTPFailedCheckAndRejectedReviewDoNotExecute(t *testing.T) {
	f := newSQLChangeHTTPFixture(t)
	f.runner.passed = false
	change := f.checked(t, f.create(t, "SQL 检查失败"), sqlchange.ErrCheckFailed)
	if change.Revision.CheckStatus != "failed" || change.Change.Status != "check_failed" {
		t.Fatal("SQL 检查失败被当作通过")
	}
	f.request(t, http.MethodPost, "/"+change.Change.ID+"/reviews", sqlchange.ActionInput{ExpectedVersion: change.Change.Version, Decision: "approve"}, f.review, 409)
	f.request(t, http.MethodPost, "/"+change.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: change.Change.Version, ConfirmDigest: change.Change.Digest}, f.writer, 409)
	f.runner.passed = true
	rechecked := sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "/"+change.Change.ID+"/check", sqlchange.ActionInput{ExpectedVersion: change.Change.Version}, f.writer, 202))
	if rechecked.Revision.ID == change.Revision.ID || rechecked.Revision.CheckStatus != "checking" {
		t.Fatal("重新检查没有创建独立修订")
	}
	rechecked = f.checked(t, rechecked)
	rejected := sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "/"+change.Change.ID+"/reviews", sqlchange.ActionInput{ExpectedVersion: rechecked.Change.Version, Decision: "reject", Reason: "需要修正本次变更"}, f.review, 200))
	if rejected.Change.Status != "rejected" || len(rejected.Reviews) != 1 || rejected.Reviews[0].Decision != "reject" {
		t.Fatal("SQL 驳回结果或原因没有保留")
	}
	f.request(t, http.MethodPost, "/"+change.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: rejected.Change.Version, ConfirmDigest: rejected.Change.Digest}, f.writer, 409)
	input := f.input("驳回后修正")
	input.ExpectedVersion = rejected.Change.Version
	f.request(t, http.MethodPut, "/"+change.Change.ID, input, f.writer, 200)
	if f.runner.executes.Load() != 0 {
		t.Fatal("检查失败、重新检查或审核驳回意外执行 SQL")
	}
}

func TestSQLChangeHTTPAutomaticCheckUsesOriginalActionPermission(t *testing.T) {
	f := newSQLChangeHTTPFixture(t)
	_, creator := f.user(t, "sql-http-create-only", "sql_change.read", "sql_change.create")
	change := sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "", f.input("仅创建权限自动检查"), creator, 200))
	change = f.checked(t, change)
	if change.Change.Status != "pending_review" || change.Revision.CheckStatus != "passed" || f.runner.checks.Load() != 1 {
		t.Fatal("创建时自动检查错误要求手动重查权限")
	}
	_, updater := f.user(t, "sql-http-update-only", "sql_change.read", "sql_change.update")
	input := f.input("仅修改权限自动检查")
	input.ExpectedVersion, input.SQL = change.Change.Version, "ALTER TABLE acceptance_sql ADD COLUMN comment TEXT;"
	change = sqlChangeHTTPDetail(t, f.request(t, http.MethodPut, "/"+change.Change.ID, input, updater, 200))
	change = f.checked(t, change)
	if change.Change.Status != "pending_review" || change.Revision.CheckStatus != "passed" || f.runner.checks.Load() != 2 {
		t.Fatal("修改时自动检查错误要求手动重查权限")
	}
	f.request(t, http.MethodPost, "/"+change.Change.ID+"/check", sqlchange.ActionInput{ExpectedVersion: change.Change.Version}, creator, 403)
	f.request(t, http.MethodPost, "/"+change.Change.ID+"/check", sqlchange.ActionInput{ExpectedVersion: change.Change.Version}, updater, 403)
}

func TestSQLChangeHTTPContentAuthorCannotReviewOwnRevision(t *testing.T) {
	f := newSQLChangeHTTPFixture(t)
	change := f.checked(t, f.create(t, "修订作者自审保护"))
	if err := f.deps.Access.SetUserPermissions(context.Background(), f.reviewer.ID, access.UserPermissionOverrides{Allow: []string{"sql_change.update", "sql_change.check"}}); err != nil {
		t.Fatal(err)
	}
	input := f.input("审核员修改 SQL")
	input.ExpectedVersion, input.SQL = change.Change.Version, "ALTER TABLE acceptance_sql ADD COLUMN reviewed_note TEXT;"
	f.request(t, http.MethodPut, "/"+change.Change.ID, input, f.review, 400)
	unchanged := sqlChangeHTTPDetail(t, f.request(t, http.MethodGet, "/"+change.Change.ID, nil, f.writer, 200))
	if unchanged.Revision.ID != change.Revision.ID || unchanged.Change.Digest != change.Change.Digest {
		t.Fatal("指定审核员修改 SQL 后仍被允许自审")
	}
	// 重查不改 SQL，不应把检查发起者当作内容作者而禁止正常审核。
	rechecked := sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "/"+change.Change.ID+"/check", sqlchange.ActionInput{ExpectedVersion: change.Change.Version}, f.review, 202))
	rechecked = f.checked(t, rechecked)
	if rechecked.Revision.AuthorID != f.creator.ID || rechecked.Revision.CreatedBy != f.reviewer.ID {
		t.Fatal("重查丢失 SQL 内容作者或检查发起者")
	}
	f.request(t, http.MethodPost, "/"+change.Change.ID+"/reviews", sqlchange.ActionInput{ExpectedVersion: rechecked.Change.Version, Decision: "approve"}, f.review, 200)
}

func TestSQLChangeHTTPQueuedExecutionRechecksProjectMembership(t *testing.T) {
	f := newSQLChangeHTTPFixture(t)
	change := f.checked(t, f.create(t, "排队后移除项目成员"))
	change = sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "/"+change.Change.ID+"/reviews", sqlchange.ActionInput{ExpectedVersion: change.Change.Version, Decision: "approve"}, f.review, 200))
	queued := sqlChangeHTTPDetail(t, f.request(t, http.MethodPost, "/"+change.Change.ID+"/execute", sqlchange.ActionInput{ExpectedVersion: change.Change.Version, ConfirmDigest: change.Change.Digest}, f.writer, 202))
	ctx := context.Background()
	if err := f.deps.Access.SetUserPermissions(ctx, f.creator.ID, access.UserPermissionOverrides{Allow: []string{"sql_change.read", "sql_change.execute", "application_database.execute"}}); err != nil {
		t.Fatal(err)
	}
	admin, err := f.deps.Accounts.FindByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	projectID := database.DefaultProjectID(f.creator.DepartmentID)
	if err := f.deps.Access.SetProjectMember(ctx, admin, projectID, f.creator.ID, ""); err != nil {
		t.Fatal(err)
	}
	permissionCtx := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: f.creator.ID, DepartmentID: f.creator.DepartmentID, ProjectID: projectID})
	allowed, err := f.deps.Access.HasPermission(permissionCtx, f.creator, "sql_change.execute")
	if err != nil || !allowed {
		t.Fatalf("用例没有保留个人动作允许: allowed=%v err=%v", allowed, err)
	}
	f.consume(t, "execute", queued, sqlchange.ErrForbidden)
	finished := sqlChangeHTTPDetail(t, f.request(t, http.MethodGet, "/"+change.Change.ID, nil, f.admin, 200))
	if f.runner.executes.Load() != 0 || finished.Change.Status != "failed" || len(finished.Executions) != 1 || finished.Executions[0].Status != "failed" {
		t.Fatal("个人允许绕过了后台项目成员撤销或仍执行 SQL")
	}
}

func TestSQLChangeHTTPOptionsIsolationAndInputBoundaries(t *testing.T) {
	f := newSQLChangeHTTPFixture(t)
	change := f.create(t, "SQL 范围隔离")
	options := f.request(t, http.MethodGet, "/options", nil, f.writer, 200)
	var choices sqlchange.OptionsResult
	if err := json.Unmarshal(options.Body.Bytes(), &choices); err != nil || len(choices.Applications) != 1 || len(choices.Databases) != 1 || len(choices.PipelineRuns) != 1 {
		t.Fatal("SQL 选择项没有限制当前范围或排除自审")
	}
	targetChoice := choices.Databases[0]
	if targetChoice.ID != f.target.ID || targetChoice.Host != f.target.Host || targetChoice.Port != f.target.Port || targetChoice.Database != f.target.Database || targetChoice.EnvironmentID != f.target.EnvironmentID || targetChoice.EnvironmentName == "" {
		t.Fatal("执行确认缺少当前目标的地址、端口、数据库或环境")
	}
	for _, forbidden := range []string{`"username":`, `"password":`, `"ciphertext":`, `"sql":`, change.SQL} {
		if strings.Contains(options.Body.String(), forbidden) {
			t.Fatal("SQL 选择项泄露凭据或 SQL 正文")
		}
	}
	foundReviewer := false
	for _, reviewer := range choices.Reviewers {
		if reviewer.ID == f.creator.ID {
			t.Fatal("SQL 审核人候选包含当前创建人")
		}
		foundReviewer = foundReviewer || reviewer.ID == f.reviewer.ID
	}
	if !foundReviewer {
		t.Fatal("SQL 审核人候选遗漏有权审核的项目成员")
	}
	project := model.Project{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, Name: "其他 SQL 项目", IsActive: true}
	if err := f.db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	foreignRepository := model.GitRepository{ID: uuid.NewString(), Name: "foreign-sql-repository", ProjectID: project.ID, DepartmentID: project.DepartmentID}
	foreignApplication := model.Application{ID: uuid.NewString(), Name: "foreign_sql_application", RepositoryID: foreignRepository.ID, ProjectID: project.ID, DepartmentID: project.DepartmentID, IsActive: true}
	foreignEnvironment := model.Environment{ID: uuid.NewString(), Name: "其他项目数据库环境", ProjectID: project.ID, DepartmentID: project.DepartmentID, IsActive: true}
	foreignTarget := f.target
	foreignTarget.RowID, foreignTarget.ID, foreignTarget.ProjectID, foreignTarget.ApplicationID, foreignTarget.EnvironmentID = 0, uuid.NewString(), project.ID, foreignApplication.ID, foreignEnvironment.ID
	foreignTarget.Host, foreignTarget.Name = "foreign.sql-http.example.invalid", "其他项目数据库"
	for _, item := range []any{&foreignRepository, &foreignApplication, &foreignEnvironment, &foreignTarget} {
		if err := f.db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	currentOptions := f.request(t, http.MethodGet, "/options", nil, f.writer, 200)
	for _, foreign := range []string{foreignTarget.ID, foreignTarget.Host, foreignTarget.Name, foreignApplication.ID, foreignEnvironment.ID} {
		if strings.Contains(currentOptions.Body.String(), foreign) {
			t.Fatal("SQL 确认选项泄露其他项目的目标")
		}
	}
	for _, request := range []struct {
		method, path string
		payload      any
	}{
		{http.MethodGet, "/" + change.Change.ID, nil},
		{http.MethodPut, "/" + change.Change.ID, f.input("跨项目修改")},
		{http.MethodDelete, "/" + change.Change.ID + "?version=1", nil},
		{http.MethodPost, "/" + change.Change.ID + "/reviews", sqlchange.ActionInput{ExpectedVersion: 1, Decision: "approve"}},
		{http.MethodPost, "/" + change.Change.ID + "/execute", sqlchange.ActionInput{ExpectedVersion: 1, ConfirmDigest: change.Change.Digest}},
	} {
		separator := "?"
		if strings.Contains(request.path, "?") {
			separator = "&"
		}
		f.request(t, request.method, request.path+separator+"project_id="+project.ID, request.payload, f.admin, 404)
	}
	department := model.Department{ID: uuid.NewString(), Name: "其他 SQL 部门", IsActive: true}
	if err := f.db.Create(&department).Error; err != nil {
		t.Fatal(err)
	}
	departmentProject := model.Project{ID: uuid.NewString(), DepartmentID: department.ID, Name: "其他部门 SQL 项目", IsActive: true}
	if err := f.db.Create(&departmentProject).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodGet, "/"+change.Change.ID+"?project_id="+departmentProject.ID, nil, f.admin, 404)
	foreignList := f.request(t, http.MethodGet, "?project_id="+departmentProject.ID, nil, f.admin, 200)
	if strings.Contains(foreignList.Body.String(), change.Change.ID) || strings.Contains(foreignList.Body.String(), change.Change.Name) {
		t.Fatal("跨部门项目列表泄露原工单")
	}
	for _, query := range []string{"?page=abc", "?page=-1", "?page_size=0", "?page_size=101", "?page=99999999999999999999999"} {
		f.request(t, http.MethodGet, query, nil, f.writer, 400)
	}
	for _, version := range []string{"", "0", "-1", "abc", "18446744073709551616"} {
		f.request(t, http.MethodDelete, "/"+change.Change.ID+"?version="+version, nil, f.writer, 400)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sql-changes", strings.NewReader(`{"sql":`))
	request.AddCookie(f.writer)
	request.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	if response.Code != 400 || strings.Contains(response.Body.String(), "unexpected EOF") {
		t.Fatal("无效 JSON 没有返回安全的中文参数错误")
	}
}
