package sqlchange

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/task"
	"edo/internal/testutil"
	"github.com/alicebob/miniredis/v2"
	"github.com/bsm/redislock"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type domainPermissions struct {
	mu   sync.Mutex
	deny map[string]bool
}

func (p *domainPermissions) HasPermission(_ context.Context, u *model.User, code string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.deny[u.ID+":"+code], nil
}
func (p *domainPermissions) block(user, code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deny[user+":"+code] = true
}

type domainRunner struct {
	checks, executions   atomic.Int32
	checkErr, executeErr error
	checkReport          json.RawMessage
	enter, release       chan struct{}
	returnNilOnCancel    bool
}

func (r *domainRunner) Check(context.Context, string, string) (CheckResult, error) {
	r.checks.Add(1)
	report := r.checkReport
	if len(report) == 0 {
		report = json.RawMessage("[]")
	}
	return CheckResult{Passed: r.checkErr == nil, Findings: report}, r.checkErr
}
func (r *domainRunner) Execute(ctx context.Context, _ model.ApplicationDatabase, _, _, _ string) error {
	r.executions.Add(1)
	if r.enter != nil {
		close(r.enter)
		select {
		case <-r.release:
		case <-ctx.Done():
			if r.returnNilOnCancel {
				return nil
			}
			return ErrExecutionUncertain
		}
	}
	return r.executeErr
}

type domainFixture struct {
	s                      *Service
	ctx                    context.Context
	owner, reviewer, other string
	input                  Input
	runner                 *domainRunner
	permissions            *domainPermissions
}

type gatedDomainLocks struct {
	delegate         LockManager
	calls            atomic.Int32
	entered, release chan struct{}
}

