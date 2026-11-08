package pipeline

import (
	"context"
	"fmt"

	"edo/internal/model"
	"gorm.io/gorm"
)

// expandDeploymentHosts 在创建不可变运行快照时展开勾选主机，复用现有逐任务状态、幂等和权限检查。
// 不改写流水线定义；每台主机保留独立任务、日志、发布记录及实例身份。
func (s *Service) expandDeploymentHosts(ctx context.Context, db *gorm.DB, workflow *model.ReleaseWorkflow) error {
	count := 0
	for i := range workflow.Stages {
		var tasks []model.WorkflowNode
		for _, node := range workflow.Stages[i].Tasks {
			var target *model.DeploymentTarget
			if node.Type == model.WorkflowNodeDeploy {
				if node.Config.InlineDeploymentPlan != nil {
					target = node.Config.InlineDeploymentPlan.DeploymentTarget
				} else {
					var plan model.DeploymentPlan
					var current model.DeploymentTarget
					if err := db.WithContext(ctx).First(&plan, "uuid = ? AND is_active = ?", node.Config.DeploymentPlanID, true).Error; err != nil {
						return s.hostExpansionError(ctx, node.ID, err)
					}
					if err := db.WithContext(ctx).First(&current, "uuid = ? AND is_active = ?", plan.DeploymentTargetID, true).Error; err != nil {
						return s.hostExpansionError(ctx, node.ID, err)
					}
					target = &current
				}
			}
			if target == nil || target.HostIDs == nil {
				tasks = append(tasks, node)
				continue
			}
			if target.Platform == model.DeploymentKubernetes || len(target.HostIDs) == 0 || len(target.HostIDs) > 100 {
				return s.hostExpansionError(ctx, node.ID, ErrInvalidWorkflow)
			}
			seen := map[string]bool{}
			for _, hostID := range target.HostIDs {
				if seen[hostID] {
					return s.hostExpansionError(ctx, node.ID, ErrInvalidWorkflow)
				}
				seen[hostID] = true
				var host model.Host
				if err := db.WithContext(ctx).First(&host, "uuid = ? AND is_active = ?", hostID, true).Error; err != nil {
					return s.hostExpansionError(ctx, node.ID, err)
				}
				copy := cloneWorkflowStages([]model.WorkflowStage{{Tasks: []model.WorkflowNode{node}}})[0].Tasks[0]
				copy.ID = model.DeploymentHostNodeID(node.ID, hostID)
				name := []rune(node.Name + " · " + host.Name)
				if len(name) > 128 {
					name = name[:128]
				}
				copy.Name = string(name)
				copy.Config.ExecutionHostID = hostID
				if copy.Config.InlineDeploymentPlan != nil {
					copy.Config.InlineDeploymentPlan.DeploymentTarget.HostIDs = []string{hostID}
					copy.Config.InlineDeploymentPlan.DeploymentTarget.HostID = hostID
					copy.Config.InlineDeploymentPlan.DeploymentTarget.RuntimeID = ""
				}
				tasks = append(tasks, copy)
			}
		}
		count += len(tasks)
		if count > 200 {
			return s.hostExpansionError(ctx, "", fmt.Errorf("展开后超过 200 个任务"))
		}
		workflow.Stages[i].Tasks = tasks
	}
	return nil
}

func (s *Service) hostExpansionError(ctx context.Context, nodeID string, err error) error {
	s.logger.WarnContext(ctx, "解析执行主机失败", "operation", "workflow_deployment_hosts", "node_id", nodeID, "err", err)
	return fmt.Errorf("%w：执行主机不可用、未勾选或数量超过限制，请检查主机组配置", ErrInvalidWorkflow)
}
