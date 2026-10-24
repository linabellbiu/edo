package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/cicd"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

type parallelScriptRunner func(context.Context, dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error)

func (f parallelScriptRunner) RunScriptContainer(ctx context.Context, in dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
	return f(ctx, in)
}

func parallelShell(id string) model.WorkflowNode {
	return model.WorkflowNode{ID: id, Type: model.WorkflowNodeShell, Name: id, Config: model.WorkflowNodeConfig{Script: id, RuntimeImage: model.DefaultRuntimeImage, WorkingDirectory: ".", TimeoutSeconds: 30}}
}

func TestParallelTasksExecuteTogetherAndWaitForStage(t *testing.T) {
	ctx := context.Background()
	first, second, third := parallelShell("first"), parallelShell("second"), parallelShell("third")
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "checks", Tasks: []model.WorkflowNode{first, second}}, {ID: "next", Tasks: []model.WorkflowNode{third}}}}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "immutable-source"})
	started := make(chan string, 2)
	release := make(chan struct{})
	s.scriptRunner = parallelScriptRunner(func(ctx context.Context, in dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
		if in.Script != "third" {
			started <- in.SourceDirectory
			select {
			case <-release:
			case <-ctx.Done():
				return dockerengine.ScriptContainerResult{}, ctx.Err()
			}
		}
		return dockerengine.ScriptContainerResult{ImageID: "sha256:" + strings.Repeat("a", 64)}, nil
	})
	if _, err := s.AdvanceRun(ctx, run.ID, "admin", ""); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("同阶段没有同时创建两个任务: %+v %v", nodes, err)
	}
	errorsCh := make(chan error, 2)
	for _, node := range nodes {
		if err := db.Model(&model.Job{}).Where("uuid = ?", node.JobID).Update("status", model.JobRunning).Error; err != nil {
			t.Fatal(err)
		}
		go func(node model.PipelineNodeRun) {
			errorsCh <- s.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.NodeID}, node.JobID)
		}(node)
	}
	dirs := []string{}
	for range 2 {
		select {
		case directory := <-started:
			dirs = append(dirs, directory)
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("两个任务未能同时进入执行器，可能仍受共享工作区串行锁限制")
		}
	}
	if dirs[0] == dirs[1] {
		close(release)
		t.Fatal("并行任务共享了可变工作区")
	}
	var count int64
	if err := db.Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ? AND node_id = ?", run.ID, third.ID).Count(&count).Error; err != nil || count != 0 {
		close(release)
		t.Fatal("当前阶段未结束就开始下一阶段")
	}
	close(release)
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	nodes, err = s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("阶段结束后没有推进: %v %+v", err, nodes)
	}
	for _, node := range nodes {
		if node.NodeID == third.ID {
			if err = s.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: third.ID}, node.JobID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var final model.PipelineRun
	if err = db.First(&final, "uuid = ?", run.ID).Error; err != nil || final.Status != model.PipelineRunSucceeded {
		t.Fatalf("流水线没有按实际结果完成: %v %+v", err, final)
	}
}

func TestParallelFailurePreventsFollowingStageAndLateSuccess(t *testing.T) {
	ctx := context.Background()
	first, second, third := parallelShell("first"), parallelShell("second"), parallelShell("third")
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "checks", Tasks: []model.WorkflowNode{first, second}}, {ID: "next", Tasks: []model.WorkflowNode{third}}}}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	if _, err := s.AdvanceRun(ctx, run.ID, "admin", ""); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	failure := taskFailureState(run.ID, nodes[0].JobID, nodes[0].NodeID, model.PipelineRunRunning)
	if err = s.finishNodeFailure(ctx, failure, false, "检查没有通过", errors.New("internal failure")); err == nil {
		t.Fatal("失败没有返回错误")
	}
	other := run
	other.ExecutionJobID = nodes[1].JobID
	other.CurrentNodeID = nodes[1].NodeID
	other.NodeExecution = true
	if err = s.completeNodeExecution(ctx, other, second, nil, nil); !errors.Is(err, ErrInvalidWorkflowTransition) {
		t.Fatalf("迟到成功覆盖了失败: %v", err)
	}
	var final model.PipelineRun
	if err = db.First(&final, "uuid = ?", run.ID).Error; err != nil || final.Status != model.PipelineRunFailed {
		t.Fatal("失败状态丢失")
	}
	nodes, err = s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 2 {
		t.Fatal("失败后仍推进了后续阶段")
	}
}

func TestParallelApprovalActsAsExecutionBarrier(t *testing.T) {
	ctx := context.Background()
	gate := model.WorkflowNode{ID: "approval", Name: "审核", Type: model.WorkflowNodeApproval}
	after := parallelShell("after")
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, ApprovalEnabled: true, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "deploy", Tasks: []model.WorkflowNode{gate, after}}}}
	s, _, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	started, err := s.AdvanceRun(ctx, run.ID, "admin", "")
	if err != nil || started.Status != model.PipelineRunAwaitingApproval {
		t.Fatalf("审核屏障没有暂停: %v %+v", err, started)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 1 || nodes[0].JobID != "" {
		t.Fatal("审核前提前投递执行任务")
	}
	if _, err = s.ApproveRun(ctx, run.ID, "admin"); !errors.Is(err, ErrWorkflowSelfApproval) {
		t.Fatal("申请人可以自审")
	}
	if _, err = s.ApproveRun(ctx, run.ID, "reviewer"); err != nil {
		t.Fatal(err)
	}
	nodes, err = s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 2 || nodes[1].JobID == "" {
		t.Fatalf("审核通过后没有自动推进: %v %+v", err, nodes)
	}
}

