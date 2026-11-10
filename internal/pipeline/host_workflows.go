package pipeline

import (
	"context"
	"errors"

	"edo/internal/model"
)

var ErrHostWorkflowsUnavailable = errors.New("读取主机关联流水线失败，请稍后重试")

type HostWorkflowReference struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ApplicationID   string `json:"application_id"`
	ApplicationName string `json:"application_name"`
	IsActive        bool   `json:"is_active"`
}

// ListHostWorkflows 按当前保存的执行主机汇总关联，不用主机组成员或历史发布推测关联。
// 同一流水线的多个部署任务及同一主机的多个组归属只计一次。
func (s *Service) ListHostWorkflows(ctx context.Context) (map[string][]HostWorkflowReference, error) {
	scope, err := s.deliveryScope(ctx)
	fail := func(err error) (map[string][]HostWorkflowReference, error) {
		s.logger.ErrorContext(ctx, "读取主机关联流水线失败", "operation", "host_workflows_list", "project_id", scope.ProjectID, "err", err)
		return nil, ErrHostWorkflowsUnavailable
	}
	if err != nil {
		return fail(err)
	}
	var flows []model.ReleaseWorkflow
	var apps []model.Application
	var plans []model.DeploymentPlan
	var targets []model.DeploymentTarget
	var hosts []model.Host
	for _, query := range []struct {
		dest   any
		fields []string
	}{
		{&flows, []string{"uuid", "name", "application_id", "is_active", "stages"}},
		{&apps, []string{"uuid", "name"}}, {&plans, []string{"uuid", "deployment_target_id"}},
		{&targets, []string{"uuid", "platform", "host_id", "host_ids"}}, {&hosts, []string{"uuid"}},
	} {
		if err := s.db.WithContext(ctx).Select(query.fields).Order("id DESC").Find(query.dest).Error; err != nil {
			return fail(err)
		}
	}
	names := map[string]string{}
	for _, app := range apps {
		names[app.ID] = app.Name
	}
	targetByID := map[string]model.DeploymentTarget{}
	for _, target := range targets {
		targetByID[target.ID] = target
	}
	planTargets := map[string]model.DeploymentTarget{}
	for _, plan := range plans {
		planTargets[plan.ID] = targetByID[plan.DeploymentTargetID]
	}
	result := map[string][]HostWorkflowReference{}
	for _, host := range hosts {
		result[host.ID] = []HostWorkflowReference{}
	}
	for _, flow := range flows {
		if _, exists := names[flow.ApplicationID]; !exists {
			continue
		}
		seen := map[string]bool{}
		for _, stage := range flow.Stages {
			for _, node := range stage.Tasks {
				if node.Type != model.WorkflowNodeDeploy {
					continue
				}
				target := planTargets[node.Config.DeploymentPlanID]
				if node.Config.InlineDeploymentPlan != nil {
					if node.Config.InlineDeploymentPlan.DeploymentTarget == nil {
						continue
					}
					target = *node.Config.InlineDeploymentPlan.DeploymentTarget
				}
				if target.Platform == model.DeploymentKubernetes {
					continue
				}
				ids := target.HostIDs
				if ids == nil && target.HostID != "" {
					ids = []string{target.HostID}
				}
				for _, id := range ids {
					if _, visible := result[id]; !visible || seen[id] {
						continue
					}
					seen[id] = true
					result[id] = append(result[id], HostWorkflowReference{ID: flow.ID, Name: flow.Name, ApplicationID: flow.ApplicationID, ApplicationName: names[flow.ApplicationID], IsActive: flow.IsActive})
				}
			}
		}
	}
	return result, nil
}