func (l *gatedDomainLocks) Obtain(ctx context.Context, key string, ttl time.Duration, options *redislock.Options) (*redislock.Lock, error) {
	if l.calls.Add(1) == 1 {
		close(l.entered)
		select {
		case <-l.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return l.delegate.Obtain(ctx, key, ttl, options)
}

func fixture(t *testing.T) *domainFixture {
	t.Helper()
	db := testutil.NewDatabase(t)
	if err := db.AutoMigrate(&model.SQLChange{}, &model.SQLChangeRevision{}, &model.SQLChangeReview{}, &model.SQLChangeExecution{}); err != nil {
		t.Fatal(err)
	}
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))))
	if err != nil {
		t.Fatal(err)
	}
	f := &domainFixture{owner: uuid.NewString(), reviewer: uuid.NewString(), other: uuid.NewString(), runner: &domainRunner{}, permissions: &domainPermissions{deny: map[string]bool{}}}
	project := uuid.NewString()
	f.ctx = database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project, DepartmentID: database.DefaultDepartmentID, UserID: f.owner})
	if err = db.Create(&model.Project{ID: project, DepartmentID: database.DefaultDepartmentID, Name: "SQL 测试项目", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{f.owner, f.reviewer, f.other} {
		user := model.User{ID: id, Username: "sql-" + []string{"owner", "reviewer", "other"}[i], DepartmentID: database.DefaultDepartmentID, IsActive: true}
		if err = db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		role := model.Role{ID: uuid.NewString(), Name: "sql_fixture_" + []string{"owner", "reviewer", "other"}[i], DisplayName: "SQL 领域测试角色"}
		if err = db.Create(&role).Error; err != nil {
			t.Fatal(err)
		}
		// 旧用例保留模拟失败分支，真实项目授权仍需完整的角色与权限记录。
		for _, permission := range access.Catalog() {
			if !strings.HasPrefix(permission.Code, "sql_change.") && permission.Code != "application_database.execute" {
				continue
			}
			if err = db.Create(&model.RolePermission{RoleID: role.ID, Permission: permission.Code}).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err = db.Create(&model.ProjectMember{ProjectID: project, UserID: id, RoleID: role.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := model.GitRepository{ID: uuid.NewString(), ProjectID: project, DepartmentID: database.DefaultDepartmentID, Name: "sql-repository"}
	app := model.Application{ID: uuid.NewString(), ProjectID: project, DepartmentID: database.DefaultDepartmentID, RepositoryID: repo.ID, Name: "sql_app", IsActive: true}
	env := model.Environment{ID: uuid.NewString(), ProjectID: project, DepartmentID: database.DefaultDepartmentID, Name: "sql-environment"}
	run := model.PipelineRun{ID: uuid.NewString(), ProjectID: project, DepartmentID: database.DefaultDepartmentID, ApplicationID: app.ID, Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), Status: model.PipelineRunSucceeded}
	target := model.ApplicationDatabase{ID: uuid.NewString(), ProjectID: project, DepartmentID: database.DefaultDepartmentID, ApplicationID: app.ID, EnvironmentID: env.ID, Name: "sql-db", Driver: "postgres", Host: "db.example", Port: 5432, Database: "application", Username: "executor", TLSMode: "verify-full", IsActive: true, Version: 1, Fingerprint: digest("postgres", "db.example", "5432", "application")}
	target.Ciphertext, err = manager.Encrypt("private-db-password", []byte("application-database:"+target.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{&repo, &app, &env, &run, &target} {
		if err = db.WithContext(f.ctx).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.s = New(db, manager, f.permissions, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.s.ConfigureRunner(f.runner)
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	f.s.ConfigureLocks(redislock.New(client), "sql-test")
	f.input = Input{Name: "本次发布 SQL", ApplicationID: app.ID, Branch: "main", PipelineRunID: run.ID, DatabaseID: target.ID, ReviewerID: f.reviewer, SQL: "INSERT INTO example(value) VALUES ('private-sql-value');"}
	return f
}
func (f *domainFixture) actor(id string) context.Context { return withActor(f.ctx, id) }
func (f *domainFixture) message(t *testing.T, kind string) task.Message {
	t.Helper()
	var job model.Job
	if err := f.s.db.WithContext(f.ctx).Where("kind = ?", kind).Order("id DESC").First(&job).Error; err != nil {
		t.Fatal(err)
	}
	return task.Message{Version: 1, JobID: job.ID, Kind: kind, Payload: json.RawMessage(job.Payload), MaxAttempts: job.MaxAttempts}
}
func (f *domainFixture) saved(t *testing.T) *DetailResult {
	t.Helper()
	d, e := f.s.Save(f.ctx, f.owner, "", f.input)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func (f *domainFixture) approved(t *testing.T) *DetailResult {
	t.Helper()
	d := f.saved(t)
	if err := f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check")); err != nil {
		t.Fatal(err)
	}
	d, e := f.s.Detail(f.ctx, d.Change.ID)
	if e != nil {
		t.Fatal(e)
	}
	d, e = f.s.Review(f.actor(f.reviewer), f.reviewer, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, Decision: "approve"})
	if e != nil {
		t.Fatal(e)
	}
	return d
}

func TestSQLChangeManualLifecycleAndEncryption(t *testing.T) {
	f := fixture(t)
	d := f.saved(t)
	if d.Change.Status != "checking" || f.runner.executions.Load() != 0 {
		t.Fatal("保存工单执行了 SQL")
	}
	if d.Change.AuthorID != f.owner || d.Revision.AuthorID != f.owner {
		t.Fatal("详情未返回当前 SQL 内容作者")
	}
	var revision model.SQLChangeRevision
	if err := f.s.db.First(&revision, "uuid = ?", d.Revision.ID).Error; err != nil {
		t.Fatal(err)
	}
	var jobs []model.Job
	if err := f.s.db.Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]any{revision, jobs})
	if strings.Contains(string(raw), "private-sql-value") || strings.Contains(revision.Ciphertext, "private-sql-value") {
		t.Fatal("SQL 正文进入了记录或队列明文")
	}
	if err := f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check")); err != nil {
		t.Fatal(err)
	}
	d, err := f.s.Detail(f.ctx, d.Change.ID)
	if err != nil || d.Change.Status != "pending_review" {
		t.Fatalf("检查状态不正确: %v", err)
	}
	if _, err = f.s.Review(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, Decision: "approve"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("作者可以自审")
	}
	d, err = f.s.Review(f.actor(f.reviewer), f.reviewer, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if f.runner.executions.Load() != 0 {
		t.Fatal("审批自动执行了 SQL")
	}
	if _, err = f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: "wrong"}); !errors.Is(err, ErrConflict) {
		t.Fatal("错误摘要允许执行")
	}
	d, err = f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if d.Change.Status != "queued" || f.runner.executions.Load() != 0 {
		t.Fatal("手动执行未按后台队列受理")
	}
	message := f.message(t, "sql_change.execute")
	if message.MaxAttempts != 1 {
		t.Fatal("SQL 执行不是一次投递上限")
	}
	if err = f.s.HandleExecute(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err = f.s.HandleExecute(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	d, err = f.s.Detail(f.ctx, d.Change.ID)
	if err != nil || d.Change.Status != "succeeded" || d.Change.SuccessAt == nil || f.runner.executions.Load() != 1 {
		t.Fatalf("执行不满足一次成功: %v", err)
	}
	f.input.ExpectedVersion = d.Change.Version
	if _, err = f.s.Save(f.ctx, f.owner, d.Change.ID, f.input); !errors.Is(err, ErrImmutable) {
		t.Fatal("成功 SQL 仍可修改")
	}
	if err = f.s.Delete(f.ctx, f.owner, d.Change.ID, d.Change.Version); !errors.Is(err, ErrImmutable) {
		t.Fatal("成功 SQL 仍可删除")
	}
	if _, err = f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest}); !errors.Is(err, ErrState) {
		t.Fatal("成功 SQL 再次入队")
	}
}

func TestSQLChangeRejectRequiresReasonWithoutMutation(t *testing.T) {
	f := fixture(t)
	created := f.saved(t)
	if err := f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check")); err != nil {
		t.Fatal(err)
	}
	before, err := f.s.Detail(f.ctx, created.Change.ID)
	if err != nil || before.Change.Status != "pending_review" {
		t.Fatalf("检查后工单未等待审核: %v", err)
	}
	var beforeJobs int64
	if err := f.s.db.Model(&model.Job{}).Count(&beforeJobs).Error; err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ name, decision, reason string }{
		{"空原因", "reject", ""}, {"空白原因", "reject", " \t\r\n\u3000"},
		{"超长原因", "reject", strings.Repeat("原", 334)}, {"修剪前超长", "reject", strings.Repeat(" ", 1000) + "原"},
		{"非法编码", "reject", string([]byte{0xff})}, {"批准超长空白", "approve", strings.Repeat(" ", 1001)},
		{"批准非法编码", "approve", string([]byte{0xff})},
	} {
		t.Run(input.name, func(t *testing.T) {
			_, err := f.s.Review(f.actor(f.reviewer), f.reviewer, before.Change.ID, ActionInput{ExpectedVersion: before.Change.Version, Decision: input.decision, Reason: input.reason})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("无效审核原因未被拒绝: %v", err)
			}
			after, err := f.s.Detail(f.ctx, before.Change.ID)
			if err != nil || after.Change.Version != before.Change.Version || after.Change.RevisionID != before.Change.RevisionID || after.Change.Status != before.Change.Status || !after.Change.UpdatedAt.Equal(before.Change.UpdatedAt) || len(after.Reviews) != 0 || len(after.Executions) != 0 {
				t.Fatalf("无效审核改变了工单或历史: %v", err)
			}
			var jobs int64
			if err := f.s.db.Model(&model.Job{}).Count(&jobs).Error; err != nil || jobs != beforeJobs || f.runner.executions.Load() != 0 {
				t.Fatalf("无效审核创建或执行了后台任务: %v", err)
			}
		})
	}
	rejected, err := f.s.Review(f.actor(f.reviewer), f.reviewer, before.Change.ID, ActionInput{ExpectedVersion: before.Change.Version, Decision: "reject", Reason: " \t请补充变更范围\u3000\n"})
	if err != nil || rejected.Change.Status != "rejected" || len(rejected.Reviews) != 1 || rejected.Reviews[0].Reason != "请补充变更范围" || rejected.Change.Version != before.Change.Version+1 {
		t.Fatalf("有效驳回没有保留修剪后的原因: %v", err)
	}
}

