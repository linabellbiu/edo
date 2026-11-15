package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"edo/internal/kube"
	"edo/internal/model"
)

// KubernetesTemplateCheck 复用集群资源鉴权和只读重名检测，供流水线检查及执行前复查。
func (s *Service) KubernetesTemplateCheck(ctx context.Context, t model.DeploymentTarget, applicationID string, cfg *model.KubernetesDeploymentSettings) error {
	if cfg == nil {
		return nil
	}
	if s.kube == nil {
		s.logger.ErrorContext(ctx, "模板部署集群服务不可用", "operation", "kubernetes_template_check", "cluster_id", t.RuntimeID, "err", ErrInvalidTarget)
		return ErrInvalidTarget
	}
	return s.kube.CheckDeploymentTemplate(ctx, t, applicationID, cfg)
}

// kubernetesTemplateSnapshot 仅恢复本次运行冻结的模板，不读取后来修改的部署方案。
func (s *Service) kubernetesTemplateSnapshot(ctx context.Context, record *model.DeploymentRecord) (*model.KubernetesDeploymentSettings, error) {
	if record.Operation != model.DeploymentRelease || record.PipelineRunID == "" {
		return nil, nil
	}
	var run model.PipelineRun
	err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ? AND application_id = ?", record.PipelineRunID, record.ProjectID, record.DepartmentID, record.ApplicationID).First(&run).Error
	if err != nil {
		s.logger.ErrorContext(ctx, "读取 Kubernetes 模板运行快照失败", "operation", "kubernetes_template_snapshot", "deployment_id", record.ID, "err", err)
		return nil, ErrInvalidTarget
	}
	var snapshot struct {
		DeploymentPlans map[string]struct {
			ID         string                              `json:"id"`
			Kubernetes *model.KubernetesDeploymentSettings `json:"kubernetes"`
		} `json:"deployment_plans"`
	}
	if err = json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		s.logger.ErrorContext(ctx, "解析 Kubernetes 模板运行快照失败", "operation", "kubernetes_template_snapshot", "deployment_id", record.ID, "err", err)
		return nil, ErrInvalidTarget
	}
	plan, ok := snapshot.DeploymentPlans[record.WorkflowNodeID]
	if !ok || plan.Kubernetes == nil {
		return nil, nil
	}
	if plan.ID != record.DeploymentPlanID || model.ValidateKubernetesDeploymentSettings(plan.Kubernetes) != nil {
		s.logger.WarnContext(ctx, "Kubernetes 模板快照与发布身份不一致", "operation", "kubernetes_template_snapshot", "deployment_id", record.ID, "err", ErrInvalidTarget)
		return nil, ErrInvalidTarget
	}
	return plan.Kubernetes, nil
}

func (s *Service) runKubernetesTemplate(ctx context.Context, record *model.DeploymentRecord, cfg *model.KubernetesDeploymentSettings, command *commandExecution) (string, error) {
	auth := executionRegistryAuth(command)
	pullConfig := kube.RegistryPullConfig(auth.ServerAddress, auth.Username, auth.Credential)
	previous, uid, err := s.kube.DeployTemplate(ctx, *record, cfg, pullConfig, time.Duration(record.RolloutTimeout)*time.Second)
	if uid != "" {
		if saveErr := s.db.WithContext(ctx).Model(&model.DeploymentRecord{}).Where("uuid = ?", record.ID).Update("kubernetes_uid", uid).Error; saveErr != nil {
			s.logger.ErrorContext(ctx, "保存模板工作负载身份失败", "operation", "kubernetes_template_identity_save", "deployment_id", record.ID, "err", saveErr)
			return previous, errors.Join(ErrInvalidTarget, err)
		}
		record.KubernetesUID = uid
	}
	return previous, err
}
