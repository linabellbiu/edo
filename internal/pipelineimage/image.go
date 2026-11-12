// Package pipelineimage 读取用户明确选择的本次流水线镜像产物。
package pipelineimage

import (
	"context"
	"errors"

	"edo/internal/model"
	"github.com/distribution/reference"
	"gorm.io/gorm"
)

var ErrBinding = errors.New("镜像构建关联无效，请选择前序阶段中已成功产出镜像的构建任务")

// TaggedReference 从真实仓库引用恢复名称，展示名称只用于读取登记的版本。
func TaggedReference(image model.Artifact) (string, error) {
	named, err := reference.ParseNormalizedNamed(image.ImageRef)
	if err != nil {
		return "", ErrBinding
	}
	display, err := reference.ParseNormalizedNamed(image.Name)
	if err != nil {
		return "", ErrBinding
	}
	tagged, ok := display.(reference.NamedTagged)
	if !ok {
		return "", ErrBinding
	}
	full, err := reference.WithTag(reference.TrimNamed(named), tagged.Tag())
	if err != nil {
		return "", ErrBinding
	}
	return reference.FamiliarString(full), nil
}

// PriorBuilds 只允许引用部署之前完整阶段中的镜像构建，不包含同阶段任务。
func PriorBuilds(stages []model.WorkflowStage, nodeID string, kinds map[string]model.BuildPlanKind) map[string]bool {
	prior := map[string]bool{}
	for _, stage := range stages {
		for _, node := range stage.Tasks {
			if node.ID == nodeID {
				return prior
			}
		}
		for _, node := range stage.Tasks {
			if node.Type == model.WorkflowNodeBuild && kinds[node.ID] == model.BuildPlanDockerfile {
				prior[node.ID] = true
			}
		}
	}
	return nil
}

func Load(ctx context.Context, db *gorm.DB, run model.PipelineRun, ids []string) (map[string]model.Artifact, error) {
	if len(ids) > 65 {
		return nil, ErrBinding
	}
	unique := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			return nil, ErrBinding
		}
		unique[id] = true
	}
	result := map[string]model.Artifact{}
	if len(unique) == 0 {
		return result, nil
	}
	var rows []struct {
		model.Artifact
		BuildNodeID string
	}
	err := db.WithContext(ctx).Model(&model.Artifact{}).Select("artifacts.*, pipeline_node_runs.node_id AS build_node_id").
		Joins("JOIN pipeline_node_runs ON pipeline_node_runs.artifact_id = artifacts.uuid").
		Where("pipeline_node_runs.pipeline_run_id = ? AND pipeline_node_runs.project_id = ? AND pipeline_node_runs.node_id IN ? AND pipeline_node_runs.status IN ?", run.ID, run.ProjectID, ids, []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunSkipped}).
		Where("artifacts.project_id = ? AND artifacts.application_id = ? AND artifacts.kind = ? AND artifacts.status = ?", run.ProjectID, run.ApplicationID, model.ArtifactKindOCIImage, model.ArtifactStatusAvailable).
		Limit(66).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, duplicate := result[row.BuildNodeID]; duplicate {
			return nil, ErrBinding
		}
		if _, err := TaggedReference(row.Artifact); err != nil {
			return nil, err
		}
		result[row.BuildNodeID] = row.Artifact
	}
	if len(result) != len(unique) {
		return nil, ErrBinding
	}
	return result, nil
}
