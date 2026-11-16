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
	return s.expandDeploymentHostsForRun(ctx, db, workflow, nil)
}

func (s *Service) expandDeploymentHostsForRun(ctx context.Context, db *gorm.DB, workflow *model.ReleaseWorkflow, failures map[string]string) error {
	// 方案、目标和主机集中读取，展开任务时不逐台查询数据库。
	planIDs := []string{}
	for _, node := range workflowTasks(workflow.Stages) {
		if node.Type == model.WorkflowNodeDeploy && node.Config.InlineDeploymentPlan == nil {
			planIDs = append(planIDs, node.Config.DeploymentPlanID)
		}
	}
	plans := map[string]model.DeploymentPlan{}
	if len(planIDs) > 0 {
		var rows []model.DeploymentPlan
		if err := db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", planIDs, true).Find(&rows).Error; err != nil {
			return s.hostExpansionError(ctx, "", err)
		}
		for _, plan := range rows {
			plans[plan.ID] = plan
		}
	}
	targetIDs := []string{}
	for _, node := range workflowTasks(workflow.Stages) {
		if plan, ok := plans[node.Config.DeploymentPlanID]; ok {
			plan = model.WorkflowDeploymentSnapshotBase(plan, node.Config.DeploymentOverride)
			if plan.DeploymentTargetID != "" {
				targetIDs = append(targetIDs, plan.DeploymentTargetID)
			}
		}
	}
	targets := map[string]model.DeploymentTarget{}
	if len(targetIDs) > 0 {
		var rows []model.DeploymentTarget
		if err := db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", targetIDs, true).Find(&rows).Error; err != nil {
			return s.hostExpansionError(ctx, "", err)
		}
		for _, target := range rows {
			targets[target.ID] = target
		}
	}
	nodeTargets := map[string]*model.DeploymentTarget{}
	lookupFailures := map[string]error{}
	hostIDs := []string{}
	for _, node := range workflowTasks(workflow.Stages) {
		if node.Type != model.WorkflowNodeDeploy {
			continue
		}
		if node.Config.InlineDeploymentPlan != nil {
			nodeTargets[node.ID] = node.Config.InlineDeploymentPlan.DeploymentTarget
		} else if plan, ok := plans[node.Config.DeploymentPlanID]; ok {
			plan = model.WorkflowDeploymentSnapshotBase(plan, node.Config.DeploymentOverride)
			if plan.Kind == model.DeploymentPlanKubernetes && (plan.DeploymentTargetID == "" || model.HasWorkflowDeploymentOverride(node.Config.DeploymentOverride)) {
				continue
			}
			if target, ok := targets[plan.DeploymentTargetID]; ok {
				nodeTargets[node.ID] = &target
			} else {
				lookupFailures[node.ID] = ErrInvalidWorkflow
			}
		} else {
			lookupFailures[node.ID] = ErrInvalidWorkflow
		}
		if target := nodeTargets[node.ID]; target != nil {
			hostIDs = append(hostIDs, target.HostIDs...)
		}
	}
	hosts := map[string]model.Host{}
	if len(hostIDs) > 0 {
		var rows []model.Host
		if err := db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", hostIDs, true).Find(&rows).Error; err != nil {
			return s.hostExpansionError(ctx, "", err)
		}
		for _, host := range rows {
			hosts[host.ID] = host
		}
	}
	count := 0
	fail := func(nodeID string, err error) error {
		failure := s.hostExpansionError(ctx, nodeID, err)
		if failures == nil {
			return failure
		}
		s.recordWorkflowTaskFailure(ctx, failures, nodeID, "workflow_task_execution_host", "执行主机不可用、未勾选或数量超过限制，请检查此任务的执行主机配置", err)
		return nil
	}
	for i := range workflow.Stages {
		var tasks []model.WorkflowNode
		for _, node := range workflow.Stages[i].Tasks {
			if err := lookupFailures[node.ID]; err != nil {
				if err := fail(node.ID, err); err != nil {
					return err
				}
				tasks = append(tasks, node)
				continue
			}
			target := nodeTargets[node.ID]
			if target == nil || target.HostIDs == nil {
				tasks = append(tasks, node)
				continue
			}
			seen := map[string]bool{}
			invalid := target.Platform == model.DeploymentKubernetes || len(target.HostIDs) == 0 || len(target.HostIDs) > 100
			for _, hostID := range target.HostIDs {
				if seen[hostID] {
					invalid = true
				}
				seen[hostID] = true
			}
			if invalid {
				if err := fail(node.ID, ErrInvalidWorkflow); err != nil {
					return err
				}
				tasks = append(tasks, node)
				continue
			}
			for _, hostID := range target.HostIDs {
				host, exists := hosts[hostID]
				copy := cloneWorkflowStages([]model.WorkflowStage{{Tasks: []model.WorkflowNode{node}}})[0].Tasks[0]
				copy.ID = model.DeploymentHostNodeID(node.ID, hostID)
				suffix := host.Name
				if !exists {
					suffix = "主机不可用"
				}
				name := []rune(node.Name + " · " + suffix)
				if len(name) > 128 {
					name = name[:128]
				}
				copy.Name = string(name)
				copy.Config.ExecutionHostID = hostID
				if copy.Config.InlineDeploymentPlan != nil && copy.Config.InlineDeploymentPlan.DeploymentTarget != nil {
					copy.Config.InlineDeploymentPlan.DeploymentTarget.HostIDs = []string{hostID}
					copy.Config.InlineDeploymentPlan.DeploymentTarget.HostID = hostID
					copy.Config.InlineDeploymentPlan.DeploymentTarget.RuntimeID = ""
				}
				if !exists {
					if err := fail(copy.ID, ErrInvalidWorkflow); err != nil {
						return err
					}
				}
				if failures != nil && failures[node.ID] != "" {
					failures[copy.ID] = failures[node.ID]
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
