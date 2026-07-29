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

	"zrt/internal/model"
)

var (
	ErrInvalidEnvironment    = errors.New("环境信息无效")
	ErrEnvironmentExists     = errors.New("环境名称已存在")
	ErrEnvironmentNotFound   = errors.New("环境不存在")
	ErrEnvironmentReferenced = errors.New("环境仍被部署配置引用，不能删除")
	ErrHostNotFound          = errors.New("所选主机不存在，请刷新后重试")
)

var environmentNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_. -]{0,127}$`)

type Input struct {
	Name        string
	Description string
	// HostIDs 是环境当前应包含的完整主机集合。更新时未包含的原成员会解除归属，
	// 已属于其他环境的主机会原子移动到当前环境。
	HostIDs []string
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
	var hosts []model.Host
	if err := s.db.WithContext(ctx).
		Where("environment_id IN ?", environmentIDs).
		Order("name ASC").
		Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("查询环境所属主机失败: %w", err)
	}

	hostsByEnvironment := make(map[string][]model.Host, len(environments))
	for i := range hosts {
		host := hosts[i]
		hostsByEnvironment[host.EnvironmentID] = append(hostsByEnvironment[host.EnvironmentID], host)
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
		default:
			return nil, fmt.Errorf("更新环境失败: %w", err)
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
		now := time.Now().UTC()
		if err := tx.Model(&model.Host{}).Where("environment_id = ?", existing.ID).
			Updates(map[string]any{"environment_id": "", "updated_at": now}).Error; err != nil {
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
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if !environmentNamePattern.MatchString(input.Name) ||
		utf8.RuneCountInString(input.Description) > 500 {
		return Input{}, ErrInvalidEnvironment
	}

	seen := make(map[string]struct{}, len(input.HostIDs))
	hostIDs := make([]string, 0, len(input.HostIDs))
	for _, rawID := range input.HostIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return Input{}, ErrInvalidEnvironment
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		hostIDs = append(hostIDs, id)
	}
	input.HostIDs = hostIDs
	return input, nil
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
	unassign := tx.Model(&model.Host{}).Where("environment_id = ?", environmentID)
	if len(hostIDs) > 0 {
		unassign = unassign.Where("id NOT IN ?", hostIDs)
	}
	if err := unassign.Updates(map[string]any{
		"environment_id": "",
		"updated_at":     now,
	}).Error; err != nil {
		return fmt.Errorf("解除环境主机归属失败: %w", err)
	}
	if len(hostIDs) == 0 {
		return nil
	}
	if err := tx.Model(&model.Host{}).
		Where("id IN ?", hostIDs).
		Updates(map[string]any{
			"environment_id": environmentID,
			"updated_at":     now,
		}).Error; err != nil {
		return fmt.Errorf("更新环境主机归属失败: %w", err)
	}
	return nil
}

func listHosts(ctx context.Context, db *gorm.DB, environmentID string) ([]model.Host, error) {
	hosts := make([]model.Host, 0)
	if err := db.WithContext(ctx).
		Where("environment_id = ?", environmentID).
		Order("name ASC").
		Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("查询环境所属主机失败: %w", err)
	}
	return hosts, nil
}
