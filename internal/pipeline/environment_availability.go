package pipeline

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"edo/internal/model"
)

var errDeploymentEnvironmentCheckFailed = errors.New("校验部署环境失败，请稍后重试")

// deploymentEnvironmentAvailable 只核当前环境是否可用于新配置或新运行快照。
// 已启动任务继续使用其不可变快照，不通过这里重新解析执行位置。
func (s *Service) deploymentEnvironmentAvailable(
	ctx context.Context,
	database *gorm.DB,
	target model.DeploymentTarget,
	operation string,
) (bool, error) {
	if target.EnvironmentID == "" && target.Platform != model.DeploymentKubernetes && target.HostID != "" {
		var host model.Host
		err := database.WithContext(ctx).First(&host, "uuid = ? AND project_id = ? AND department_id = ? AND is_active = ?", target.HostID, target.ProjectID, target.DepartmentID, true).Error
		if err == nil {
			return true, nil
		}
		s.logger.WarnContext(ctx, "校验未分组执行主机失败", "operation", operation, "host_id", target.HostID, "project_id", target.ProjectID, "err", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, errDeploymentEnvironmentCheckFailed
	}
	var current model.Environment
	err := database.WithContext(ctx).First(&current,
		"uuid = ? AND project_id = ? AND department_id = ? AND is_active = ?",
		target.EnvironmentID, target.ProjectID, target.DepartmentID, true,
	).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "部署环境当前不可用", "operation", operation,
				"deployment_target_id", target.ID, "environment_id", target.EnvironmentID,
				"project_id", target.ProjectID, "department_id", target.DepartmentID, "err", err)
		}
		return false, nil
	}
	if err != nil {
		if s.logger != nil {
			s.logger.ErrorContext(ctx, "校验部署环境失败", "operation", operation,
				"deployment_target_id", target.ID, "environment_id", target.EnvironmentID,
				"project_id", target.ProjectID, "department_id", target.DepartmentID, "err", err)
		}
		return false, errDeploymentEnvironmentCheckFailed
	}
	return true, nil
}
