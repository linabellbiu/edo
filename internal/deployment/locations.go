package deployment

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"edo/internal/model"
)

type LocationRecord struct {
	model.DeploymentRecord
	ScopeApplicationID string `json:"-"`
	WorkflowID         string `json:"workflow_id,omitempty"`
	WorkflowName       string `json:"workflow_name,omitempty"`
	TaskName           string `json:"task_name,omitempty"`
	HostName           string `json:"host_name,omitempty"`
	ComposeProject     string `json:"compose_project,omitempty"`
}

// ListApplicationLocations 每个实际位置只返回最近成功和最近尝试，删除标记同样参与选择。
func (s *Service) ListApplicationLocations(ctx context.Context) ([]LocationRecord, error) {
	const runJoin = "LEFT JOIN pipeline_runs AS r ON r.uuid = deployment_records.pipeline_run_id AND r.project_id = deployment_records.project_id AND r.department_id = deployment_records.department_id AND (deployment_records.application_id = '' OR deployment_records.application_id = r.application_id)"
	const app = "COALESCE(NULLIF(deployment_records.application_id, ''), r.application_id)"
	db := s.db.WithContext(ctx)
	groups := db.Model(&model.DeploymentRecord{}).Joins(runJoin).
		Where("deployment_records.pipeline_run_id <> '' AND deployment_records.deployment_plan_id <> '' AND "+app+" <> ''").
		Group("deployment_records.project_id, deployment_records.department_id, "+app+", deployment_records.deployment_plan_id, deployment_records.target_id, deployment_records.platform, deployment_records.environment_id, deployment_records.host_id, deployment_records.runtime_id, deployment_records.namespace, deployment_records.workload_name, deployment_records.container_name, deployment_records.compose_service").
		Select("MAX(deployment_records.id) AS attempt, MAX(CASE WHEN deployment_records.status = ? THEN deployment_records.id END) AS success", model.DeploymentSucceeded)
	// 只取展示字段，部署脚本、凭据和完整运行快照均不进入响应。
	const fields = "id, uuid, project_id, department_id, pipeline_run_id, workflow_node_id, target_id, target_name, platform, environment_id, host_id, runtime_id, namespace, workload_name, container_name, kubernetes_uid, deployment_plan_id, deployment_plan_kind, compose_service, operation, image, image_display, expected_image_id, previous_image, previous_image_id, status, error_message, warning_message, rollback_source_id, runtime_deleted_at, created_at, updated_at, finished_at"
	var rows []LocationRecord
	columns := "deployment_records." + strings.ReplaceAll(fields, ", ", ", deployment_records.")
	err := db.Model(&model.DeploymentRecord{}).
		Joins("JOIN (?) AS locations ON deployment_records.id = locations.attempt OR deployment_records.id = locations.success", groups).
		Joins(runJoin).
		Joins("LEFT JOIN release_workflows AS w ON w.uuid = r.workflow_id AND w.application_id = r.application_id AND w.project_id = r.project_id AND w.department_id = r.department_id").
		Joins("LEFT JOIN pipeline_node_runs AS n ON n.pipeline_run_id = r.uuid AND n.node_id = deployment_records.workflow_node_id AND n.project_id = r.project_id AND n.department_id = r.department_id").
		Select(columns + ", " + app + " AS scope_application_id, r.workflow_id, w.name AS workflow_name, n.name AS task_name").
		Order("deployment_records.id DESC").Find(&rows).Error
	if err == nil {
		err = s.attachLocationNames(ctx, rows)
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "读取应用部署位置失败", "operation", "application_deployment_locations", "err", err)
		return nil, errors.New("读取应用部署位置失败，请稍后重试")
	}
	return rows, nil
}