func TestSQLChangeApproveStillAllowsEmptyReason(t *testing.T) {
	f := fixture(t)
	created := f.saved(t)
	if err := f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check")); err != nil {
		t.Fatal(err)
	}
	before, err := f.s.Detail(f.ctx, created.Change.ID)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := f.s.Review(f.actor(f.reviewer), f.reviewer, before.Change.ID, ActionInput{ExpectedVersion: before.Change.Version, Decision: "approve", Reason: " \t\u3000"})
	if err != nil || approved.Change.Status != "approved" || len(approved.Reviews) != 1 || approved.Reviews[0].Reason != "" || len(approved.Executions) != 0 || f.runner.executions.Load() != 0 {
		t.Fatalf("批准空原因规则被改变或意外执行SQL: %v", err)
	}
}

func TestSQLChangeStaleChecksAndContentAuthorBoundaries(t *testing.T) {
	f := fixture(t)
	d := f.saved(t)
	old := f.message(t, "sql_change.check")
	f.input.ExpectedVersion = d.Change.Version
	f.input.SQL = "SELECT 2;"
	next, err := f.s.Save(f.ctx, f.owner, d.Change.ID, f.input)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.HandleCheck(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if f.runner.checks.Load() != 0 {
		t.Fatal("旧检查仍调用检查器")
	}
	if err = f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check")); err != nil {
		t.Fatal(err)
	}
	d, err = f.s.Detail(f.ctx, d.Change.ID)
	if err != nil || d.Revision.ID != next.Revision.ID || d.Change.Status != "pending_review" {
		t.Fatal("旧响应覆盖了新修订")
	}
	f.input.ExpectedVersion = d.Change.Version
	f.input.ReviewerID = f.reviewer
	if _, err = f.s.Save(f.actor(f.reviewer), f.reviewer, d.Change.ID, f.input); !errors.Is(err, ErrInvalid) {
		t.Fatal("指定审核人修改 SQL 后仍可自审")
	}
	checked, err := f.s.Recheck(f.actor(f.reviewer), f.reviewer, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version})
	if err != nil {
		t.Fatal(err)
	}
	if checked.Revision.AuthorID != f.owner || checked.Revision.CreatedBy != f.reviewer {
		t.Fatal("重新检查篡改了 SQL 内容作者")
	}
	if err = f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check")); err != nil {
		t.Fatal(err)
	}
	checked, err = f.s.Detail(f.ctx, d.Change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Review(f.actor(f.reviewer), f.reviewer, d.Change.ID, ActionInput{ExpectedVersion: checked.Change.Version, Decision: "approve"}); err != nil {
		t.Fatal("重新检查的人无法审核原作者内容", err)
	}
}

