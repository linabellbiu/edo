package cicd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

type policyReviewerFixture struct {
	service     *Service
	ctx         context.Context
	project     model.Project
	other       model.Project
	environment model.Environment
	reviewer    model.User
	role        model.Role
	readerRole  model.Role
}

func newPolicyReviewerFixture(t *testing.T) policyReviewerFixture {
	t.Helper()
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	f := policyReviewerFixture{
		service: s, ctx: ctx,
		project:     model.Project{ID: scope.ProjectID, DepartmentID: scope.DepartmentID, Name: "审批授权项目", IsActive: true},
		other:       model.Project{ID: uuid.NewString(), DepartmentID: scope.DepartmentID, Name: "其他审批项目", IsActive: true},
		environment: model.Environment{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Name: "审批授权环境"},
		reviewer:    model.User{ID: uuid.NewString(), Username: "policy_reviewer", Nickname: "发布审批人", DepartmentID: scope.DepartmentID, IsActive: true},
		role:        model.Role{ID: uuid.NewString(), Name: "policy_reviewer", DisplayName: "发布审批人"},
		readerRole:  model.Role{ID: uuid.NewString(), Name: "policy_reader", DisplayName: "项目只读成员"},
	}
	for _, item := range []any{
		&f.project, &f.other, &f.environment, &f.reviewer, &f.role, &f.readerRole,
		&model.RolePermission{RoleID: f.role.ID, Permission: "deployment.review"},
		&model.RolePermission{RoleID: f.readerRole.ID, Permission: "deployment.read"},
		&model.ProjectMember{ProjectID: f.project.ID, UserID: f.reviewer.ID, RoleID: f.role.ID},
	} {
		if err := s.db.WithContext(ctx).Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f policyReviewerFixture) input() PolicyInput {
	return PolicyInput{AllowedRefs: []string{"refs/heads/main"}, ReviewerIDs: []string{f.reviewer.ID}, RequireApproval: true, MinimumApprovals: 1, IsActive: true}
}

func TestEnvironmentPolicyReviewerWithProjectReviewPermissionCanBeSelectedAndSaved(t *testing.T) {
	f := newPolicyReviewerFixture(t)
	choices, err := f.service.Reviewers(f.ctx)
	if err != nil || len(choices) != 1 || choices[0].ID != f.reviewer.ID {
		t.Fatalf("有项目审批权限的成员应可选择: choices=%+v err=%v", choices, err)
	}
	policy, err := f.service.SavePolicy(f.ctx, "variable-owner", f.environment.ID, f.input())
	if err != nil || policy == nil || !slices.Equal(policy.ReviewerIDs, []string{f.reviewer.ID}) || policy.Version != 1 {
		t.Fatalf("有项目审批权限的成员应能保存为审核人: policy=%+v err=%v", policy, err)
	}
}

func TestEnvironmentPolicyReviewerAllowsCurrentPersonalGrantAndSuperuserRecovery(t *testing.T) {
	for _, name := range []string{"personal_allow", "member_superuser", "nonmember_superuser", "cross_department_superuser"} {
		t.Run(name, func(t *testing.T) {
			f := newPolicyReviewerFixture(t)
			if err := f.service.db.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", f.project.ID, f.reviewer.ID).Update("role_id", f.readerRole.ID).Error; err != nil {
				t.Fatal(err)
			}
			if name == "personal_allow" {
				if err := f.service.db.Create(&model.UserPermission{UserID: f.reviewer.ID, Permission: "deployment.review", Effect: model.PermissionAllow}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.service.db.Model(&model.User{}).Where("uuid = ?", f.reviewer.ID).Update("is_superuser", true).Error; err != nil {
					t.Fatal(err)
				}
			}
			if name == "nonmember_superuser" {
				if err := f.service.db.Where("project_id = ? AND user_id = ?", f.project.ID, f.reviewer.ID).Delete(&model.ProjectMember{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if name == "cross_department_superuser" {
				department := model.Department{ID: uuid.NewString(), Name: "超级管理员部门", IsActive: true}
				if err := f.service.db.Create(&department).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.service.db.Model(&model.User{}).Where("uuid = ?", f.reviewer.ID).Update("department_id", department.ID).Error; err != nil {
					t.Fatal(err)
				}
				f.ctx = database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: f.project.ID, DepartmentID: f.project.DepartmentID, UserID: "variable-owner"})
			}
			choices, err := f.service.Reviewers(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			selected := slices.ContainsFunc(choices, func(choice ReviewerChoice) bool { return choice.ID == f.reviewer.ID })
			if selected != (name != "nonmember_superuser") {
				t.Errorf("审核人列表应保留原项目成员可见范围: choices=%+v", choices)
			}
			if _, err := f.service.SavePolicy(f.ctx, "variable-owner", f.environment.ID, f.input()); err != nil {
				t.Fatalf("当前个人授权或超级管理员恢复入口不应被拒绝: err=%v", err)
			}
		})
	}
}

func TestEnvironmentPolicyReviewerMustRetainCurrentProjectReviewPermission(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, policyReviewerFixture)
	}{
		{"role_permission_revoked", func(t *testing.T, f policyReviewerFixture) {
			t.Helper()
			if err := f.service.db.Where("role_id = ? AND permission = ?", f.role.ID, "deployment.review").Delete(&model.RolePermission{}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"personal_explicit_deny", func(t *testing.T, f policyReviewerFixture) {
			t.Helper()
			if err := f.service.db.Create(&model.UserPermission{UserID: f.reviewer.ID, Permission: "deployment.review", Effect: model.PermissionDeny}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"project_role_changed_to_reader", func(t *testing.T, f policyReviewerFixture) {
			t.Helper()
			if err := f.service.db.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", f.project.ID, f.reviewer.ID).Update("role_id", f.readerRole.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"membership_removed", func(t *testing.T, f policyReviewerFixture) {
			t.Helper()
			if err := f.service.db.Where("project_id = ? AND user_id = ?", f.project.ID, f.reviewer.ID).Delete(&model.ProjectMember{}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"review_permission_only_in_other_project", func(t *testing.T, f policyReviewerFixture) {
			t.Helper()
			if err := f.service.db.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", f.project.ID, f.reviewer.ID).Update("role_id", f.readerRole.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.service.db.Create(&model.ProjectMember{ProjectID: f.other.ID, UserID: f.reviewer.ID, RoleID: f.role.ID}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"review_permission_only_in_platform_role", func(t *testing.T, f policyReviewerFixture) {
			t.Helper()
			if err := f.service.db.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", f.project.ID, f.reviewer.ID).Update("role_id", f.readerRole.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.service.db.Create(&model.UserRole{UserID: f.reviewer.ID, RoleID: f.role.ID}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"reviewer_disabled", func(t *testing.T, f policyReviewerFixture) {
			t.Helper()
			if err := f.service.db.Model(&model.User{}).Where("uuid = ?", f.reviewer.ID).Update("is_active", false).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"reviewer_moved_to_other_department", func(t *testing.T, f policyReviewerFixture) {
			t.Helper()
			department := model.Department{ID: uuid.NewString(), Name: "其他审批部门", IsActive: true}
			if err := f.service.db.Create(&department).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.service.db.Model(&model.User{}).Where("uuid = ?", f.reviewer.ID).Update("department_id", department.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"project_disabled", func(t *testing.T, f policyReviewerFixture) {
			t.Helper()
			if err := f.service.db.Model(&model.Project{}).Where("uuid = ?", f.project.ID).Update("is_active", false).Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newPolicyReviewerFixture(t)
			original, err := f.service.SavePolicy(f.ctx, "variable-owner", f.environment.ID, f.input())
			if err != nil {
				t.Fatal(err)
			}
			test.change(t, f)

			choices, err := f.service.Reviewers(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(choices, func(choice ReviewerChoice) bool { return choice.ID == f.reviewer.ID }) {
				t.Errorf("已无当前项目审批权限的成员仍出现在审核人选项中: %+v", choices)
			}

			input := f.input()
			input.ExpectedVersion = original.Version
			input.AllowedRefs = []string{"refs/heads/release"}
			_, err = f.service.SavePolicy(f.ctx, "variable-owner", f.environment.ID, input)
			if !errors.Is(err, ErrInvalidPolicy) || err.Error() != ErrInvalidPolicy.Error() {
				t.Errorf("旧表单应以安全中文错误拒绝已失权审核人: err=%v", err)
			}
			var stored model.EnvironmentReleasePolicy
			if err := f.service.db.WithContext(f.ctx).First(&stored, "uuid = ?", original.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Version != original.Version || !slices.Equal(stored.AllowedRefs, original.AllowedRefs) || !slices.Equal(stored.ReviewerIDs, original.ReviewerIDs) {
				t.Errorf("被拒绝的审核人变更不得修改已保存保护规则: original=%+v stored=%+v", original, stored)
			}

			fresh := model.Environment{ID: uuid.NewString(), ProjectID: f.project.ID, DepartmentID: f.project.DepartmentID, Name: "新增审核环境"}
			if err := f.service.db.WithContext(f.ctx).Create(&fresh).Error; err != nil {
				t.Fatal(err)
			}
			_, err = f.service.SavePolicy(f.ctx, "variable-owner", fresh.ID, f.input())
			if !errors.Is(err, ErrInvalidPolicy) || err.Error() != ErrInvalidPolicy.Error() {
				t.Errorf("新配置应以安全中文错误拒绝已失权审核人: err=%v", err)
			}
			var count int64
			if err := f.service.db.WithContext(f.ctx).Model(&model.EnvironmentReleasePolicy{}).Where("environment_id = ?", fresh.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("无效审核人配置不应被创建: count=%d", count)
			}
		})
	}
}

func TestEnvironmentPolicyReviewerStorageFailureIsSafeAndPreservesPolicy(t *testing.T) {
	for _, operation := range []string{"list", "save"} {
		t.Run(operation, func(t *testing.T) {
			f := newPolicyReviewerFixture(t)
			original, err := f.service.SavePolicy(f.ctx, "variable-owner", f.environment.ID, f.input())
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			f.service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			storageErr := errors.New("role_permissions SELECT failed at private-database.internal")
			callback := "test:environment_reviewer_authorization_storage_error"
			if err := f.service.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "role_permissions" {
					tx.AddError(storageErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.service.db.Callback().Query().Remove(callback) })
			if operation == "list" {
				choices, listErr := f.service.Reviewers(f.ctx)
				err = listErr
				if choices != nil {
					t.Errorf("授权查询失败不得返回部分候选: choices=%+v", choices)
				}
			} else {
				input := f.input()
				input.ExpectedVersion = original.Version
				input.AllowedRefs = []string{"refs/heads/release"}
				_, err = f.service.SavePolicy(f.ctx, "variable-owner", f.environment.ID, input)
			}
			if !errors.Is(err, ErrStorage) || err.Error() != ErrStorage.Error() {
				t.Errorf("授权存储故障应返回稳定安全的中文提示: err=%v", err)
			}
			for _, expected := range []string{"environment_policy_reviewer_authorization", f.project.ID, f.reviewer.ID, storageErr.Error()} {
				if !strings.Contains(logs.String(), expected) {
					t.Errorf("授权诊断日志缺少必要上下文 %q: logs=%s", expected, logs.String())
				}
			}
			var stored model.EnvironmentReleasePolicy
			if err := f.service.db.WithContext(f.ctx).First(&stored, "uuid = ?", original.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Version != original.Version || !slices.Equal(stored.AllowedRefs, original.AllowedRefs) || !slices.Equal(stored.ReviewerIDs, original.ReviewerIDs) {
				t.Errorf("授权存储故障不得修改保护规则: original=%+v stored=%+v", original, stored)
			}
		})
	}
}

func TestEnvironmentPolicyReviewerAndEnvironmentRemainProjectScoped(t *testing.T) {
	f := newPolicyReviewerFixture(t)
	otherContext := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: f.other.ID, DepartmentID: f.other.DepartmentID, UserID: "variable-owner", AllDepartments: true})
	choices, err := f.service.Reviewers(otherContext)
	if err != nil || len(choices) != 0 {
		t.Fatalf("其他项目不能列出当前项目审核人: choices=%+v err=%v", choices, err)
	}
	otherEnvironment := model.Environment{ID: uuid.NewString(), ProjectID: f.other.ID, DepartmentID: f.other.DepartmentID, Name: "其他项目环境"}
	if err := f.service.db.WithContext(otherContext).Create(&otherEnvironment).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SavePolicy(f.ctx, "variable-owner", otherEnvironment.ID, f.input()); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("当前项目不得为其他项目环境保存保护: err=%v", err)
	}
	if _, err := f.service.SavePolicy(otherContext, "variable-owner", otherEnvironment.ID, f.input()); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("其他项目不得选择当前项目审核人: err=%v", err)
	}
}
