package access

import (
	"context"
	"edo/internal/database"
	"edo/internal/model"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrProjectNotFound = errors.New("项目不存在或无权访问")
	ErrProjectInvalid  = errors.New("请输入有效的项目名称和说明")
	ErrProjectMember   = errors.New("成员必须是项目所属部门的有效用户，角色只能包含项目权限")
	ErrProjectInUse    = errors.New("项目仍有业务资源，不能删除")
	ErrProjectConflict = errors.New("本部门已存在同名项目")
	ErrProjectDefault  = errors.New("默认项目不能停用或删除")
)

func (s *Service) Projects(ctx context.Context, user *model.User) ([]model.Project, error) {
	query := s.db.WithContext(ctx).Model(&model.Project{})
	if !user.IsSuperuser {
		query = query.Where("department_id = ? AND (uuid IN (?))", user.DepartmentID, s.db.Model(&model.ProjectMember{}).Select("project_id").Where("user_id = ?", user.ID))
	}
	var result []model.Project
	if err := query.Order("is_default DESC, name ASC").Find(&result).Error; err != nil {
		return nil, fmt.Errorf("查询项目空间失败: %w", err)
	}
	departmentIDs := make([]string, 0, len(result))
	for _, project := range result {
		departmentIDs = append(departmentIDs, project.DepartmentID)
	}
	if len(departmentIDs) > 0 {
		var departments []model.Department
		if err := s.db.WithContext(ctx).Select("uuid", "name").Where("uuid IN ?", departmentIDs).Find(&departments).Error; err != nil {
			return nil, fmt.Errorf("读取项目所属部门失败: %w", err)
		}
		names := make(map[string]string, len(departments))
		for _, department := range departments {
			names[department.ID] = department.Name
		}
		for index := range result {
			result[index].DepartmentName = names[result[index].DepartmentID]
		}
	}
	return result, nil
}

func (s *Service) ResolveProject(ctx context.Context, user *model.User, id string) (*model.Project, error) {
	if id == "" {
		return nil, ErrProjectNotFound
	}
	var project model.Project
	if err := s.db.WithContext(ctx).First(&project, "uuid = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProjectNotFound
		}
		return nil, fmt.Errorf("读取项目归属失败: %w", err)
	}
	if !user.IsSuperuser {
		if user.DepartmentID != project.DepartmentID {
			return nil, ErrProjectNotFound
		}
		{
			var count int64
			if err := s.db.WithContext(ctx).Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", id, user.ID).Count(&count).Error; err != nil {
				return nil, fmt.Errorf("读取项目成员失败: %w", err)
			}
			if count == 0 {
				return nil, ErrProjectNotFound
			}
		}
	}
	return &project, nil
}

func projectContext(ctx context.Context, user *model.User, project *model.Project) context.Context {
	return database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: user.ID, DepartmentID: project.DepartmentID, ProjectID: project.ID, AllDepartments: user.IsSuperuser})
}

func (s *Service) CreateProject(ctx context.Context, user *model.User, name, description, departmentID string) (*model.Project, error) {
	if err := s.EnsureProjectRoles(ctx); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 96 || utf8.RuneCountInString(description) > 255 {
		return nil, ErrProjectInvalid
	}
	if !user.IsSuperuser || departmentID == "" {
		departmentID = user.DepartmentID
	}
	var department model.Department
	if err := s.db.WithContext(ctx).First(&department, "uuid = ? AND is_active = ?", departmentID, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProjectInvalid
		}
		return nil, fmt.Errorf("校验项目部门失败: %w", err)
	}
	now := time.Now().UTC()
	project := model.Project{ID: uuid.NewString(), DepartmentID: departmentID, Name: name, Description: description, IsActive: true, CreatedAt: now, UpdatedAt: now}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&project).Error; err != nil {
			return err
		}
		return tx.Create(&model.ProjectMember{ProjectID: project.ID, UserID: user.ID, RoleID: ProjectOwnerRoleID, CreatedAt: now}).Error
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil, ErrProjectConflict
	}
	if err != nil {
		return nil, fmt.Errorf("创建项目失败: %w", err)
	}
	if err := s.syncPolicy("project_create"); err != nil {
		return nil, ErrPolicySync
	}
	return &project, nil
}

