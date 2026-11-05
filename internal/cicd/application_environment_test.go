package cicd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"edo/internal/model"
)

func TestApplicationEnvironmentAutomaticScopeAndFrozenSecrets(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	repo := model.GitRepository{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Name: "环境配置仓库", CloneURL: "https://example.test/env.git"}
	if err := s.db.WithContext(ctx).Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: uuid.NewString(), RepositoryID: repo.ID, Name: "环境配置应用", ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID}
	other := model.Application{ID: uuid.NewString(), RepositoryID: repo.ID, Name: "其他应用", ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID}
	prod := model.Environment{ID: uuid.NewString(), Name: "生产", ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID}
	test := model.Environment{ID: uuid.NewString(), Name: "测试", ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID}
	for _, item := range []any{&app, &other, &prod, &test} {
		if err := s.db.WithContext(ctx).Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	auto := true
	input := VariableInput{Name: "APPLICATION_ENVIRONMENT", ApplicationID: app.ID, EnvironmentID: prod.ID, Usage: "deploy", Kind: "env_text", Value: pointer("TOKEN=first-secret"), IsSecret: true, IsActive: true, AutoDeploy: &auto}
	v, err := s.SaveVariable(ctx, "owner", "", input)
	if err != nil {
		t.Fatal(err)
	}
	shared := VariableInput{Name: "SHARED", Usage: "deploy", Kind: "env", Value: pointer("shared"), IsActive: true}
	if _, err = s.SaveVariable(ctx, "owner", "", shared); err != nil {
		t.Fatal(err)
	}
	legacy := shared
	legacy.Name, legacy.ApplicationID = "LEGACY", app.ID
	if _, err = s.SaveVariable(ctx, "owner", "", legacy); err != nil {
		t.Fatal(err)
	}
	bindings, err := s.FreezeDeploymentVariables(ctx, scope.ProjectID, app.ID, prod.ID, "refs/heads/main", nil)
	if err != nil || len(bindings) != 1 || bindings[0].VariableID != v.ID {
		t.Fatalf("应用自动配置未正确选择: %v %#v", err, bindings)
	}
	for _, target := range [][2]string{{app.ID, test.ID}, {other.ID, prod.ID}} {
		got, err := s.FreezeDeploymentVariables(ctx, scope.ProjectID, target[0], target[1], "refs/heads/main", nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("自动配置越过应用或环境范围: %v", err)
		}
	}
	explicit, err := s.FreezeDeploymentVariables(ctx, scope.ProjectID, app.ID, prod.ID, "refs/heads/main", []string{"SHARED"})
	if err != nil || len(explicit) != 2 {
		t.Fatal("共享变量显式引用失败", err)
	}
	input.Value, input.ExpectedVersion = pointer("TOKEN=second-secret"), v.Version
	input.AutoDeploy = nil
	updated, err := s.SaveVariable(ctx, "owner", v.ID, input)
	if err != nil || !updated.AutoDeploy {
		t.Fatal(err)
	}
	values, _, _, err := s.ResolveVariables(ctx, scope.ProjectID, "refs/heads/main", bindings)
	if err != nil || values["TOKEN"] != "first-secret" {
		t.Fatal("旧快照没有保持版本", err)
	}
	encoded, _ := json.Marshal(bindings)
	if strings.Contains(string(encoded), "first-secret") {
		t.Fatal("快照包含明文")
	}
	input.Value, input.ExpectedVersion, input.IsActive = nil, 2, false
	if _, err = s.SaveVariable(ctx, "owner", v.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.ResolveVariables(ctx, scope.ProjectID, "refs/heads/main", bindings); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatal("停用后旧快照仍能解密执行")
	}
	shared.AutoDeploy = &auto
	if _, err = s.SaveVariable(ctx, "owner", "", shared); !errors.Is(err, ErrInvalidVariable) {
		t.Fatal("共享变量被自动注入所有应用")
	}
}