func TestSQLChangeFailedUncertainAndSkippedNeverReplay(t *testing.T) {
	for _, state := range []string{"failed", "uncertain", "skipped"} {
		t.Run(state, func(t *testing.T) {
			f := fixture(t)
			d := f.approved(t)
			var err error
			if state == "skipped" {
				d, err = f.s.Skip(f.actor(f.reviewer), f.reviewer, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, Reason: "本次发布暂不执行"})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				f.runner.executeErr = errors.New("driver copied private-sql-value and private-db-password")
				if state == "uncertain" {
					f.runner.executeErr = ErrExecutionUncertain
				}
				d, err = f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest})
				if err != nil {
					t.Fatal(err)
				}
				_ = f.s.HandleExecute(context.Background(), f.message(t, "sql_change.execute"))
				d, err = f.s.Detail(f.ctx, d.Change.ID)
				if err != nil || d.Change.Status != state {
					t.Fatalf("结果状态错误: %v", err)
				}
			}
			f.input.ExpectedVersion = d.Change.Version
			f.input.SQL = "SELECT 3;"
			d, err = f.s.Save(f.ctx, f.owner, d.Change.ID, f.input)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check")); err != nil {
				t.Fatal(err)
			}
			d, _ = f.s.Detail(f.ctx, d.Change.ID)
			d, err = f.s.Review(f.actor(f.reviewer), f.reviewer, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, Decision: "approve"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest}); !errors.Is(err, ErrState) {
				t.Fatal("通过换修订绕过了一次执行限制")
			}
		})
	}
}

