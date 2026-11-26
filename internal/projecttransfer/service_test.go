package projecttransfer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestMoveApplicationIncludesWorkflowHistoryAndConfiguration(t *testing.T) {
	db := testutil.NewDatabase(t)
	svc := New(db, nil)
	source := database.DefaultProjectID(database.DefaultDepartmentID)
	target := uuid.NewString()
	actor := uuid.NewString()
	now := time.Now().UTC()
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: source, DepartmentID: database.DefaultDepartmentID, UserID: actor})
	repo := model.GitRepository{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, Name: "仓库", IsActive: true}
	app := model.Application{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, Name: "应用", RepositoryID: repo.ID}
	wf := model.ReleaseWorkflow{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, Name: "流水线", ApplicationID: app.ID}
	run := model.PipelineRun{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, ApplicationID: app.ID, WorkflowID: wf.ID, Status: model.PipelineRunSucceeded}
	node := model.PipelineNodeRun{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, PipelineRunID: run.ID, NodeID: "node"}
	variable := model.DeliveryVariable{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, ApplicationID: app.ID, Name: "CONFIG", Kind: "env", Usage: "variable"}
	variable.ScopeKey = model.DeliveryVariableScopeKey(source, app.ID, "", variable.Usage, variable.Name)
	revision := model.DeliveryVariableRevision{ID: uuid.NewString(), ProjectID: source, DepartmentID: database.DefaultDepartmentID, VariableID: variable.ID, Version: 1, Value: "保留正文"}
	for _, row := range []any{
		&model.Project{ID: target, DepartmentID: database.DefaultDepartmentID, Name: "接收项目", IsActive: true, CreatedAt: now, UpdatedAt: now},
		&repo, &app, &wf, &run, &node, &variable, &revision,
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	authorizations, audits := 0, 0
	input := Input{TargetProjectID: target, Resources: []Resource{{Kind: "application", ID: app.ID}},
		Authorize: func(*gorm.DB) error { authorizations++; return nil },
		Audit:     func(*gorm.DB, Plan) error { audits++; return errors.New("audit unavailable") },
	}
	p, err := svc.Preview(ctx, source, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Blockers) != 1 || p.Blockers[0].ID != repo.ID || p.Confirmation != "" {
		t.Fatalf("必须明确提示未选仓库: %+v", p.Blockers)
	}
	input.Resources = append(input.Resources, Resource{Kind: "repository", ID: repo.ID})
	p, err = svc.Preview(ctx, source, input)
	if err != nil || len(p.Blockers) > 0 || len(p.Resources) != 4 || p.HistoryCount != 1 || p.Confirmation == "" {
		t.Fatalf("预览未包含自动归属的流水线、配置和历史: %+v %v", p, err)
	}
	input.Confirmation = p.Confirmation
	if err := db.Model(&run).Update("status", model.PipelineRunRunning).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(ctx, source, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("已开始运行必须阻断: %v", err)
	}
	if err := db.Model(&run).Update("status", model.PipelineRunSucceeded).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&repo).Update("name", "新仓库名").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Move(ctx, source, input); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("仓库变化后旧预览必须作废: %v", err)
	}
	p, err = svc.Preview(ctx, source, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Confirmation = p.Confirmation
	if _, err := svc.Move(ctx, source, input); err == nil || audits != 1 {
		t.Fatalf("审计失败必须回滚: %v", err)
	}
	if err := db.First(&run, "uuid = ?", run.ID).Error; err != nil || run.ProjectID != source {
		t.Fatalf("回滚后运行记录仍在原项目: %+v %v", run, err)
	}
	input.Audit = func(*gorm.DB, Plan) error { audits++; return nil }
	if _, err := svc.Move(ctx, source, input); err != nil {
		t.Fatal(err)
	}
	if audits != 2 || authorizations != 7 {
		t.Fatalf("权限复核或审计次数不正确: %d %d", authorizations, audits)
	}
	for table, id := range map[string]string{"applications": app.ID, "git_repositories": repo.ID, "release_workflows": wf.ID, "pipeline_runs": run.ID, "pipeline_node_runs": node.ID, "delivery_variables": variable.ID, "delivery_variable_revisions": revision.ID} {
		var project string
		if err := db.Table(table).Where("uuid = ?", id).Pluck("project_id", &project).Error; err != nil || project != target {
			t.Fatalf("%s 归属=%s，期望 %s: %v", table, project, target, err)
		}
	}
	if err := db.First(&variable, "uuid = ?", variable.ID).Error; err != nil || variable.ScopeKey != model.DeliveryVariableScopeKey(target, app.ID, "", variable.Usage, variable.Name) {
		t.Fatalf("变量作用域未随项目更新: %v", err)
	}
}
