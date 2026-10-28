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

	"github.com/DATA-DOG/go-sqlmock"
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

func TestVariablesWithSameNameRemainIsolatedAcrossProjects(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	other := database.WithDepartmentScope(ctx, database.DepartmentScope{
		ProjectID: uuid.NewString(), DepartmentID: scope.DepartmentID, UserID: scope.UserID, AllDepartments: true,
	})
	input := VariableInput{Name: "PROJECT_TOKEN", Kind: "env", Usage: "deploy", Value: pointer("project-a-value"), IsActive: true}
	first, err := s.SaveVariable(ctx, "owner", "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Value = pointer("project-b-value")
	second, err := s.SaveVariable(other, "owner", "", input)
	if err != nil {
		t.Fatalf("不同项目无法配置同名变量: %v", err)
	}
	if first.ID == second.ID || first.ProjectID == second.ProjectID {
		t.Fatal("不同项目的变量没有独立身份")
	}
	for _, candidate := range []struct {
		ctx   context.Context
		id    string
		value string
	}{{ctx, first.ID, "project-a-value"}, {other, second.ID, "project-b-value"}} {
		views, listErr := s.ListVariables(candidate.ctx)
		if listErr != nil || len(views) != 1 || views[0].ID != candidate.id || views[0].Value == nil || *views[0].Value != candidate.value {
			t.Fatalf("变量列表没有按当前项目隔离: %v", listErr)
		}
	}
	input.ExpectedVersion = first.Version
	input.Value = pointer("cross-project-change")
	if _, err = s.SaveVariable(other, "owner", first.ID, input); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatalf("跨项目修改未被拒绝: %v", err)
	}
	if err = s.DeleteVariable(other, first.ID, first.Version); !errors.Is(err, ErrVariableConflict) {
		t.Fatalf("跨项目删除未被拒绝: %v", err)
	}
	views, err := s.ListVariables(ctx)
	if err != nil || len(views) != 1 || views[0].Version != first.Version || views[0].Value == nil || *views[0].Value != "project-a-value" || !views[0].IsActive {
		t.Fatalf("越权操作改变了其他项目的变量: %v", err)
	}
	var revisions int64
	if err = s.db.WithContext(ctx).Model(&model.DeliveryVariableRevision{}).Where("variable_id = ?", first.ID).Count(&revisions).Error; err != nil || revisions != 1 {
		t.Fatalf("越权操作写入了变量修订: count=%d err=%v", revisions, err)
	}
}

func TestVariableRuntimeRequiresMatchingProjectContext(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	input := VariableInput{Name: "RUNTIME_TOKEN", Kind: "env", Usage: "build", Value: pointer("runtime-secret-value"), IsSecret: true, IsActive: true}
	if _, err := s.SaveVariable(ctx, "owner", "", input); err != nil {
		t.Fatal(err)
	}
	bindings, err := s.FreezeVariables(ctx, scope.ProjectID, "", "", "build", "refs/heads/main", []string{input.Name})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		name       string
		ctx        context.Context
		resolveErr error
	}{
		{"没有上下文", context.Background(), ErrScope},
		{"没有项目", database.WithDepartmentScope(ctx, database.DepartmentScope{DepartmentID: scope.DepartmentID, AllDepartments: true}), ErrScope},
		{"没有部门", database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: scope.ProjectID, AllDepartments: true}), ErrScope},
		{"其他项目", database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: uuid.NewString(), DepartmentID: scope.DepartmentID, AllDepartments: true}), ErrVariableUnavailable},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			if frozen, freezeErr := s.FreezeVariables(candidate.ctx, scope.ProjectID, "", "", "build", "refs/heads/main", []string{input.Name}); !errors.Is(freezeErr, ErrScope) || frozen != nil {
				t.Fatalf("无有效项目上下文仍可固定变量: %v", freezeErr)
			}
			values, files, redactions, resolveErr := s.ResolveVariables(candidate.ctx, scope.ProjectID, "refs/heads/main", bindings)
			if !errors.Is(resolveErr, candidate.resolveErr) || values != nil || files != nil || redactions != nil {
				t.Fatalf("无有效项目上下文仍可解密变量: %v", resolveErr)
			}
		})
	}
	if bindings, err := s.FreezeVariables(context.Background(), "", "", "", "build", "", nil); err != nil || len(bindings) != 0 {
		t.Fatalf("未引用变量的任务被项目检查阻断: %v", err)
	}
	if values, files, redactions, err := s.ResolveVariables(context.Background(), "", "", nil); err != nil || len(values)+len(files)+len(redactions) != 0 {
		t.Fatalf("未引用变量的任务被项目检查阻断: %v", err)
	}
}

