package cicd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/testutil"
)

func variableFixture(t *testing.T) (*Service, context.Context) {
	t.Helper()
	db := testutil.NewDatabase(t)
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("v", 32))))
	if err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, UserID: "variable-owner", AllDepartments: true})
	return New(db, manager, slog.New(slog.NewTextHandler(io.Discard, nil))), ctx
}

func pointer(v string) *string { return &v }

func TestSecretVariableFreezesVersionWithoutCopyingValue(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	input := VariableInput{Name: "DB_PASSWORD", Kind: "env", Usage: "deploy", Value: pointer("first-very-private-value"), IsSecret: true, IsActive: true, ProtectedRefs: []string{"refs/heads/main"}}
	v, err := s.SaveVariable(ctx, "owner", "", input)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := s.FreezeVariables(ctx, scope.ProjectID, "", "", "deploy", "refs/heads/main", []string{"DB_PASSWORD"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(bindings)
	if strings.Contains(string(encoded), *input.Value) {
		t.Fatal("运行引用泄漏密钥")
	}
	var revision model.DeliveryVariableRevision
	if err = s.db.WithContext(ctx).Where("variable_id = ?", v.ID).First(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if revision.Value != "" || revision.Ciphertext == "" || strings.Contains(revision.Ciphertext, *input.Value) {
		t.Fatal("密钥没有加密保存")
	}
	input.Value = pointer("second-very-private-value")
	input.ExpectedVersion = 1
	if _, err = s.SaveVariable(ctx, "owner", v.ID, input); err != nil {
		t.Fatal(err)
	}
	values, _, redactions, err := s.ResolveVariables(ctx, scope.ProjectID, "refs/heads/main", bindings)
	if err != nil || values["DB_PASSWORD"] != "first-very-private-value" || len(redactions) != 1 {
		t.Fatalf("运行没有使用启动时固定版本: %v", err)
	}
	views, err := s.ListVariables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(views)
	if strings.Contains(string(data), "private-value") || views[0].Value != nil {
		t.Fatal("变量接口回显密钥")
	}
	if _, err = s.FreezeVariables(ctx, scope.ProjectID, "", "", "deploy", "refs/heads/test", []string{"DB_PASSWORD"}); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatal("非允许版本获得保护变量")
	}
	if _, _, _, err = s.ResolveVariables(ctx, uuid.NewString(), "refs/heads/main", bindings); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatal("跨项目读取变量成功")
	}
}

func TestVariableScopePrecedenceAndRevocation(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	repository := model.GitRepository{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Name: "variable-repository", CloneURL: "https://example.com/repository.git"}
	if err := s.db.WithContext(ctx).Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: uuid.NewString(), RepositoryID: repository.ID, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Name: "scoped-variable-app", CreatedBy: "owner"}
	env := model.Environment{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Name: "自定义环境"}
	if err := s.db.WithContext(ctx).Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.WithContext(ctx).Create(&env).Error; err != nil {
		t.Fatal(err)
	}
	input := VariableInput{Name: "API_URL", Kind: "env", Usage: "deploy", Value: pointer("project-value"), IsActive: true}
	if _, err := s.SaveVariable(ctx, "owner", "", input); err != nil {
		t.Fatal(err)
	}
	input.ApplicationID = app.ID
	input.Value = pointer("application-value")
	if _, err := s.SaveVariable(ctx, "owner", "", input); err != nil {
		t.Fatal(err)
	}
	input.EnvironmentID = env.ID
	input.Value = pointer("environment-value")
	v, err := s.SaveVariable(ctx, "owner", "", input)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.FreezeVariables(ctx, scope.ProjectID, app.ID, env.ID, "deploy", "refs/heads/main", []string{"API_URL"})
	if err != nil {
		t.Fatal(err)
	}
	values, _, _, err := s.ResolveVariables(ctx, scope.ProjectID, "refs/heads/main", b)
	if err != nil || values["API_URL"] != "environment-value" {
		t.Fatal("变量覆盖顺序不正确")
	}
	if err = s.DeleteVariable(ctx, v.ID, v.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.ResolveVariables(ctx, scope.ProjectID, "refs/heads/main", b); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatal("已撤销的变量仍可注入")
	}
	other := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: uuid.NewString(), DepartmentID: scope.DepartmentID, AllDepartments: true})
	views, err := s.ListVariables(other)
	if err != nil || len(views) != 0 {
		t.Fatal("变量列表越过项目边界")
	}
}

func TestVariableUpdatesRequireVersionAndPreserveSecret(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	in := VariableInput{Name: "ACCESS_TOKEN", Kind: "file", Usage: "build", Value: pointer("file-token-value"), IsSecret: true, IsActive: true}
	v, err := s.SaveVariable(ctx, "owner", "", in)
	if err != nil {
		t.Fatal(err)
	}
	in.Value = nil
	in.ExpectedVersion = 1
	in.Description = "只改说明"
	updated, err := s.SaveVariable(ctx, "owner", v.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Value != nil {
		t.Fatal("修改密钥元数据失败")
	}
	if _, err = s.SaveVariable(ctx, "owner", v.ID, in); !errors.Is(err, ErrVariableConflict) {
		t.Fatal("并发修改没有被拒绝")
	}
	in.ExpectedVersion = 2
	in.IsSecret = false
	if _, err = s.SaveVariable(ctx, "owner", v.ID, in); !errors.Is(err, ErrInvalidVariable) {
		t.Fatal("密钥被降级为明文")
	}
	bindings, err := s.FreezeVariables(ctx, scope.ProjectID, "", "", "build", "refs/heads/main", []string{in.Name})
	if err != nil {
		t.Fatal(err)
	}
	values, files, redactions, err := s.ResolveVariables(ctx, scope.ProjectID, "refs/heads/main", bindings)
	if err != nil || len(values) != 0 || files[in.Name] != "file-token-value" || len(redactions) != 1 {
		t.Fatal("文件型密钥内容错误")
	}
	if _, err = s.ListVariables(context.Background()); !errors.Is(err, ErrScope) {
		t.Fatal("未选择项目仍可查询变量")
	}
}
