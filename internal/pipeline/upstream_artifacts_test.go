package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestBuildArgumentUsesFixedRunIdentityWithoutShellExpansion(t *testing.T) {
	run := model.PipelineRun{ID: "run-one", ApplicationID: "app-one", CommitSHA: strings.Repeat("a", 40), Ref: "refs/tags/dev-plat-one"}
	value := "${EDO_COMMIT_SHA}|${EDO_GIT_REF}|${EDO_PIPELINE_RUN_ID}|${EDO_APPLICATION_ID}|${HOME}|$(id)"
	want := run.CommitSHA + "|" + run.Ref + "|run-one|app-one|${HOME}|$(id)"
	if got := expandPipelineBuildArgument(value, run); got != want {
		t.Fatalf("构建参数未固定运行身份: %s", got)
	}
}

func TestUpstreamArtifactManifestBindsRunStageCommitAndProject(t *testing.T) {
	current := model.WorkflowNode{ID: "package", Type: model.WorkflowNodeBuild}
	snapshot := workflowSnapshot{Stages: []model.WorkflowStage{
		{ID: "images", Tasks: []model.WorkflowNode{{ID: "service", Type: model.WorkflowNodeBuild}, {ID: "agent", Type: model.WorkflowNodeBuild}}},
		{ID: "bundle", Tasks: []model.WorkflowNode{current, {ID: "same-stage", Type: model.WorkflowNodeBuild}}},
	}}
	s, db, _, run := newBuildExecutorTestRun(t, current, snapshot, map[string]string{})
	for _, nodeID := range []string{"service", "agent", "same-stage"} {
		build := model.BuildRun{ID: "build-" + nodeID, ApplicationID: run.ApplicationID, PipelineRunID: run.ID, WorkflowNodeID: nodeID, CommitSHA: run.CommitSHA, ProjectID: run.ProjectID, Status: model.BuildRunStatusSucceeded}
		stored := model.Artifact{ID: "artifact-" + nodeID, ApplicationID: run.ApplicationID, PipelineRunID: run.ID, BuildRunID: build.ID, ProjectID: run.ProjectID, Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, StorageKind: model.ArtifactStorageKindRegistry, Digest: "sha256:" + strings.Repeat("a", 64), ImageRef: "registry.example.com/app@sha256:" + strings.Repeat("a", 64), StorageKey: "private-storage-path"}
		node := model.PipelineNodeRun{ID: "node-" + nodeID, PipelineRunID: run.ID, NodeID: nodeID, ProjectID: run.ProjectID, Status: model.PipelineRunSucceeded, ArtifactID: stored.ID}
		for _, record := range []any{&build, &stored, &node} {
			if err := db.Create(record).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	prepared := &buildExecutionContext{run: run, node: current, snapshot: snapshot}
	raw, err := s.upstreamArtifactsJSON(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	var list []upstreamArtifact
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].NodeID != "service" || list[1].NodeID != "agent" || strings.Contains(raw, "private-storage-path") {
		t.Fatalf("上游清单未按前序阶段限定: %s", raw)
	}
	for _, mutate := range []map[string]any{
		{"project_id": "other-project"}, {"pipeline_run_id": "other-run"}, {"application_id": "other-app"}, {"status": model.ArtifactStatusExpired},
	} {
		if err := db.Model(&model.Artifact{}).Where("uuid = ?", "artifact-service").Updates(mutate).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := s.upstreamArtifactsJSON(context.Background(), prepared); err == nil {
			t.Fatalf("接受了范围外或失效的制品: %v", mutate)
		}
		if err := db.Model(&model.Artifact{}).Where("uuid = ?", "artifact-service").Updates(map[string]any{"project_id": run.ProjectID, "pipeline_run_id": run.ID, "application_id": run.ApplicationID, "status": model.ArtifactStatusAvailable}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.BuildRun{}).Where("uuid = ?", "build-service").Update("commit_sha", strings.Repeat("b", 40)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.upstreamArtifactsJSON(context.Background(), prepared); err == nil {
		t.Fatal("接受了不同提交的镜像")
	}
}
