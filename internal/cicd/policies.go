package cicd

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"time"

	"edo/internal/access"
	"edo/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrInvalidPolicy = errors.New("环境保护配置无效，请检查分支、审核人与时间窗口")
var ErrReleaseProtected = errors.New("当前版本不满足环境保护规则，请查看环境设置")

type PolicyInput struct {
	AllowedRefs      []string `json:"allowed_refs"`
	ReviewerIDs      []string `json:"reviewer_ids"`
	RequireApproval  bool     `json:"require_approval"`
	MinimumApprovals int      `json:"minimum_approvals"`
	WindowStart      string   `json:"window_start"`
	WindowEnd        string   `json:"window_end"`
	IsActive         bool     `json:"is_active"`
	ExpectedVersion  uint64   `json:"expected_version"`
}

func (s *Service) ListPolicies(ctx context.Context) ([]model.EnvironmentReleasePolicy, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, s.failure(ctx, "environment_policy_scope", err, err)
	}
	items := []model.EnvironmentReleasePolicy{}
	err = s.db.WithContext(ctx).Where("project_id = ?", scope.ProjectID).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, s.failure(ctx, "environment_policy_list", err, ErrStorage)
	}
	return items, nil
}
func (s *Service) SavePolicy(ctx context.Context, actorID, environmentID string, in PolicyInput) (*model.EnvironmentReleasePolicy, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, s.failure(ctx, "environment_policy_scope", err, err)
	}
	if len(in.AllowedRefs) > 32 || len(in.ReviewerIDs) > 32 || in.MinimumApprovals < 1 || in.MinimumApprovals > 32 || (in.RequireApproval && in.MinimumApprovals > len(in.ReviewerIDs)) {
		return nil, s.failure(ctx, "environment_policy_validate", ErrInvalidPolicy, ErrInvalidPolicy)
	}
	for _, ref := range in.AllowedRefs {
		if !validRefPattern(ref) {
			return nil, s.failure(ctx, "environment_policy_ref", ErrInvalidPolicy, ErrInvalidPolicy)
		}
	}
	if (in.WindowStart == "") != (in.WindowEnd == "") {
		return nil, s.failure(ctx, "environment_policy_window", ErrInvalidPolicy, ErrInvalidPolicy)
	}
	for _, clock := range []string{in.WindowStart, in.WindowEnd} {
		if clock != "" {
			if parsed, e := time.Parse("15:04", clock); e != nil || parsed.Format("15:04") != clock {
				return nil, s.failure(ctx, "environment_policy_window", e, ErrInvalidPolicy)
			}
		}
	}
	item := model.EnvironmentReleasePolicy{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var env model.Environment
		if e := tx.First(&env, "uuid = ? AND project_id = ?", environmentID, scope.ProjectID).Error; e != nil {
			return ErrInvalidPolicy
		}
		seen := map[string]bool{}
		for _, id := range in.ReviewerIDs {
			if seen[id] {
				return ErrInvalidPolicy
			}
			seen[id] = true
			allowed, e := s.policyReviewerAllowed(ctx, tx, scope.ProjectID, id)
			if e != nil {
				return e
			}
			if !allowed {
				s.logger.WarnContext(ctx, "审核人已无当前项目审批权限", "operation", "environment_policy_reviewer_validate", "project_id", scope.ProjectID, "environment_id", environmentID, "reviewer_id", id, "err", access.ErrAccessDelegationDenied)
				return ErrInvalidPolicy
			}
		}
		e := tx.First(&item, "environment_id = ? AND project_id = ?", environmentID, scope.ProjectID).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			if in.ExpectedVersion != 0 {
				return ErrVariableConflict
			}
			item = model.EnvironmentReleasePolicy{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, EnvironmentID: environmentID, Version: 1}
		} else if e != nil {
			return e
		} else if item.Version != in.ExpectedVersion {
			return ErrVariableConflict
		} else {
			item.Version++
		}
		item.AllowedRefs, item.ReviewerIDs, item.RequireApproval = in.AllowedRefs, in.ReviewerIDs, in.RequireApproval
		item.MinimumApprovals, item.WindowStart, item.WindowEnd, item.IsActive, item.UpdatedBy = in.MinimumApprovals, in.WindowStart, in.WindowEnd, in.IsActive, actorID
		if item.RowID == 0 {
			return tx.Create(&item).Error
		}
		result := tx.Model(&model.EnvironmentReleasePolicy{}).Where("uuid = ? AND version = ?", item.ID, in.ExpectedVersion).Select("*").Omit("id", "uuid", "project_id", "department_id", "created_at").Updates(&item)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrVariableConflict
		}
		return nil
	})
	if err != nil {
		return nil, s.failure(ctx, "environment_policy_save", err, safeControlError(err, ErrInvalidPolicy))
	}
	return &item, nil
}
func (s *Service) Policy(ctx context.Context, projectID, environmentID, ref string, now time.Time) (*model.EnvironmentReleasePolicy, error) {
	var item model.EnvironmentReleasePolicy
	err := s.db.WithContext(ctx).Where("project_id = ? AND environment_id = ? AND is_active = ?", projectID, environmentID, true).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, s.failure(ctx, "environment_policy_read", err, ErrStorage)
	}
	if !refAllowed(item.AllowedRefs, ref) || !policyWindow(item, now) {
		return nil, s.failure(ctx, "environment_policy_check", ErrReleaseProtected, ErrReleaseProtected)
	}
	return &item, nil
}
func PolicyReviewer(policy model.EnvironmentReleasePolicy, actorID string) bool {
	return policy.RequireApproval && slices.Contains(policy.ReviewerIDs, actorID)
}
func policyWindow(p model.EnvironmentReleasePolicy, now time.Time) bool {
	if p.WindowStart == "" {
		return true
	}
	zone, _ := time.LoadLocation("Asia/Shanghai")
	clock := now.In(zone).Format("15:04")
	if p.WindowStart <= p.WindowEnd {
		return clock >= p.WindowStart && clock <= p.WindowEnd
	}
	return clock >= p.WindowStart || clock <= p.WindowEnd
}
func validRefPattern(ref string) bool {
	if len(ref) > 255 || (!strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/")) {
		return false
	}
	_, err := path.Match(ref, "refs/heads/main")
	return err == nil
}
func safeControlError(err, validation error) error {
	for _, known := range []error{validation, ErrVariableConflict, ErrReleaseProtected} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrStorage
}

type ReviewerChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Service) Reviewers(ctx context.Context) ([]ReviewerChoice, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, s.failure(ctx, "environment_policy_reviewer_scope", err, err)
	}
	items := []ReviewerChoice{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		candidates := []ReviewerChoice{}
		if e := tx.Table("users AS u").Select("DISTINCT u.uuid AS id, u.username AS name").Joins("JOIN project_members AS m ON m.user_id = u.uuid").Where("m.project_id = ? AND u.is_active = ?", scope.ProjectID, true).Order("u.username ASC").Scan(&candidates).Error; e != nil {
			return e
		}
		// 候选先按项目收敛，再复用当前授权算法；不能缓存已经失效的角色或个人例外。
		for _, candidate := range candidates {
			allowed, e := s.policyReviewerAllowed(ctx, tx, scope.ProjectID, candidate.ID)
			if e != nil {
				return e
			}
			if allowed {
				items = append(items, candidate)
			}
		}
		return nil
	})
	if err != nil {
		return nil, s.failure(ctx, "environment_policy_reviewers", err, ErrStorage)
	}
	return items, nil
}

func (s *Service) policyReviewerAllowed(ctx context.Context, tx *gorm.DB, projectID, reviewerID string) (bool, error) {
	authorization, err := access.FreshProjectPermissions(tx, ctx, reviewerID, projectID)
	if errors.Is(err, access.ErrAccessDelegationDenied) {
		return false, nil
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "读取环境审核人当前授权失败", "operation", "environment_policy_reviewer_authorization", "project_id", projectID, "reviewer_id", reviewerID, "err", err)
		return false, err
	}
	return authorization.Permissions[access.PermissionDeploymentReview], nil
}