func (s *Service) UpdateProject(ctx context.Context, user *model.User, id, name, description string, active bool) error {
	project, err := s.ResolveProject(ctx, user, id)
	if err != nil {
		return err
	}
	if err := s.authorizeProjectManagement(ctx, user, project, "project.update"); err != nil {
		return err
	}
	if project.IsDefault && !active {
		return ErrProjectDefault
	}
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" || utf8.RuneCountInString(name) > 96 || utf8.RuneCountInString(description) > 255 {
		return ErrProjectInvalid
	}
	if err := s.db.WithContext(ctx).Model(&model.Project{}).Where("uuid = ?", id).Updates(map[string]any{"name": name, "description": description, "is_active": active, "updated_at": time.Now().UTC()}).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrProjectConflict
		}
		return fmt.Errorf("更新项目失败: %w", err)
	}
	return nil
}

type ProjectMemberView struct {
	model.ProjectMember
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	RoleName string `json:"role_name"`
}
type ProjectUserChoice struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}
type ProjectAccessView struct {
	Catalog     []Permission          `json:"catalog"`
	Project     *model.Project        `json:"project"`
	Members     []ProjectMemberView   `json:"members"`
	Users       []ProjectUserChoice   `json:"users"`
	Roles       []RoleWithPermissions `json:"roles"`
	Permissions []string              `json:"permissions"`
}

func (s *Service) ProjectAccess(ctx context.Context, user *model.User, id string) (*ProjectAccessView, error) {
	project, err := s.ResolveProject(ctx, user, id)
	if err != nil {
		return nil, err
	}
	permissions, err := s.UserPermissions(projectContext(ctx, user, project), user)
	if err != nil {
		return nil, err
	}
	result := &ProjectAccessView{Project: project, Members: []ProjectMemberView{}, Users: []ProjectUserChoice{}, Roles: []RoleWithPermissions{}, Permissions: permissions}
	for _, item := range Catalog() {
		if ProjectPermission(item.Code) && !strings.HasPrefix(item.Code, "delivery.") {
			result.Catalog = append(result.Catalog, item)
		}
	}
	allowed, err := s.HasPermission(projectContext(ctx, user, project), user, "project.member.update")
	if err != nil {
		return nil, err
	}
	if !allowed {
		return result, nil
	}
	if err := s.db.WithContext(ctx).Table("project_members").Select("project_members.*, users.username, users.nickname, roles.display_name AS role_name").Joins("JOIN users ON users.uuid = project_members.user_id").Joins("JOIN roles ON roles.uuid = project_members.role_id").Where("project_members.project_id = ?", id).Scan(&result.Members).Error; err != nil {
		return nil, fmt.Errorf("读取项目成员列表失败: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&model.User{}).Select("uuid AS id, username, nickname").Where("department_id = ? AND is_active = ?", project.DepartmentID, true).Find(&result.Users).Error; err != nil {
		return nil, fmt.Errorf("读取可选项目成员失败: %w", err)
	}
	roles, err := s.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	for _, role := range roles {
		valid := len(role.Permissions) > 0
		for _, code := range role.Permissions {
			if !ProjectPermission(code) {
				valid = false
				break
			}
		}
		if valid {
			result.Roles = append(result.Roles, role)
		}
	}
	return result, nil
}

func (s *Service) SetProjectMember(ctx context.Context, user *model.User, projectID, userID, roleID string) error {
	project, err := s.ResolveProject(ctx, user, projectID)
	if err != nil {
		return err
	}
	scope := projectContext(ctx, user, project)
	allowed, err := s.HasPermission(scope, user, "project.member.update")
	if err != nil {
		return err
	}
	if !allowed {
		return ErrAccessDelegationDenied
	}
	// 普通管理员不能通过改自己的项目角色提权；超级管理员始终保留恢复入口。
	if !user.IsSuperuser && userID == user.ID {
		return ErrSelfAccessUpdate
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentActor model.User
		if err := tx.First(&currentActor, "uuid = ?", user.ID).Error; err != nil && !user.IsSuperuser {
			return ErrAccessDelegationDenied
		}
		actorPermissions, err := projectPermissionSetFromDB(tx, user.ID, projectID)
		if err != nil {
			return err
		}
		if !user.IsSuperuser {
			if !currentActor.IsActive || currentActor.DepartmentID != project.DepartmentID {
				return ErrAccessDelegationDenied
			}
			if !actorPermissions["project.member.update"] {
				return ErrAccessDelegationDenied
			}
		}
		var target model.User
		if err := tx.First(&target, "uuid = ?", userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProjectMember
			}
			return err
		}
		if target.DepartmentID != project.DepartmentID || !target.IsActive {
			return ErrProjectMember
		}
		if roleID == "" {
			return tx.Where("project_id = ? AND user_id = ?", projectID, userID).Delete(&model.ProjectMember{}).Error
		}
		var role model.Role
		if err := tx.First(&role, "uuid = ?", roleID).Error; err != nil {
			return ErrProjectMember
		}
		var permissions []string
		if err := tx.Model(&model.RolePermission{}).Where("role_id = ?", roleID).Pluck("permission", &permissions).Error; err != nil {
			return err
		}
		if len(permissions) == 0 {
			return ErrProjectMember
		}
		for _, code := range permissions {
			if !ProjectPermission(code) {
				return ErrProjectMember
			}
			if !user.IsSuperuser && !actorPermissions[code] {
				return ErrAccessDelegationDenied
			}
		}
		member := model.ProjectMember{ProjectID: projectID, UserID: userID, RoleID: roleID, CreatedAt: time.Now().UTC()}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "project_id"}, {Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"role_id"})}).Create(&member).Error
	})
	if err != nil {
		return err
	}
	// 成员关系在每次请求和长连接授权时重读；同步失败也不能继续访问已移除项目。
	if err := s.syncPolicy("project_member_update"); err != nil {
		var target model.User
		if readErr := s.db.WithContext(ctx).First(&target, "uuid = ?", userID).Error; readErr == nil && target.IsSuperuser {
			return ErrPolicySync
		}
		return s.syncUserAccessOrDisable(ctx, userID, "project_member_update")
	}
	return nil
}

