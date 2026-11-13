package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/model"
	"gorm.io/gorm"
)

func TestWorkflowRunHistoryHTTPFiltersExactWorkflowAndState(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	first := seedRealtimeRun(t, db, model.PipelineRunSucceeded)
	if err := db.Model(&first).Update("workflow_id", "flow-a").Error; err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		id, workflow string
		status       model.PipelineRunStatus
	}{
		{"other-workflow-run", "flow-b", model.PipelineRunSucceeded},
		{"active-workflow-run", "flow-a", model.PipelineRunRunning},
	} {
		run := model.PipelineRun{ID: entry.id, ApplicationID: first.ApplicationID, WorkflowID: entry.workflow, Status: entry.status}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
	}
	cookie := realtimeLogin(t, router)
	for _, group := range []struct{ name, want string }{{"finished", first.ID}, {"active", "active-workflow-run"}} {
		path := "/api/v1/pipeline-runs?application_id=" + first.ApplicationID + "&workflow_id=flow-a&status_group=" + group.name + "&limit=50"
		response := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
		var result struct {
			Runs     []model.PipelineRun `json:"pipeline_runs"`
			Total    int                 `json:"total"`
			PageSize int                 `json:"page_size"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Total != 1 || len(result.Runs) != 1 || result.Runs[0].ID != group.want || result.PageSize != 50 {
			t.Fatalf("正常接口没有按流水线及当前/历史状态筛选: status=%d body=%s", response.Code, response.Body.String())
		}
	}
	invalid := performJSONRequest(t, router, http.MethodGet, "/api/v1/pipeline-runs?status_group=unknown", nil, cookie)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("未拒绝未知状态组: %d %s", invalid.Code, invalid.Body.String())
	}
	anonymous := performJSONRequest(t, router, http.MethodGet, "/api/v1/pipeline-runs?workflow_id=flow-a&status_group=finished", nil, nil)
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("匿名请求读取了运行历史: %d", anonymous.Code)
	}
}
