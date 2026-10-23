package access

import (
	"errors"
	"fmt"

	casbinmodel "github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"
	"gorm.io/gorm"

	"edo/internal/model"
)

const (
	userSubjectPrefix = "user:"
	roleSubjectPrefix = "role:"
)

var errReadOnlyPolicyAdapter = errors.New("EDO 权限策略适配器只允许从事务表加载策略")

func newAuthorizationModel() (casbinmodel.Model, error) {
	result := casbinmodel.NewModel()
	definitions := []struct {
		section string
		name    string
		value   string
	}{
		{"r", "r", "sub, perm, project"},
		{"p", "p", "sub, perm, eft, scope"},
		{"g", "g", "_, _, _"},
		{"e", "e", "some(where (p.eft == allow)) && !some(where (p.eft == deny))"},
		// 权限码先匹配，避免为无关策略计算角色和项目关系。
		{"m", "m", `r.perm == p.perm && (r.sub == p.sub || (g(r.sub, p.sub, "") && p.scope == "platform") || (r.project != "" && g(r.sub, p.sub, r.project)))`},
	}
	for _, definition := range definitions {
		if !result.AddDef(definition.section, definition.name, definition.value) {
			return nil, fmt.Errorf("初始化 Casbin 模型定义 %s 失败", definition.section)
		}
	}
	return result, nil
}

// policyAdapter 将现有 RBAC 事务表投影为 Casbin 策略。
// 写操作仍由 Service 在同一个 GORM 事务中完成，避免角色元数据和权限策略分裂。
type policyAdapter struct {
	db *gorm.DB
}

func (a *policyAdapter) LoadPolicy(target casbinmodel.Model) error {
	var rolePermissions []model.RolePermission
	if err := a.db.Order("role_id ASC, permission ASC").Find(&rolePermissions).Error; err != nil {
		return fmt.Errorf("加载角色权限策略失败: %w", err)
	}
	for _, item := range rolePermissions {
		for _, code := range []string{item.Permission} {
			if !IsKnown(code) {
				continue
			}
			scope := "platform"
			if ProjectPermission(code) {
				scope = "project"
				if err := persist.LoadPolicyArray([]string{"p", "project-role:" + item.RoleID, code, string(model.PermissionAllow), scope}, target); err != nil {
					return err
				}
			}
			if err := persist.LoadPolicyArray([]string{
				"p", roleSubject(item.RoleID), code, string(model.PermissionAllow), scope,
			}, target); err != nil {
				return fmt.Errorf("装载角色权限策略失败: %w", err)
			}
		}
	}

	var userPermissions []model.UserPermission
	if err := a.db.Order("user_id ASC, permission ASC").Find(&userPermissions).Error; err != nil {
		return fmt.Errorf("加载用户权限覆盖失败: %w", err)
	}
	for _, item := range userPermissions {
		for _, code := range []string{item.Permission} {
			if err := persist.LoadPolicyArray([]string{
				"p", userSubject(item.UserID), code, string(item.Effect), "direct",
			}, target); err != nil {
				return fmt.Errorf("装载用户权限覆盖失败: %w", err)
			}
		}
	}

	var assignments []model.UserRole
	if err := a.db.Order("user_id ASC, role_id ASC").Find(&assignments).Error; err != nil {
		return fmt.Errorf("加载用户角色关系失败: %w", err)
	}
	for _, item := range assignments {
		if err := persist.LoadPolicyArray([]string{
			"g", userSubject(item.UserID), roleSubject(item.RoleID), "",
		}, target); err != nil {
			return fmt.Errorf("装载用户角色关系失败: %w", err)
		}
	}
	var members []model.ProjectMember
	if !a.db.Migrator().HasTable(&model.ProjectMember{}) {
		return nil
	}
	if err := a.db.Find(&members).Error; err != nil {
		return fmt.Errorf("加载项目成员策略失败: %w", err)
	}
	for _, item := range members {
		if err := persist.LoadPolicyArray([]string{"g", userSubject(item.UserID), "project-role:" + item.RoleID, item.ProjectID}, target); err != nil {
			return fmt.Errorf("装载项目成员策略失败: %w", err)
		}
	}
	return nil
}

func (*policyAdapter) SavePolicy(casbinmodel.Model) error {
	return errReadOnlyPolicyAdapter
}

func (*policyAdapter) AddPolicy(string, string, []string) error {
	return errReadOnlyPolicyAdapter
}

func (*policyAdapter) RemovePolicy(string, string, []string) error {
	return errReadOnlyPolicyAdapter
}

func (*policyAdapter) RemoveFilteredPolicy(string, string, int, ...string) error {
	return errReadOnlyPolicyAdapter
}

func userSubject(userID string) string { return userSubjectPrefix + userID }

func roleSubject(roleID string) string { return roleSubjectPrefix + roleID }
