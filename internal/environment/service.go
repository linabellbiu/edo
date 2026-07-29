package environment

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"zrt/internal/model"
)

var (
	ErrInvalidEnvironment       = errors.New("环境信息无效")
	ErrEnvironmentExists        = errors.New("环境名称已存在")
	ErrEnvironmentNotFound      = errors.New("环境不存在")
	ErrEnvironmentReferenced    = errors.New("环境仍被部署配置引用，不能删除")
	ErrHostMembershipReferenced = errors.New("所选主机仍被该环境的部署配置引用，不能移除")
	ErrHostNotFound             = errors.New("所选主机不存在，请刷新后重试")
)

var environmentNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_. -]{0,127}$`)

type Input struct {
	Name        string
	Description string
	// HostIDs 是当前环境包含的完整主机集合。主机可以同时属于多个环境，
	// 更新只增删当前环境的关系，不影响该主机在其他环境中的成员关系。
	HostIDs []string
}

type ProfileInput struct {
	Name        string
	Description string
}

type Detail struct {
	Environment model.Environment
	Hosts       []model.Host
}

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func (s *Service) List(ctx context.Context) ([]Detail, error) {
	var environments []model.Environment
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&environments).Error; err != nil {
		return nil, fmt.Errorf("查询环境列表失败: %w", err)
	}
	if len(environments) == 0 {
		return []Detail{}, nil
	}

	environmentIDs := make([]string, 0, len(environments))
	for i := range environments {
		environmentIDs = append(environmentIDs, environments[i].ID)
	}
	var memberships []model.EnvironmentHost
	if err := s.db.WithContext(ctx).
		Where("environment_id IN ?", environmentIDs).
		Find(&memberships).Error; err != nil {
		return nil, fmt.Errorf("查询环境所属主机失败: %w", err)
	}
	hostEnvironmentIDs := make(map[string][]string, len(memberships))
	hostIDs := make([]string, 0, len(memberships))
	for i := range memberships {
		if _, exists := hostEnvironmentIDs[memberships[i].HostID]; !exists {
			hostIDs = append(hostIDs, memberships[i].HostID)
		}
		hostEnvironmentIDs[memberships[i].HostID] = append(
			hostEnvironmentIDs[memberships[i].HostID], memberships[i].EnvironmentID,
		)
	}
	hostsByEnvironment := make(map[string][]model.Host, len(environments))
	if len(hostIDs) > 0 {
		var hosts []model.Host
		if err := s.db.WithContext(ctx).Where("id IN ?", hostIDs).Order("name ASC").Find(&hosts).Error; err != nil {
			return nil, fmt.Errorf("查询环境所属主机失败: %w", err)
		}
		for i := range hosts {
			for _, environmentID := range hostEnvironmentIDs[hosts[i].ID] {
				hostsByEnvironment[environmentID] = append(hostsByEnvironment[environmentID], hosts[i])
			}
		}
	}
	result := make([]Detail, 0, len(environments))
	for i := range environments {
		environment := environments[i]
		environmentHosts := hostsByEnvironment[environment.ID]
		if environmentHosts == nil {
			environmentHosts = []model.Host{}
		}
		result = append(result, Detail{Environment: environment, Hosts: environmentHosts})
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Detail, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrEnvironmentNotFound
	}
	var environment model.Environment
	if err := s.db.WithContext(ctx).First(&environment, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("查询环境失败: %w", err)
	}
	hosts, err := listHosts(ctx, s.db, environment.ID)
	if err != nil {
		return nil, err
	}
	return &Detail{Environment: environment, Hosts: hosts}, nil
}

func (s *Service) Create(ctx context.Context, actorID string, input Input) (*Detail, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	environment := &model.Environment{
		ID:          uuid.NewString(),
		Name:        input.Name,
		Description: input.Description,
		Level:       "", // 旧数据库列仍有 NOT NULL 约束，但不再承载业务语义。
		IsActive:    true,
		CreatedBy:   strings.TrimSpace(actorID),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureHostsExist(tx, input.HostIDs); err != nil {
			return err
		}
		if err := tx.Create(environment).Error; err != nil {
			return err
		}
		return replaceHosts(tx, environment.ID, input.HostIDs, now)
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrDuplicatedKey):
			return nil, ErrEnvironmentExists
		case errors.Is(err, ErrHostNotFound):
			return nil, ErrHostNotFound
		default:
			return nil, fmt.Errorf("创建环境失败: %w", err)
		}
	}
	return s.Get(ctx, environment.ID)
}

func (s *Service) Update(ctx context.Context, id string, input Input) (*Detail, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrEnvironmentNotFound
	}
	input, err := normalizeInput(input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Environment
		if err := tx.First(&existing, "id = ?", id).Error; err != nil {
			return err
		}
		if err := ensureHostsExist(tx, input.HostIDs); err != nil {
			return err
		}
		if err := tx.Model(&existing).Updates(map[string]any{
			"name":        input.Name,
			"description": input.Description,
			"updated_at":  now,
		}).Error; err != nil {
			return err
		}
		return replaceHosts(tx, existing.ID, input.HostIDs, now)
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return nil, ErrEnvironmentNotFound
		case errors.Is(err, gorm.ErrDuplicatedKey):
			return nil, ErrEnvironmentExists
		case errors.Is(err, ErrHostNotFound):
			return nil, ErrHostNotFound
		case errors.Is(err, ErrHostMembershipReferenced):
			return nil, ErrHostMembershipReferenced
		default:
			return nil, fmt.Errorf("更新环境失败: %w", err)
		}
	}
	return s.Get(ctx, id)
}

func (s *Service) UpdateProfile(ctx context.Context, id string, input ProfileInput) (*Detail, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrEnvironmentNotFound
	}
	input, err := normalizeProfileInput(input)
	if err != nil {
		return nil, err
	}
	result := s.db.WithContext(ctx).
		Model(&model.Environment{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"name":        input.Name,
			"description": input.Description,
			"updated_at":  time.Now().UTC(),
		})
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
			return nil, ErrEnvironmentExists
		}
		return nil, fmt.Errorf("更新环境基本信息失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, ErrEnvironmentNotFound
	}
	return s.Get(ctx, id)
}

func (s *Service) ReplaceHosts(ctx context.Context, id string, hostIDs []string) (*Detail, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrEnvironmentNotFound
	}
	hostIDs, err := normalizeHostIDs(hostIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Environment
		if err := tx.First(&existing, "id = ?", id).Error; err != nil {
			return err
		}
		if err := ensureHostsExist(tx, hostIDs); err != nil {
			return err
		}
		if err := replaceHosts(tx, existing.ID, hostIDs, now); err != nil {
			return err
		}
		return tx.Model(&existing).Update("updated_at", now).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return nil, ErrEnvironmentNotFound
		case errors.Is(err, ErrHostNotFound):
			return nil, ErrHostNotFound
		case errors.Is(err, ErrHostMembershipReferenced):
			return nil, ErrHostMembershipReferenced
		default:
			return nil, fmt.Errorf("调整环境主机归属失败: %w", err)
		}
	}
	return s.Get(ctx, id)
}

func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrEnvironmentNotFound
	}
	result := s.db.WithContext(ctx).
		Model(&model.Environment{}).
		Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改环境状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrEnvironmentNotFound
	}
	return nil
}

func (s *Service) Remove(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrEnvironmentNotFound
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Environment
		if err := tx.First(&existing, "id = ?", id).Error; err != nil {
			return err
		}
		var targetCount int64
		if err := tx.Model(&model.DeploymentTarget{}).
			Where("platform = ? AND environment_id = ?", model.DeploymentSSH, existing.ID).
			Count(&targetCount).Error; err != nil {
			return err
		}
		if targetCount > 0 {
			return ErrEnvironmentReferenced
		}
		if err := tx.Where("environment_id = ?", existing.ID).Delete(&model.EnvironmentHost{}).Error; err != nil {
			return err
		}
		return tx.Delete(&existing).Error
	}); err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return ErrEnvironmentNotFound
		case errors.Is(err, ErrEnvironmentReferenced):
			return ErrEnvironmentReferenced
		default:
			return fmt.Errorf("删除环境失败: %w", err)
		}
	}
	return nil
}

func normalizeInput(input Input) (Input, error) {
	profile, err := normalizeProfileInput(ProfileInput{Name: input.Name, Description: input.Description})
	if err != nil {
		return Input{}, ErrInvalidEnvironment
	}
	hostIDs, err := normalizeHostIDs(input.HostIDs)
	if err != nil {
		return Input{}, err
	}
	input.Name = profile.Name
	input.Description = profile.Description
	input.HostIDs = hostIDs
	return input, nil
}

func normalizeProfileInput(input ProfileInput) (ProfileInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if !environmentNamePattern.MatchString(input.Name) ||
		utf8.RuneCountInString(input.Description) > 500 {
		return ProfileInput{}, ErrInvalidEnvironment
	}
	return input, nil
}

func normalizeHostIDs(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	hostIDs := make([]string, 0, len(values))
	for _, rawID := range values {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, ErrInvalidEnvironment
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		hostIDs = append(hostIDs, id)
	}
	return hostIDs, nil
}

func ensureHostsExist(tx *gorm.DB, hostIDs []string) error {
	if len(hostIDs) == 0 {
		return nil
	}
	var count int64
	if err := tx.Model(&model.Host{}).Where("id IN ?", hostIDs).Count(&count).Error; err != nil {
		return fmt.Errorf("查询所选主机失败: %w", err)
	}
	if count != int64(len(hostIDs)) {
		return ErrHostNotFound
	}
	return nil
}

func replaceHosts(tx *gorm.DB, environmentID string, hostIDs []string, now time.Time) error {
	remove := tx.Model(&model.EnvironmentHost{}).Where("environment_id = ?", environmentID)
	if len(hostIDs) > 0 {
		remove = remove.Where("host_id NOT IN ?", hostIDs)
	}
	var removedHostIDs []string
	if err := remove.Pluck("host_id", &removedHostIDs).Error; err != nil {
		return fmt.Errorf("查询待移除环境主机失败: %w", err)
	}
	if len(removedHostIDs) > 0 {
		var targetCount int64
		if err := tx.Model(&model.DeploymentTarget{}).
			Where("platform = ? AND environment_id = ? AND host_id IN ?", model.DeploymentSSH, environmentID, removedHostIDs).
			Count(&targetCount).Error; err != nil {
			return fmt.Errorf("检查环境主机部署配置引用失败: %w", err)
		}
		if targetCount > 0 {
			return ErrHostMembershipReferenced
		}
	}
	if err := remove.Delete(&model.EnvironmentHost{}).Error; err != nil {
		return fmt.Errorf("解除环境主机归属失败: %w", err)
	}
	if len(hostIDs) == 0 {
		return nil
	}
	memberships := make([]model.EnvironmentHost, 0, len(hostIDs))
	for _, hostID := range hostIDs {
		memberships = append(memberships, model.EnvironmentHost{
			EnvironmentID: environmentID,
			HostID:        hostID,
			CreatedAt:     now,
		})
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&memberships).Error; err != nil {
		return fmt.Errorf("更新环境主机归属失败: %w", err)
	}
	return nil
}

func listHosts(ctx context.Context, db *gorm.DB, environmentID string) ([]model.Host, error) {
	hosts := make([]model.Host, 0)
	if err := db.WithContext(ctx).
		Joins("JOIN environment_hosts AS membership ON membership.host_id = hosts.id").
		Where("membership.environment_id = ?", environmentID).
		Order("hosts.name ASC").
		Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("查询环境所属主机失败: %w", err)
	}
	return hosts, nil
}