// Compose 项目名及展开前的主机任务名由执行快照确定；只解码展示所需字段。
func (s *Service) attachLocationNames(ctx context.Context, rows []LocationRecord) error {
	byRun := make(map[string][]int)
	var ids []string
	for i := range rows {
		row := &rows[i]
		row.ApplicationID = row.ScopeApplicationID
		if row.WorkflowNodeID == "" || row.HostID == "" && row.DeploymentPlanKind != model.DeploymentPlanComposeStack && row.TaskName != "" {
			continue
		}
		if _, ok := byRun[row.PipelineRunID]; !ok {
			ids = append(ids, row.PipelineRunID)
		}
		byRun[row.PipelineRunID] = append(byRun[row.PipelineRunID], i)
	}
	for batch := range slices.Chunk(ids, 50) {
		var runs []model.PipelineRun
		if err := s.db.WithContext(ctx).Select("uuid", "project_id", "department_id", "application_id", "workflow_snapshot").Where("uuid IN ?", batch).Find(&runs).Error; err != nil {
			return err
		}
		for _, run := range runs {
			var snapshot struct {
				Stages []struct {
					Tasks []struct {
						ID, Name string
						Config   struct {
							Task    string `json:"execution_task_name"`
							Host    string `json:"execution_host_name"`
							Compose struct {
								ProjectName string `json:"project_name"`
							} `json:"compose"`
						}
					}
				}
			}
			if run.WorkflowSnapshot == "" {
				continue
			}
			if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
				s.logger.WarnContext(ctx, "无法读取历史部署任务名称", "operation", "deployment_location_name", "run_id", run.ID, "err", err)
				continue
			}
			for _, stage := range snapshot.Stages {
				for _, node := range stage.Tasks {
					for _, index := range byRun[run.ID] {
						row := &rows[index]
						if row.WorkflowNodeID == node.ID && row.ApplicationID == run.ApplicationID && row.ProjectID == run.ProjectID && row.DepartmentID == run.DepartmentID {
							row.TaskName = cmp.Or(node.Config.Task, node.Name, row.TaskName)
							row.HostName = node.Config.Host
							row.ComposeProject = node.Config.Compose.ProjectName
						}
					}
				}
			}
		}
	}
	return nil
}

// 已改到其他主机或集群的成功记录只用于历史展示。每次运行操作都重新读取当前应用的任务绑定。
func (s *Service) checkConfiguredLocation(ctx context.Context, record *model.DeploymentRecord) error {
	db := s.db.WithContext(ctx)
	var run model.PipelineRun
	result := db.Select("workflow_id").Where("uuid = ? AND project_id = ? AND department_id = ? AND application_id = ?",
		record.PipelineRunID, record.ProjectID, record.DepartmentID, record.ApplicationID).Limit(1).Find(&run)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 && record.WorkflowNodeID != "" {
		return ErrDeploymentNotCurrent
	}
	if run.WorkflowID == "" {
		// 早期记录没有流水线身份，只能使用既有的最新成功记录校验。
		return nil
	}
	return s.checkApplicationBindings(ctx, record)
}

