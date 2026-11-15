package pipeline

import (
	"context"
	"encoding/json"
	"reflect"

	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

// resolveWorkflowLibraryPlan 在检查和冻结时共用有效配置解析，不保存库方案或生成目标记录。
func (s *Service) resolveWorkflowLibraryPlan(ctx context.Context, application *model.Application, workflowID string, node model.WorkflowNode, base model.DeploymentPlan, baseTarget *model.DeploymentTarget, db *gorm.DB) (*model.DeploymentPlan, *model.DeploymentTarget, error) {
	if !model.HasWorkflowDeploymentOverride(node.Config.DeploymentOverride) && base.DeploymentTargetID != "" {
		return &base, baseTarget, nil
	}
	if application == nil && !model.HasWorkflowDeploymentOverride(node.Config.DeploymentOverride) {
		return &base, nil, nil
	}
	if base.Kind != model.DeploymentPlanKubernetes || s.deployments == nil {
		s.logger.WarnContext(ctx, "所选方案不支持部署阶段覆盖", "operation", "workflow_deployment_override", "node_id", node.ID, "deployment_plan_id", base.ID, "err", model.ErrDeploymentOverride)
		return nil, nil, model.ErrDeploymentOverride
	}
	if application != nil && (application.ProjectID != base.ProjectID || application.DepartmentID != base.DepartmentID) {
		s.logger.WarnContext(ctx, "部署阶段方案不属于当前应用项目", "operation", "workflow_deployment_override_scope", "node_id", node.ID, "deployment_plan_id", base.ID, "err", database.ErrProjectScope)
		return nil, nil, model.ErrDeploymentOverride
	}
	if err := s.validateDeploymentOverridePatchPermission(ctx, db, node.Config.DeploymentOverride); err != nil {
		return nil, nil, err
	}
	plan, target, err := s.deployments.WithTransaction(db).ResolveWorkflowLibraryPlan(ctx, base, baseTarget, node.Config.DeploymentOverride, workflowID, node.ID)
	if err != nil {
		return nil, nil, err
	}
	if err = s.validateDeploymentOverrideNetworkPermission(ctx, db, base, plan); err != nil {
		return nil, nil, err
	}
	if err = s.validateDeploymentExecutionReferences(ctx, plan.ExecutionConfig, db); err != nil {
		return nil, nil, err
	}
	if err = normalizeDeploymentExecution(plan.Kind, plan.ComposeYAML, plan.ExecutionConfig); err != nil {
		return nil, nil, err
	}
	return plan, target, nil
}

func cloneDeploymentExecutionConfig(input *model.DeploymentExecutionConfig) *model.DeploymentExecutionConfig {
	if input == nil {
		return nil
	}
	return model.DeploymentExecutionFromNode(input.NodeConfig())
}

// 关闭通用方案中已开启的网络配置也属于修改，不能通过恢复默认值绕过授权。
func (s *Service) validateDeploymentOverrideNetworkPermission(ctx context.Context, db *gorm.DB, base model.DeploymentPlan, effective *model.DeploymentPlan) error {
	if !kubernetesNetworkSettingsDiffer(base, effective) {
		return nil
	}
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.UserID == "" || scope.ProjectID == "" {
		s.logger.WarnContext(ctx, "修改部署阶段网络配置缺少操作者范围", "operation", "workflow_deployment_override_network", "deployment_plan_id", base.ID, "err", ErrKubernetesTemplatePermission)
		return ErrKubernetesTemplatePermission
	}
	fresh, err := access.FreshProjectPermissions(db, ctx, scope.UserID, scope.ProjectID)
	if err != nil || !fresh.Permissions["kubernetes.network.update"] {
		if err == nil {
			err = access.ErrAccessDelegationDenied
		}
		s.logger.WarnContext(ctx, "修改部署阶段网络配置权限不足", "operation", "workflow_deployment_override_network", "deployment_plan_id", base.ID, "user_id", scope.UserID, "project_id", scope.ProjectID, "err", err)
		return ErrKubernetesTemplatePermission
	}
	return nil
}

func kubernetesNetworkSettingsDiffer(base model.DeploymentPlan, effective *model.DeploymentPlan) bool {
	before, after := model.DefaultKubernetesDeploymentSettings(), model.DefaultKubernetesDeploymentSettings()
	if base.ExecutionConfig != nil && base.ExecutionConfig.Kubernetes != nil {
		before = base.ExecutionConfig.Kubernetes
	}
	if effective.ExecutionConfig != nil && effective.ExecutionConfig.Kubernetes != nil {
		after = effective.ExecutionConfig.Kubernetes
	}
	return before.NodePort != after.NodePort || !reflect.DeepEqual(before.Ports, after.Ports)
}

// validateWorkflowNetworkChanges 在保存事务中比较旧有效值和新有效值，删除覆盖同样复核权限。
// 库方案只批量读取一次，不按任务逐个查询数据库。
func (s *Service) validateWorkflowNetworkChanges(ctx context.Context, db *gorm.DB, beforeStages, afterStages []model.WorkflowStage) error {
	beforeNodes, afterNodes := map[string]model.WorkflowNode{}, map[string]model.WorkflowNode{}
	ids, seen := []string{}, map[string]bool{}
	collect := func(stages []model.WorkflowStage, nodes map[string]model.WorkflowNode) {
		for _, stage := range stages {
			for _, node := range stage.Tasks {
				nodes[node.ID] = node
				if node.Type == model.WorkflowNodeDeploy && node.Config.DeploymentPlanID != "" && !seen[node.Config.DeploymentPlanID] {
					seen[node.Config.DeploymentPlanID] = true
					ids = append(ids, node.Config.DeploymentPlanID)
				}
			}
		}
	}
	collect(beforeStages, beforeNodes)
	collect(afterStages, afterNodes)
	plans := map[string]model.DeploymentPlan{}
	if len(ids) > 0 {
		var rows []model.DeploymentPlan
		if err := db.WithContext(ctx).Unscoped().Where("uuid IN ?", ids).Find(&rows).Error; err != nil {
			s.logger.WarnContext(ctx, "读取阶段网络配置来源失败", "operation", "workflow_network_change_sources", "err", err)
			return model.ErrDeploymentOverride
		}
		for _, plan := range rows {
			plans[plan.ID] = plan
		}
	}
	effective := func(node model.WorkflowNode) (*model.DeploymentPlan, error) {
		result := &model.DeploymentPlan{}
		if node.Type != model.WorkflowNodeDeploy {
			return result, nil
		}
		if inline := node.Config.InlineDeploymentPlan; inline != nil {
			result.Kind = inline.Kind
			result.ExecutionConfig = cloneDeploymentExecutionConfig(inline.ExecutionConfig)
			return result, nil
		}
		plan, ok := plans[node.Config.DeploymentPlanID]
		if !ok {
			return result, nil
		}
		result = &plan
		result.ExecutionConfig = cloneDeploymentExecutionConfig(plan.ExecutionConfig)
		if override := node.Config.DeploymentOverride; override != nil && len(override.Kubernetes) > 0 {
			if result.ExecutionConfig == nil {
				result.ExecutionConfig = &model.DeploymentExecutionConfig{}
			}
			patched, err := model.ApplyWorkflowKubernetesPatch(result.ExecutionConfig.Kubernetes, override.Kubernetes)
			if err != nil {
				return nil, err
			}
			result.ExecutionConfig.Kubernetes = patched
		}
		return result, nil
	}
	var changedBefore, changedAfter *model.DeploymentPlan
	workloadChanged := false
	for id, node := range beforeNodes {
		other := afterNodes[id]
		if reflect.DeepEqual(node.Config, other.Config) && node.Type == other.Type {
			continue
		}
		before, err := effective(node)
		if err != nil {
			return err
		}
		after, err := effective(other)
		if err != nil {
			return err
		}
		if (node.Config.DeploymentOverride != nil || other.Config.DeploymentOverride != nil) && kubernetesWorkloadSettingsDiffer(*before, after) {
			workloadChanged = true
		}
		if kubernetesNetworkSettingsDiffer(*before, after) {
			changedBefore, changedAfter = before, after
		}
	}
	for id, node := range afterNodes {
		if _, exists := beforeNodes[id]; exists {
			continue
		}
		after, err := effective(node)
		if err != nil {
			return err
		}
		before := &model.DeploymentPlan{}
		if kubernetesNetworkSettingsDiffer(*before, after) {
			changedBefore, changedAfter = before, after
		}
	}
	if workloadChanged {
		if err := s.requireWorkflowOverrideWorkloadPermission(ctx, db); err != nil {
			return err
		}
	}
	if changedBefore != nil {
		return s.validateDeploymentOverrideNetworkPermission(ctx, db, *changedBefore, changedAfter)
	}
	return nil
}

// 即使目标尚未填完，已提交的资源覆盖仍必须先核验修改权限，不能借无效草稿绕过。
func (s *Service) validateDeploymentOverridePatchPermission(ctx context.Context, db *gorm.DB, override *model.WorkflowDeploymentOverride) error {
	if override == nil || !model.HasWorkflowDeploymentOverride(&model.WorkflowDeploymentOverride{Kubernetes: override.Kubernetes}) {
		return nil
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(override.Kubernetes, &patch); err != nil {
		s.logger.WarnContext(ctx, "解析阶段覆盖权限字段失败", "operation", "workflow_deployment_override_permission", "err", err)
		return model.ErrDeploymentOverride
	}
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.UserID == "" || scope.ProjectID == "" {
		s.logger.WarnContext(ctx, "部署阶段覆盖缺少操作者范围", "operation", "workflow_deployment_override_permission", "err", ErrKubernetesTemplatePermission)
		return ErrKubernetesTemplatePermission
	}
	_, ports := patch["ports"]
	_, nodePort := patch["node_port"]
	fresh, err := access.FreshProjectPermissions(db, ctx, scope.UserID, scope.ProjectID)
	if err != nil || !fresh.Permissions["kubernetes.workload.update"] || ((ports || nodePort) && !fresh.Permissions["kubernetes.network.update"]) {
		if err == nil {
			err = access.ErrAccessDelegationDenied
		}
		s.logger.WarnContext(ctx, "部署阶段覆盖修改权限不足", "operation", "workflow_deployment_override_permission", "user_id", scope.UserID, "project_id", scope.ProjectID, "err", err)
		return ErrKubernetesTemplatePermission
	}
	return nil
}

func kubernetesWorkloadSettingsDiffer(base model.DeploymentPlan, effective *model.DeploymentPlan) bool {
	before, after := *model.DefaultKubernetesDeploymentSettings(), *model.DefaultKubernetesDeploymentSettings()
	if base.ExecutionConfig != nil && base.ExecutionConfig.Kubernetes != nil {
		before = *base.ExecutionConfig.Kubernetes
	}
	if effective.ExecutionConfig != nil && effective.ExecutionConfig.Kubernetes != nil {
		after = *effective.ExecutionConfig.Kubernetes
	}
	before.NodePort, after.NodePort = false, false
	before.Ports, after.Ports = nil, nil
	return !reflect.DeepEqual(before, after)
}

func (s *Service) requireWorkflowOverrideWorkloadPermission(ctx context.Context, db *gorm.DB) error {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.UserID == "" || scope.ProjectID == "" {
		s.logger.WarnContext(ctx, "修改已保存的阶段资源覆盖缺少操作者范围", "operation", "workflow_deployment_override_resource_change", "err", ErrKubernetesTemplatePermission)
		return ErrKubernetesTemplatePermission
	}
	fresh, err := access.FreshProjectPermissions(db, ctx, scope.UserID, scope.ProjectID)
	if err != nil || !fresh.Permissions["kubernetes.workload.update"] {
		if err == nil {
			err = access.ErrAccessDelegationDenied
		}
		s.logger.WarnContext(ctx, "修改已保存的阶段资源覆盖权限不足", "operation", "workflow_deployment_override_resource_change", "user_id", scope.UserID, "project_id", scope.ProjectID, "err", err)
		return ErrKubernetesTemplatePermission
	}
	return nil
}