func TestVariableStaleDeletePreservesLatestRevision(t *testing.T) {
	s, ctx := variableFixture(t)
	in := VariableInput{Name: "CURRENT_VALUE", Kind: "env", Usage: "build", Value: pointer("first-value"), IsActive: true}
	variable, err := s.SaveVariable(ctx, "owner", "", in)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion = variable.Version
	in.Value = pointer("latest-value")
	updated, err := s.SaveVariable(ctx, "owner", variable.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteVariable(ctx, variable.ID, variable.Version); !errors.Is(err, ErrVariableConflict) {
		t.Fatalf("陈旧页面删除了新版变量: %v", err)
	}
	views, err := s.ListVariables(ctx)
	if err != nil || len(views) != 1 || views[0].Version != updated.Version || !views[0].IsActive || views[0].Value == nil || *views[0].Value != "latest-value" {
		t.Fatalf("陈旧删除改变了最新变量: %v", err)
	}
}

func TestVariableDeleteRejectsVersionChangeAfterRead(t *testing.T) {
	for _, candidate := range []string{"停用前版本变化", "删除前版本变化"} {
		t.Run(candidate, func(t *testing.T) {
			db, mock := testutil.NewSQLMock(t)
			service := New(db, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "variable-project", DepartmentID: database.DefaultDepartmentID})
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT .* FROM "delivery_variables"`).
				WillReturnRows(sqlmock.NewRows([]string{"id", "uuid", "project_id", "version"}).AddRow(1, "variable-id", "variable-project", 1))
			if candidate == "停用前版本变化" {
				mock.ExpectExec(`UPDATE "delivery_variables" SET .* WHERE \(uuid = .* AND project_id = .* AND version = .*\)`).WillReturnResult(sqlmock.NewResult(0, 0))
			} else {
				mock.ExpectExec(`UPDATE "delivery_variables" SET .* WHERE \(uuid = .* AND project_id = .* AND version = .*\)`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`UPDATE "delivery_variables" SET "deleted_at"=.* WHERE \(uuid = .* AND project_id = .* AND version = .*\)`).WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectRollback()
			if err := service.DeleteVariable(ctx, "variable-id", 1); !errors.Is(err, ErrVariableConflict) {
				t.Fatalf("删除未检查最新版本或未回滚: %v", err)
			}
		})
	}
}

func TestVariableDeleteKeepsStorageFailureInternal(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	service := New(db, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "variable-project", DepartmentID: database.DefaultDepartmentID})
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "delivery_variables"`).WillReturnError(errors.New("模拟数据库连接故障"))
	mock.ExpectRollback()
	if err := service.DeleteVariable(ctx, "variable-id", 1); !errors.Is(err, ErrStorage) {
		t.Fatalf("存储故障没有返回稳定安全提示: %v", err)
	}
}

func TestVariableAssociationsMustBelongToCurrentProject(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	other := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: uuid.NewString(), DepartmentID: scope.DepartmentID, AllDepartments: true})
	repository := model.GitRepository{ID: uuid.NewString(), Name: "foreign-variable-repository", CloneURL: "https://example.com/foreign.git"}
	application := model.Application{ID: uuid.NewString(), Name: "foreign-variable-app", RepositoryID: repository.ID}
	environment := model.Environment{ID: uuid.NewString(), Name: "其他项目环境"}
	for _, item := range []any{&repository, &application, &environment} {
		if err := s.db.WithContext(other).Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []VariableInput{
		{Name: "FOREIGN_APP", Kind: "env", Usage: "deploy", Value: pointer("value"), IsActive: true, ApplicationID: application.ID},
		{Name: "FOREIGN_ENV", Kind: "env", Usage: "deploy", Value: pointer("value"), IsActive: true, EnvironmentID: environment.ID},
	} {
		if _, err := s.SaveVariable(ctx, "owner", "", input); !errors.Is(err, ErrInvalidVariable) {
			t.Fatalf("变量关联了其他项目的应用或环境: %v", err)
		}
	}
	views, err := s.ListVariables(ctx)
	if err != nil || len(views) != 0 {
		t.Fatalf("非法关联留下了变量记录: %v", err)
	}
}
