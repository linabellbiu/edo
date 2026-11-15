package pipeline

import (
	"context"
	"errors"
	"reflect"

	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

var ErrKubernetesTemplatePermission = errors.New("修改 Kubernetes 模板的副本或资源配置需要工作负载修改权限，修改网络需要服务与网络修改权限")

func (s *Service) validateKubernetesTemplateConfiguration(ctx context.Context, db *gorm.DB, cfg *model.KubernetesDeploymentSettings) error {
	if cfg == nil {
		return nil
	}
	if err := model.ValidateKubernetesDeploymentSettings(cfg); err != nil {
		return err
	}
	defaults := model.DefaultKubernetesDeploymentSettings()
	networkChanged := cfg.NodePort || !reflect.DeepEqual(cfg.Ports, defaults.Ports)
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.UserID == "" || scope.ProjectID == "" {
		s.logger.WarnContext(ctx, "Kubernetes 模板修改缺少操作者范围", "operation", "kubernetes_template_config_authorization", "err", ErrKubernetesTemplatePermission)
		return ErrKubernetesTemplatePermission
	}
	fresh, err := access.FreshProjectPermissions(db, ctx, scope.UserID, scope.ProjectID)
	if err != nil || !fresh.Permissions["kubernetes.workload.update"] || (networkChanged && !fresh.Permissions["kubernetes.network.update"]) {
		if err == nil {
			err = access.ErrAccessDelegationDenied
		}
		s.logger.WarnContext(ctx, "Kubernetes 模板修改权限不足", "operation", "kubernetes_template_config_authorization", "project_id", scope.ProjectID, "user_id", scope.UserID, "err", err)
		return ErrKubernetesTemplatePermission
	}
	return nil
}

func checkDuplicateKubernetesTargets(stages []model.WorkflowStage, plans map[string]model.DeploymentPlan, targets map[string]model.DeploymentTarget) []WorkflowIssue {
	seen := map[string]bool{}
	issues := []WorkflowIssue{}
	for _, stage := range stages {
		for _, node := range stage.Tasks {
			if node.Type != model.WorkflowNodeDeploy {
				continue
			}
			plan, ok := plans[workflowDeploymentPlanKey(node)]
			if !ok {
				continue
			}
			t, ok := targets[plan.DeploymentTargetID]
			if !ok || t.Platform != model.DeploymentKubernetes {
				continue
			}
			key := t.RuntimeID + "/" + t.Namespace + "/" + t.WorkloadName
			if seen[key] {
				issues = append(issues, WorkflowIssue{Code: "duplicate_kubernetes_target", Message: "流水线存在重复的 Kubernetes 集群、命名空间和 Deployment 目标，不能执行", NodeID: node.ID, StageID: stage.ID})
			}
			seen[key] = true
		}
	}
	return issues
}
