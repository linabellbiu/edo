package pipeline

import (
	"context"
	"encoding/json"
	"errors"

	"edo/internal/model"
)

type upstreamArtifact struct {
	NodeID      string                    `json:"node_id"`
	ID          string                    `json:"id"`
	Kind        model.ArtifactKind        `json:"kind"`
	StorageKind model.ArtifactStorageKind `json:"storage_kind"`
	Digest      string                    `json:"digest"`
	ImageRef    string                    `json:"image_ref,omitempty"`
	CommitSHA   string                    `json:"commit_sha"`
}

func validateUpstreamArtifactStages(stages []model.WorkflowStage) []WorkflowIssue {
	prior := map[string]bool{}
	issues := []WorkflowIssue{}
	for _, stage := range stages {
		for _, node := range stage.Tasks {
			if source := node.Config.ComposeSource; source != nil && source.Kind == "upstream" && !prior[source.ArtifactNodeID] {
				issues = append(issues, WorkflowIssue{Code: "invalid_compose_source_stage", Message: "Compose 文件只能来自前序阶段的构建产物", NodeID: node.ID, StageID: stage.ID})
			}
			for _, input := range node.Config.DeploymentInputs {
				if input.Kind == "upstream" && !prior[input.ArtifactNodeID] {
					issues = append(issues, WorkflowIssue{Code: "invalid_deployment_input_stage", Message: "部署文件只能引用前序阶段的构建任务", NodeID: node.ID, StageID: stage.ID})
				}
			}
			for _, source := range node.Config.ArtifactNodeIDs {
				if !prior[source] {
					issues = append(issues, WorkflowIssue{Code: "invalid_upstream_stage", Message: "汇总制品必须引用前序阶段的构建任务", NodeID: node.ID, StageID: stage.ID})
				}
			}
		}
		for _, node := range stage.Tasks {
			if node.Type == model.WorkflowNodeBuild {
				prior[node.ID] = true
			}
		}
	}
	return issues
}

// 只向后续阶段提供本次运行已成功构建的制品身份，不包含凭据、文件路径或其他运行的产物。
func (s *Service) upstreamArtifactsJSON(ctx context.Context, prepared *buildExecutionContext) (string, error) {
	prior := []string{}
	for _, stage := range prepared.snapshot.Stages {
		for _, node := range stage.Tasks {
			if node.ID == prepared.node.ID {
				return s.encodeUpstreamArtifacts(ctx, prepared.run, prior)
			}
		}
		for _, node := range stage.Tasks {
			if node.Type == model.WorkflowNodeBuild {
				prior = append(prior, node.ID)
			}
		}
	}
	return "[]", nil
}

func (s *Service) encodeUpstreamArtifacts(ctx context.Context, run model.PipelineRun, prior []string) (string, error) {
	result := []upstreamArtifact{}
	if len(prior) == 0 {
		return "[]", nil
	}
	var nodes []model.PipelineNodeRun
	if err := s.db.WithContext(ctx).Where("pipeline_run_id = ? AND project_id = ? AND node_id IN ? AND status = ? AND artifact_id <> ''", run.ID, run.ProjectID, prior, model.PipelineRunSucceeded).Find(&nodes).Error; err != nil {
		return "", err
	}
	byNode := make(map[string]model.PipelineNodeRun, len(nodes))
	for _, node := range nodes {
		byNode[node.NodeID] = node
	}
	for _, nodeID := range prior {
		node, exists := byNode[nodeID]
		if !exists {
			continue
		}
		var stored model.Artifact
		if err := s.db.WithContext(ctx).Where("uuid = ? AND pipeline_run_id = ? AND project_id = ? AND application_id = ? AND status = ?", node.ArtifactID, run.ID, run.ProjectID, run.ApplicationID, model.ArtifactStatusAvailable).First(&stored).Error; err != nil {
			return "", err
		}
		var build model.BuildRun
		if err := s.db.WithContext(ctx).Where("uuid = ? AND pipeline_run_id = ? AND workflow_node_id = ? AND commit_sha = ? AND status = ?", stored.BuildRunID, run.ID, nodeID, run.CommitSHA, model.BuildRunStatusSucceeded).First(&build).Error; err != nil {
			return "", err
		}
		result = append(result, upstreamArtifact{NodeID: nodeID, ID: stored.ID, Kind: stored.Kind, StorageKind: stored.StorageKind, Digest: stored.Digest, ImageRef: stored.ImageRef, CommitSHA: build.CommitSHA})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if len(encoded) > 64*1024 {
		return "", errors.New("上游制品清单超过允许大小")
	}
	return string(encoded), nil
}
