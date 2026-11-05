package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
	"github.com/google/uuid"
)

func TestNodeRunLogsUseFrozenNodeIDAndCursor(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	app := model.Application{ID: uuid.NewString(), Name: "任务日志测试", RepositoryID: repositoryID}
	if err := db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "build", Tasks: []model.WorkflowNode{{ID: "one", Name: "同名任务", Type: model.WorkflowNodeBuild}, {ID: "two", Name: "同名任务", Type: model.WorkflowNodeBuild}, {ID: "empty", Type: model.WorkflowNodeManual}}}}})
	if err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{ID: uuid.NewString(), ApplicationID: app.ID, Status: model.PipelineRunRunning, WorkflowSnapshot: string(snapshot)}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	task := model.PipelineNodeRun{ID: uuid.NewString(), PipelineRunID: run.ID, NodeID: "one", Status: model.PipelineRunSucceeded}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	service.appendRunLog(context.Background(), run.ID, "build", "info", "全局日志")
	for _, node := range []string{"one", "two", "one"} {
		service.appendRunLog(withPipelineNodeLog(context.Background(), node, "同名任务"), run.ID, "build", "output", node)
	}
	// 同一 node_id 在另一次运行中的日志也不能混入。
	other := model.PipelineRun{ID: uuid.NewString(), ApplicationID: app.ID, Status: model.PipelineRunRunning, WorkflowSnapshot: string(snapshot)}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	service.appendRunLog(withPipelineNodeLog(context.Background(), "one", "同名任务"), other.ID, "build", "output", "other")
	first, status, err := service.ListNodeRunLogs(context.Background(), run.ID, "one", 0, 1)
	if err != nil || status != model.PipelineRunSucceeded || len(first) != 1 || first[0].NodeID != "one" {
		t.Fatalf("任务日志首批错误: %+v %s %v", first, status, err)
	}
	rest, _, err := service.ListNodeRunLogs(context.Background(), run.ID, "one", first[0].ID, 10)
	if err != nil || len(rest) != 1 || rest[0].NodeID != "one" || rest[0].PipelineRunID != run.ID {
		t.Fatalf("任务游标或运行隔离错误: %+v %v", rest, err)
	}
	empty, _, err := service.ListNodeRunLogs(context.Background(), run.ID, "empty", 0, 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("未执行任务不应返回全局日志: %+v %v", empty, err)
	}
	if _, _, err = service.ListNodeRunLogs(context.Background(), run.ID, "unknown", 0, 10); !errors.Is(err, ErrPipelineLogNodeNotFound) {
		t.Fatalf("未知任务未拒绝: %v", err)
	}
	scoped := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: uuid.NewString()})
	if _, _, err = service.ListNodeRunLogs(scoped, run.ID, "one", 0, 10); !errors.Is(err, ErrPipelineRunNotFound) {
		t.Fatalf("跨项目读取未拒绝: %v", err)
	}
	all, _, err := service.ListRunLogs(context.Background(), run.ID, 0, 10)
	if err != nil || len(all) != 4 {
		t.Fatalf("整个运行日志入口受影响: %+v %v", all, err)
	}
}
