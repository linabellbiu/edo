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
	ErrReleasePlanNotEditable     = errors.New("发布计划正在执行，暂时不能修改")
	ErrReleasePlanDisabled        = errors.New("发布计划已停用，不能执行")
	ErrInvalidReleaseGroup        = errors.New("发布组配置无效")
	ErrReleaseGroupExists         = errors.New("发布组名称已存在")
	ErrReleaseGroupNotFound       = errors.New("发布组不存在")
	ErrReleaseGroupDependency     = errors.New("发布组依赖不能形成循环")
	ErrReleaseApplicationAssigned = errors.New("应用已属于当前计划的其他发布组")
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

type ReleasePlanConfigurationInput struct {
	Description string
	Groups      []ReleaseGroupConfigurationInput
}

type ReleaseGroupConfigurationInput struct {
	ID                string
	Name              string
	Mode              model.ReleaseGroupMode
	FailurePolicy     model.ReleaseGroupFailurePolicy
	Applications      []ReleaseApplicationInput
	DependsOnGroupIDs []string
}

func (s *Service) ListReleasePlans(ctx context.Context) ([]model.ReleasePlan, error) {
	var plans []model.ReleasePlan
	err := releasePlanQuery(s.db.WithContext(ctx)).Order("created_at DESC").Find(&plans).Error
	if err != nil {
		return nil, fmt.Errorf("查询发布计划失败: %w", err)
	}
	if err := s.attachLatestReleasePlanExecutions(ctx, plans); err != nil {
		return nil, err
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
	plans := []model.ReleasePlan{plan}
	if err := s.attachLatestReleasePlanExecutions(ctx, plans); err != nil {
		return nil, err
	}
	plan = plans[0]
	return &plan, nil
}

func (s *Service) attachLatestReleasePlanExecutions(ctx context.Context, plans []model.ReleasePlan) error {
	if len(plans) == 0 {
		return nil
	}
	planIDs := make([]string, 0, len(plans))
	for i := range plans {
		planIDs = append(planIDs, plans[i].ID)
	}
	var executions []model.ReleasePlanExecution
	if err := s.db.WithContext(ctx).
		Select("id", "release_plan_id", "request_id", "status", "created_by", "started_at", "finished_at", "created_at", "updated_at").
		Where("release_plan_id IN ?", planIDs).Order("created_at DESC").Find(&executions).Error; err != nil {
		return fmt.Errorf("查询发布计划最近执行失败: %w", err)
	}
	latest := make(map[string]*model.ReleasePlanExecution, len(executions))
	for i := range executions {
		if latest[executions[i].ReleasePlanID] == nil {
			latest[executions[i].ReleasePlanID] = &executions[i]
		}
	}
	for i := range plans {
		plans[i].LatestExecution = latest[plans[i].ID]
	}
	return nil
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
		Status: input.Status, IsActive: true, CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.ReleasePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReleasePlanNotFound
			}
			return err
		}
		if err := ensureReleasePlanNotRunning(tx, plan.ID); err != nil {
			return err
		}
		// Name 和 Version 是服务端生成的内部标识；界面更新说明时不会回传，必须保留原值。
		if strings.TrimSpace(input.Name) == "" {
			input.Name = plan.Name
		}
		if strings.TrimSpace(input.Version) == "" {
			input.Version = plan.Version
		}
		if input.Status == "" {
			input.Status = plan.Status
		}
		normalized, normalizeErr := normalizeReleasePlanInput(input, false)
		if normalizeErr != nil {
			return normalizeErr
		}
		// 生命周期由执行器推进；保留 draft -> active 仅供已有内部调用兼容。
		if normalized.Status != plan.Status && !(plan.Status == model.ReleasePlanDraft && normalized.Status == model.ReleasePlanActive) {
			return ErrInvalidReleasePlan
		}
		return tx.Model(&plan).Updates(map[string]any{
			"name": normalized.Name, "version": normalized.Version, "description": normalized.Description,
			"status": normalized.Status, "updated_by": actorID, "updated_at": time.Now().UTC(),
		}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrReleasePlanExists
		}
		if errors.Is(err, ErrReleasePlanNotFound) || errors.Is(err, ErrReleasePlanNotEditable) || errors.Is(err, ErrInvalidReleasePlan) {
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
	validStatus := input.Status == model.ReleasePlanDraft || input.Status == model.ReleasePlanActive ||
		input.Status == model.ReleasePlanCompleted || input.Status == model.ReleasePlanCanceled
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

// SetReleasePlanActive 只控制后续是否允许创建执行，不改变发布计划及历史执行的生命周期状态。
func (s *Service) SetReleasePlanActive(ctx context.Context, id, actorID string, active bool) (*model.ReleasePlan, error) {
	result := s.db.WithContext(ctx).Model(&model.ReleasePlan{}).Where("id = ?", id).Updates(map[string]any{
		"is_active": active, "updated_by": actorID, "updated_at": time.Now().UTC(),
	})
	if result.Error != nil {
		return nil, fmt.Errorf("更新发布计划启用状态失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, ErrReleasePlanNotFound
	}
	return s.FindReleasePlan(ctx, id)
}

// SaveReleasePlanConfiguration 原子替换计划说明和完整发布组结构。
// 运行中的执行只读取启动快照，但仍禁止并发改写配置，避免操作者误判当前运行所用版本。
func (s *Service) SaveReleasePlanConfiguration(
	ctx context.Context,
	id, actorID string,
	input ReleasePlanConfigurationInput,
) (*model.ReleasePlan, error) {
	input.Description = strings.TrimSpace(input.Description)
	if utf8.RuneCountInString(input.Description) > 500 || len(input.Groups) > 50 {
		return nil, ErrInvalidReleasePlan
	}
	seenNames := make(map[string]struct{}, len(input.Groups))
	seenApplications := make(map[string]struct{})
	for index := range input.Groups {
		group := &input.Groups[index]
		group.ID = strings.TrimSpace(group.ID)
		group.Name = strings.TrimSpace(group.Name)
		if group.ID != "" && len(group.ID) > 36 {
			return nil, ErrInvalidReleaseGroup
		}
		if group.Mode == "" {
			group.Mode = model.ReleaseGroupParallel
		}
		if group.FailurePolicy == "" {
			group.FailurePolicy = model.ReleaseGroupStopOnFailure
		}
		if !validResourceName(group.Name) ||
			(group.Mode != model.ReleaseGroupParallel && group.Mode != model.ReleaseGroupSequential) ||
			(group.FailurePolicy != model.ReleaseGroupStopOnFailure && group.FailurePolicy != model.ReleaseGroupContinue) {
			return nil, ErrInvalidReleaseGroup
		}
		if _, duplicate := seenNames[group.Name]; duplicate {
			return nil, ErrReleaseGroupExists
		}
		seenNames[group.Name] = struct{}{}
		if len(group.Applications) > 0 {
			applications, err := s.normalizeReleaseApplications(ctx, group.Applications)
			if err != nil {
				if errors.Is(err, errInvalidReleaseApplications) {
					return nil, ErrInvalidReleaseGroup
				}
				return nil, err
			}
			group.Applications = applications
		}
		for _, application := range group.Applications {
			if _, duplicate := seenApplications[application.ApplicationID]; duplicate {
				return nil, ErrReleaseApplicationAssigned
			}
			seenApplications[application.ApplicationID] = struct{}{}
		}
		group.DependsOnGroupIDs = uniqueTrimmedIDs(group.DependsOnGroupIDs)
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.ReleasePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReleasePlanNotFound
			}
			return err
		}
		if err := ensureReleasePlanNotRunning(tx, plan.ID); err != nil {
			return err
		}
		var existingGroups []model.ReleaseGroup
		if err := tx.Where("release_plan_id = ?", plan.ID).Find(&existingGroups).Error; err != nil {
			return err
		}
		existingByID := make(map[string]model.ReleaseGroup, len(existingGroups))
		existingIDs := make([]string, 0, len(existingGroups))
		for _, group := range existingGroups {
			existingByID[group.ID] = group
			existingIDs = append(existingIDs, group.ID)
		}
		finalIDs := make(map[string]struct{}, len(input.Groups))
		for index := range input.Groups {
			group := &input.Groups[index]
			if group.ID == "" {
				group.ID = uuid.NewString()
			} else if _, exists := existingByID[group.ID]; !exists {
				return ErrReleaseGroupNotFound
			}
			if _, duplicate := finalIDs[group.ID]; duplicate {
				return ErrInvalidReleaseGroup
			}
			finalIDs[group.ID] = struct{}{}
		}
		for index := range input.Groups {
			for _, dependencyID := range input.Groups[index].DependsOnGroupIDs {
				if dependencyID == input.Groups[index].ID {
					return ErrReleaseGroupDependency
				}
				if _, exists := finalIDs[dependencyID]; !exists {
					return ErrInvalidReleaseGroup
				}
			}
		}

		if len(existingIDs) > 0 {
			if err := tx.Where("release_group_id IN ? OR depends_on_group_id IN ?", existingIDs, existingIDs).
				Delete(&model.ReleaseGroupDependency{}).Error; err != nil {
				return err
			}
			if err := tx.Where("release_group_id IN ?", existingIDs).Delete(&model.ReleaseGroupApplication{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", existingIDs).Delete(&model.ReleaseGroup{}).Error; err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		groups := make([]model.ReleaseGroup, 0, len(input.Groups))
		applications := make([]model.ReleaseGroupApplication, 0, len(seenApplications))
		dependencies := make([]model.ReleaseGroupDependency, 0)
		for index, groupInput := range input.Groups {
			createdAt := now
			if existing, exists := existingByID[groupInput.ID]; exists {
				createdAt = existing.CreatedAt
			}
			groups = append(groups, model.ReleaseGroup{
				ID: groupInput.ID, ReleasePlanID: plan.ID, Name: groupInput.Name,
				Mode: groupInput.Mode, FailurePolicy: groupInput.FailurePolicy, SortOrder: index,
				CreatedAt: createdAt, UpdatedAt: now,
			})
			applications = append(applications, releaseGroupApplicationModels(groupInput.ID, groupInput.Applications, now)...)
			for _, dependencyID := range groupInput.DependsOnGroupIDs {
				dependencies = append(dependencies, model.ReleaseGroupDependency{
					ReleaseGroupID: groupInput.ID, DependsOnGroupID: dependencyID,
				})
			}
		}
		if len(groups) > 0 {
			if err := tx.Create(&groups).Error; err != nil {
				return err
			}
		}
		if len(applications) > 0 {
			if err := tx.Create(&applications).Error; err != nil {
				return err
			}
		}
		if len(dependencies) > 0 {
			if err := tx.Create(&dependencies).Error; err != nil {
				return err
			}
		}
		cyclic, err := releaseGroupDependenciesCyclic(tx, plan.ID)
		if err != nil {
			return err
		}
		if cyclic {
			return ErrReleaseGroupDependency
		}
		return tx.Model(&plan).Updates(map[string]any{
			"description": input.Description, "updated_by": actorID, "updated_at": now,
		}).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrDuplicatedKey):
			return nil, ErrReleaseGroupExists
		case errors.Is(err, ErrReleasePlanNotFound), errors.Is(err, ErrReleasePlanNotEditable),
			errors.Is(err, ErrReleaseGroupNotFound), errors.Is(err, ErrReleaseGroupDependency),
			errors.Is(err, ErrReleaseApplicationAssigned), errors.Is(err, ErrInvalidReleaseGroup):
			return nil, err
		default:
			return nil, fmt.Errorf("保存发布计划配置失败: %w", err)
		}
	}
	return s.FindReleasePlan(ctx, id)
}

func ensureReleasePlanNotRunning(tx *gorm.DB, planID string) error {
	var count int64
	if err := tx.Model(&model.ReleasePlanExecution{}).
		Where("release_plan_id = ? AND status IN ?", planID, []model.ReleasePlanExecutionStatus{
			model.ReleasePlanExecutionPending, model.ReleasePlanExecutionRunning,
		}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrReleasePlanNotEditable
	}
	return nil
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
		if err := ensureReleasePlanNotRunning(tx, plan.ID); err != nil {
			return err
		}
		// 只软删除计划本身；发布组、应用编排和历史执行继续保留，供审计与运行快照查询。
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
		if err := ensureReleasePlanNotRunning(tx, plan.ID); err != nil {
			return err
		}
		if err := ensureReleaseApplicationsUnassigned(tx, planID, groupID, input.Applications); err != nil {
			return err
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
		if len(applications) > 0 {
			if err := tx.Create(&applications).Error; err != nil {
				return err
			}
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
		return tx.Model(&model.ReleasePlan{}).Where("id = ?", plan.ID).
			Update("updated_at", now).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrDuplicatedKey):
			return nil, ErrReleaseGroupExists
		case errors.Is(err, ErrReleasePlanNotFound), errors.Is(err, ErrReleasePlanNotEditable),
			errors.Is(err, ErrReleaseGroupNotFound), errors.Is(err, ErrReleaseGroupDependency),
			errors.Is(err, ErrReleaseApplicationAssigned):
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
	if len(input.Applications) == 0 && len(input.ApplicationIDs) > 0 {
		input.ApplicationIDs = uniqueTrimmedIDs(input.ApplicationIDs)
		input.Applications = make([]ReleaseApplicationInput, 0, len(input.ApplicationIDs))
		for _, applicationID := range input.ApplicationIDs {
			input.Applications = append(input.Applications, ReleaseApplicationInput{ApplicationID: applicationID})
		}
	}
	if len(input.Applications) > 0 {
		var err error
		input.Applications, err = s.normalizeReleaseApplications(ctx, input.Applications)
		if err != nil {
			if errors.Is(err, errInvalidReleaseApplications) {
				return input, ErrInvalidReleaseGroup
			}
			return input, err
		}
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

func ensureReleaseApplicationsUnassigned(tx *gorm.DB, planID, groupID string, applications []ReleaseApplicationInput) error {
	if len(applications) == 0 {
		return nil
	}
	applicationIDs := make([]string, 0, len(applications))
	for _, application := range applications {
		applicationIDs = append(applicationIDs, application.ApplicationID)
	}
	query := tx.Table("release_group_applications AS group_application").
		Joins("JOIN release_groups AS release_group ON release_group.id = group_application.release_group_id").
		Where("release_group.release_plan_id = ? AND group_application.application_id IN ?", planID, applicationIDs)
	if groupID != "" {
		query = query.Where("group_application.release_group_id <> ?", groupID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrReleaseApplicationAssigned
	}
	return nil
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
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", planID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReleasePlanNotFound
			}
			return err
		}
		if err := ensureReleasePlanNotRunning(tx, plan.ID); err != nil {
			return err
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
		if err := tx.Delete(&group).Error; err != nil {
			return err
		}
		return tx.Model(&model.ReleasePlan{}).Where("id = ?", plan.ID).
			Update("updated_at", time.Now().UTC()).Error
	})
	if err != nil {
		if errors.Is(err, ErrReleasePlanNotFound) || errors.Is(err, ErrReleasePlanNotEditable) || errors.Is(err, ErrReleaseGroupNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("删除发布组失败: %w", err)
	}
	return s.FindReleasePlan(ctx, planID)
}
