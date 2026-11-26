package projecttransfer

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestCrossDepartmentApplicationMoveUsesCurrentAdminAndMovesHistoryScope(t *testing.T) {
	db := testutil.NewDatabase(t)
	svc := New(db, nil)
	source, target, department, actorID := database.DefaultProjectID(database.DefaultDepartmentID), uuid.NewString(), uuid.NewString(), uuid.NewString()
	actor := model.User{ID: actorID, Username: "transfer-operator", Nickname: "迁移操作者", DepartmentID: database.DefaultDepartmentID, IsActive: true, AuthVersion: 1}
	repo := model.GitRepository{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, Name: "跨部门仓库", CredentialCiphertext: "项目仓库的测试密文", IsActive: true}
	app := model.Application{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, Name: "跨部门应用", RepositoryID: repo.ID}
	wf := model.ReleaseWorkflow{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, ApplicationID: app.ID, Name: "发布"}
	run := model.PipelineRun{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, ApplicationID: app.ID, WorkflowID: wf.ID, Status: model.PipelineRunSucceeded}
	node := model.PipelineNodeRun{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, PipelineRunID: run.ID, NodeID: "history"}
	variable := model.DeliveryVariable{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, ApplicationID: app.ID, Name: "CONFIG", Kind: "env", Usage: "build"}
	variable.ScopeKey = model.DeliveryVariableScopeKey(source, app.ID, "", variable.Usage, variable.Name)
	revision := model.DeliveryVariableRevision{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, VariableID: variable.ID, Version: 1, Value: "原配置值"}
	for _, row := range []any{&model.Department{ID: department, Name: "目标部门", IsActive: true}, &model.Project{ID: target, DepartmentID: department, Name: "目标项目", IsActive: true}, &actor, &repo, &app, &wf, &run, &node, &variable, &revision} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: source, DepartmentID: database.DefaultDepartmentID, UserID: actorID, AllDepartments: true})
	input := Input{TargetProjectID: target, Resources: []Resource{{Kind: "application", ID: app.ID}}, IncludeDependencies: true, Authorize: func(*gorm.DB) error { return nil }, Audit: func(*gorm.DB, Plan) error { return nil }}
	if _, err := svc.Preview(ctx, source, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("普通用户不能伪造跨部门范围: %v", err)
	}
	if err := db.Model(&actor).Update("is_superuser", true).Error; err != nil {
		t.Fatal(err)
	}
	p, err := svc.Preview(ctx, source, input)
	if err != nil || len(p.Blockers) != 0 || p.TargetDepartmentID != department || p.SourceDepartmentID != database.DefaultDepartmentID || p.HistoryCount != 1 {
		t.Fatalf("管理员应能预览真实目标范围: %+v %v", p, err)
	}
	// 个人凭据不能因移动仓库而被目标部门间接使用。
	credential := model.GitCredential{ID: uuid.NewString(), UserID: actorID, Name: "私人令牌", Provider: model.GitProviderGitHub, AuthType: model.GitAuthToken, SecretCiphertext: "保留的测试密文"}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatal(err)
	}
	targetOwner := model.User{ID: uuid.NewString(), Username: "target-owner", DepartmentID: department, IsActive: true}
	targetCredential := model.GitCredential{ID: uuid.NewString(), UserID: targetOwner.ID, Name: "目标部门的个人令牌", Provider: model.GitProviderGitHub, AuthType: model.GitAuthToken, SecretCiphertext: "目标账户的测试密文"}
	for _, row := range []any{&targetOwner, &targetCredential} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ field, credentialID string }{
		{"credential_id", credential.ID},
		{"credential_id", targetCredential.ID},
		{"api_credential_id", targetCredential.ID},
	} {
		references := map[string]any{"credential_id": nil, "api_credential_id": nil}
		references[test.field] = test.credentialID
		if err := db.Model(&repo).Updates(references).Error; err != nil {
			t.Fatal(err)
		}
		blocked, err := svc.Preview(ctx, source, input)
		if err != nil || len(blocked.Blockers) != 1 || blocked.Blockers[0].ID != repo.ID || blocked.Confirmation != "" {
			t.Fatalf("跨部门个人令牌引用 %s 必须阻断，即使所有者属于目标部门: %+v %v", test.field, blocked.Blockers, err)
		}
	}
	if err := db.Model(&repo).Updates(map[string]any{"credential_id": nil, "api_credential_id": nil}).Error; err != nil {
		t.Fatal(err)
	}
	p, err = svc.Preview(ctx, source, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Confirmation = p.Confirmation
	if err := db.Model(&actor).Update("is_superuser", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(ctx, source, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("管理员降权后不能用旧预览跨部门迁移: %v", err)
	}
	if err := db.Model(&actor).Update("is_superuser", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(ctx, source, input); err != nil {
		t.Fatal(err)
	}
	for table, id := range map[string]string{"applications": app.ID, "git_repositories": repo.ID, "release_workflows": wf.ID, "pipeline_runs": run.ID, "pipeline_node_runs": node.ID, "delivery_variables": variable.ID, "delivery_variable_revisions": revision.ID} {
		var scope struct{ ProjectID, DepartmentID string }
		if err := db.Table(table).Where("uuid = ?", id).Select("project_id", "department_id").Take(&scope).Error; err != nil || scope.ProjectID != target || scope.DepartmentID != department {
			t.Fatalf("%s 归属必须完整转入目标: %+v %v", table, scope, err)
		}
	}
	var saved model.GitCredential
	if err := db.First(&saved, "uuid = ?", credential.ID).Error; err != nil || saved.UserID != actorID || saved.SecretCiphertext != credential.SecretCiphertext {
		t.Fatalf("原用户令牌不应变化: %v", err)
	}
	var movedRepository model.GitRepository
	if err := db.First(&movedRepository, "uuid = ?", repo.ID).Error; err != nil || movedRepository.CredentialCiphertext != repo.CredentialCiphertext {
		t.Fatalf("仓库自身保存的密文应原样随迁: %v", err)
	}
}