func TestSQLChangeRunningCannotMutateOrBeOverwrittenByDuplicate(t *testing.T) {
	f := fixture(t)
	d := f.approved(t)
	d, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.enter = make(chan struct{})
	f.runner.release = make(chan struct{})
	finished := make(chan error, 1)
	message := f.message(t, "sql_change.execute")
	go func() { finished <- f.s.HandleExecute(context.Background(), message) }()
	select {
	case <-f.runner.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("SQL 未开始执行")
	}
	d, err = f.s.Detail(f.ctx, d.Change.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.input.ExpectedVersion = d.Change.Version
	if _, err = f.s.Save(f.ctx, f.owner, d.Change.ID, f.input); !errors.Is(err, ErrState) {
		t.Fatal("执行中可以修改")
	}
	if err = f.s.Delete(f.ctx, f.owner, d.Change.ID, d.Change.Version); !errors.Is(err, ErrState) {
		t.Fatal("执行中可以删除")
	}
	if _, err = f.s.Skip(f.actor(f.reviewer), f.reviewer, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, Reason: "不能跳过正在运行"}); !errors.Is(err, ErrState) {
		t.Fatal("执行中可以跳过")
	}
	if err = f.s.HandleExecute(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	close(f.runner.release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if f.runner.executions.Load() != 1 {
		t.Fatal("重复消息执行了两次")
	}
}

func TestSQLChangePermissionsScopeAndControlBoundaries(t *testing.T) {
	f := fixture(t)
	d := f.saved(t)
	if _, err := f.s.Detail(context.Background(), d.Change.ID); !errors.Is(err, ErrScope) {
		t.Fatal("无项目可以读取")
	}
	foreign := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, UserID: f.owner})
	foreignScope, _ := scopeFrom(foreign)
	if err := f.s.db.Create(&model.Project{ID: foreignScope.ProjectID, DepartmentID: database.DefaultDepartmentID, Name: "另一 SQL 项目", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	scope, _ := scopeFrom(f.ctx)
	var ownerMember model.ProjectMember
	if err := f.s.db.Where("project_id = ? AND user_id = ?", scope.ProjectID, f.owner).First(&ownerMember).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.Create(&model.ProjectMember{ProjectID: foreignScope.ProjectID, UserID: f.owner, RoleID: ownerMember.RoleID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Detail(foreign, d.Change.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("跨项目可以读取")
	}
	if _, err := f.s.Save(f.ctx, f.other, "", f.input); !errors.Is(err, ErrForbidden) {
		t.Fatal("可冒用其他操作者")
	}
	f.permissions.block(f.owner, "sql_change.read")
	if _, err := f.s.Recheck(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version}); !errors.Is(err, ErrForbidden) {
		t.Fatal("动作权限绕过了正文查看拒绝")
	}
	for _, sql := range []string{"", strings.Repeat("a", MaxSQLBytes+1), "SELECT '\x00';", string([]byte{0xff}), "-- +goose NO TRANSACTION\nSELECT 1;", "-- sqlfluff:rules:all\nSELECT 1;", "-- noqa\nSELECT 1;"} {
		if validateSQL(sql) {
			t.Fatal("无效或控制 SQL 被接受")
		}
	}
	if !validateSQL("SELECT 'noqa';") {
		t.Fatal("普通字面值被误当控制注释")
	}
}

func TestSQLChangeAutomaticCheckUsesOriginalSavePermission(t *testing.T) {
	f := fixture(t)
	f.permissions.block(f.owner, "sql_change.check")
	d := f.saved(t)
	if err := f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check")); err != nil {
		t.Fatal("自动检查错误地要求手动检查权限", err)
	}
	d, _ = f.s.Detail(f.ctx, d.Change.ID)
	if d.Change.Status != "pending_review" {
		t.Fatal("自动检查未正常完成")
	}
	if _, err := f.s.Recheck(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version}); !errors.Is(err, ErrForbidden) {
		t.Fatal("手动重新检查未校验独立权限")
	}
}

func TestSQLChangeTerminalFailureDoesNotReplayOrExposeRuntimeErrors(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "running"}[started], func(t *testing.T) {
			f := fixture(t)
			d := f.approved(t)
			d, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest})
			if err != nil {
				t.Fatal(err)
			}
			message := f.message(t, "sql_change.execute")
			var job model.Job
			if err = f.s.db.First(&job, "uuid = ?", message.JobID).Error; err != nil {
				t.Fatal(err)
			}
			if started {
				if err = f.s.db.Model(&model.SQLChange{}).Where("uuid = ?", d.Change.ID).Update("status", "running").Error; err != nil {
					t.Fatal(err)
				}
				if err = f.s.db.Model(&model.SQLChangeExecution{}).Where("job_id = ?", job.ID).Update("status", "running").Error; err != nil {
					t.Fatal(err)
				}
			}
			if err = f.s.db.Transaction(func(tx *gorm.DB) error {
				return f.s.HandleTerminalTaskFailure(context.Background(), tx, job, "worker_crash", "private-sql-value")
			}); err != nil {
				t.Fatal(err)
			}
			d, err = f.s.Detail(f.ctx, d.Change.ID)
			expected := "failed"
			if started {
				expected = "uncertain"
			}
			if err != nil || d.Change.Status != expected {
				t.Fatalf("任务终止未收口: %s %v", d.Change.Status, err)
			}
			if strings.Contains(d.Executions[0].Reason, "private-sql-value") {
				t.Fatal("Worker 原始错误进入工单")
			}
			if err = f.s.db.Model(&model.Job{}).Where("uuid = ?", job.ID).Update("status", model.JobFailed).Error; err != nil {
				t.Fatal(err)
			}
			if _, err = task.NewService(f.s.db, 4).Retry(f.ctx, job.ID); !errors.Is(err, task.ErrJobNotRetryable) {
				t.Fatal("任务中心仍允许 SQL 重试", err)
			}
			if f.runner.executions.Load() != 0 {
				t.Fatal("终止处理触发了真实执行")
			}
		})
	}
}