func TestEnvironmentRequiresTwoDifferentReviewers(t *testing.T) {
	ctx := context.Background()
	gate := model.WorkflowNode{ID: "environment-gate", Name: "环境审核", Type: model.WorkflowNodeApproval}
	after := parallelShell("after")
	policy := model.EnvironmentReleasePolicy{RequireApproval: true, ReviewerIDs: []string{"reviewer-a", "reviewer-b"}, MinimumApprovals: 2}
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, ApprovalEnabled: true, AuthorityID: "owner", Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{gate, after}}}, ReleasePolicies: map[string]model.EnvironmentReleasePolicy{gate.ID: policy}}
	s, _, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	if _, err := s.AdvanceRun(ctx, run.ID, "admin", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveRun(ctx, run.ID, "outsider"); err == nil {
		t.Fatal("指定名单之外的用户可以审核")
	}
	first, err := s.ApproveRun(ctx, run.ID, "reviewer-a")
	if err != nil || first.Status != model.PipelineRunAwaitingApproval {
		t.Fatalf("人数不足时提前推进: %v", err)
	}
	if _, err = s.ApproveRun(ctx, run.ID, "reviewer-a"); err == nil {
		t.Fatal("同一审核人重复计数")
	}
	final, err := s.ApproveRun(ctx, run.ID, "reviewer-b")
	if err != nil || final.CurrentNodeID != after.ID || final.ExecutionJobID == "" {
		t.Fatalf("审核人数满足后没有推进: %v %+v", err, final)
	}
}
func TestParallelBuildArtifactSourceMustBeExplicit(t *testing.T) {
	stages := []model.WorkflowStage{{ID: "build", Tasks: []model.WorkflowNode{{ID: "one", Type: model.WorkflowNodeBuild}, {ID: "two", Type: model.WorkflowNodeBuild}}}, {ID: "deploy", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy}}}}
	if _, err := artifactSources(stages); err == nil {
		t.Fatal("并行构建没有选择制品来源仍可部署")
	}
	stages[1].Tasks[0].Config.ArtifactNodeID = "one"
	sources, err := artifactSources(stages)
	if err != nil || sources["deploy"] != "one" {
		t.Fatal("明确来源没有正确解析")
	}
	run := model.PipelineRun{ID: "run-one", CurrentNodeID: "one", NodeExecution: true, CommitSHA: strings.Repeat("a", 40)}
	one, err := executionImageTag(run)
	if err != nil {
		t.Fatal(err)
	}
	run.CurrentNodeID = "two"
	two, _ := executionImageTag(run)
	if one == two || strings.ContainsAny(one, ":/") {
		t.Fatal("并行构建镜像标签冲突或无效")
	}
}
func TestDatabaseMigrationSeparatesParallelBatches(t *testing.T) {
	before, migration, after := parallelShell("before"), parallelShell("migration"), parallelShell("after")
	migration.Config.Migration = &model.WorkflowMigrationConfig{DatabaseID: "database", Directory: "migrations"}
	batches := executionBatches([]model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{before, migration, after}}})
	if len(batches) != 3 || len(batches[1]) != 1 || batches[1][0].ID != migration.ID {
		t.Fatal("数据库变更与下游任务同时执行")
	}
}

func TestSelectedArtifactPreservesMigrationAndApproval(t *testing.T) {
	ctx := context.Background()
	build := model.WorkflowNode{ID: "build", Type: model.WorkflowNodeBuild, Name: "构建"}
	gate := model.WorkflowNode{ID: "database-review", Type: model.WorkflowNodeApproval, Name: "数据库审核"}
	migration, check := parallelShell("migration"), parallelShell("check")
	migration.Config.Migration = &model.WorkflowMigrationConfig{DatabaseID: "database", Directory: "migrations"}
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, ApprovalEnabled: true, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{build, gate, migration, check}}}}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	producer := model.BuildRun{ID: "selected-build", ApplicationID: run.ApplicationID, ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, WorkflowNodeID: build.ID}
	selected := model.Artifact{ID: "selected-artifact", ApplicationID: run.ApplicationID, ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, BuildRunID: producer.ID}
	for _, item := range []any{&producer, &selected} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Updates(map[string]any{"artifact_id": "selected-artifact", "current_node_id": check.ID, "status": model.PipelineRunReady, "stage": "task_succeeded"}).Error; err != nil {
		t.Fatal(err)
	}
	started, err := s.AdvanceRun(ctx, run.ID, "admin", "")
	if err != nil || started.Status != model.PipelineRunAwaitingApproval || started.CurrentNodeID != gate.ID {
		t.Fatalf("复用制品绕过数据库审核: %v %+v", err, started)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.NodeID == migration.ID {
			t.Fatal("审核前跳过或投递了数据库变更")
		}
		if node.NodeID == build.ID {
			deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{ArtifactNodeID: build.ID}}
			artifactID, err := parallelUpstreamArtifact(&snapshot, deploy, map[string]model.PipelineNodeRun{build.ID: node}, selected.ID)
			if err != nil || artifactID != selected.ID {
				t.Fatalf("已有制品未保留准确构建来源: %s %v", artifactID, err)
			}
		}
	}
}

func TestReleasePolicyAddedAfterSnapshotBlocksExecution(t *testing.T) {
	node := parallelShell("migration")
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{node}}}}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	policy := model.EnvironmentReleasePolicy{ID: "late-policy", ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, EnvironmentID: "environment", Version: 1, IsActive: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.validateReleasePolicy(context.Background(), run, node, snapshot, policy.EnvironmentID); !errors.Is(err, cicd.ErrReleaseProtected) {
		t.Fatalf("运行创建后新增的保护被绕过: %v", err)
	}
}
