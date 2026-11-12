package deployment

import (
	"encoding/json"
	"strings"
	"testing"

	"edo/internal/model"
	"github.com/google/uuid"
)

func TestRecordPipelineEnvironmentFiltersStageProjectAndNodeStatus(t *testing.T) {
	f := newContainerControlFixture(t)
	var run model.PipelineRun
	if err := f.db.First(&run, "uuid = ?", f.record.PipelineRunID).Error; err != nil {
		t.Fatal(err)
	}
	run.Ref, run.CommitSHA = "refs/tags/git-current", strings.Repeat("c", 40)
	prior := []model.WorkflowNode{}
	plans := map[string]any{}
	foreignID := ""
	for _, item := range []struct {
		id, tag, project string
		status           model.PipelineRunStatus
		kind             model.BuildPlanKind
	}{
		{"prior", "actual-version", run.ProjectID, model.PipelineRunSkipped, model.BuildPlanDockerfile},
		{"foreign", "foreign-version", "other-project", model.PipelineRunSucceeded, model.BuildPlanDockerfile},
		{"script", "script-version", run.ProjectID, model.PipelineRunSucceeded, model.BuildPlanScript},
		{"failed", "failed-version", run.ProjectID, model.PipelineRunFailed, model.BuildPlanDockerfile},
		{"parallel", "parallel-version", run.ProjectID, model.PipelineRunSucceeded, model.BuildPlanDockerfile},
		{"later", "later-version", run.ProjectID, model.PipelineRunSucceeded, model.BuildPlanDockerfile},
	} {
		artifact := model.Artifact{ID: uuid.NewString(), ProjectID: item.project, ApplicationID: run.ApplicationID, PipelineRunID: "earlier-run", Name: "example/app:" + item.tag, Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, ImageRef: "example/app@sha256:" + strings.Repeat("a", 64)}
		if err := f.db.Create(&artifact).Error; err != nil {
			t.Fatal(err)
		}
		if item.id == "foreign" {
			foreignID = artifact.ID
		}
		node := model.PipelineNodeRun{ID: uuid.NewString(), ProjectID: run.ProjectID, PipelineRunID: run.ID, NodeID: item.id, ArtifactID: artifact.ID, Status: item.status}
		if err := f.db.Create(&node).Error; err != nil {
			t.Fatal(err)
		}
		plans[item.id] = map[string]any{"kind": item.kind}
		if item.id != "parallel" && item.id != "later" {
			prior = append(prior, model.WorkflowNode{ID: item.id, Type: model.WorkflowNodeBuild})
		}
	}
	snapshot := map[string]any{"independent_deployment_images": true, "build_plans": plans, "stages": []model.WorkflowStage{{ID: "build", Tasks: prior}, {ID: "deploy", Tasks: []model.WorkflowNode{{ID: "parallel", Type: model.WorkflowNodeBuild}, {ID: f.record.WorkflowNodeID, Type: model.WorkflowNodeDeploy}}}, {ID: "after", Tasks: []model.WorkflowNode{{ID: "later", Type: model.WorkflowNodeBuild}}}}}
	encoded, _ := json.Marshal(snapshot)
	run.WorkflowSnapshot = string(encoded)
	f.record.ArtifactID = ""
	values, independent, err := f.service.recordPipelineEnvironment(f.ctx, &f.record, run)
	if err != nil || !independent || values["EDO_IMAGE_TAG"] != "actual-version" || values["EDO_GIT_TAG"] != "git-current" || values["EDO_IMAGE"] != "" {
		t.Fatal("运行变量受到其他阶段、项目或失败任务影响，或自动绑定了镜像", err)
	}
	if err := f.db.Model(&model.Artifact{}).Where("uuid = ?", foreignID).Update("project_id", run.ProjectID).Error; err != nil {
		t.Fatal(err)
	}
	values, _, err = f.service.recordPipelineEnvironment(f.ctx, &f.record, run)
	if err != nil || values["EDO_IMAGE_TAG"] != "" || values["IMAGE_TAG"] != "" {
		t.Fatal("多个前序版本存在歧义时错误退回 Git 版本", err)
	}
	wrong := run
	wrong.ProjectID = "other-project"
	if _, _, err := f.service.recordPipelineEnvironment(f.ctx, &f.record, wrong); err == nil {
		t.Fatal("接受了其他项目的运行上下文")
	}
}

func TestRecordPipelineEnvironmentRestoresSelectedFullImage(t *testing.T) {
	f := newContainerControlFixture(t)
	var run model.PipelineRun
	if err := f.db.First(&run, "uuid = ?", f.record.PipelineRunID).Error; err != nil {
		t.Fatal(err)
	}
	image := model.Artifact{ID: uuid.NewString(), ProjectID: run.ProjectID, ApplicationID: run.ApplicationID, PipelineRunID: run.ID, Name: "api:v3", Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, ImageRef: "registry.example.com/team/custom/api@sha256:" + strings.Repeat("a", 64)}
	if err := f.db.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.PipelineNodeRun{ID: uuid.NewString(), ProjectID: run.ProjectID, PipelineRunID: run.ID, NodeID: "selected-api", ArtifactID: image.ID, Status: model.PipelineRunSucceeded}).Error; err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"selected-api", "", "missing"} {
		snapshot := map[string]any{"image_tag_includes_name": true, "independent_deployment_images": true, "build_plans": map[string]any{"selected-api": map[string]any{"kind": model.BuildPlanDockerfile}}, "stages": []model.WorkflowStage{{Tasks: []model.WorkflowNode{{ID: "selected-api", Type: model.WorkflowNodeBuild}}}, {Tasks: []model.WorkflowNode{{ID: f.record.WorkflowNodeID, Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{ImageBuildNodeID: selected}}}}}}
		encoded, _ := json.Marshal(snapshot)
		run.WorkflowSnapshot = string(encoded)
		values, independent, err := f.service.recordPipelineEnvironment(f.ctx, &f.record, run)
		if selected == "missing" {
			if err == nil {
				t.Fatal("接受丢失的构建任务")
			}
			continue
		}
		if err != nil || !independent {
			t.Fatal(err)
		}
		if selected == "" {
			if values["EDO_IMAGE_TAG"] != "" {
				t.Fatal("未选择时猜测了镜像")
			}
			continue
		}
		if values["EDO_IMAGE_TAG"] != "registry.example.com/team/custom/api:v3" || values["EDO_IMAGE"] != image.ImageRef || values["IMAGE_TAG"] != "" {
			t.Fatal("重建未恢复完整镜像及固定摘要", values)
		}
	}
}