func TestSQLChangeCheckMustBeRetriedThroughTheChange(t *testing.T) {
	f := fixture(t)
	d := f.saved(t)
	message := f.message(t, "sql_change.check")
	var job model.Job
	if err := f.s.db.First(&job, "uuid = ?", message.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.Transaction(func(tx *gorm.DB) error {
		return f.s.HandleTerminalTaskFailure(context.Background(), tx, job, "check_interrupted", "")
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.Model(&model.Job{}).Where("uuid = ?", job.ID).Update("status", model.JobFailed).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := task.NewService(f.s.db, 4).Retry(f.ctx, job.ID); !errors.Is(err, task.ErrJobNotRetryable) {
		t.Fatal("旧修订检查可被任务中心换 Job ID 重放", err)
	}
	latest, err := f.s.Detail(f.ctx, d.Change.ID)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := f.s.Recheck(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: latest.Change.Version})
	if err != nil || checked.Change.RevisionID == d.Change.RevisionID || checked.Change.Status != "checking" {
		t.Fatalf("工单重新检查未建立新修订: %v", err)
	}
}

func TestSQLChangeLogicalSubjectsAndEncryptedReportBounds(t *testing.T) {
	f := fixture(t)
	d := f.approved(t)
	if _, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"sql_change.check", "sql_change.execute"} {
		message := f.message(t, kind)
		var job model.Job
		var event model.OutboxEvent
		if err := f.s.db.First(&job, "uuid = ?", message.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.s.db.First(&event, "aggregate_id = ?", message.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if job.Subject != task.LogicalSubjectPrefix+"."+kind || event.Subject != job.Subject {
			t.Fatal("任务逻辑主题混入了传输命名空间")
		}
		for _, prefix := range []string{"edo.custom", "edo.task.custom"} {
			mapped, err := task.TransportSubject(event.Subject, kind, prefix)
			if err != nil || mapped != prefix+"."+kind {
				t.Fatalf("自定义传输前缀无法投递: %s %v", mapped, err)
			}
		}
	}
	for _, size := range []int{maxCheckReportBytes - 64, maxCheckReportBytes + 64} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			f := fixture(t)
			f.runner.checkReport = json.RawMessage(`[{"description":"` + strings.Repeat("x", size) + `"}]`)
			d := f.saved(t)
			err := f.s.HandleCheck(context.Background(), f.message(t, "sql_change.check"))
			want := "pending_review"
			if size > maxCheckReportBytes {
				want = "check_failed"
				if !errors.Is(err, ErrCheckFailed) {
					t.Fatalf("超限报告未安全拒绝: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			latest, err := f.s.Detail(f.ctx, d.Change.ID)
			if err != nil || latest.Change.Status != want {
				t.Fatalf("加密报告容量导致状态未收口: %v", err)
			}
			var revision model.SQLChangeRevision
			if err := f.s.db.First(&revision, "uuid = ?", latest.Change.RevisionID).Error; err != nil {
				t.Fatal(err)
			}
			if len(revision.CheckReport) > 65535 {
				t.Fatal("加密报告超过 MySQL TEXT 容量")
			}
		})
	}
}

func TestSQLChangeNewReleaseDoesNotReusePreviousSuccessOrSkip(t *testing.T) {
	f := fixture(t)
	first := f.approved(t)
	first, err := f.s.Skip(f.actor(f.reviewer), f.reviewer, first.Change.ID, ActionInput{ExpectedVersion: first.Change.Version, Reason: "本次暂不执行"})
	if err != nil {
		t.Fatal(err)
	}
	var run model.PipelineRun
	if err = f.s.db.WithContext(f.ctx).First(&run, "uuid = ?", f.input.PipelineRunID).Error; err != nil {
		t.Fatal(err)
	}
	run.RowID = 0
	run.ID = uuid.NewString()
	if err = f.s.db.WithContext(f.ctx).Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	f.input.PipelineRunID = run.ID
	second := f.approved(t)
	second, err = f.s.Execute(f.ctx, f.owner, second.Change.ID, ActionInput{ExpectedVersion: second.Change.Version, ConfirmDigest: second.Change.Digest})
	if err != nil {
		t.Fatal("另一次发布错误沿用了跳过", err)
	}
	if err = f.s.HandleExecute(context.Background(), f.message(t, "sql_change.execute")); err != nil {
		t.Fatal(err)
	}
	if f.runner.executions.Load() != 1 {
		t.Fatal("本次发布未独立执行")
	}
	first, err = f.s.Detail(f.ctx, first.Change.ID)
	if err != nil || first.Change.Status != "skipped" {
		t.Fatal("新发布修改了旧发布状态")
	}
}

func TestSQLChangeOptionsAndEscapedSearchRemainScoped(t *testing.T) {
	f := fixture(t)
	f.input.Name = "SQL_%"
	d := f.saved(t)
	list, err := f.s.List(f.ctx, ListInput{Search: "_%", Page: 1, PageSize: 10})
	if err != nil || list.Total != 1 || list.Items[0].ID != d.Change.ID {
		t.Fatal("搜索未按字面值匹配", err)
	}
	if list.Items[0].AuthorID != f.owner {
		t.Fatal("列表未返回当前 SQL 内容作者")
	}
	f.permissions.block(f.other, "sql_change.read")
	options, err := f.s.Options(f.ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, reviewer := range options.Reviewers {
		if reviewer.ID == f.owner || reviewer.ID == f.other {
			t.Fatal("候选包含自审或无查看权限的人")
		}
	}
	if len(options.Applications) != 1 || len(options.Databases) != 1 || len(options.PipelineRuns) != 1 {
		t.Fatal("选择项缺失")
	}
	if target := options.Databases[0]; target.Host != "db.example" || target.Port != 5432 || target.Database != "application" {
		t.Fatal("执行确认缺少精确目标地址和数据库名")
	}
	raw, _ := json.Marshal(options)
	if strings.Contains(string(raw), "private-db-password") || strings.Contains(string(raw), "executor") || strings.Contains(string(raw), "private-sql-value") {
		t.Fatal("候选泄露凭据或 SQL 内容")
	}
}

func TestSQLChangeQueuedDuplicateLockFailureCannotOverwriteRunningOwner(t *testing.T) {
	f := fixture(t)
	d := f.approved(t)
	_, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest})
	if err != nil {
		t.Fatal(err)
	}
	gate := &gatedDomainLocks{delegate: f.s.locks, entered: make(chan struct{}), release: make(chan struct{})}
	f.s.ConfigureLocks(gate, "sql-test")
	f.runner.enter = make(chan struct{})
	f.runner.release = make(chan struct{})
	message := f.message(t, "sql_change.execute")
	slow, owner := make(chan error, 1), make(chan error, 1)
	go func() { slow <- f.s.HandleExecute(context.Background(), message) }()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("首消息没有固定 queued 状态")
	}
	go func() { owner <- f.s.HandleExecute(context.Background(), message) }()
	select {
	case <-f.runner.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("后消息没有取得执行权")
	}
	close(gate.release)
	if err = <-slow; err != nil {
		t.Fatal("过期 queued 消息导致任务失败", err)
	}
	current, err := f.s.Detail(f.ctx, d.Change.ID)
	if err != nil || current.Change.Status != "running" {
		t.Fatal("旧消息覆盖了执行者状态", err)
	}
	close(f.runner.release)
	if err = <-owner; err != nil {
		t.Fatal(err)
	}
	if f.runner.executions.Load() != 1 {
		t.Fatal("竞争触发重复执行")
	}
}

func TestSQLChangeQueuedExecutionRechecksMembershipProjectAndReviewer(t *testing.T) {
	for _, change := range []string{"remove-actor", "disable-project", "remove-reviewer", "move-reviewer", "change-database"} {
		t.Run(change, func(t *testing.T) {
			f := fixture(t)
			d := f.approved(t)
			_, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest})
			if err != nil {
				t.Fatal(err)
			}
			scope, _ := scopeFrom(f.ctx)
			switch change {
			case "remove-actor":
				err = f.s.db.Where("project_id = ? AND user_id = ?", scope.ProjectID, f.owner).Delete(&model.ProjectMember{}).Error
			case "disable-project":
				err = f.s.db.Model(&model.Project{}).Where("uuid = ?", scope.ProjectID).Update("is_active", false).Error
			case "remove-reviewer":
				err = f.s.db.Where("project_id = ? AND user_id = ?", scope.ProjectID, f.reviewer).Delete(&model.ProjectMember{}).Error
			case "move-reviewer":
				err = f.s.db.Model(&model.User{}).Where("uuid = ?", f.reviewer).Update("department_id", uuid.NewString()).Error
			case "change-database":
				err = f.s.db.Model(&model.ApplicationDatabase{}).Where("uuid = ?", f.input.DatabaseID).Update("version", 2).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.HandleExecute(context.Background(), f.message(t, "sql_change.execute")); err == nil {
				t.Fatal("失效授权仍正常执行")
			}
			if f.runner.executions.Load() != 0 {
				t.Fatal("授权或目标变化后执行了 SQL")
			}
			var item model.SQLChange
			if err = f.s.db.First(&item, "uuid = ?", d.Change.ID).Error; err != nil || item.Status != "failed" {
				t.Fatal("未安全收口失效任务", err)
			}
		})
	}
}

