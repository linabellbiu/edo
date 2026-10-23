package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestLatestReleasePlanExecutionsKeepOnlySelectedLatestRows(t *testing.T) {
	db := testutil.NewDatabase(t)
	service := &Service{db: db}
	plans := []model.ReleasePlan{{ID: uuid.NewString()}, {ID: uuid.NewString()}, {ID: uuid.NewString()}}
	base := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	first := latestExecutionFixture(plans[0].ID, base)
	second := latestExecutionFixture(plans[1].ID, base.Add(time.Hour))
	old := latestExecutionFixture(plans[0].ID, base.Add(-time.Hour))
	outside := latestExecutionFixture(uuid.NewString(), base.Add(2*time.Hour))
	for _, execution := range []*model.ReleasePlanExecution{first, second, old, outside} {
		if err := db.Create(execution).Error; err != nil {
			t.Fatal(err)
		}
	}
	for order := range 2 {
		item := latestExecutionItemFixture(first.ID, order)
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	oldItem := latestExecutionItemFixture(old.ID, 0)
	if err := db.Create(&oldItem).Error; err != nil {
		t.Fatal(err)
	}
	plans[2].LatestExecution = outside
	if err := service.attachLatestReleasePlanExecutions(context.Background(), plans); err != nil {
		t.Fatal(err)
	}
	if plans[0].LatestExecution == nil || plans[0].LatestExecution.ID != first.ID ||
		plans[1].LatestExecution == nil || plans[1].LatestExecution.ID != second.ID || plans[2].LatestExecution != nil {
		t.Fatalf("最近执行关联错误: %+v", plans)
	}
	items := plans[0].LatestExecution.Items
	if len(items) != 2 || items[0].SortOrder != 0 || items[1].SortOrder != 1 || items[0].ReleasePlanExecutionID != first.ID {
		t.Fatalf("没有仅加载最近执行的有序应用项: %+v", items)
	}
}

func TestLatestReleasePlanExecutionsBreakTimeTiesByID(t *testing.T) {
	db := testutil.NewDatabase(t)
	service := &Service{db: db}
	plans := []model.ReleasePlan{{ID: uuid.NewString()}}
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	low := latestExecutionFixture(plans[0].ID, now)
	high := latestExecutionFixture(plans[0].ID, now)
	low.ID, high.ID = "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"
	// 反向插入，避免依赖插入顺序得到预期结果。
	for _, execution := range []*model.ReleasePlanExecution{high, low} {
		if err := db.Create(execution).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if err := service.attachLatestReleasePlanExecutions(context.Background(), plans); err != nil {
			t.Fatal(err)
		}
		if plans[0].LatestExecution == nil || plans[0].LatestExecution.ID != high.ID {
			t.Fatalf("相同创建时间没有按 ID 稳定选择: %+v", plans[0].LatestExecution)
		}
	}
}

func TestLatestReleasePlanExecutionsDoNotMaterializeHistoricalSnapshots(t *testing.T) {
	db := testutil.NewDatabase(t)
	service := &Service{db: db}
	plans, expected := seedLatestExecutionHistory(t, db, 4, 128, 16<<10)
	var loadedExecutions int64
	if err := db.Callback().Query().After("gorm:query").Register("test:count_latest_executions", func(tx *gorm.DB) {
		if tx.Statement.Table == "release_plan_executions" {
			loadedExecutions += tx.RowsAffected
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.attachLatestReleasePlanExecutions(context.Background(), plans); err != nil {
		t.Fatal(err)
	}
	if loadedExecutions != int64(len(plans)) {
		t.Fatalf("数据库仍向进程返回完整执行历史: loaded=%d want=%d", loadedExecutions, len(plans))
	}
	for _, plan := range plans {
		if plan.LatestExecution == nil || plan.LatestExecution.ID != expected[plan.ID] || plan.LatestExecution.Snapshot != "" {
			t.Fatalf("最近执行错误或读取了大型快照: %+v", plan.LatestExecution)
		}
	}
}

func TestLatestReleasePlanExecutionsPreserveDepartmentAndProjectScope(t *testing.T) {
	assertLatestExecutionScope(t, testutil.NewDatabase(t))
}

func TestLatestReleasePlanExecutionsExternalDatabases(t *testing.T) {
	for _, target := range []struct{ driver, variable string }{
		{"postgres", "EDO_TEST_POSTGRES_DSN"}, {"mysql", "EDO_TEST_MYSQL_DSN"},
	} {
		t.Run(target.driver, func(t *testing.T) {
			selected := os.Getenv("EDO_TEST_DATABASE_DRIVER")
			if selected != "" && selected != target.driver {
				t.Skip("当前任务验证其他数据库")
			}
			dsn := os.Getenv(target.variable)
			if dsn == "" {
				if selected == target.driver {
					t.Fatalf("已选定的数据库缺少 %s", target.variable)
				}
				t.Skip("未配置独立的真实数据库测试连接")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			db, err := database.Open(ctx, config.Database{
				Driver: target.driver, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 2,
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal("打开真实测试数据库失败")
			}
			t.Cleanup(func() { _ = database.Close(db) })
			if !db.Migrator().HasTable(&model.ReleasePlanExecution{}) || !db.Migrator().HasTable(&model.ReleasePlanExecutionItem{}) {
				t.Fatal("请先为可丢弃的测试数据库执行真实迁移测试")
			}
			assertLatestExecutionScope(t, db)
			assertLatestExecutionExternalOrdering(t, db)
		})
	}
}

func assertLatestExecutionExternalOrdering(t *testing.T, db *gorm.DB) {
	t.Helper()
	plans, expected := seedLatestExecutionHistory(t, db, 2, 3, 8192)
	planIDs := []string{plans[0].ID, plans[1].ID, uuid.NewString()}
	t.Cleanup(func() {
		if err := db.Where("release_plan_id IN ?", planIDs).Delete(&model.ReleasePlanExecution{}).Error; err != nil {
			t.Error("清理测试执行历史失败")
		}
	})
	now := time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC)
	low, high := latestExecutionFixture(plans[0].ID, now), latestExecutionFixture(plans[0].ID, now)
	prefix := uuid.NewString()[:24]
	low.ID, high.ID = prefix+"000000000001", prefix+"000000000002"
	outside := latestExecutionFixture(planIDs[2], now.Add(time.Hour))
	for _, execution := range []*model.ReleasePlanExecution{high, low, outside} {
		if err := db.Create(execution).Error; err != nil {
			t.Fatal(err)
		}
	}
	expected[plans[0].ID] = high.ID
	service := &Service{db: db}
	if err := service.attachLatestReleasePlanExecutions(context.Background(), plans); err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		if plan.LatestExecution == nil || plan.LatestExecution.ID != expected[plan.ID] || plan.LatestExecution.Snapshot != "" {
			t.Fatalf("真实数据库的多计划排序或字段裁剪错误: %+v", plan.LatestExecution)
		}
	}
}

func assertLatestExecutionScope(t *testing.T, db *gorm.DB) {
	t.Helper()
	service := &Service{db: db}
	planID := uuid.NewString()
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	visible := latestExecutionFixture(planID, now)
	visible.DepartmentID, visible.ProjectID = uuid.NewString(), uuid.NewString()
	otherProject := latestExecutionFixture(planID, now.Add(time.Hour))
	otherProject.DepartmentID, otherProject.ProjectID = visible.DepartmentID, uuid.NewString()
	otherDepartment := latestExecutionFixture(planID, now.Add(2*time.Hour))
	otherDepartment.DepartmentID, otherDepartment.ProjectID = uuid.NewString(), visible.ProjectID
	all := []*model.ReleasePlanExecution{visible, otherProject, otherDepartment}
	ids := make([]string, 0, len(all))
	for _, execution := range all {
		ids = append(ids, execution.ID)
		if err := db.Create(execution).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := db.Where("id IN ?", ids).Delete(&model.ReleasePlanExecution{}).Error; err != nil {
			t.Error("清理测试执行记录失败")
		}
	})
	for _, expected := range all {
		ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
			UserID: "test-user", DepartmentID: expected.DepartmentID, ProjectID: expected.ProjectID,
		})
		plans := []model.ReleasePlan{{ID: planID}}
		if err := service.attachLatestReleasePlanExecutions(ctx, plans); err != nil {
			t.Fatal(err)
		}
		if plans[0].LatestExecution == nil || plans[0].LatestExecution.ID != expected.ID {
			t.Fatalf("范围外的新执行影响当前范围的最近执行: got=%+v want=%s", plans[0].LatestExecution, expected.ID)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: "test-user", DepartmentID: visible.DepartmentID, ProjectID: uuid.NewString(),
	})
	plans := []model.ReleasePlan{{ID: planID}}
	if err := service.attachLatestReleasePlanExecutions(ctx, plans); err != nil || plans[0].LatestExecution != nil {
		t.Fatalf("空项目范围不应读取其他项目的执行: execution=%+v err=%v", plans[0].LatestExecution, err)
	}
}

func latestExecutionFixture(planID string, created time.Time) *model.ReleasePlanExecution {
	return &model.ReleasePlanExecution{
		ID: uuid.NewString(), ReleasePlanID: planID, RequestID: uuid.NewString(),
		Status: model.ReleasePlanExecutionSucceeded, Snapshot: "{}",
		DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID),
		CreatedBy: "test-admin", CreatedAt: created, UpdatedAt: created,
	}
}

