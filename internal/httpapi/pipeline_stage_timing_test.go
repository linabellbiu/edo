package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"gorm.io/gorm"
)

func TestPipelineStageTimingIsAvailableToRunReader(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	repo := createRepositoryForDepartmentTest(t, router, admin, "阶段耗时测试")
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	base := time.Now().UTC().Add(-time.Minute)
	end := base.Add(12 * time.Second)
	run := model.PipelineRun{ID: "timing-run", ApplicationID: "timing-app", ProjectID: project, DepartmentID: database.DefaultDepartmentID, Status: model.PipelineRunSucceeded, CreatedAt: base, UpdatedAt: end, WorkflowSnapshot: `{"schema_version":1,"source":{"id":"source","type":"trigger","name":"代码源"},"stages":[{"id":"build","name":"构建","tasks":[{"id":"image","name":"构建镜像","type":"build"}]}]}`}
	for _, row := range []any{
		&model.Application{ID: run.ApplicationID, Name: "耗时应用", RepositoryID: repo, ProjectID: project, DepartmentID: database.DefaultDepartmentID},
		&run,
		&model.PipelineNodeRun{ID: "timing-node", PipelineRunID: run.ID, NodeID: "image", StageID: "build", ProjectID: project, DepartmentID: database.DefaultDepartmentID, Status: model.PipelineRunSucceeded, CreatedAt: base, StartedAt: &base, FinishedAt: &end},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	role := createRoleForHTTPTest(t, router, admin, "timing-reader", []string{"pipeline_run.read"})
	createUserForHTTPTest(t, router, admin, "timing-reader", database.DefaultDepartmentID, []string{role})
	reader := loginForDepartmentTest(t, router, "timing-reader")
	for _, path := range []string{"/api/v1/pipeline-runs", "/api/v1/pipeline-runs/" + run.ID} {
		response := performJSONRequest(t, router, http.MethodGet, path, nil, reader)
		var body struct {
			Runs []model.PipelineRun `json:"pipeline_runs"`
			Run  model.PipelineRun   `json:"pipeline_run"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("运行查看权限应能读取耗时: %d %s", response.Code, response.Body.String())
		}
		got := body.Run
		if len(body.Runs) > 0 {
			got = body.Runs[0]
		}
		if got.ExecutionGraph == nil || len(got.ExecutionGraph.Stages) != 1 {
			t.Fatalf("接口缺少阶段: %+v", got)
		}
		timing := got.ExecutionGraph.Stages[0].Timing
		if timing == nil || timing.DurationMS == nil || *timing.DurationMS != 12000 || timing.Running || timing.FinishedAt == nil {
			t.Fatalf("接口未返回实际固定耗时: %+v", timing)
		}
		if got.Timing == nil || got.Timing.DurationMS == nil || *got.Timing.DurationMS != 12000 || got.Timing.Running {
			t.Fatalf("运行查看权限未取得真实总耗时: %+v", got.Timing)
		}
	}
}