func TestSQLChangeRunningExecutionContinuouslyRechecksAuthorityAndTarget(t *testing.T) {
	for _, change := range []string{"actor-read", "actor-execute", "database-execute", "reviewer-read", "reviewer-review", "remove-actor", "remove-reviewer", "disable-project", "change-database", "change-fingerprint"} {
		t.Run(change, func(t *testing.T) {
			f := fixture(t)
			f.s.executionCheckInterval = 20 * time.Millisecond
			d := f.approved(t)
			if _, err := f.s.Execute(f.ctx, f.owner, d.Change.ID, ActionInput{ExpectedVersion: d.Change.Version, ConfirmDigest: d.Change.Digest}); err != nil {
				t.Fatal(err)
			}
			f.runner.enter = make(chan struct{})
			f.runner.release = make(chan struct{})
			// 即使运行器把取消误报为成功，领域也必须保守落账为结果不确定。
			f.runner.returnNilOnCancel = change == "actor-read"
			ctx, cancel := context.WithCancel(context.Background())
			result, exited := make(chan error, 1), make(chan struct{})
			message := f.message(t, "sql_change.execute")
			go func() {
				defer close(exited)
				result <- f.s.HandleExecute(ctx, message)
			}()
			defer func() {
				cancel()
				select {
				case <-exited:
				case <-time.After(5 * time.Second):
					t.Error("执行协程未随测试取消退出")
				}
			}()
			select {
			case <-f.runner.enter:
			case <-time.After(5 * time.Second):
				t.Fatal("SQL 未进入执行阶段")
			}
			scope, _ := scopeFrom(f.ctx)
			var err error
			switch change {
			case "actor-read":
				f.permissions.block(f.owner, "sql_change.read")
			case "actor-execute":
				f.permissions.block(f.owner, "sql_change.execute")
			case "database-execute":
				f.permissions.block(f.owner, "application_database.execute")
			case "reviewer-read":
				f.permissions.block(f.reviewer, "sql_change.read")
			case "reviewer-review":
				f.permissions.block(f.reviewer, "sql_change.review")
			case "remove-actor":
				err = f.s.db.Where("project_id = ? AND user_id = ?", scope.ProjectID, f.owner).Delete(&model.ProjectMember{}).Error
			case "remove-reviewer":
				err = f.s.db.Where("project_id = ? AND user_id = ?", scope.ProjectID, f.reviewer).Delete(&model.ProjectMember{}).Error
			case "disable-project":
				err = f.s.db.Model(&model.Project{}).Where("uuid = ?", scope.ProjectID).Update("is_active", false).Error
			case "change-database":
				err = f.s.db.Model(&model.ApplicationDatabase{}).Where("uuid = ?", f.input.DatabaseID).Update("version", 2).Error
			case "change-fingerprint":
				err = f.s.db.Model(&model.ApplicationDatabase{}).Where("uuid = ?", f.input.DatabaseID).Update("fingerprint", digest("other-target")).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-result:
				if !errors.Is(err, ErrExecutionUncertain) {
					t.Fatalf("执行中授权变化未保守收口: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("执行中授权变化后未取消运行器")
			}
			var item model.SQLChange
			if err = f.s.db.First(&item, "uuid = ?", d.Change.ID).Error; err != nil || item.Status != "uncertain" || item.SuccessAt != nil {
				t.Fatal("执行中撤权被错误标为成功", err)
			}
			if err = f.s.HandleExecute(context.Background(), message); err != nil || f.runner.executions.Load() != 1 {
				t.Fatal("结果不确定的 SQL 被重放", err)
			}
		})
	}
}
