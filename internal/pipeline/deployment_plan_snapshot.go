package pipeline

import (
	"context"
	"edo/internal/model"
	"encoding/json"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// freezeWorkflowPlanDefaults 固定配置来源；阶段差异继续独立保存。
func freezeWorkflowPlanDefaults(stages []model.WorkflowStage, plans map[string]model.DeploymentPlan, targets map[string]model.DeploymentTarget) bool {
	changed := false
	for i := range stages {
		for j := range stages[i].Tasks {
			node := &stages[i].Tasks[j]
			if node.Type != model.WorkflowNodeDeploy || node.Config.InlineDeploymentPlan != nil {
				continue
			}
			plan, ok := plans[node.Config.DeploymentPlanID]
			if !ok || plan.Kind != model.DeploymentPlanKubernetes {
				continue
			}
			if node.Config.DeploymentOverride != nil && node.Config.DeploymentOverride.PlanSnapshot != nil {
				continue
			}
			var target *model.DeploymentTarget
			if plan.DeploymentTargetID != "" {
				t, exists := targets[plan.DeploymentTargetID]
				if !exists {
					continue
				}
				target = &t
			}
			if node.Config.DeploymentOverride == nil {
				node.Config.DeploymentOverride = &model.WorkflowDeploymentOverride{}
			}
			node.Config.DeploymentOverride.PlanSnapshot = model.NewWorkflowDeploymentPlanSnapshot(plan, target)
			changed = true
		}
	}
	return changed
}

// 旧客户端未携带快照时保留已保存的同一来源，不能意外恢复动态继承。
func retainWorkflowPlanSnapshots(stages, previous []model.WorkflowStage) {
	old := map[string]model.WorkflowNode{}
	for _, stage := range previous {
		for _, node := range stage.Tasks {
			old[node.ID] = node
		}
	}
	for i := range stages {
		for j := range stages[i].Tasks {
			node := &stages[i].Tasks[j]
			prior := old[node.ID]
			if node.Type != model.WorkflowNodeDeploy || node.Config.InlineDeploymentPlan != nil || node.Config.DeploymentPlanID == "" || prior.Config.DeploymentPlanID != node.Config.DeploymentPlanID || prior.Config.DeploymentOverride == nil || prior.Config.DeploymentOverride.PlanSnapshot == nil {
				continue
			}
			if node.Config.DeploymentOverride == nil {
				node.Config.DeploymentOverride = &model.WorkflowDeploymentOverride{}
			}
			{
				data, _ := json.Marshal(prior.Config.DeploymentOverride.PlanSnapshot)
				var snapshot model.WorkflowDeploymentPlanSnapshot
				_ = json.Unmarshal(data, &snapshot)
				node.Config.DeploymentOverride.PlanSnapshot = &snapshot
			}
		}
	}
}

func (s *Service) freezeWorkflowPlanDefaults(ctx context.Context, db *gorm.DB, stages []model.WorkflowStage) error {
	ids, seen := []string{}, map[string]bool{}
	for _, stage := range stages {
		for _, node := range stage.Tasks {
			if node.Type == model.WorkflowNodeDeploy && node.Config.InlineDeploymentPlan == nil && node.Config.DeploymentPlanID != "" && !seen[node.Config.DeploymentPlanID] {
				seen[node.Config.DeploymentPlanID] = true
				ids = append(ids, node.Config.DeploymentPlanID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	plans := []model.DeploymentPlan{}
	if err := db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", ids, true).Find(&plans).Error; err != nil {
		s.logger.WarnContext(ctx, "读取部署方案快照来源失败", "operation", "workflow_plan_defaults", "err", err)
		return model.ErrDeploymentOverride
	}
	byID := map[string]model.DeploymentPlan{}
	targetIDs := []string{}
	for _, plan := range plans {
		byID[plan.ID] = plan
		if plan.DeploymentTargetID != "" {
			targetIDs = append(targetIDs, plan.DeploymentTargetID)
		}
	}
	targets := map[string]model.DeploymentTarget{}
	if len(targetIDs) > 0 {
		var rows []model.DeploymentTarget
		if err := db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", targetIDs, true).Find(&rows).Error; err != nil {
			s.logger.WarnContext(ctx, "读取部署目标快照失败", "operation", "workflow_plan_defaults_targets", "err", err)
			return model.ErrDeploymentOverride
		}
		for _, row := range rows {
			targets[row.ID] = row
		}
	}
	freezeWorkflowPlanDefaults(stages, byID, targets)
	return nil
}

// 修改库方案前，为旧引用补齐原配置。每条定义使用版本锁写入，避免覆盖并发草稿。
func (s *Service) freezeDeploymentPlanConsumers(ctx context.Context, tx *gorm.DB, plan model.DeploymentPlan) error {
	if plan.Kind != model.DeploymentPlanKubernetes {
		return nil
	}
	targets := map[string]model.DeploymentTarget{}
	if plan.DeploymentTargetID != "" {
		var target model.DeploymentTarget
		if err := tx.WithContext(ctx).First(&target, "uuid = ? AND project_id = ? AND department_id = ?", plan.DeploymentTargetID, plan.ProjectID, plan.DepartmentID).Error; err != nil {
			s.logger.WarnContext(ctx, "固定来源方案目标失败", "operation", "deployment_plan_consumer_snapshot", "deployment_plan_id", plan.ID, "err", err)
			return model.ErrDeploymentOverride
		}
		targets[target.ID] = target
	}
	plans := map[string]model.DeploymentPlan{plan.ID: plan}
	var workflows []model.ReleaseWorkflow
	var templates []model.ReleaseWorkflowTemplate
	query := func(db *gorm.DB) *gorm.DB {
		return db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ? AND department_id = ? AND stages LIKE ?", plan.ProjectID, plan.DepartmentID, "%"+plan.ID+"%")
	}
	if err := query(tx).Find(&workflows).Error; err != nil {
		s.logger.WarnContext(ctx, "读取引用流水线失败", "operation", "deployment_plan_consumer_snapshot", "deployment_plan_id", plan.ID, "err", err)
		return model.ErrDeploymentOverride
	}
	if err := query(tx).Find(&templates).Error; err != nil {
		s.logger.WarnContext(ctx, "读取引用流水线方案失败", "operation", "deployment_plan_consumer_snapshot", "deployment_plan_id", plan.ID, "err", err)
		return model.ErrDeploymentOverride
	}
	persist := func(entity any, id string, revision uint64, stages []model.WorkflowStage) error {
		if !freezeWorkflowPlanDefaults(stages, plans, targets) {
			return nil
		}
		data, err := json.Marshal(stages)
		if err != nil {
			return err
		}
		result := tx.WithContext(ctx).Model(entity).Where("uuid = ? AND revision = ?", id, revision).UpdateColumn("stages", string(data))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkflowRevisionConflict
		}
		return nil
	}
	for _, row := range workflows {
		if err := persist(&model.ReleaseWorkflow{}, row.ID, row.Revision, row.Stages); err != nil {
			s.logger.WarnContext(ctx, "固定引用流水线配置失败", "operation", "deployment_plan_consumer_snapshot", "deployment_plan_id", plan.ID, "workflow_id", row.ID, "err", err)
			return model.ErrDeploymentOverride
		}
	}
	for _, row := range templates {
		if err := persist(&model.ReleaseWorkflowTemplate{}, row.ID, row.Revision, row.Stages); err != nil {
			s.logger.WarnContext(ctx, "固定引用流水线方案配置失败", "operation", "deployment_plan_consumer_snapshot", "deployment_plan_id", plan.ID, "workflow_template_id", row.ID, "err", err)
			return model.ErrDeploymentOverride
		}
	}
	return nil
}
