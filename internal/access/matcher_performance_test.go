package access

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/casbin/casbin/v3"

	"edo/internal/database"
	"edo/internal/model"
)

const roleFirstAuthorizationMatcher = `(r.sub == p.sub || (g(r.sub, p.sub, "") && p.scope == "platform") || (r.project != "" && g(r.sub, p.sub, r.project))) && r.perm == p.perm`

func TestAuthorizationMatcherPermissionFirstPreservesBoundaries(t *testing.T) {
	policies := [][]string{
		{"role:platform", PermissionUserRead, "allow", "platform"},
		{"role:platform", "application.create", "allow", "project"},
		{"project-role:reader", "application.read", "allow", "project"},
		{"user:denied", "application.read", "allow", "direct"},
		{"user:denied", "application.read", "deny", "direct"},
		{"user:exception", "application.create", "allow", "direct"},
		{"user:deny-other", PermissionUserDelete, "deny", "direct"},
	}
	for _, permission := range Catalog() {
		if ProjectPermission(permission.Code) {
			policies = append(policies, []string{"project-role:owner", permission.Code, "allow", "project"})
		}
	}
	groups := [][]string{
		{"user:platform", "role:platform", ""},
		{"user:reader", "project-role:reader", "project-a"},
		{"user:owner", "project-role:owner", "project-a"},
		{"user:owner", "project-role:reader", "project-b"},
		{"user:denied", "project-role:owner", "project-a"},
		{"user:deny-other", "role:platform", ""},
	}
	cases := []struct {
		name, user, permission, project string
		want                            bool
	}{
		{"平台管理权限", "platform", PermissionUserRead, "project-a", true},
		{"平台角色不能授予项目动作", "platform", "application.create", "project-a", false},
		{"平台角色不能授予默认项目动作", "platform", "application.create", database.DefaultProjectID("department-a"), false},
		{"项目只读权限", "reader", "application.read", "project-a", true},
		{"项目角色不能跨项目", "reader", "application.read", "project-b", false},
		{"无项目不能借用项目角色", "reader", "application.read", "", false},
		{"项目只读角色不能创建", "reader", "application.create", "project-a", false},
		{"项目所有者可以创建", "owner", "application.create", "project-a", true},
		{"同一用户不同项目角色", "owner", "application.create", "project-b", false},
		{"项目所有者不能管理平台用户", "owner", PermissionUserRead, "project-a", false},
		{"显式拒绝覆盖角色与个人允许", "denied", "application.read", "project-a", false},
		{"个人例外允许", "exception", "application.create", "project-a", true},
		{"其他动作的拒绝不影响当前动作", "deny-other", PermissionUserRead, "project-a", true},
		{"没有授权的用户", "unassigned", "application.read", "project-a", false},
		{"没有授权的动作", "reader", PermissionRepositorySecretRead, "project-a", false},
		{"未知权限仍被拒绝", "owner", "unknown.read", "project-a", false},
	}
	// 拒绝语义不能依赖策略的插入顺序。
	for _, reverse := range []bool{false, true} {
		ordered := slices.Clone(policies)
		if reverse {
			slices.Reverse(ordered)
		}
		before := matcherPerformanceService(t, roleFirstAuthorizationMatcher, ordered, groups)
		after := matcherPerformanceService(t, "", ordered, groups)
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/reverse_%t", tc.name, reverse), func(t *testing.T) {
				user := &model.User{ID: tc.user, DepartmentID: "department-a", IsActive: true}
				ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
					UserID: user.ID, DepartmentID: user.DepartmentID, ProjectID: tc.project,
				})
				oldAllowed, err := before.HasPermission(ctx, user, tc.permission)
				if err != nil || oldAllowed != tc.want {
					t.Fatalf("原规则与安全边界预期不一致: allowed=%t want=%t err=%v", oldAllowed, tc.want, err)
				}
				newAllowed, err := after.HasPermission(ctx, user, tc.permission)
				if err != nil || newAllowed != oldAllowed {
					t.Fatalf("调整比较顺序后权限结果改变: allowed=%t before=%t err=%v", newAllowed, oldAllowed, err)
				}
			})
		}
	}
}

func matcherPerformanceService(tb testing.TB, matcher string, policies, groups [][]string) *Service {
	tb.Helper()
	authorization, err := newAuthorizationModel()
	if err != nil {
		tb.Fatal(err)
	}
	if matcher != "" && !authorization.AddDef("m", "m", matcher) {
		tb.Fatal("设置基准权限表达式失败")
	}
	enforcer, err := casbin.NewSyncedEnforcer(authorization)
	if err != nil {
		tb.Fatal(err)
	}
	enforcer.EnableAutoSave(false)
	if _, err := enforcer.AddPolicies(policies); err != nil {
		tb.Fatal(err)
	}
	if _, err := enforcer.AddNamedGroupingPolicies("g", groups); err != nil {
		tb.Fatal(err)
	}
	return &Service{enforcer: enforcer}
}

func BenchmarkAuthorizationMatcher(b *testing.B) {
	codes := make([]string, 0, 50)
	for _, permission := range Catalog() {
		if ProjectPermission(permission.Code) {
			codes = append(codes, permission.Code)
			if len(codes) == 50 {
				break
			}
		}
	}
	for _, roleCount := range []int{10, 100} {
		policies := make([][]string, 0, roleCount*len(codes))
		for role := range roleCount {
			for _, permission := range codes {
				policies = append(policies, []string{fmt.Sprintf("project-role:role-%d", role), permission, "allow", "project"})
			}
		}
		groups := make([][]string, 0, 20)
		users := make([]*model.User, 0, 20)
		for user := range 20 {
			users = append(users, &model.User{ID: fmt.Sprintf("user-%d", user), DepartmentID: "department-a", IsActive: true})
			groups = append(groups, []string{userSubject(users[user].ID), "project-role:role-0", "project-a"})
		}
		for _, matcher := range []struct{ name, value string }{
			{"role_first", roleFirstAuthorizationMatcher}, {"permission_first", ""},
		} {
			b.Run(fmt.Sprintf("roles_%d/%s", roleCount, matcher.name), func(b *testing.B) {
				service := matcherPerformanceService(b, matcher.value, policies, groups)
				ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
					DepartmentID: "department-a", ProjectID: "project-a",
				})
				for _, user := range users {
					allowed, err := service.HasPermission(ctx, user, codes[0])
					if err != nil || !allowed {
						b.Fatalf("预热权限检查失败: allowed=%t err=%v", allowed, err)
					}
				}
				b.ReportAllocs()
				index := 0
				for b.Loop() {
					allowed, err := service.HasPermission(ctx, users[index%len(users)], codes[0])
					if err != nil || !allowed {
						b.Fatalf("权限检查失败: allowed=%t err=%v", allowed, err)
					}
					index++
				}
			})
		}
	}
}