func latestExecutionItemFixture(executionID string, order int) model.ReleasePlanExecutionItem {
	return model.ReleasePlanExecutionItem{
		ID: uuid.NewString(), ReleasePlanExecutionID: executionID, ReleaseGroupID: uuid.NewString(),
		ReleaseGroupApplicationID: uuid.NewString(), ApplicationID: uuid.NewString(), WorkflowID: uuid.NewString(),
		PipelineRunID: uuid.NewString(), Status: model.ReleasePlanExecutionItemSucceeded,
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), SourceNodeID: "source", SortOrder: order,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func seedLatestExecutionHistory(tb testing.TB, db *gorm.DB, planCount, historyCount, payloadSize int) ([]model.ReleasePlan, map[string]string) {
	tb.Helper()
	plans := make([]model.ReleasePlan, planCount)
	expected := make(map[string]string, planCount)
	payload := `{"workflow":"` + strings.Repeat("x", payloadSize) + `"}`
	rows := make([]model.ReleasePlanExecution, 0, planCount*historyCount)
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	for i := range plans {
		plans[i].ID = uuid.NewString()
		for version := range historyCount {
			execution := latestExecutionFixture(plans[i].ID, now.Add(time.Duration(version)*time.Second))
			execution.Snapshot = payload
			rows = append(rows, *execution)
			expected[plans[i].ID] = execution.ID
		}
	}
	if err := db.CreateInBatches(rows, 50).Error; err != nil {
		tb.Fatal(err)
	}
	return plans, expected
}

func BenchmarkLatestReleasePlanExecutions(b *testing.B) {
	db := testutil.NewDatabase(b)
	service := &Service{db: db}
	for _, historyCount := range []int{10, 1000} {
		plans, _ := seedLatestExecutionHistory(b, db, 4, historyCount, 4096)
		planIDs := make([]string, len(plans))
		for i := range plans {
			planIDs[i] = plans[i].ID
		}
		b.Run(fmt.Sprintf("history_%d/all_rows", historyCount), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := attachLatestExecutionLegacyBenchmark(db, plans, planIDs); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("history_%d/latest_rows", historyCount), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := service.attachLatestReleasePlanExecutions(context.Background(), plans); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// 保留原始全历史查询作为基准，比较相同返回字段和应用项查询的分配量。
func attachLatestExecutionLegacyBenchmark(db *gorm.DB, plans []model.ReleasePlan, planIDs []string) error {
	var executions []model.ReleasePlanExecution
	if err := db.Select("id", "release_plan_id", "request_id", "status", "created_by", "started_at", "finished_at", "created_at", "updated_at").
		Where("release_plan_id IN ?", planIDs).Order("created_at DESC").Find(&executions).Error; err != nil {
		return err
	}
	latest := make(map[string]*model.ReleasePlanExecution, len(executions))
	for i := range executions {
		if latest[executions[i].ReleasePlanID] == nil {
			latest[executions[i].ReleasePlanID] = &executions[i]
		}
	}
	ids := make([]string, 0, len(latest))
	for i := range plans {
		plans[i].LatestExecution = latest[plans[i].ID]
		if plans[i].LatestExecution != nil {
			ids = append(ids, plans[i].LatestExecution.ID)
		}
	}
	var items []model.ReleasePlanExecutionItem
	return db.Where("release_plan_execution_id IN ?", ids).Order("sort_order ASC").Find(&items).Error
}
