package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestQueryRunsFiltersBeforePaginationAndKeepsStableIDOrder(t *testing.T) {
	db := testutil.NewDatabase(t)
	service := NewService(db, nil, nil)
	service.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	base := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	if err := db.Create(&model.GitRepository{ID: "query-repository", Name: "query-repository"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Application{ID: "query-application", Name: "query-app", RepositoryID: "query-repository", CreatedAt: base, UpdatedAt: base}).Error; err != nil {
		t.Fatal(err)
	}
	runs := []model.PipelineRun{
		queryRunFixture("00000000-0000-4000-8000-000000000009", base.Add(-time.Second), model.PipelineRunFailed),
		queryRunFixture("00000000-0000-4000-8000-000000000003", base, model.PipelineRunFailed),
		queryRunFixture("00000000-0000-4000-8000-000000000004", base, model.PipelineRunFailed),
		queryRunFixture("00000000-0000-4000-8000-000000000000", base.Add(-24*time.Hour), model.PipelineRunSucceeded),
		queryRunFixture("00000000-0000-4000-8000-000000000001", base.Add(2*time.Hour), model.PipelineRunFailed),
	}
	runs[1].CurrentNodeID = "build"
	runs[1].WorkflowSnapshot = `{"schema_version":1,"source":{"id":"source","name":"代码源"},"stages":[{"id":"stage","name":"构建","tasks":[{"id":"build","name":"镜像构建","type":"build"}]}]}`
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	from := base.In(time.FixedZone("东八区", 8*60*60))
	to := base.Add(2 * time.Hour)
	options := RunQueryOptions{Status: model.PipelineRunFailed, From: &from, To: &to, PageSize: 1}
	first, err := service.QueryRuns(context.Background(), options)
	if err != nil || first.Total != 2 || first.Page != 1 || first.PageSize != 1 || len(first.Runs) != 1 || first.Runs[0].ID != runs[2].ID {
		t.Fatalf("筛选应包含开始时刻、排除结束时刻，并在分页前统计: result=%+v err=%v", first, err)
	}
	options.Page = 2
	second, err := service.QueryRuns(context.Background(), options)
	if err != nil || second.Total != 2 || len(second.Runs) != 1 || second.Runs[0].ID != runs[1].ID {
		t.Fatalf("同一创建时刻的跨页记录不能重复或遗漏: result=%+v err=%v", second, err)
	}
	if second.Runs[0].CurrentNodeName != "镜像构建" || second.Runs[0].ExecutionGraph == nil {
		t.Fatal("筛选查询遗漏运行图及当前任务信息")
	}
	options.Page = 3
	empty, err := service.QueryRuns(context.Background(), options)
	if err != nil || empty.Total != 2 || empty.Runs == nil || len(empty.Runs) != 0 {
		t.Fatalf("超出末页应返回空数组并保留筛选总数: result=%+v err=%v", empty, err)
	}

	for _, test := range []struct {
		order string
		want  []string
	}{
		{"", []string{runs[4].ID, runs[3].ID, runs[2].ID, runs[1].ID, runs[0].ID}},
		{"asc", []string{runs[0].ID, runs[1].ID, runs[2].ID, runs[3].ID, runs[4].ID}},
	} {
		result, err := service.QueryRuns(context.Background(), RunQueryOptions{Order: test.order})
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, len(result.Runs))
		for index := range result.Runs {
			got[index] = result.Runs[index].ID
		}
		if !slices.Equal(got, test.want) {
			t.Fatalf("自增行号排序不能被随机 UUID 顺序替代: order=%q got=%v want=%v", test.order, got, test.want)
		}
	}
	result, err := service.QueryRuns(context.Background(), RunQueryOptions{Status: model.PipelineRunSucceeded})
	if err != nil || result.Total != 1 || len(result.Runs) != 1 || result.Runs[0].ID != runs[3].ID {
		t.Fatalf("成功状态筛选错误: result=%+v err=%v", result, err)
	}
}

