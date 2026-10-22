package pipeline

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"edo/internal/config"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

type realStopScriptRunner struct {
	service *dockerengine.Service
	ready   chan struct{}
	once    sync.Once
}

func (r *realStopScriptRunner) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "EDO_STOP_READY") {
		r.once.Do(func() { close(r.ready) })
	}
	return len(data), nil
}

func (r *realStopScriptRunner) RunScriptContainer(ctx context.Context, input dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
	input.Stdout = io.MultiWriter(input.Stdout, r)
	return r.service.RunScriptContainer(ctx, input)
}

func TestStopPipelineWithRealDockerContainer(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_STOP") != "1" {
		t.Skip("设置 EDO_TEST_DOCKER_STOP=1 后使用本地 Docker 验证隔离容器停止")
	}
	node := model.WorkflowNode{ID: "stop-" + uuid.NewString(), Type: model.WorkflowNodeShell, Name: "停止测试", Config: model.WorkflowNodeConfig{RuntimeImage: "alpine:3.22", Script: "echo EDO_STOP_READY; sleep 120; echo SHOULD_NOT_REACH", TimeoutSeconds: 180}}
	service, db, _, run := newBuildExecutorTestRun(t, node, workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}}, map[string]string{"README": "停止测试"})
	if err := db.Create(&model.Job{ID: run.ExecutionJobID, Kind: "pipeline.build", Payload: []byte(`{}`), Status: model.JobRunning, MaxAttempts: 1}).Error; err != nil {
		t.Fatal(err)
	}
	docker := dockerengine.NewService(db, service.secrets, config.Runtime{})
	runner := &realStopScriptRunner{service: docker, ready: make(chan struct{})}
	service.scriptRunner = runner
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	status, err := docker.InspectScriptRuntimeImage(ctx, node.Config.RuntimeImage)
	if err != nil || !status.Installed {
		t.Fatalf("需要已有 alpine:3.22，不自动下载: %+v %v", status, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- service.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, run.ExecutionJobID)
	}()
	select {
	case <-runner.ready:
	case err := <-done:
		t.Fatalf("真实容器没有运行: %v", err)
	case <-ctx.Done():
		t.Fatal("等待真实容器超时")
	}
	started := time.Now()
	if _, err := service.StopRun(ctx, run.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("容器未在超时前停止")
	}
	t.Logf("真实容器停止耗时: %s", time.Since(started))
	apiClient, err := docker.BuilderClient()
	if err != nil {
		t.Fatal(err)
	}
	defer apiClient.Close()
	containers, err := apiClient.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, container := range containers.Items {
		if container.Labels["io.edo.pipeline.run.id"] == run.ID {
			t.Fatalf("临时执行容器未清理: %s", container.ID)
		}
	}
	var stored model.PipelineRun
	if err := db.First(&stored, "id = ?", run.ID).Error; err != nil || stored.Status != model.PipelineRunCanceled {
		t.Fatalf("运行没有停止: %+v %v", stored, err)
	}
	var logs []model.PipelineRunLog
	db.Where("pipeline_run_id = ?", run.ID).Find(&logs)
	for _, log := range logs {
		if strings.Contains(log.Message, "SHOULD_NOT_REACH") {
			t.Fatal("停止后仍执行脚本后续内容")
		}
	}
}
