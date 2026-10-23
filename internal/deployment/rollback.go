package deployment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"edo/internal/kube"
	"edo/internal/model"
)

var ErrRollbackTargetChanged = errors.New("发布目标或回退版本已变化，请重新确认")

type RollbackConfirmation struct {
	ExpectedCurrentImage    string `json:"expected_current_image"`
	ExpectedTargetImage     string `json:"expected_target_image"`
	ExpectedKubernetesUID   string `json:"expected_kubernetes_uid"`
	ExpectedSourceUpdatedAt string `json:"expected_source_updated_at"`
}

type RollbackPreview struct {
	CanRollback        bool   `json:"can_rollback"`
	Reason             string `json:"reason"`
	SourceDeploymentID string `json:"source_deployment_id"`
	SourceUpdatedAt    string `json:"source_updated_at"`
	CurrentImage       string `json:"current_image"`
	TargetImage        string `json:"target_image"`
	KubernetesUID      string `json:"kubernetes_uid"`
	ClusterName        string `json:"cluster_name"`
	RuntimeID          string `json:"runtime_id"`
	Namespace          string `json:"namespace"`
	WorkloadName       string `json:"workload_name"`
	ContainerName      string `json:"container_name"`
}

func (s *Service) RollbackPreview(ctx context.Context, sourceID string) (*RollbackPreview, error) {
	source, err := s.rollbackSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	preview := rollbackPreviewFor(source)
	if source.Platform != model.DeploymentKubernetes {
		preview.Reason = "当前入口只支持 Kubernetes 镜像回退"
		return preview, nil
	}
	preview.Reason, err = s.validateKubernetesRollback(ctx, source)
	preview.CanRollback = err == nil && preview.Reason == ""
	if s.kube != nil {
		if cluster, findErr := s.kube.Find(ctx, source.RuntimeID); findErr == nil {
			preview.ClusterName = cluster.Name
		}
	}
	return preview, err
}

func rollbackPreviewFor(source *model.DeploymentRecord) *RollbackPreview {
	return &RollbackPreview{
		SourceDeploymentID: source.ID, SourceUpdatedAt: source.UpdatedAt.Format(time.RFC3339Nano),
		CurrentImage: source.Image, TargetImage: rollbackTargetImage(source), KubernetesUID: source.KubernetesUID,
		RuntimeID: source.RuntimeID, Namespace: source.Namespace, WorkloadName: source.WorkloadName, ContainerName: source.ContainerName,
	}
}

func (s *Service) rollbackSource(ctx context.Context, sourceID string) (*model.DeploymentRecord, error) {
	var source model.DeploymentRecord
	if err := s.db.WithContext(ctx).First(&source, "id = ?", sourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeploymentNotFound
		}
		s.logger.Error("查询待回退发布记录失败", "operation", "deployment_rollback_source", "source_deployment_id", sourceID, "err", err)
		return nil, fmt.Errorf("查询待回退发布记录失败: %w", err)
	}
	return &source, nil
}

func (s *Service) validateKubernetesRollback(ctx context.Context, source *model.DeploymentRecord) (string, error) {
	if source.Status != model.DeploymentSucceeded && source.Status != model.DeploymentFailed {
		return "当前发布状态不支持镜像回退", nil
	}
	if source.KubernetesUID == "" {
		return "该历史记录未保存工作负载身份，无法安全回退", nil
	}
	if !immutableImageReference(source.PreviousImage) || !immutableImageReference(source.Image) {
		return "没有可安全回退的上一镜像 Digest", nil
	}
	if rollbackTargetImage(source) == source.Image {
		return "上一镜像与当前镜像相同，无需回退", nil
	}
	if source.ApplicationID == "" || source.PipelineRunID == "" || source.DeploymentPlanID == "" {
		return "该记录缺少应用流水线归属，无法安全回退", nil
	}
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ? AND application_id = ? AND project_id = ? AND department_id = ?",
		source.PipelineRunID, source.ApplicationID, source.ProjectID, source.DepartmentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "发布记录的应用流水线归属已变化", nil
		}
		return "", s.rollbackValidationError(source, "deployment_rollback_run", err)
	}
	var plan model.DeploymentPlan
	if err := s.db.WithContext(ctx).First(&plan, "id = ? AND project_id = ? AND department_id = ? AND is_active = ?",
		source.DeploymentPlanID, source.ProjectID, source.DepartmentID, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "部署方案已删除或停用，无法回退历史目标", nil
		}
		return "", s.rollbackValidationError(source, "deployment_rollback_plan", err)
	}
	var target model.DeploymentTarget
	if err := s.db.WithContext(ctx).First(&target, "id = ? AND project_id = ? AND department_id = ? AND is_active = ?",
		plan.DeploymentTargetID, source.ProjectID, source.DepartmentID, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "部署目标已删除或停用，无法回退历史目标", nil
		}
		return "", s.rollbackValidationError(source, "deployment_rollback_target", err)
	}
	if plan.Kind != model.DeploymentPlanKubernetes || target.ID != source.TargetID ||
		target.Platform != source.Platform || target.RuntimeID != source.RuntimeID || target.EnvironmentID != source.EnvironmentID ||
		target.HostID != source.HostID || target.Namespace != source.Namespace || target.WorkloadName != source.WorkloadName || target.ContainerName != source.ContainerName {
		return "部署方案的目标已变化，不能回退原位置", nil
	}
	if s.kube == nil {
		return "当前无法连接 Kubernetes 发布能力", nil
	}
	identity, err := s.kube.DeploymentImageIdentity(ctx, source.RuntimeID, source.Namespace, source.WorkloadName, source.ContainerName)
	if err != nil {
		s.logger.Warn("读取回退目标身份失败", "operation", "deployment_rollback_inspect", "deployment_id", source.ID,
			"cluster_id", source.RuntimeID, "namespace", source.Namespace, "workload_name", source.WorkloadName, "err", err)
		return "无法确认工作负载当前身份，请检查集群和目标容器", nil
	}
	if identity.UID != source.KubernetesUID {
		return "同名工作负载已被重建，不能回退旧对象", nil
	}
	if identity.Image != source.Image || identity.DeploymentID != source.ID {
		return "工作负载镜像或发布归属已变化，请查看最近发布", nil
	}
	return "", nil
}

