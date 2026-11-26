package access

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

// ProjectResourceMoveTargets 批量读取当前可用目标，只有超级管理员可以跨部门移动。
func (s *Service) ProjectResourceMoveTargets(ctx context.Context, actor *model.User) (projects []model.Project, err error) {
	projects = []model.Project{}
	scope, ok := database.DepartmentScopeFromContext(ctx)
	defer func() {
		if err != nil {
			s.logger.WarnContext(ctx, "查询资源移动目标失败", "operation", "project_resource_move_targets", "project_id", scope.ProjectID, "err", err)
		}
	}()
	if !ok || actor == nil || scope.ProjectID == "" || scope.DepartmentID == "" || scope.UserID != actor.ID {
		return nil, ErrAccessDelegationDenied
	}
	identityContext := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: actor.ID, AllDepartments: true})
	var current model.User
	if err := s.db.WithContext(identityContext).First(&current, "uuid = ?", actor.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAccessDelegationDenied
		}
		return nil, err
	}
	if !current.IsActive || current.AuthVersion != actor.AuthVersion || current.DepartmentID != actor.DepartmentID || current.IsSuperuser != actor.IsSuperuser || (!current.IsSuperuser && current.DepartmentID != scope.DepartmentID) {
		return nil, ErrAccessDelegationDenied
	}
	queryContext := ctx
	if current.IsSuperuser {
		queryContext = identityContext
	}
	query := s.db.WithContext(queryContext).Model(&model.Project{}).
		Where("projects.is_active = ? AND projects.uuid <> ?", true, scope.ProjectID)
	if !current.IsSuperuser {
		query = query.Where("projects.department_id = ?", scope.DepartmentID)
		// 所有候选项目在一次查询中应用成员、角色及个人覆盖，拒绝优先于允许。
		query = query.Where(`EXISTS (
			SELECT 1 FROM project_members pm WHERE pm.project_id = projects.uuid AND pm.user_id = ?
			AND (EXISTS (SELECT 1 FROM role_permissions rp WHERE rp.role_id = pm.role_id AND rp.permission = ?)
			OR EXISTS (SELECT 1 FROM user_permissions up WHERE up.user_id = ? AND up.permission = ? AND up.effect = ?))
		) AND NOT EXISTS (SELECT 1 FROM user_permissions denied WHERE denied.user_id = ? AND denied.permission = ? AND denied.effect = ?)`,
			current.ID, PermissionProjectResourceMove, current.ID, PermissionProjectResourceMove, model.PermissionAllow,
			current.ID, PermissionProjectResourceMove, model.PermissionDeny)
	}
	if err := query.Order("projects.name ASC, projects.uuid ASC").Find(&projects).Error; err != nil {
		return nil, err
	}
	allowed := projects[:0]
	for _, project := range projects {
		targetContext := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: current.ID, DepartmentID: project.DepartmentID, ProjectID: project.ID, AllDepartments: current.IsSuperuser})
		permit, err := s.HasPermission(targetContext, &current, PermissionProjectResourceMove)
		if err != nil {
			return nil, err
		}
		if permit {
			allowed = append(allowed, project)
		}
	}
	if err := s.projectResourceMoveDepartmentNames(queryContext, allowed); err != nil {
		return nil, err
	}
	return allowed, nil
}

// ProjectResourceMoveSource 使用请求中的真实项目身份补齐移动来源，不能由客户端填写。
func (s *Service) ProjectResourceMoveSource(ctx context.Context, actor *model.User) (project *model.Project, err error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	defer func() {
		if err != nil {
			s.logger.WarnContext(ctx, "查询资源移动来源失败", "operation", "project_resource_move_source", "project_id", scope.ProjectID, "err", err)
		}
	}()
	if !ok || actor == nil || scope.UserID != actor.ID || scope.ProjectID == "" {
		return nil, ErrAccessDelegationDenied
	}
	project, err = s.ResolveProject(ctx, actor, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	if !project.IsActive || project.DepartmentID != scope.DepartmentID {
		return nil, ErrAccessDelegationDenied
	}
	projects := []model.Project{*project}
	if err := s.projectResourceMoveDepartmentNames(ctx, projects); err != nil {
		return nil, err
	}
	return &projects[0], nil
}

func (s *Service) projectResourceMoveDepartmentNames(ctx context.Context, projects []model.Project) error {
	if len(projects) == 0 {
		return nil
	}
	ids := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		ids[project.DepartmentID] = struct{}{}
	}
	departmentIDs := make([]string, 0, len(ids))
	for id := range ids {
		departmentIDs = append(departmentIDs, id)
	}
	var departments []model.Department
	if err := s.db.WithContext(ctx).Select("uuid", "name").Where("uuid IN ?", departmentIDs).Find(&departments).Error; err != nil {
		return err
	}
	names := make(map[string]string, len(departments))
	for _, department := range departments {
		names[department.ID] = department.Name
	}
	for index := range projects {
		projects[index].DepartmentName = names[projects[index].DepartmentID]
	}
	return nil
}
