package access

import (
	"context"

	"edo/internal/model"
)

func (s *Service) authorizeProjectManagement(ctx context.Context, user *model.User, project *model.Project, permission string) (err error) {
	defer func() {
		if err != nil {
			s.logger.WarnContext(ctx, "项目管理权限复核失败", "operation", "project_management_authorize", "project_id", project.ID, "user_id", user.ID, "permission", permission, "err", err)
		}
	}()
	scope := projectContext(ctx, user, project)
	current, err := freshProjectPermissions(s.db, scope, user.ID, project.ID, true)
	if err != nil {
		return err
	}
	allowed, err := s.HasPermission(scope, &current.User, permission)
	if err != nil {
		return err
	}
	if !allowed || !current.Permissions[permission] {
		return ErrAccessDelegationDenied
	}
	return nil
}