func (s *Service) rollbackValidationError(source *model.DeploymentRecord, operation string, err error) error {
	s.logger.Error("校验镜像回退来源失败", "operation", operation, "deployment_id", source.ID, "application_id", source.ApplicationID, "err", err)
	return fmt.Errorf("校验镜像回退来源失败: %w", err)
}

func rollbackConfirmationMatches(source *model.DeploymentRecord, expected *RollbackConfirmation) bool {
	return expected != nil && expected.ExpectedCurrentImage == source.Image && expected.ExpectedTargetImage == rollbackTargetImage(source) &&
		expected.ExpectedKubernetesUID == source.KubernetesUID && expected.ExpectedSourceUpdatedAt == source.UpdatedAt.Format(time.RFC3339Nano)
}

func rollbackTargetImage(source *model.DeploymentRecord) string {
	if image, err := validateImage(source.PreviousImage, true); err == nil {
		return image
	}
	return source.PreviousImage
}

func (s *Service) rollbackArtifactID(ctx context.Context, source *model.DeploymentRecord, image string) (string, error) {
	var artifact model.Artifact
	err := s.db.WithContext(ctx).Where("application_id = ? AND project_id = ? AND department_id = ? AND image_ref = ? AND kind = ? AND status = ?",
		source.ApplicationID, source.ProjectID, source.DepartmentID, image, model.ArtifactKindOCIImage, model.ArtifactStatusAvailable).
		Order("created_at DESC").Order("id DESC").First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", s.rollbackValidationError(source, "deployment_rollback_artifact", err)
	}
	return artifact.ID, nil
}

func (s *Service) kubernetesExecutionIdentity(ctx context.Context, record *model.DeploymentRecord) (kube.DeploymentImageIdentity, error) {
	if record.Operation == model.DeploymentRollback {
		source, err := s.rollbackSource(ctx, record.RollbackSourceID)
		if err != nil {
			return kube.DeploymentImageIdentity{}, err
		}
		if source.ApplicationID != record.ApplicationID || source.PipelineRunID != record.PipelineRunID ||
			source.ProjectID != record.ProjectID || source.DepartmentID != record.DepartmentID || source.KubernetesUID != record.KubernetesUID ||
			rollbackTargetImage(source) != record.Image || source.TargetID != record.TargetID || source.RuntimeID != record.RuntimeID ||
			source.Namespace != record.Namespace || source.WorkloadName != record.WorkloadName || source.ContainerName != record.ContainerName {
			return kube.DeploymentImageIdentity{}, ErrRollbackTargetChanged
		}
		reason, err := s.validateKubernetesRollback(ctx, source)
		if err != nil {
			return kube.DeploymentImageIdentity{}, err
		}
		if reason != "" {
			s.logger.Warn("回退执行前目标校验失败", "operation", "deployment_rollback_revalidate", "deployment_id", record.ID, "source_deployment_id", source.ID, "reason", reason)
			return kube.DeploymentImageIdentity{}, ErrRollbackTargetChanged
		}
		return kube.DeploymentImageIdentity{UID: source.KubernetesUID, Image: source.Image, DeploymentID: source.ID}, nil
	}
	identity, err := s.kube.DeploymentImageIdentity(ctx, record.RuntimeID, record.Namespace, record.WorkloadName, record.ContainerName)
	if err != nil {
		return kube.DeploymentImageIdentity{}, err
	}
	// 小范围增量字段只为新发布保存真实 UID；历史记录保持空值且禁止猜测补齐。
	if err := s.db.WithContext(ctx).Model(&model.DeploymentRecord{}).Where("id = ?", record.ID).Update("kubernetes_uid", identity.UID).Error; err != nil {
		return kube.DeploymentImageIdentity{}, s.rollbackValidationError(record, "deployment_kubernetes_identity_save", err)
	}
	record.KubernetesUID = identity.UID
	return identity, nil
}
