package pipeline

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"zrt/internal/model"
)

var (
	ErrInvalidReleasePlan         = errors.New("发布计划配置无效")
	ErrReleasePlanExists          = errors.New("发布版本已存在")
	ErrReleasePlanNotFound        = errors.New("发布计划不存在")
	ErrReleasePlanNotEditable     = errors.New("当前状态的发布计划不能修改")
	ErrInvalidReleaseGroup        = errors.New("发布组配置无效")
	ErrReleaseGroupExists         = errors.New("发布组名称已存在")
	ErrReleaseGroupNotFound       = errors.New("发布组不存在")
	ErrReleaseGroupDependency     = errors.New("发布组依赖不能形成循环")
	errInvalidReleaseApplications = errors.New("发布应用配置无效")
)

var releaseVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`)
var releaseCommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type ReleaseApplicationInput struct {
	ApplicationID string
	ManualDeploy  bool
	SourceType    model.ReleaseApplicationSourceType
	SourceValue   string
}

type ReleasePlanInput struct {
	Name         string
	Version      string
	Description  string
	Status       model.ReleasePlanStatus
	Applications []ReleaseApplicationInput
}

type ReleaseGroupInput struct {
	Name              string
	Mode              model.ReleaseGroupMode
	FailurePolicy     model.ReleaseGroupFailurePolicy
	ApplicationIDs    []string
	Applications      []ReleaseApplicationInput
	DependsOnGroupIDs []string
}

func (s *Service) ListReleasePlans(ctx context.Context) ([]model.ReleasePlan, error) {
	var plans []model.ReleasePlan
	err := releasePlanQuery(s.db.WithContext(ctx)).Order("created_at DESC").Find(&plans).Error
	if err != nil {
		return nil, fmt.Errorf("查询发布计划失败: %w", err)
	}
	return plans, nil
}

func (s *Service) FindReleasePlan(ctx context.Context, id string) (*model.ReleasePlan, error) {
	var plan model.ReleasePlan
	if err := releasePlanQuery(s.db.WithContext(ctx)).First(&plan, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReleasePlanNotFound
		}
		return nil, fmt.Errorf("读取发布计划失败: %w", err)
	}
	return &plan, nil
}

func releasePlanQuery(db *gorm.DB) *gorm.DB {
	return db.Preload("Groups", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Groups.Applications", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Preload("Groups.Applications.Application").Preload("Groups.Dependencies")
}

func (s *Service) CreateReleasePlan(ctx context.Context, actorID string, input ReleasePlanInput) (*model.ReleasePlan, error) {
	planID := uuid.NewString()
	if strings.TrimSpace(input.Name) == "" {
		input.Name = "发布计划-" + planID[:8]
	}
	if strings.TrimSpace(input.Version) == "" {
		input.Version = "plan-" + planID
	}
	input, err := normalizeReleasePlanInput(input, true)
	if err != nil {
		return nil, err
	}
	input.Applications, err = s.normalizeReleaseApplications(ctx, input.Applications)
	if err != nil {
		if errors.Is(err, errInvalidReleaseApplications) {
			return nil, ErrInvalidReleasePlan
		}
		return nil, fmt.Errorf("检查发布计划应用失败: %w", err)
	}
	now := time.Now().UTC()
	plan := &model.ReleasePlan{
		ID: planID, Name: input.Name, Version: input.Version, Description: input.Description,
		Status: input.Status, CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(plan).Error; err != nil {
			return err
		}
		group := model.ReleaseGroup{
			ID: uuid.NewString(), ReleasePlanID: plan.ID, Name: "默认发布组",
			Mode: model.ReleaseGroupParallel, FailurePolicy: model.ReleaseGroupStopOnFailure,
			SortOrder: 0, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&group).Error; err != nil {
			return err
		}
		applications := releaseGroupApplicationModels(group.ID, input.Applications, now)
		return tx.Create(&applications).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrReleasePlanExists
		}
		return nil, fmt.Errorf("创建发布计划失败: %w", err)
	}
	return s.FindReleasePlan(ctx, plan.ID)
}

func (s *Service) UpdateReleasePlan(ctx context.Context, id, actorID string, input ReleasePlanInput) (*model.ReleasePlan, error) {
	input, err := normalizeReleasePlanInput(input, false)
	if err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.ReleasePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReleasePlanNotFound
			}
			return err
		}
		if plan.Status == model.ReleasePlanCompleted || plan.Status == model.ReleasePlanCanceled {
			return ErrReleasePlanNotEditable
		}
		return tx.Model(&plan).Updates(map[string]any{
			"name": input.Name, "version": input.Version, "description": input.Description,
			"status": input.Status, "updated_by": actorID, "updated_at": time.Now().UTC(),
		}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrReleasePlanExists
		}
		if errors.Is(err, ErrReleasePlanNotFound) || errors.Is(err, ErrReleasePlanNotEditable) {
			return nil, err
		}
		return nil, fmt.Errorf("更新发布计划失败: %w", err)
	}
	return s.FindReleasePlan(ctx, id)
}

func normalizeReleasePlanInput(input ReleasePlanInput, creating bool) (ReleasePlanInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Version = strings.TrimSpace(input.Version)
	input.Description = strings.TrimSpace(input.Description)
	if input.Status == "" {
		input.Status = model.ReleasePlanDraft
	}
	validStatus := input.Status == model.ReleasePlanDraft || input.Status == model.ReleasePlanActive
	if creating && input.Status != model.ReleasePlanDraft {
		validStatus = false
	}
	if !validResourceName(input.Name) || !releaseVersionPattern.MatchString(input.Version) ||
		(creating && input.Description == "") ||
		utf8.RuneCountInString(input.Description) > 500 || !validStatus {
		return input, ErrInvalidReleasePlan
	}
	return input, nil
}

func (s *Service) DeleteReleasePlan(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.ReleasePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReleasePlanNotFound
			}
			return fmt.Errorf("读取待删除发布计划失败: %w", err)
		}
		if plan.Status != model.ReleasePlanDraft {
			return ErrReleasePlanNotEditable
		}
		var groupIDs []string
		if err := tx.Model(&model.ReleaseGroup{}).Where("release_plan_id = ?", id).Pluck("id", &groupIDs).Error; err != nil {
			return err
		}
		if len(groupIDs) > 0 {
			if err := tx.Where("release_group_id IN ? OR depends_on_group_id IN ?", groupIDs, groupIDs).Delete(&model.ReleaseGroupDependency{}).Error; err != nil {
				return err
			}
			if err := tx.Where("release_group_id IN ?", groupIDs).Delete(&model.ReleaseGroupApplication{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", groupIDs).Delete(&model.ReleaseGroup{}).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&plan).Error
	})
}

func (s *Service) CreateReleaseGroup(ctx context.Context, planID string, input ReleaseGroupInput) (*model.ReleasePlan, error) {
	return s.saveReleaseGroup(ctx, planID, "", input)
}

func (s *Service) UpdateReleaseGroup(ctx context.Context, planID, groupID string, input ReleaseGroupInput) (*model.ReleasePlan, error) {
	return s.saveReleaseGroup(ctx, planID, groupID, input)
}

func (s *Service) saveReleaseGroup(ctx context.Context, planID, groupID string, input ReleaseGroupInput) (*model.ReleasePlan, error) {
	input, err := s.normalizeReleaseGroupInput(ctx, planID, groupID, input)
	if err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.ReleasePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", planID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReleasePlanNotFound
			}
			return err
		}
		if plan.Status == model.ReleasePlanCompleted || plan.Status == model.ReleasePlanCanceled {
			return ErrReleasePlanNotEditable
		}
		now := time.Now().UTC()
		group := model.ReleaseGroup{
			ID: groupID, ReleasePlanID: planID, Name: input.Name, Mode: input.Mode,
			FailurePolicy: input.FailurePolicy, UpdatedAt: now,
		}
		if groupID == "" {
			group.ID, group.CreatedAt = uuid.NewString(), now
			var maxOrder int
			if err := tx.Model(&model.ReleaseGroup{}).Where("release_plan_id = ?", planID).
				Select("COALESCE(MAX(sort_order), -1)").Scan(&maxOrder).Error; err != nil {
				return err
			}
			group.SortOrder = maxOrder + 1
			if err := tx.Create(&group).Error; err != nil {
				return err
			}
		} else {
			var existing model.ReleaseGroup
			if err := tx.First(&existing, "id = ? AND release_plan_id = ?", groupID, planID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrReleaseGroupNotFound
				}
				return err
			}
			group.SortOrder, group.CreatedAt = existing.SortOrder, existing.CreatedAt
			if err := tx.Model(&existing).Updates(map[string]any{
				"name": group.Name, "mode": group.Mode, "failure_policy": group.FailurePolicy, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if err := tx.Where("release_group_id = ?", group.ID).Delete(&model.ReleaseGroupApplication{}).Error; err != nil {
				return err
			}
			if err := tx.Where("release_group_id = ?", group.ID).Delete(&model.ReleaseGroupDependency{}).Error; err != nil {
				return err
			}
		}
		applications := releaseGroupApplicationModels(group.ID, input.Applications, now)
		if err := tx.Create(&applications).Error; err != nil {
			return err
		}
		dependencies := make([]model.ReleaseGroupDependency, 0, len(input.DependsOnGroupIDs))
		for _, dependencyID := range input.DependsOnGroupIDs {
			dependencies = append(dependencies, model.ReleaseGroupDependency{ReleaseGroupID: group.ID, DependsOnGroupID: dependencyID})
		}
		if len(dependencies) > 0 {
			if err := tx.Create(&dependencies).Error; err != nil {
				return err
			}
		}
		cyclic, err := releaseGroupDependenciesCyclic(tx, planID)
		if err != nil {
			return err
		}
		if cyclic {
			return ErrReleaseGroupDependency
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrDuplicatedKey):
			return nil, ErrReleaseGroupExists
		case errors.Is(err, ErrReleasePlanNotFound), errors.Is(err, ErrReleasePlanNotEditable),
			errors.Is(err, ErrReleaseGroupNotFound), errors.Is(err, ErrReleaseGroupDependency):
			return nil, err
		default:
			return nil, fmt.Errorf("保存发布组失败: %w", err)
		}
	}
	return s.FindReleasePlan(ctx, planID)
}

func (s *Service) normalizeReleaseGroupInput(ctx context.Context, planID, groupID string, input ReleaseGroupInput) (ReleaseGroupInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Mode == "" {
		input.Mode = model.ReleaseGroupParallel
	}
	if input.FailurePolicy == "" {
		input.FailurePolicy = model.ReleaseGroupStopOnFailure
	}
	if !validResourceName(input.Name) ||
		(input.Mode != model.ReleaseGroupParallel && input.Mode != model.ReleaseGroupSequential) ||
		(input.FailurePolicy != model.ReleaseGroupStopOnFailure && input.FailurePolicy != model.ReleaseGroupContinue) {
		return input, ErrInvalidReleaseGroup
	}
	if len(input.Applications) == 0 {
		input.ApplicationIDs = uniqueTrimmedIDs(input.ApplicationIDs)
		input.Applications = make([]ReleaseApplicationInput, 0, len(input.ApplicationIDs))
		for _, applicationID := range input.ApplicationIDs {
			input.Applications = append(input.Applications, ReleaseApplicationInput{ApplicationID: applicationID})
		}
	}
	var err error
	input.Applications, err = s.normalizeReleaseApplications(ctx, input.Applications)
	if err != nil {
		if errors.Is(err, errInvalidReleaseApplications) {
			return input, ErrInvalidReleaseGroup
		}
		return input, err
	}
	input.ApplicationIDs = make([]string, 0, len(input.Applications))
	for _, application := range input.Applications {
		input.ApplicationIDs = append(input.ApplicationIDs, application.ApplicationID)
	}
	input.DependsOnGroupIDs = uniqueTrimmedIDs(input.DependsOnGroupIDs)
	if len(input.DependsOnGroupIDs) > 0 {
		for _, dependencyID := range input.DependsOnGroupIDs {
			if dependencyID == groupID {
				return input, ErrReleaseGroupDependency
			}
		}
		var dependencyCount int64
		if err := s.db.WithContext(ctx).Model(&model.ReleaseGroup{}).
			Where("release_plan_id = ? AND id IN ?", planID, input.DependsOnGroupIDs).Count(&dependencyCount).Error; err != nil {
			return input, fmt.Errorf("检查发布组依赖失败: %w", err)
		}
		if dependencyCount != int64(len(input.DependsOnGroupIDs)) {
			return input, ErrInvalidReleaseGroup
		}
	}
	return input, nil
}

func (s *Service) normalizeReleaseApplications(ctx context.Context, inputs []ReleaseApplicationInput) ([]ReleaseApplicationInput, error) {
	if len(inputs) == 0 || len(inputs) > 50 {
		return nil, errInvalidReleaseApplications
	}
	seen := make(map[string]struct{}, len(inputs))
	result := make([]ReleaseApplicationInput, 0, len(inputs))
	for _, input := range inputs {
		input.ApplicationID = strings.TrimSpace(input.ApplicationID)
		if input.ApplicationID == "" {
			return nil, errInvalidReleaseApplications
		}
		if _, exists := seen[input.ApplicationID]; exists {
			return nil, errInvalidReleaseApplications
		}
		seen[input.ApplicationID] = struct{}{}
		input.SourceValue = strings.TrimSpace(input.SourceValue)
		if !input.ManualDeploy {
			input.SourceType, input.SourceValue = "", ""
		} else {
			switch input.SourceType {
			case model.ReleaseApplicationSourceBranch:
				input.SourceValue = strings.TrimPrefix(input.SourceValue, "refs/heads/")
				if plumbing.NewBranchReferenceName(input.SourceValue).Validate() != nil {
					return nil, errInvalidReleaseApplications
				}
			case model.ReleaseApplicationSourceCommit:
				input.SourceValue = strings.ToLower(input.SourceValue)
				if !releaseCommitPattern.MatchString(input.SourceValue) {
					return nil, errInvalidReleaseApplications
				}
			default:
				return nil, errInvalidReleaseApplications
			}
		}
		result = append(result, input)
	}
	applicationIDs := make([]string, 0, len(result))
	for _, input := range result {
		applicationIDs = append(applicationIDs, input.ApplicationID)
	}
	var applicationCount int64
	if err := s.db.WithContext(ctx).Model(&model.Application{}).
		Where("id IN ? AND is_active = ?", applicationIDs, true).Count(&applicationCount).Error; err != nil {
		return nil, fmt.Errorf("检查发布组应用失败: %w", err)
	}
	if applicationCount != int64(len(applicationIDs)) {
		return nil, errInvalidReleaseApplications
	}
	return result, nil
}

func releaseGroupApplicationModels(groupID string, inputs []ReleaseApplicationInput, now time.Time) []model.ReleaseGroupApplication {
	applications := make([]model.ReleaseGroupApplication, 0, len(inputs))
	for i, input := range inputs {
		applications = append(applications, model.ReleaseGroupApplication{
			ID: uuid.NewString(), ReleaseGroupID: groupID, ApplicationID: input.ApplicationID,
			ManualDeploy: input.ManualDeploy, SourceType: input.SourceType, SourceValue: input.SourceValue,
			SortOrder: i, CreatedAt: now,
		})
	}
	return applications
}

func uniqueTrimmedIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func releaseGroupDependenciesCyclic(tx *gorm.DB, planID string) (bool, error) {
	var groups []model.ReleaseGroup
	if err := tx.Select("id").Where("release_plan_id = ?", planID).Find(&groups).Error; err != nil {
		return false, err
	}
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	var dependencies []model.ReleaseGroupDependency
	if len(ids) > 0 {
		if err := tx.Where("release_group_id IN ?", ids).Find(&dependencies).Error; err != nil {
			return false, err
		}
	}
	outgoing := make(map[string][]string, len(ids))
	indegree := make(map[string]int, len(ids))
	for _, id := range ids {
		indegree[id] = 0
	}
	for _, dependency := range dependencies {
		outgoing[dependency.DependsOnGroupID] = append(outgoing[dependency.DependsOnGroupID], dependency.ReleaseGroupID)
		indegree[dependency.ReleaseGroupID]++
	}
	queue := make([]string, 0, len(ids))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, target := range outgoing[id] {
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	return visited != len(ids), nil
}

func (s *Service) DeleteReleaseGroup(ctx context.Context, planID, groupID string) (*model.ReleasePlan, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.ReleasePlan
		if err := tx.First(&plan, "id = ?", planID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReleasePlanNotFound
			}
			return err
		}
		if plan.Status == model.ReleasePlanCompleted || plan.Status == model.ReleasePlanCanceled {
			return ErrReleasePlanNotEditable
		}
		var group model.ReleaseGroup
		if err := tx.First(&group, "id = ? AND release_plan_id = ?", groupID, planID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReleaseGroupNotFound
			}
			return err
		}
		if err := tx.Where("release_group_id = ? OR depends_on_group_id = ?", groupID, groupID).Delete(&model.ReleaseGroupDependency{}).Error; err != nil {
			return err
		}
		if err := tx.Where("release_group_id = ?", groupID).Delete(&model.ReleaseGroupApplication{}).Error; err != nil {
			return err
		}
		return tx.Delete(&group).Error
	})
	if err != nil {
		if errors.Is(err, ErrReleasePlanNotFound) || errors.Is(err, ErrReleasePlanNotEditable) || errors.Is(err, ErrReleaseGroupNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("删除发布组失败: %w", err)
	}
	return s.FindReleasePlan(ctx, planID)
}