func TestQueryRunsMatchesExactReleaseExecution(t *testing.T) {
	db := testutil.NewDatabase(t)
	service := NewService(db, nil, nil)
	base := time.Now().UTC()
	for _, value := range []any{&model.GitRepository{ID: "query-repository", Name: "query-repository"}, &model.Application{ID: "query-application", Name: "query-app", RepositoryID: "query-repository", CreatedAt: base, UpdatedAt: base}} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	records := []model.PipelineRun{queryRunFixture("run-a", base, model.PipelineRunFailed), queryRunFixture("run-b", base, model.PipelineRunSucceeded), queryRunFixture("run-c", base, model.PipelineRunFailed)}
	records[0].ReleasePlanExecutionID = "execution-a"
	records[1].ReleasePlanExecutionID = "execution-a"
	records[2].ReleasePlanExecutionID = "execution-b"
	if err := db.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	result, err := service.QueryRuns(context.Background(), RunQueryOptions{ReleasePlanExecutionID: "execution-a"})
	if err != nil || result.Total != 2 || len(result.Runs) != 2 || result.Runs[0].ID != "run-b" || result.Runs[1].ID != "run-a" {
		t.Fatalf("同次发布的完整运行列表匹配错误: %+v %v", result, err)
	}
}

func TestQueryRunsPreservesDepartmentAndProjectScope(t *testing.T) {
	db := testutil.NewDatabase(t)
	service := NewService(db, nil, nil)
	base := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	if err := db.Create(&model.GitRepository{ID: "query-repository", Name: "query-repository"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Application{ID: "query-application", Name: "query-app", RepositoryID: "query-repository", CreatedAt: base, UpdatedAt: base}).Error; err != nil {
		t.Fatal(err)
	}
	runs := []model.PipelineRun{
		queryRunFixture("scope-visible", base, model.PipelineRunFailed),
		queryRunFixture("scope-other-project", base.Add(time.Hour), model.PipelineRunFailed),
		queryRunFixture("scope-other-department", base.Add(2*time.Hour), model.PipelineRunFailed),
	}
	runs[0].DepartmentID, runs[0].ProjectID = "department-a", "project-a"
	runs[1].DepartmentID, runs[1].ProjectID = "department-a", "project-b"
	runs[2].DepartmentID, runs[2].ProjectID = "department-b", "project-a"
	if err := db.Create(&runs).Error; err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
			UserID: "query-user", DepartmentID: run.DepartmentID, ProjectID: run.ProjectID,
		})
		result, err := service.QueryRuns(ctx, RunQueryOptions{Status: model.PipelineRunFailed, PageSize: 1})
		if err != nil || result.Total != 1 || len(result.Runs) != 1 || result.Runs[0].ID != run.ID {
			t.Fatalf("列表和总数都必须使用当前部门及项目: result=%+v want=%s err=%v", result, run.ID, err)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: "query-user", DepartmentID: "department-a", ProjectID: "empty-project",
	})
	result, err := service.QueryRuns(ctx, RunQueryOptions{})
	if err != nil || result.Total != 0 || len(result.Runs) != 0 {
		t.Fatalf("空项目不能泄漏其他项目运行或总数: result=%+v err=%v", result, err)
	}
}

func TestQueryRunsRejectsInvalidFiltersAndHidesDatabaseErrors(t *testing.T) {
	service := &Service{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	from := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	to := from.Add(-time.Hour)
	for _, options := range []RunQueryOptions{
		{Status: "failed' OR 1=1"}, {Order: "desc; DROP TABLE pipeline_runs"}, {Order: "random"},
		{Page: -1}, {PageSize: -1}, {PageSize: 201}, {Page: int(^uint(0) >> 1), PageSize: 200},
		{From: &from, To: &to}, {From: &from, To: &from}, {From: new(time.Time)},
	} {
		if _, err := service.QueryRuns(context.Background(), options); !errors.Is(err, ErrInvalidRunQuery) {
			t.Fatalf("无效筛选条件未在查询前拒绝: options=%+v err=%v", options, err)
		}
	}
	db := testutil.NewDatabase(t)
	service.db = db
	if err := db.Migrator().DropTable(&model.PipelineRun{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.QueryRuns(context.Background(), RunQueryOptions{}); !errors.Is(err, ErrRunQueryFailed) || err.Error() != "查询运行记录失败，请稍后重试" {
		t.Fatalf("数据库细节不得进入查询错误: %v", err)
	}
}

func queryRunFixture(id string, created time.Time, status model.PipelineRunStatus) model.PipelineRun {
	return model.PipelineRun{
		ID: id, ApplicationID: "query-application", Trigger: "manual", Ref: "refs/heads/main",
		CommitSHA: "aabbccdd", Status: status, Stage: "build", CreatedAt: created, UpdatedAt: created,
	}
}