func (s *Service) DeleteProject(ctx context.Context, user *model.User, id string) error {
	project, err := s.ResolveProject(ctx, user, id)
	if err != nil {
		return err
	}
	if project.IsDefault {
		return ErrProjectDefault
	}
	if err := s.authorizeProjectManagement(ctx, user, project, "project.delete"); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"applications", "git_repositories", "build_plans", "image_registries", "deployment_plans", "release_plans", "release_workflow_templates", "hosts", "environments", "docker_endpoints", "kubernetes_clusters", "dns_provider_accounts", "notification_channels", "scheduled_tasks", "monitor_rules", "pipeline_runs", "artifacts", "jobs", "outbox_events", "release_workflows", "build_runs", "deployment_targets", "deployment_records", "release_plan_executions", "notifications"} {
			var count int64
			if err := tx.Table(table).Where("project_id = ?", id).Count(&count).Error; err != nil {
				return fmt.Errorf("检查项目资源失败: %w", err)
			}
			if count > 0 {
				return ErrProjectInUse
			}
		}
		var activeAssistant int64
		if err := tx.Model(&model.AssistantConversation{}).Where("project_id = ? AND lease_until > ?", id, time.Now().UTC()).Count(&activeAssistant).Error; err != nil {
			return fmt.Errorf("检查助手运行状态失败: %w", err)
		}
		if activeAssistant > 0 {
			return ErrProjectInUse
		}
		if err := tx.Model(&model.AssistantAction{}).Where("project_id = ? AND (status = ? OR (status = ? AND expires_at > ?))", id, "executing", "pending", time.Now().UTC()).Count(&activeAssistant).Error; err != nil {
			return fmt.Errorf("检查助手确认状态失败: %w", err)
		}
		if activeAssistant > 0 {
			return ErrProjectInUse
		}
		if err := tx.Where("project_id = ?", id).Delete(&model.ProjectMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Project{}, "uuid = ?", id).Error
	})
}

const (
	ProjectOwnerRoleID     = "bbf0404f-7165-5119-8620-194a3fab6c01"
	projectDeveloperRoleID = "bbf0404f-7165-5119-8620-194a3fab6c03"
)

func projectDeveloperWritePermission(code string) bool {
	// 开发角色只包含日常开发动作；新增的创建、修改或执行权限必须逐项评估。
	switch code {
	case "application.create", "application.update", "application.execute",
		"repository.create", "repository.update",
		"workflow.create", "workflow.update",
		"build_plan.create", "build_plan.update", "pipeline_run.execute":
		return true
	default:
		return false
	}
}

