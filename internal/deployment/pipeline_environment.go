package deployment

import (
	"context"
	"encoding/json"
	"errors"

	"edo/internal/model"
	"edo/internal/pipelineimage"
	"github.com/distribution/reference"
)

// 重建和自定义容器操作复用成功部署所在运行的版本，不能借用后续阶段的产物。
func (s *Service) recordPipelineEnvironment(ctx context.Context, record *model.DeploymentRecord, run model.PipelineRun) (map[string]string, bool, error) {
	fail := func(operation string, err error) (map[string]string, bool, error) {
		if s.logger != nil {
			s.logger.ErrorContext(ctx, "恢复部署运行变量失败", "operation", operation, "deployment_id", record.ID, "pipeline_run_id", run.ID, "node_id", record.WorkflowNodeID, "err", err)
		}
		return nil, false, ErrRuntimeControlInvalid
	}
	if run.ID != record.PipelineRunID || run.ProjectID != record.ProjectID || run.ApplicationID != record.ApplicationID {
		return fail("runtime_pipeline_identity", ErrRuntimeControlInvalid)
	}
	values := map[string]string{
		"EDO_PIPELINE_RUN_ID": run.ID, "EDO_APPLICATION_ID": record.ApplicationID,
		"EDO_GIT_REF": run.Ref, "EDO_GIT_TAG": run.GitTag(), "EDO_COMMIT_SHA": run.ShortCommitSHA(),
		"EDO_DEPLOYMENT_ID": record.ID, "EDO_DEPLOYMENT_TARGET_ID": record.TargetID,
	}
	var snapshot struct {
		ImageTagIncludesName bool                  `json:"image_tag_includes_name"`
		Independent          bool                  `json:"independent_deployment_images"`
		Stages               []model.WorkflowStage `json:"stages"`
		BuildPlans           map[string]struct {
			Kind model.BuildPlanKind `json:"kind"`
		} `json:"build_plans"`
	}
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		return fail("runtime_pipeline_snapshot", err)
	}
	if !snapshot.Independent {
		return values, false, nil
	}
	if snapshot.ImageTagIncludesName {
		kinds := map[string]model.BuildPlanKind{}
		for id, plan := range snapshot.BuildPlans {
			kinds[id] = plan.Kind
		}
		prior := pipelineimage.PriorBuilds(snapshot.Stages, record.WorkflowNodeID, kinds)
		var selected string
		found := false
		for _, stage := range snapshot.Stages {
			for _, node := range stage.Tasks {
				if node.ID == record.WorkflowNodeID && node.Type == model.WorkflowNodeDeploy {
					selected = node.Config.ImageBuildNodeID
					found = true
				}
			}
		}
		if !found {
			return fail("runtime_pipeline_node", pipelineimage.ErrBinding)
		}
		if selected == "" {
			return values, true, nil
		}
		if !prior[selected] {
			return fail("runtime_pipeline_build", pipelineimage.ErrBinding)
		}
		images, err := pipelineimage.Load(ctx, s.db, run, []string{selected})
		if err != nil {
			return fail("runtime_pipeline_selected_image", err)
		}
		image := images[selected]
		tagged, err := pipelineimage.TaggedReference(image)
		if err != nil {
			return fail("runtime_pipeline_image_reference", err)
		}
		values["EDO_IMAGE_TAG"], values["EDO_IMAGE"] = tagged, image.ImageRef
		if image.StorageKind == model.ArtifactStorageKindDockerDaemon {
			values["EDO_IMAGE"] = image.LocalImageID
		}
		return values, true, nil
	}
	prior := []string{}
	found := false
	for _, stage := range snapshot.Stages {
		for _, node := range stage.Tasks {
			found = found || node.ID == record.WorkflowNodeID && node.Type == model.WorkflowNodeDeploy
		}
		if found {
			break
		}
		for _, node := range stage.Tasks {
			if node.Type == model.WorkflowNodeBuild && snapshot.BuildPlans[node.ID].Kind == model.BuildPlanDockerfile {
				prior = append(prior, node.ID)
			}
		}
	}
	if !found {
		return fail("runtime_pipeline_node", ErrRuntimeControlInvalid)
	}
	if tag := run.GitTag(); tag != "" {
		values["EDO_IMAGE_TAG"] = tag
	}
	if len(prior) == 0 {
		return values, true, nil
	}
	var images []model.Artifact
	err := s.db.WithContext(ctx).Model(&model.Artifact{}).
		Select("artifacts.uuid", "artifacts.name", "artifacts.image_ref", "artifacts.storage_kind", "artifacts.local_image_id").Distinct().
		Joins("JOIN pipeline_node_runs ON pipeline_node_runs.artifact_id = artifacts.uuid").
		Where("pipeline_node_runs.pipeline_run_id = ? AND pipeline_node_runs.project_id = ? AND pipeline_node_runs.node_id IN ? AND pipeline_node_runs.status IN ?", run.ID, run.ProjectID, prior, []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunSkipped}).
		Where("artifacts.project_id = ? AND artifacts.application_id = ? AND artifacts.kind = ? AND artifacts.status = ?", run.ProjectID, run.ApplicationID, model.ArtifactKindOCIImage, model.ArtifactStatusAvailable).
		Limit(65).Find(&images).Error
	if err != nil {
		return fail("runtime_pipeline_images", err)
	}
	if len(images) == 0 {
		return values, true, nil
	}
	// 一旦存在真实产物，就不能在歧义或数据无效时退回 Git 版本。
	delete(values, "EDO_IMAGE_TAG")
	if len(images) > 64 {
		return values, true, nil
	}
	tag := ""
	for _, image := range images {
		named, err := reference.ParseNormalizedNamed(image.Name)
		if err != nil {
			return fail("runtime_pipeline_image_tag", err)
		}
		tagged, ok := named.(reference.NamedTagged)
		if !ok {
			return fail("runtime_pipeline_image_tag", errors.New("镜像产物缺少已登记版本"))
		}
		if tag != "" && tag != tagged.Tag() {
			return values, true, nil
		}
		tag = tagged.Tag()
		if record.ArtifactID == image.ID {
			values["EDO_IMAGE"] = image.ImageRef
			if image.StorageKind == model.ArtifactStorageKindDockerDaemon {
				values["EDO_IMAGE"] = image.LocalImageID
			}
		}
	}
	values["EDO_IMAGE_TAG"], values["IMAGE_TAG"] = tag, tag
	return values, true, nil
}
