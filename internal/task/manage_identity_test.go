package task

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
	"gorm.io/datatypes"
)

func TestTaskDetailUsesUUIDAndKeepsProjectScope(t *testing.T) {
	db := testutil.NewDatabase(t)
	service := NewService(db, 4)
	now := time.Now().UTC()
	runID := "00000000-0000-4000-8000-000000000001"
	jobs := []model.Job{
		{ID: "00000000-0000-4000-8000-000000000009", ProjectID: "project-a", DepartmentID: "department-a", Kind: "pipeline.build", Status: model.JobFailed, Payload: datatypes.JSON(`{"pipeline_run_id":"` + runID + `","password":"private-test-value"}`), MaxAttempts: 4, ErrorMessage: "构建失败，请检查 Dockerfile", CreatedAt: now, UpdatedAt: now},
		{ID: "00000000-0000-4000-8000-000000000002", ProjectID: "project-b", DepartmentID: "department-a", Kind: "pipeline.build", Status: model.JobFailed, Payload: datatypes.JSON(`{}`), MaxAttempts: 4, CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "operator", ProjectID: "project-a", DepartmentID: "department-a"})
	view, err := service.Get(ctx, jobs[0].ID)
	if err != nil || view.ID != jobs[0].ID || view.RowID != jobs[0].RowID || view.PipelineRunID != runID {
		t.Fatalf("任务详情标识错误: %+v %v", view, err)
	}
	data, err := json.Marshal(view)
	if err != nil || strings.Contains(string(data), "private-test-value") || strings.Contains(string(data), "payload") {
		t.Fatalf("任务详情泄漏内部参数: %s %v", data, err)
	}
	for _, id := range []string{jobs[1].ID, strconv.FormatUint(jobs[0].RowID, 10), "missing"} {
		if _, err := service.Get(ctx, id); !errors.Is(err, ErrJobNotFound) {
			t.Fatalf("关联必须使用UUID且受项目隔离: %s %v", id, err)
		}
	}
	list, err := service.List(context.Background(), model.JobFailed, 1)
	if err != nil || len(list) != 1 || list[0].ID != jobs[1].ID {
		t.Fatalf("列表必须按行号倒序: %+v %v", list, err)
	}
}
