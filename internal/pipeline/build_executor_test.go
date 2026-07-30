package pipeline

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"zrt/internal/artifact"
	"zrt/internal/dockerengine"
	"zrt/internal/model"
	"zrt/internal/repository"
)

type buildExecutorCheckoutStub struct {
	files map[string]string
}

type buildExecutorScriptRunner struct{}

func (buildExecutorScriptRunner) RunScriptContainer(
	ctx context.Context,
	input dockerengine.ScriptContainerInput,
) (dockerengine.ScriptContainerResult, error) {
	if err := ctx.Err(); err != nil {
		return dockerengine.ScriptContainerResult{}, err
	}
	if input.Image != model.DefaultRuntimeImage {
		return dockerengine.ScriptContainerResult{}, errors.New("测试脚本容器没有收到运行镜像")
	}
	workingDirectory := filepath.Join(input.SourceDirectory, filepath.FromSlash(input.WorkingDirectory))
	if info, err := os.Stat(workingDirectory); err != nil || !info.IsDir() {
		return dockerengine.ScriptContainerResult{}, errors.New("测试脚本容器缺少工作目录")
	}
	result := dockerengine.ScriptContainerResult{ImageID: "sha256:" + strings.Repeat("b", 64)}
	if input.ArtifactPath != "" {
		output := filepath.Join(input.OutputDirectory, filepath.Base(filepath.FromSlash(input.ArtifactPath)))
		if err := os.MkdirAll(output, 0o700); err != nil {
			return result, err
		}
		content := input.Environment["BUILD_VALUE"] + "|" + input.SystemEnvironment["ZRT_PIPELINE_RUN_ID"] + "|" + filepath.Base(workingDirectory)
		if err := os.WriteFile(filepath.Join(output, "result.txt"), []byte(content), 0o600); err != nil {
			return result, err
		}
		result.ArtifactPath = output
		return result, nil
	}
	_, err := io.WriteString(input.Stdout, filepath.Base(workingDirectory)+"|"+input.Environment["SHELL_VALUE"]+"|"+
		input.SystemEnvironment["ZRT_PIPELINE_RUN_ID"]+"|"+input.SystemEnvironment["ZRT_COMMIT_SHA"])
	return result, err
}

func (s buildExecutorCheckoutStub) ListRefs(context.Context, model.GitRepository, string) (repository.RefResult, error) {
	return repository.RefResult{}, nil
}