func (s *Service) EnsureProjectRoles(ctx context.Context) error {
	var roleID string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, preset := range []struct{ id, name, label string }{{ProjectOwnerRoleID, "edo_project_owner", "项目管理员"}, {"bbf0404f-7165-5119-8620-194a3fab6c02", "edo_project_viewer", "项目只读成员"}, {projectDeveloperRoleID, "edo_project_developer", "项目开发成员"}} {
			roleID = preset.id
			var count int64
			if err := tx.Model(&model.Role{}).Where("uuid = ?", preset.id).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 && preset.id != projectDeveloperRoleID {
				continue
			}
			now := time.Now().UTC()
			if count == 0 {
				role := model.Role{ID: preset.id, Name: preset.name, DisplayName: preset.label, Description: "仅包含项目内能力，不授予公司及账号管理权限", CreatedAt: now, UpdatedAt: now}
				if err := tx.Create(&role).Error; err != nil {
					return err
				}
			}
			var permissions []string
			for _, item := range catalog {
				if !ProjectPermission(item.Code) || strings.HasPrefix(item.Code, "delivery.") || item.Code == PermissionK3sUninstall {
					continue
				}
				include := preset.id == ProjectOwnerRoleID || item.Action == "read" && !item.Dangerous || preset.id == projectDeveloperRoleID && projectDeveloperWritePermission(item.Code)
				if include {
					permissions = append(permissions, item.Code)
				}
			}
			permissions = uniqueSorted(permissions)
			if count > 0 {
				// 只收敛固定 UUID 的内置开发模板，保留角色本体及成员、自定义角色关联。
				var existing []string
				if err := tx.Model(&model.RolePermission{}).Where("role_id = ?", preset.id).Order("permission ASC").Pluck("permission", &existing).Error; err != nil {
					return err
				}
				// 数据库排序规则可能忽略标点，使用相同字节序比较以保留稳定权限行。
				slices.Sort(existing)
				if slices.Equal(existing, permissions) {
					continue
				}
			}
			if err := replacePermissions(tx, preset.id, permissions, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.logger.Error("初始化项目角色失败", "operation", "project_role_bootstrap", "role_id", roleID, "err", err)
		return err
	}
	return s.syncPolicy("project_role_bootstrap")
}

func (s *Service) Organization(ctx context.Context) (*model.Organization, error) {
	var result model.Organization
	if err := s.db.WithContext(ctx).First(&result, "uuid = ?", database.DefaultDepartmentID).Error; err != nil {
		return nil, fmt.Errorf("读取公司资料失败: %w", err)
	}
	return &result, nil
}
func (s *Service) UpdateOrganization(ctx context.Context, name, description string) error {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" || utf8.RuneCountInString(name) > 96 || utf8.RuneCountInString(description) > 255 {
		return ErrProjectInvalid
	}
	if err := s.db.WithContext(ctx).Model(&model.Organization{}).Where("uuid = ?", database.DefaultDepartmentID).Updates(map[string]any{"name": name, "description": description, "updated_at": time.Now().UTC()}).Error; err != nil {
		return fmt.Errorf("保存公司资料失败: %w", err)
	}
	return nil
}

func projectPermissionSetFromDB(tx *gorm.DB, userID, projectID string) (map[string]bool, error) {
	result := make(map[string]bool)
	var codes []string
	if err := tx.Model(&model.RolePermission{}).Select("role_permissions.permission").Joins("JOIN project_members ON project_members.role_id = role_permissions.role_id").Where("project_members.project_id = ? AND project_members.user_id = ?", projectID, userID).Pluck("role_permissions.permission", &codes).Error; err != nil {
		return nil, fmt.Errorf("读取项目委派权限失败: %w", err)
	}
	for _, code := range codes {
		if ProjectPermission(code) {
			result[code] = true
		}
	}
	var overrides []model.UserPermission
	if err := tx.Where("user_id = ?", userID).Find(&overrides).Error; err != nil {
		return nil, fmt.Errorf("读取项目委派例外失败: %w", err)
	}
	for _, item := range overrides {
		if item.Effect == model.PermissionAllow && ProjectPermission(item.Permission) {
			result[item.Permission] = true
		}
	}
	for _, item := range overrides {
		if item.Effect == model.PermissionDeny {
			delete(result, item.Permission)
		}
	}
	return result, nil
}
