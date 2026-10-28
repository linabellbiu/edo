package pipeline

import (
	"context"
	"testing"

	"edo/internal/model"
)

func TestCompletedApprovalNodeExplainsSuccess(t *testing.T) {
	ctx := context.Background()
	gate := model.WorkflowNode{ID: "approval", Name: "发布审核", Type: model.WorkflowNodeApproval}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, ExecutionVersion: 2, ApprovalEnabled: true,
		Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{gate}}}}
	service, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"source.txt": "source"})
	if _, err := service.AdvanceRun(ctx, run.ID, "admin", ""); err != nil {
		t.Fatal(err)
	}
	finished, err := service.ApproveRun(ctx, run.ID, "reviewer")
	if err != nil || finished.Status != model.PipelineRunSucceeded {
		t.Fatalf("审核未完成: run=%+v err=%v", finished, err)
	}
	var stored model.PipelineNodeRun
	if err := db.First(&stored, "pipeline_run_id = ? AND node_id = ?", run.ID, gate.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.PipelineRunSucceeded || stored.Message != "审核已通过" {
		t.Fatalf("完成审核记录仍显示等待: status=%s message=%q", stored.Status, stored.Message)
	}
	if err := db.Model(&model.PipelineNodeRun{}).Where("uuid = ?", stored.ID).Update("message", "等待其他成员审核").Error; err != nil {
		t.Fatal(err)
	}
	items, err := service.ListNodeRuns(ctx, run.ID)
	if err != nil || len(items) != 1 || items[0].Message != "审核已通过" {
		t.Fatalf("既有成功记录展示仍含等待说明: items=%+v err=%v", items, err)
	}
	if err := db.First(&stored, "uuid = ?", stored.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Message != "等待其他成员审核" {
		t.Fatal("只读展示投影不应改写既有持久化记录")
	}
}
