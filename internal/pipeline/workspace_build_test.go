package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"edo/internal/dockerengine"
	"edo/internal/model"
)

func TestWorkspaceBuildUsesLocalToolsAndExportsOutputsWithoutDocker(t *testing.T) {
	workspace, outputs := t.TempDir(), t.TempDir()
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "edo-build-tool"), []byte("#!/bin/sh\nprintf '%s' \"$BUILD_VALUE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EDO_DATABASE_DSN", "must-not-inherit")
	var log bytes.Buffer
	input := dockerengine.ScriptContainerInput{SourceDirectory: workspace, OutputDirectory: outputs, Timeout: 30 * time.Second, Environment: map[string]string{"BUILD_VALUE": "built"}, FileVariables: map[string]string{"BUILD_FILE": "file-value"}, Script: "test -z \"${EDO_DATABASE_DSN-}\"\ntest \"$(pwd)\" -ef \"$EXPECTED_WORKSPACE\"\nmkdir -p output .cache\nedo-build-tool > output/app\ncat \"$BUILD_FILE\" > output/config\nprintf cache > .cache/value\nprintf '{\"ok\":true}' > report.json\necho complete\n", SystemEnvironment: map[string]string{"EXPECTED_WORKSPACE": workspace}, NamedOutputs: []dockerengine.ScriptOutput{{Name: "APP", Paths: []string{"output/app", "output/config"}}}, ReportPaths: []string{"report.json"}, CachePaths: []string{".cache"}, Stdout: &log, Stderr: &log}
	result, err := runWorkspaceBuild(context.Background(), input)
	if err != nil || result.ExitCode != 0 {
		t.Fatal(result, err)
	}
	for name, expected := range map[string]string{"app": "built", "config": "file-value"} {
		body, err := os.ReadFile(filepath.Join(result.ArtifactPath, "APP", "output", name))
		if err != nil || string(body) != expected {
			t.Fatal(name, string(body), err)
		}
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Reports["report.json"]) != "{\"ok\":true}" || result.CachePaths[".cache"] != filepath.Join(canonicalWorkspace, ".cache") || !strings.Contains(log.String(), "complete") {
		t.Fatal("报告、缓存或构建日志丢失", result, log.String())
	}
}

func TestWorkspaceBuildRejectsEscapingOutputAndStopsProcessGroup(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "output")); err != nil {
		t.Fatal(err)
	}
	_, err := runWorkspaceBuild(context.Background(), dockerengine.ScriptContainerInput{SourceDirectory: workspace, OutputDirectory: t.TempDir(), Timeout: 30 * time.Second, Script: "true", ArtifactPath: "output"})
	if err == nil {
		t.Fatal("不能导出指向工作区外的链接")
	}
	if err := syscall.Mkfifo(filepath.Join(workspace, "report.pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = runWorkspaceBuild(context.Background(), dockerengine.ScriptContainerInput{SourceDirectory: workspace, Timeout: 30 * time.Second, Script: "true", ReportPaths: []string{"report.pipe"}})
	if err == nil {
		t.Fatal("报告不能读取会阻塞的特殊文件")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = runWorkspaceBuild(ctx, dockerengine.ScriptContainerInput{SourceDirectory: workspace, Timeout: 30 * time.Second, Script: "(sleep 0.4; echo leaked > child-result) &\nwait"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("取消没有返回超时", err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err = os.Stat(filepath.Join(workspace, "child-result")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("取消后子进程仍在写工作区", err)
	}
}

func TestNewScriptBuildSnapshotUsesWorkspaceAndKeepsHistoricalExecutionMode(t *testing.T) {
	current := buildPlanSnapshot(model.BuildPlan{ID: "plan", Kind: model.BuildPlanScript, RuntimeImage: "node:24-alpine", WorkingDirectory: "subdirectory"})
	if !current.Workspace || current.WorkingDirectory != "." || current.RuntimeImage != "" {
		t.Fatal("新自定义构建仍依赖镜像或可配置目录", current)
	}
	historical := workflowBuildPlanSnapshot{Kind: model.BuildPlanScript, RuntimeImage: "alpine:3.22"}
	if historical.Workspace {
		t.Fatal("历史运行不能静默改变执行模式")
	}
	if buildPlanSnapshot(model.BuildPlan{Kind: model.BuildPlanDockerfile}).Workspace {
		t.Fatal("镜像构建不能切换到工作区脚本执行器")
	}
}

func TestWorkspaceBuildTaskPersistsRealArtifactWithoutContainerRunner(t *testing.T) {
	node := model.WorkflowNode{ID: "native-build", Name: "自定义构建", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "native-plan"}}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "build", Tasks: []model.WorkflowNode{node}}}, BuildPlans: map[string]workflowBuildPlanSnapshot{node.ID: buildPlanSnapshot(model.BuildPlan{ID: "native-plan", Kind: model.BuildPlanScript, Script: "mkdir -p output\nprintf '%s' \"$EDO_PIPELINE_RUN_ID\" > output/result.txt", ArtifactPath: "output", TimeoutSeconds: 30})}}
	service, db, artifacts, run := newBuildExecutorTestRun(t, node, snapshot, map[string]string{"README.md": "native workspace"})
	service.scriptRunner = nil
	if err := service.ExecuteBuildTask(pipelineExecutionTestContext(context.Background(), run), BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, run.ExecutionJobID); err != nil {
		t.Fatal("正常构建任务未使用工作区执行器", err)
	}
	var updated model.PipelineRun
	if err := db.Where("uuid = ?", run.ID).First(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.PipelineRunSucceeded || updated.ArtifactID == "" {
		t.Fatal("任务成功与制品未持久化", updated.Status, updated.ArtifactID)
	}
	_, file, err := artifacts.OpenDownload(context.Background(), updated.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if body := readTarGzipFile(t, file, "result.txt"); body != run.ID {
		t.Fatal("真实工作区产物内容不匹配", body)
	}
}
