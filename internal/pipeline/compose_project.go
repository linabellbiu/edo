package pipeline

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/composeconfig"
	"edo/internal/model"
)

func (s *Service) freezeComposeProject(ctx context.Context, prepared *executionContext) error {
	if prepared.deploymentPlan.Kind != model.DeploymentPlanComposeStack {
		return nil
	}
	if prepared.application.ID != prepared.run.ApplicationID {
		return ErrPipelineExecutionConfig
	}
	document, err := parseComposeStack(prepared.deploymentPlan.ComposeYAML)
	if err != nil {
		return err
	}
	values, err := deploymentScriptEnvironment(prepared)
	if err != nil {
		return err
	}
	interpolation := composeInterpolationValues(prepared)
	secretUsed := false
	project, err := composeconfig.ResolveProjectName(document, prepared.application.ID, prepared.target.ID, func(name string) (string, bool) {
		value, exists := values[name]
		if !exists {
			value, exists = interpolation[name]
		}
		for _, secret := range prepared.variableRedactions {
			if exists && secret != "" && value == secret {
				secretUsed = true
				return "", false
			}
		}
		return value, exists
	})
	if err != nil || secretUsed {
		return composeconfig.ErrProjectName
	}
	if err := s.requirePipelineExecutionPermission(ctx, prepared.run, prepared.snapshot); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid = ? AND project_id = ? AND department_id = ? AND application_id = ?", prepared.run.ID, prepared.run.ProjectID, prepared.run.DepartmentID, prepared.run.ApplicationID).First(&run).Error; err != nil {
			return err
		}
		if run.Status != model.PipelineRunRunning {
			return ErrPipelineExecutionConfig
		}
		snapshot, err := parseWorkflowSnapshot(&run)
		if err != nil {
			return err
		}
		plan, target := snapshot.DeploymentPlans[prepared.node.ID], snapshot.DeploymentTargets[prepared.node.ID]
		if plan.ID != prepared.deploymentPlan.ID || plan.Kind != prepared.deploymentPlan.Kind || plan.ComposeYAML != prepared.deploymentPlan.ComposeYAML || target.ID != prepared.target.ID || target.HostID != prepared.target.HostID || target.WorkingDirectory != prepared.target.WorkingDirectory || prepared.node.Config.Compose == nil {
			return ErrPipelineExecutionConfig
		}
		found := false
		for i := range snapshot.Stages {
			for j := range snapshot.Stages[i].Tasks {
				node := &snapshot.Stages[i].Tasks[j]
				if node.ID != prepared.node.ID {
					continue
				}
				if found || node.Type != model.WorkflowNodeDeploy || node.Config.Compose == nil || node.Config.Compose.ProjectName != "" && node.Config.Compose.ProjectName != project {
					return composeconfig.ErrProjectName
				}
				found = true
				node.Config.Compose.ProjectName = project
			}
		}
		if !found {
			return ErrPipelineExecutionConfig
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if string(encoded) != run.WorkflowSnapshot {
			result := tx.Model(&model.PipelineRun{}).Where("uuid = ? AND workflow_snapshot = ?", run.ID, run.WorkflowSnapshot).Update("workflow_snapshot", string(encoded))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrPipelineExecutionConfig
			}
		}
		prepared.node.Config.Compose.ProjectName = project
		prepared.snapshot = *snapshot
		prepared.run.WorkflowSnapshot = string(encoded)
		return nil
	})
}
