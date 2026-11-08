package pipeline

import (
	"context"
	"edo/internal/model"
	"encoding/json"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) resolveProducedCompose(ctx context.Context, prepared *executionContext) error {
	source := prepared.node.Config.ComposeSource
	if source == nil || source.Kind != "upstream" || prepared.deploymentPlan.ComposeYAML != "" {
		return nil
	}
	if err := s.requirePipelineExecutionPermission(ctx, prepared.run, prepared.snapshot); err != nil {
		return err
	}
	prior := false
	for _, stage := range prepared.snapshot.Stages {
		found := false
		for _, node := range stage.Tasks {
			if node.ID == prepared.node.ID {
				found = true
			}
		}
		if found {
			break
		}
		for _, node := range stage.Tasks {
			if node.ID == source.ArtifactNodeID && node.Type == model.WorkflowNodeBuild {
				prior = true
			}
		}
	}
	if !prior || s.artifacts == nil {
		return ErrComposeSource
	}
	var nodeRun model.PipelineNodeRun
	if err := s.db.WithContext(ctx).Where("pipeline_run_id = ? AND project_id = ? AND node_id = ? AND status IN ?", prepared.run.ID, prepared.run.ProjectID, source.ArtifactNodeID, []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunSkipped}).First(&nodeRun).Error; err != nil {
		return err
	}
	stored, err := s.artifacts.Find(ctx, nodeRun.ArtifactID)
	if err != nil {
		return err
	}
	if stored.ProjectID != prepared.run.ProjectID || stored.ApplicationID != prepared.run.ApplicationID || !artifactAvailableToPipelineRun(func() model.PipelineRun { check := prepared.run; check.ArtifactID = stored.ID; return check }(), stored) || stored.Kind != model.ArtifactKindFileBundle || stored.Status != model.ArtifactStatusAvailable {
		return ErrComposeSource
	}
	var producer model.BuildRun
	if err = s.db.WithContext(ctx).Where("uuid = ? AND pipeline_run_id = ? AND workflow_node_id = ? AND commit_sha = ? AND status = ?", stored.BuildRunID, stored.PipelineRunID, source.ArtifactNodeID, prepared.run.CommitSHA, model.BuildRunStatusSucceeded).First(&producer).Error; err != nil {
		return err
	}
	content, err := s.artifacts.ReadBundleText(ctx, stored.ID, source.ArtifactName, source.Path, maximumComposeStackBytes)
	if err != nil {
		return err
	}
	if _, err = composeServiceNames(content); err != nil {
		return err
	}
	if err = validateComposeBindingsYAML(content, prepared.node.Config); err != nil {
		return err
	}
	// 只补全本次运行已冻结来源的派生正文，锁内合并并行任务各自的快照结果。
	if err := s.requirePipelineExecutionPermission(ctx, prepared.run, prepared.snapshot); err != nil {
		return err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid = ? AND project_id = ?", prepared.run.ID, prepared.run.ProjectID).First(&run).Error; err != nil {
			return err
		}
		snapshot, err := parseWorkflowSnapshot(&run)
		if err != nil {
			return err
		}
		plan, ok := snapshot.DeploymentPlans[prepared.node.ID]
		if !ok || plan.ID != prepared.deploymentPlan.ID {
			return ErrComposeSource
		}
		if plan.ComposeYAML != "" && plan.ComposeYAML != content {
			return ErrComposeSource
		}
		if plan.ComposeYAML == content {
			prepared.snapshot = *snapshot
			prepared.run.WorkflowSnapshot = run.WorkflowSnapshot
			return nil
		}
		plan.ComposeYAML = content
		snapshot.DeploymentPlans[prepared.node.ID] = plan
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		result := tx.Model(&model.PipelineRun{}).Where("uuid = ? AND workflow_snapshot = ?", run.ID, run.WorkflowSnapshot).Update("workflow_snapshot", string(encoded))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrComposeSource
		}
		prepared.snapshot = *snapshot
		prepared.run.WorkflowSnapshot = string(encoded)
		return nil
	})
	if err == nil {
		prepared.deploymentPlan.ComposeYAML = content
	}
	return err
}