func (s *Service) checkApplicationBindings(ctx context.Context, record *model.DeploymentRecord) error {
	db := s.db.WithContext(ctx)
	var workflows []model.ReleaseWorkflow
	if err := db.Select("uuid", "stages").Where("application_id = ? AND project_id = ? AND department_id = ?", record.ApplicationID, record.ProjectID, record.DepartmentID).Find(&workflows).Error; err != nil {
		return err
	}
	type binding struct {
		workflowID string
		task       model.WorkflowNode
	}
	var references []binding
	var planIDs []string
	for _, workflow := range workflows {
		for _, stage := range workflow.Stages {
			for _, task := range stage.Tasks {
				if task.Type != model.WorkflowNodeDeploy {
					continue
				}
				if inline := task.Config.InlineDeploymentPlan; inline != nil {
					nodeID := task.ID
					if inline.DeploymentTarget != nil && inline.DeploymentTarget.HostIDs != nil {
						nodeID = model.DeploymentHostNodeID(task.ID, record.HostID)
					}
					planID, targetID := model.InlineDeploymentIdentity(workflow.ID, nodeID)
					if planID == record.DeploymentPlanID && targetID == record.TargetID && savedLocationMatches(record, inline.Kind, inline.DeploymentTarget, inline.ServiceName) {
						return nil
					}
					continue
				}
				id := task.Config.DeploymentPlanID
				derived, _ := model.InlineDeploymentIdentity(workflow.ID, task.ID)
				if id != "" && (record.DeploymentPlanID == id || record.DeploymentPlanID == derived) {
					references = append(references, binding{workflow.ID, task})
					planIDs = append(planIDs, id)
				}
			}
		}
	}
	if len(references) == 0 {
		return ErrDeploymentNotCurrent
	}
	var plans []model.DeploymentPlan
	if err := db.Select("uuid", "kind", "deployment_target_id", "service_name").Preload("DeploymentTarget", "project_id = ? AND department_id = ?", record.ProjectID, record.DepartmentID).
		Where("uuid IN ? AND project_id = ? AND department_id = ?", planIDs, record.ProjectID, record.DepartmentID).Find(&plans).Error; err != nil {
		return err
	}
	for _, ref := range references {
		for _, plan := range plans {
			if plan.ID != ref.task.Config.DeploymentPlanID {
				continue
			}
			override := ref.task.Config.DeploymentOverride
			base := model.WorkflowDeploymentSnapshotBase(plan, override)
			target := base.DeploymentTarget
			planID, targetID := base.ID, base.DeploymentTargetID
			if base.Kind == model.DeploymentPlanKubernetes && (target == nil || model.HasWorkflowDeploymentOverride(override)) {
				planID, targetID = model.InlineDeploymentIdentity(ref.workflowID, ref.task.ID)
				if override != nil && override.DeploymentTarget != nil {
					selected := *override.DeploymentTarget
					if target != nil {
						selected.EnvironmentID = target.EnvironmentID
					}
					target = &selected
				}
				if base.DeploymentTarget != nil && target != nil && sameKubernetesLocation(base.DeploymentTarget, target) {
					targetID = base.DeploymentTargetID
				}
			} else if target != nil && target.HostIDs != nil {
				targetID = model.DeploymentHostNodeID(target.ID, record.HostID)
			}
			if planID == record.DeploymentPlanID && targetID == record.TargetID && savedLocationMatches(record, base.Kind, target, base.ServiceName) {
				return nil
			}
		}
	}
	return ErrDeploymentNotCurrent
}

func sameKubernetesLocation(a, b *model.DeploymentTarget) bool {
	return a.RuntimeID == b.RuntimeID && a.Namespace == b.Namespace && a.WorkloadName == b.WorkloadName && a.ContainerName == b.ContainerName
}

func savedLocationMatches(record *model.DeploymentRecord, kind model.DeploymentPlanKind, target *model.DeploymentTarget, serviceName string) bool {
	if target == nil || target.Platform != record.Platform || record.DeploymentPlanKind != "" && record.DeploymentPlanKind != kind {
		return false
	}
	if kind == model.DeploymentPlanKubernetes {
		return target.EnvironmentID == record.EnvironmentID && target.RuntimeID == record.RuntimeID && target.Namespace == record.Namespace &&
			target.WorkloadName == record.WorkloadName && target.ContainerName == record.ContainerName
	}
	if target.HostIDs != nil {
		if !slices.Contains(target.HostIDs, record.HostID) {
			return false
		}
	} else if target.HostID != "" && target.HostID != record.HostID || target.RuntimeID != "" && target.RuntimeID != record.RuntimeID {
		return false
	}
	if target.EnvironmentID != record.EnvironmentID {
		return false
	}
	switch kind {
	case model.DeploymentPlanCompose:
		return serviceName == "" || serviceName == record.ComposeService
	case model.DeploymentPlanDocker:
		return target.WorkloadName == "" || target.WorkloadName == record.WorkloadName
	}
	return true
}