func (s buildExecutorCheckoutStub) Checkout(
	ctx context.Context,
	_ model.GitRepository,
	_, ref, commitSHA, destination string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ref == "" || commitSHA == "" {
		return errors.New("测试检出缺少固定版本")
	}
	for name, content := range s.files {
		path := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func TestExecuteScriptBuildProducesFileBundleAndAdvances(t *testing.T) {
	buildNode := model.WorkflowNode{
		ID: "build-script", Type: model.WorkflowNodeBuild, Name: "脚本构建",
		Config: model.WorkflowNodeConfig{BuildPlanID: "plan-script"},
	}
	nextNode := model.WorkflowNode{
		ID: "shell-next", Type: model.WorkflowNodeShell, Name: "后续检查",
		Config: model.WorkflowNodeConfig{
			Script: "printf next", RuntimeImage: model.DefaultRuntimeImage, WorkingDirectory: "src", TimeoutSeconds: 30,
		},
	}
	snapshot := workflowSnapshot{
		SchemaVersion: model.WorkflowSchemaVersion,
		Source:        model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"},
		Stages:        []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{buildNode, nextNode}}},
		BuildPlans: map[string]workflowBuildPlanSnapshot{
			buildNode.ID: {
				ID: "plan-script", Kind: model.BuildPlanScript, ConfigVersion: 1,
				Script:           "mkdir -p dist\nprintf '%s|%s|%s' \"$BUILD_VALUE\" \"$ZRT_PIPELINE_RUN_ID\" \"${PWD##*/}\" > dist/result.txt\n",
				WorkingDirectory: "src", ArtifactPath: "src/dist", RuntimeImage: model.DefaultRuntimeImage,
				EnvironmentVariables: map[string]string{"BUILD_VALUE": "from-plan"}, TimeoutSeconds: 30,
			},
		},
	}
	service, db, artifactService, run := newBuildExecutorTestRun(t, buildNode, snapshot, map[string]string{
		"src/input.txt": "source",
	})

	if err := service.ExecuteBuildTask(context.Background(), BuildTaskPayload{
		PipelineRunID: run.ID, WorkflowNodeID: buildNode.ID,
	}, run.ExecutionJobID); err != nil {
		t.Fatalf("执行脚本构建失败: %v", err)
	}

	var updated model.PipelineRun
	if err := db.First(&updated, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.ArtifactID == "" {
		t.Fatal("脚本构建完成后没有绑定制品")
	}
	if updated.CurrentNodeID != nextNode.ID || updated.Status != model.PipelineRunRunning || updated.Stage != "queued" ||
		updated.ExecutionJobID == "" || updated.ExecutionJobID == run.ExecutionJobID {
		t.Fatalf("脚本构建完成后没有自动推进到下一任务: %+v", updated)
	}
	var nextJob model.Job
	if err := db.First(&nextJob, "id = ?", updated.ExecutionJobID).Error; err != nil {
		t.Fatal(err)
	}
	if nextJob.MaxAttempts != 1 || nextJob.IsIdempotent {
		t.Fatalf("可能产生副作用的 Shell 任务只能执行一次且不能声明幂等: %+v", nextJob)
	}

	stored, file, err := artifactService.OpenDownload(context.Background(), updated.ArtifactID)
	if err != nil {
		t.Fatalf("打开脚本构建制品失败: %v", err)
	}
	defer file.Close()
	if stored.Kind != model.ArtifactKindFileBundle || stored.StorageKind != model.ArtifactStorageKindLocalFile ||
		stored.Status != model.ArtifactStatusAvailable || !strings.HasPrefix(stored.Digest, "sha256:") {
		t.Fatalf("脚本构建没有登记可校验的文件制品: %+v", stored)
	}
	if content := readTarGzipFile(t, file, "result.txt"); content != "from-plan|"+run.ID+"|src" {
		t.Fatalf("脚本构建工作目录或环境变量未进入产物: %q", content)
	}

	var buildRun model.BuildRun
	if err := db.First(&buildRun, "id = ?", stored.BuildRunID).Error; err != nil {
		t.Fatal(err)
	}
	if buildRun.ProducerKind != model.BuildRunProducerScript || buildRun.Status != model.BuildRunStatusSucceeded ||
		buildRun.WorkflowNodeID != buildNode.ID || buildRun.BuildPlanID != "plan-script" {
		t.Fatalf("脚本构建审计记录不完整: %+v", buildRun)
	}
}

func TestExecuteShellTaskUsesWorkingDirectoryAndEnvironment(t *testing.T) {
	node := model.WorkflowNode{
		ID: "shell-task", Type: model.WorkflowNodeShell, Name: "Shell 检查",
		Config: model.WorkflowNodeConfig{
			Script:       "printf '%s|%s|%s|%s' \"${PWD##*/}\" \"$SHELL_VALUE\" \"$ZRT_PIPELINE_RUN_ID\" \"$ZRT_COMMIT_SHA\"\n",
			RuntimeImage: model.DefaultRuntimeImage, WorkingDirectory: "work", TimeoutSeconds: 30,
			EnvironmentVariables: map[string]string{"SHELL_VALUE": "configured"},
		},
	}
	service, db, _, run := newBuildExecutorTestRun(t, node, workflowSnapshot{
		SchemaVersion: model.WorkflowSchemaVersion,
		Source:        model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"},
		Stages:        []model.WorkflowStage{{ID: "verify", Name: "检查", Tasks: []model.WorkflowNode{node}}},
	}, map[string]string{"work/source.txt": "source"})

	if err := service.ExecuteBuildTask(context.Background(), BuildTaskPayload{
		PipelineRunID: run.ID, WorkflowNodeID: node.ID,
	}, run.ExecutionJobID); err != nil {
		t.Fatalf("执行 Shell 任务失败: %v", err)
	}
	var updated model.PipelineRun
	if err := db.First(&updated, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.PipelineRunSucceeded || updated.Stage != "completed" {
		t.Fatalf("末尾 Shell 任务没有完成流水线: %+v", updated)
	}
	var logs []model.PipelineRunLog
	if err := db.Where("pipeline_run_id = ? AND stage = ? AND level = ?", run.ID, "shell", "output").
		Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for i := range logs {
		output.WriteString(logs[i].Message)
	}
	expected := "work|configured|" + run.ID + "|" + run.CommitSHA
	if !strings.Contains(output.String(), expected) {
		t.Fatalf("Shell 任务没有使用快照工作目录或环境变量: output=%q expected=%q", output.String(), expected)
	}
}

func TestStaleBuildDeliveryAfterAdvanceIsNoOp(t *testing.T) {
	oldNode := model.WorkflowNode{
		ID: "build-old", Type: model.WorkflowNodeBuild, Name: "旧构建",
		Config: model.WorkflowNodeConfig{BuildPlanID: "plan-old"},
	}
	nextNode := model.WorkflowNode{ID: "approval-next", Type: model.WorkflowNodeApproval, Name: "审核"}
	service, db, _, run := newBuildExecutorTestRun(t, oldNode, workflowSnapshot{
		SchemaVersion: model.WorkflowSchemaVersion,
		Source:        model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"},
		Stages:        []model.WorkflowStage{{ID: "delivery", Name: "交付", Tasks: []model.WorkflowNode{oldNode, nextNode}}},
		BuildPlans: map[string]workflowBuildPlanSnapshot{
			oldNode.ID: {
				ID: "plan-old", Kind: model.BuildPlanScript, ConfigVersion: 1, Script: "exit 99",
				WorkingDirectory: ".", ArtifactPath: "dist", RuntimeImage: model.DefaultRuntimeImage, TimeoutSeconds: 30,
			},
		},
		ApprovalEnabled: true,
	}, nil)
	oldJobID := run.ExecutionJobID
	if err := db.Model(&model.PipelineRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"current_node_id": nextNode.ID, "execution_job_id": "job-next", "status": model.PipelineRunAwaitingApproval,
		"stage": string(model.WorkflowNodeApproval), "message": "等待其他成员审核",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", run.ID).
		Update("status", model.PipelineRunRepositoryReady).Error; err != nil {
		t.Fatal(err)
	}

	if err := service.ExecuteBuildTask(context.Background(), BuildTaskPayload{
		PipelineRunID: run.ID, WorkflowNodeID: oldNode.ID,
	}, oldJobID); err != nil {
		t.Fatalf("旧构建消息重投应安全忽略: %v", err)
	}
	var updated model.PipelineRun
	if err := db.First(&updated, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.CurrentNodeID != nextNode.ID || updated.ExecutionJobID != "job-next" ||
		updated.Status != model.PipelineRunAwaitingApproval || updated.Stage != string(model.WorkflowNodeApproval) {
		t.Fatalf("旧构建消息覆盖了新节点状态: %+v", updated)
	}
	var component model.PipelineRunRepository
	if err := db.First(&component, "pipeline_run_id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if component.Status != model.PipelineRunRepositoryReady {
		t.Fatalf("旧构建消息错误地把仓库状态改为失败: %+v", component)
	}
}

func TestScriptBuildJobRunsAtMostOnce(t *testing.T) {
	service, db, _, repositoryID := newPipelineTestService(t)
	application, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: "单次脚本构建", RepositoryID: repositoryID, PollIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	node := model.WorkflowNode{
		ID: "script-build", Type: model.WorkflowNodeBuild, Name: "脚本构建",
		Config: model.WorkflowNodeConfig{BuildPlanID: "script-plan"},
	}
	snapshotJSON, err := json.Marshal(workflowSnapshot{
		SchemaVersion: model.WorkflowSchemaVersion,
		Source:        model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"},
		Stages:        []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{node}}},
		BuildPlans: map[string]workflowBuildPlanSnapshot{
			node.ID: {ID: "script-plan", Kind: model.BuildPlanScript},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := model.PipelineRun{
		ID: "script-build-single-attempt", ApplicationID: application.ID, Trigger: "manual",
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), Status: model.PipelineRunReady,
		Stage: string(model.WorkflowNodeTrigger), CurrentNodeID: "source", WorkflowSnapshot: string(snapshotJSON),
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	queued, err := service.enqueueBuildExecution(context.Background(), &run, node)
	if err != nil {
		t.Fatalf("投递脚本构建失败: %v", err)
	}
	var job model.Job
	if err := db.First(&job, "id = ?", queued.ExecutionJobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.MaxAttempts != 1 || job.IsIdempotent {
		t.Fatalf("脚本构建不得在进程中断后重复执行: %+v", job)
	}
}

func TestExecuteBuildTaskRecoversRegisteredArtifactWithoutRebuilding(t *testing.T) {
	node := model.WorkflowNode{
		ID: "build-recovery", Type: model.WorkflowNodeBuild, Name: "恢复构建",
		Config: model.WorkflowNodeConfig{BuildPlanID: "plan-recovery"},
	}
	plan := workflowBuildPlanSnapshot{
		ID: "plan-recovery", Kind: model.BuildPlanScript, ConfigVersion: 1,
		Script: "exit 42\n", WorkingDirectory: ".", ArtifactPath: "dist", RuntimeImage: model.DefaultRuntimeImage,
		EnvironmentVariables: map[string]string{}, TimeoutSeconds: 30,
	}
	snapshot := workflowSnapshot{
		SchemaVersion: model.WorkflowSchemaVersion,
		Source:        model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"},
		Stages:        []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{node}}},
		BuildPlans:    map[string]workflowBuildPlanSnapshot{node.ID: plan},
	}
	service, db, artifactService, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "result.bin")
	if err := os.WriteFile(output, []byte("immutable-output"), 0o600); err != nil {
		t.Fatal(err)
	}
	var component model.PipelineRunRepository
	if err := db.First(&component, "pipeline_run_id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := artifactService.CreateFileFromPath(context.Background(), artifact.BuildOutputInput{
		BuildMetadata: artifact.BuildMetadata{
			ApplicationID: "app-build-executor", PipelineRunID: run.ID,
			RepositoryID: component.RepositoryID, WorkflowNodeID: node.ID, BuildPlanID: plan.ID,
			ProducerKind: model.BuildRunProducerScript, Ref: run.Ref, CommitSHA: run.CommitSHA,
			PlanSnapshot: string(planJSON), PlanDigest: sha256Digest(planJSON), CreatedBy: "admin",
		},
		SourcePath: output, Name: "result.bin",
	})
	if err != nil {
		t.Fatalf("预先登记构建制品失败: %v", err)
	}
	if err := service.ExecuteBuildTask(context.Background(), BuildTaskPayload{
		PipelineRunID: run.ID, WorkflowNodeID: node.ID,
	}, run.ExecutionJobID); err != nil {
		t.Fatalf("已有制品时仍然重复执行失败脚本: %v", err)
	}
	var updated model.PipelineRun
	if err := db.First(&updated, "id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.PipelineRunSucceeded || updated.ArtifactID != stored.ID {
		t.Fatalf("没有恢复并绑定已登记制品: run=%+v artifact=%+v", updated, stored)
	}
}

func TestSecureWorkspacePathRejectsTraversalAndEscapingSymlink(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	expectedInside, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := secureWorkspacePath(workspace, "inside")
	if err != nil || resolved != expectedInside {
		t.Fatalf("合法工作区路径解析失败: path=%q err=%v", resolved, err)
	}
	for _, value := range []string{"../outside", filepath.Join(workspace, "inside")} {
		if _, err := secureWorkspacePath(workspace, value); !errors.Is(err, ErrPipelineExecutionConfig) {
			t.Fatalf("路径越界未被拒绝: value=%q err=%v", value, err)
		}
	}

	outside := t.TempDir()
	link := filepath.Join(workspace, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前文件系统不支持符号链接: %v", err)
	}
	if _, err := secureWorkspacePath(workspace, "outside-link"); !errors.Is(err, ErrPipelineExecutionConfig) {
		t.Fatalf("指向工作区外的符号链接未被拒绝: %v", err)
	}
}

func TestArtifactNameMatchesActualFileFormat(t *testing.T) {
	commit := strings.Repeat("a", 40)
	tests := []struct {
		path      string
		directory bool
		want      string
	}{
		{path: "dist", directory: true, want: "api-aaaaaaaaaaaa.tar.gz"},
		{path: "build/service.jar", want: "api-aaaaaaaaaaaa.jar"},
		{path: "bin/server", want: "api-aaaaaaaaaaaa"},
	}
	for _, test := range tests {
		if got := artifactName("api", commit, test.path, test.directory); got != test.want {
			t.Fatalf("制品名称与真实格式不一致: path=%q got=%q want=%q", test.path, got, test.want)
		}
	}
}

func newBuildExecutorTestRun(
	t *testing.T,
	current model.WorkflowNode,
	snapshot workflowSnapshot,
	checkoutFiles map[string]string,
) (*Service, *gorm.DB, *artifact.Service, model.PipelineRun) {
	t.Helper()
	service, db, secrets, repositoryID := newPipelineTestService(t)
	service.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	service.repositories = repository.NewService(db, secrets, nil, buildExecutorCheckoutStub{files: checkoutFiles}, 4)
	artifactService, err := artifact.NewService(db, t.TempDir(), 32*1024*1024, service.logger)
	if err != nil {
		t.Fatalf("初始化构建制品存储失败: %v", err)
	}
	service.ConfigureArtifacts(artifactService)
	service.scriptRunner = buildExecutorScriptRunner{}

	now := time.Now().UTC()
	application := model.Application{
		ID: "app-build-executor", Name: "构建执行链测试", RepositoryID: repositoryID,
		PollIntervalSeconds: 30, SyncStatus: model.ApplicationSyncIdle, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&application).Error; err != nil {
		t.Fatalf("创建构建执行测试应用失败: %v", err)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{
		ID: "run-" + current.ID, ApplicationID: application.ID, Trigger: "manual",
		Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40),
		Status: model.PipelineRunRunning, Stage: "queued", CurrentNodeID: current.ID,
		WorkflowSnapshot: string(snapshotJSON), ExecutionJobID: "job-" + current.ID,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	component := model.PipelineRunRepository{
		ID: "component-" + current.ID, PipelineRunID: run.ID, RepositoryID: repositoryID,
		Ref: run.Ref, CommitSHA: run.CommitSHA, Status: model.PipelineRunRepositoryPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		return tx.Create(&component).Error
	}); err != nil {
		t.Fatalf("创建构建执行测试运行失败: %v", err)
	}
	return service, db, artifactService, run
}

func readTarGzipFile(t *testing.T, source io.Reader, name string) string {
	t.Helper()
	gzipReader, err := gzip.NewReader(source)
	if err != nil {
		t.Fatalf("打开 tar.gz 制品失败: %v", err)
	}
	defer gzipReader.Close()
	archive := tar.NewReader(gzipReader)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("读取 tar.gz 制品失败: %v", err)
		}
		if header.Name != name {
			continue
		}
		content, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	t.Fatalf("tar.gz 制品缺少 %s", name)
	return ""
}

func TestPipelineRunCheckoutRefUsesMergedTargetBranch(t *testing.T) {
	merged := model.PipelineRun{
		Ref: "refs/pull/12/head", TriggerAction: "merged", TargetBranch: "release/2026.08",
	}
	if got := pipelineRunCheckoutRef(merged); got != "refs/heads/release/2026.08" {
		t.Fatalf("合并 PR 应从目标分支检出 merge commit: %q", got)
	}
	opened := model.PipelineRun{
		Ref: "refs/pull/12/head", TriggerAction: "opened", TargetBranch: "release/2026.08",
	}
	if got := pipelineRunCheckoutRef(opened); got != opened.Ref {
		t.Fatalf("未合并 PR 应继续使用公开 PR Ref: %q", got)
	}
}
