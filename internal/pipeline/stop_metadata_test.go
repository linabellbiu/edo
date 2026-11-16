package pipeline

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"edo/internal/model"
	"edo/internal/repository"
)

type stopMetadataClient struct {
	pipelineRefLister
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *stopMetadataClient) CachedCommitAuthorAt(ctx context.Context, _, _ string) (string, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return "提交作者", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestStopResponseMetadataDoesNotHoldPipelineSchedulingLock(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(map[bool]string{false: "串行", true: "并行"}[parallel], func(t *testing.T) {
			node := parallelShell("slow-metadata")
			snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}}
			service, db, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
			if parallel {
				if err := db.Create(&model.PipelineNodeRun{ID: "stopping-node", PipelineRunID: run.ID, NodeID: node.ID, StageID: "stage", Name: node.Name, Type: node.Type, Status: model.PipelineRunRunning, Stage: "queued"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			client := &stopMetadataClient{started: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(client.release) }) }
			defer release()
			service.repositories = repository.NewService(db, service.secrets, nil, client, 4)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := service.StopRun(ctx, run.ID, "admin")
				done <- err
			}()
			select {
			case <-client.started:
			case err := <-done:
				t.Fatalf("停止响应未读取展示字段: %v", err)
			case <-ctx.Done():
				t.Fatal("停止请求没有完成状态更新")
			}
			otherSnapshot, err := json.Marshal(workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "other-source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "other-stage", Tasks: []model.WorkflowNode{parallelShell("independent")}}}})
			if err != nil {
				t.Fatal(err)
			}
			other := model.PipelineRun{ID: "independent-run", ApplicationID: run.ApplicationID, Status: model.PipelineRunRunning, Stage: "trigger", CurrentNodeID: "other-source", WorkflowSnapshot: string(otherSnapshot), CreatedBy: "admin"}
			if err := db.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			advanced := make(chan error, 1)
			go func() {
				_, err := service.AdvanceRun(ctx, other.ID, "admin", "")
				advanced <- err
			}()
			select {
			case err := <-advanced:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("一个停止响应的展示字段阻塞了其他流水线调度")
			}
			var jobCount int64
			if err := db.Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ? AND job_id <> ?", other.ID, "").Count(&jobCount).Error; err != nil || jobCount != 1 {
				t.Fatalf("独立任务未被投递: count=%d err=%v", jobCount, err)
			}
			release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("停止响应未能退出")
			}
			var stored model.PipelineRun
			if err := db.First(&stored, "uuid = ?", run.ID).Error; err != nil || stored.Status != model.PipelineRunCanceled {
				t.Fatalf("停止结果没有持久化: status=%s err=%v", stored.Status, err)
			}
		})
	}
}
