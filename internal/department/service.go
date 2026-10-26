package department

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/database"
	"edo/internal/model"
)

var (
	ErrInvalidDepartment   = errors.New("部门信息无效")
	ErrDepartmentNotFound  = errors.New("部门不存在")
	ErrDepartmentNameExist = errors.New("部门名称已存在")
	ErrDepartmentInUse     = errors.New("部门仍有成员或业务资源，不能删除")
	ErrDefaultDepartment   = errors.New("默认部门不能删除")
)

type View struct {
	model.Department
	MemberCount int64 `json:"member_count"`
	IsDefault   bool  `json:"is_default"`
}

type Input struct {
	ParentID    string
	Name        string
	Description string
}

type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) List(ctx context.Context) ([]View, error) {
	query := s.db.WithContext(ctx).Model(&model.Department{}).
		Select("departments.*, COUNT(users.uuid) AS member_count").
		Joins("LEFT JOIN users ON users.department_id = departments.uuid").
		Group("departments.id").Order("departments.name ASC")
	if scope, ok := database.DepartmentScopeFromContext(ctx); ok && !scope.AllDepartments {
		if scope.DepartmentID == "" {
			return nil, database.ErrDepartmentScopeRequired
		}
		query = query.Where("departments.uuid = ?", scope.DepartmentID)
	}
	var result []View
	if err := query.Scan(&result).Error; err != nil {
		return nil, fmt.Errorf("查询部门列表失败: %w", err)
	}
	for i := range result {
		result[i].IsDefault = result[i].ID == database.DefaultDepartmentID
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, id string) (*View, error) {
	return s.find(ctx, strings.TrimSpace(id))
}

func (s *Service) Create(ctx context.Context, input Input) (*View, error) {
	input, err := normalize(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := model.Department{
		ParentID: input.ParentID,
		ID:       uuid.NewString(), Name: input.Name, Description: input.Description,
		IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCompany(tx); err != nil {
			return err
		}
		if err := validateParent(tx, item.ID, input.ParentID); err != nil {
			return err
		}
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		return database.EnsureDepartmentProject(tx, item.ID)
	}); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrDepartmentNameExist
		}
		return nil, fmt.Errorf("创建部门失败: %w", err)
	}
	return &View{Department: item, IsDefault: item.ID == database.DefaultDepartmentID}, nil
}

func (s *Service) Update(ctx context.Context, id string, input Input) (*View, error) {
	input, err := normalize(input)
	if err != nil {
		return nil, err
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCompany(tx); err != nil {
			return err
		}
		if err := validateParent(tx, id, input.ParentID); err != nil {
			return err
		}
		result := (&Service{db: tx}).allowedDepartment(ctx, id).Updates(map[string]any{"name": input.Name, "description": input.Description, "parent_id": input.ParentID, "updated_at": time.Now().UTC()})
		if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
			return ErrDepartmentNameExist
		}
		if result.Error != nil {
			return fmt.Errorf("更新部门失败: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrDepartmentNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.find(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if id == database.DefaultDepartmentID {
		return ErrDefaultDepartment
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCompany(tx); err != nil {
			return err
		}
		service := &Service{db: tx}
		item, err := service.find(ctx, id)
		if err != nil {
			return err
		}
		if item.MemberCount > 0 {
			return ErrDepartmentInUse
		}
		var children int64
		if err := tx.Model(&model.Department{}).Where("parent_id = ?", id).Count(&children).Error; err != nil {
			return fmt.Errorf("检查下级部门失败: %w", err)
		}
		if children > 0 {
			return ErrDepartmentInUse
		}
		for _, table := range departmentResourceTables {
			var count int64
			if err := tx.Table(table).Where("department_id = ?", id).Count(&count).Error; err != nil {
				return fmt.Errorf("检查部门资源失败: %w", err)
			}
			if count > 0 {
				return ErrDepartmentInUse
			}
		}
		var projects int64
		if err := tx.Model(&model.Project{}).Where("department_id = ? AND is_default = ?", id, false).Count(&projects).Error; err != nil {
			return fmt.Errorf("检查部门项目失败: %w", err)
		}
		if projects > 0 {
			return ErrDepartmentInUse
		}
		// 用户删除空部门时，只清理该部门没有业务资源的初始空间和成员关联。
		projectID := database.DefaultProjectID(id)
		if err := tx.Where("project_id = ?", projectID).Delete(&model.ProjectMember{}).Error; err != nil {
			return fmt.Errorf("清理空部门项目成员失败: %w", err)
		}
		if err := tx.Where("uuid = ? AND department_id = ?", projectID, id).Delete(&model.Project{}).Error; err != nil {
			return fmt.Errorf("清理空部门初始空间失败: %w", err)
		}
		result := service.allowedDepartment(ctx, id).Delete(&model.Department{})
		if result.Error != nil {
			return fmt.Errorf("删除部门失败: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrDepartmentNotFound
		}
		return nil
	})
}

func lockCompany(tx *gorm.DB) error {
	var company model.Organization
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&company, "uuid = ?", database.DefaultDepartmentID).Error; err != nil {
		return fmt.Errorf("锁定组织结构失败: %w", err)
	}
	return nil
}

func (s *Service) find(ctx context.Context, id string) (*View, error) {
	var item View
	query := s.db.WithContext(ctx).Model(&model.Department{}).
		Select("departments.*, COUNT(users.uuid) AS member_count").
		Joins("LEFT JOIN users ON users.department_id = departments.uuid").
		Where("departments.uuid = ?", id).Group("departments.id")
	if scope, ok := database.DepartmentScopeFromContext(ctx); ok && !scope.AllDepartments {
		query = query.Where("departments.uuid = ?", scope.DepartmentID)
	}
	if err := query.Scan(&item).Error; err != nil {
		return nil, fmt.Errorf("查询部门失败: %w", err)
	}
	if item.ID == "" {
		return nil, ErrDepartmentNotFound
	}
	item.IsDefault = item.ID == database.DefaultDepartmentID
	return &item, nil
}

func (s *Service) allowedDepartment(ctx context.Context, id string) *gorm.DB {
	query := s.db.WithContext(ctx).Model(&model.Department{}).Where("uuid = ?", id)
	if scope, ok := database.DepartmentScopeFromContext(ctx); ok && !scope.AllDepartments {
		query = query.Where("uuid = ?", scope.DepartmentID)
	}
	return query
}

func normalize(input Input) (Input, error) {
	input.ParentID = strings.TrimSpace(input.ParentID)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 64 || utf8.RuneCountInString(input.Description) > 255 {
		return Input{}, ErrInvalidDepartment
	}
	return input, nil
}

var departmentResourceTables = []string{
	"git_repositories", "applications", "release_workflows", "release_workflow_templates",
	"build_plans", "image_registries", "deployment_plans", "release_plans", "environments", "hosts",
	"docker_endpoints", "kubernetes_clusters", "deployment_targets", "dns_provider_accounts", "dns_domains",
	"notification_channels", "monitor_rules", "scheduled_tasks", "pipeline_runs", "build_runs", "artifacts",
	"deployment_records", "release_plan_executions", "jobs", "audit_logs", "notifications",
}

func validateParent(tx *gorm.DB, id, parent string) error {
	visited := map[string]bool{id: true}
	for parent != "" {
		if visited[parent] {
			return ErrInvalidDepartment
		}
		visited[parent] = true
		var item model.Department
		if err := tx.First(&item, "uuid = ?", parent).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidDepartment
			}
			return fmt.Errorf("读取上级部门失败: %w", err)
		}
		parent = item.ParentID
	}
	return nil
}
