package cicd

import (
	"edo/internal/model"
	"errors"
	"testing"
	"time"
)

func TestEnvironmentPolicyIsExplicitScopedAndVersioned(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	env := model.Environment{ID: "policy-environment", Name: "环境保护测试", ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID}
	if err := s.db.WithContext(ctx).Create(&env).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 15, 30, 0, 0, time.UTC)
	if policy, err := s.Policy(ctx, scope.ProjectID, env.ID, "refs/heads/main", now); err != nil || policy != nil {
		t.Fatal("未配置的环境被隐式保护")
	}
	input := PolicyInput{AllowedRefs: []string{"refs/heads/main"}, MinimumApprovals: 1, WindowStart: "22:00", WindowEnd: "06:00", IsActive: true}
	policy, err := s.SavePolicy(ctx, "owner", env.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Policy(ctx, scope.ProjectID, env.ID, "refs/heads/main", now); err != nil {
		t.Fatal("跨午夜窗口未正确允许发布")
	}
	if _, err = s.Policy(ctx, scope.ProjectID, env.ID, "refs/heads/other", now); !errors.Is(err, ErrReleaseProtected) {
		t.Fatal("环境允许版本没有生效")
	}
	if _, err = s.Policy(ctx, scope.ProjectID, env.ID, "refs/heads/main", now.Add(-3*time.Hour)); !errors.Is(err, ErrReleaseProtected) {
		t.Fatal("窗口外发布没有阻止")
	}
	if _, err = s.SavePolicy(ctx, "owner", env.ID, input); !errors.Is(err, ErrVariableConflict) {
		t.Fatal("过期表单覆盖了环境规则")
	}
	input.ExpectedVersion = policy.Version
	input.IsActive = false
	if _, err = s.SavePolicy(ctx, "owner", env.ID, input); err != nil {
		t.Fatal(err)
	}
	if policy, err = s.Policy(ctx, scope.ProjectID, env.ID, "refs/heads/other", now); err != nil || policy != nil {
		t.Fatal("显式关闭未生效")
	}
	input.WindowStart = "9:00"
	if _, err = s.SavePolicy(ctx, "owner", env.ID, input); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("非规范时间被接受")
	}
}
func TestSQLAndSecurityToolsProduceFixedCommands(t *testing.T) {
	for _, tool := range []string{"sqlfluff", "trivy", "gitleaks"} {
		node := model.WorkflowNode{Type: model.WorkflowNodeShell, Config: model.WorkflowNodeConfig{Script: "arbitrary", Check: &model.WorkflowCheckConfig{Tool: tool, Dialect: "postgres", Path: "sql"}}}
		if err := PrepareCheck(&node); err != nil {
			t.Fatal(err)
		}
		if node.Config.Script == "arbitrary" || node.Config.RuntimeImage == "" || len(node.Config.Reports) != 1 || !node.Config.Reports[0].Required {
			t.Fatal("检查命令或门槛没有固定")
		}
	}
	node := model.WorkflowNode{Type: model.WorkflowNodeShell, Config: model.WorkflowNodeConfig{Check: &model.WorkflowCheckConfig{Tool: "sqlfluff", Dialect: "mysql", Path: "../outside"}}}
	if err := PrepareCheck(&node); !errors.Is(err, ErrInvalidCheck) {
		t.Fatal("检查路径越界被接受")
	}
}
