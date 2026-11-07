package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"edo/internal/model"
)

func nameTestApplication(t *testing.T, service *Service, repositoryID, name string) *model.Application {
	t.Helper()
	app, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{
		Name: name, RepositoryID: repositoryID, EmptyWorkflow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func nameTestWorkflow(t *testing.T, service *Service, appID, name string) *model.ReleaseWorkflow {
	t.Helper()
	result, err := service.CreateApplicationWorkflow(context.Background(), appID, "admin", WorkflowCreateInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	// 以真实持久化结果为基准，保留数据库时间精度与时区下的完整记录比较。
	stored, err := service.FindApplicationWorkflow(context.Background(), appID, result.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func nameTestInput(w *model.ReleaseWorkflow, name string) WorkflowInput {
	return WorkflowInput{
		SchemaVersion: 1, Name: name, Revision: w.Revision,
		Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Events: []string{"manual"}}},
		Stages: []model.WorkflowStage{{ID: "hold", Name: "人工阶段", Tasks: []model.WorkflowNode{{ID: "manual", Type: model.WorkflowNodeManual, Name: "人工放行"}}}},
	}
}

func TestApplicationWorkflowNameUniqueness(t *testing.T) {
	t.Run("同应用改为另一活记录名称必须拒绝且不改版本图", func(t *testing.T) {
		service, db, _, repositoryID := newPipelineTestService(t)
		app := nameTestApplication(t, service, repositoryID, "rename_conflict")
		a := nameTestWorkflow(t, service, app.ID, "name-a")
		b := nameTestWorkflow(t, service, app.ID, "name-b")
		result, err := service.SaveApplicationWorkflow(context.Background(), app.ID, b.ID, "admin", nameTestInput(b, "  "+a.Name+"  "))
		if result != nil || !errors.Is(err, ErrWorkflowExists) {
			t.Errorf("重复改名必须返回既有业务冲突: result=%+v err=%v", result, err)
		}
		stored, err := service.FindApplicationWorkflow(context.Background(), app.ID, b.ID)
		if err != nil || !reflect.DeepEqual(*stored, *b) {
			t.Errorf("失败改名改变了原记录: before=%+v after=%+v err=%v", b, stored, err)
		}
		var count int64
		if err := db.Model(&model.ReleaseWorkflow{}).Where("application_id = ? AND name = ?", app.ID, a.Name).Count(&count).Error; err != nil || count != 1 {
			t.Errorf("同应用活记录名称不唯一: count=%d err=%v", count, err)
		}
	})
	t.Run("保留自己的原名可以保存", func(t *testing.T) {
		service, _, _, repositoryID := newPipelineTestService(t)
		app := nameTestApplication(t, service, repositoryID, "same_name_self")
		w := nameTestWorkflow(t, service, app.ID, "self-name")
		result, err := service.SaveApplicationWorkflow(context.Background(), app.ID, w.ID, "admin", nameTestInput(w, w.Name))
		if err != nil || result.Workflow.Name != w.Name || result.Workflow.Revision != w.Revision+1 {
			t.Fatalf("自己的名称被误拒绝: result=%+v err=%v", result, err)
		}
	})
	t.Run("另一应用相同名称继续允许", func(t *testing.T) {
		service, _, _, repositoryID := newPipelineTestService(t)
		appA := nameTestApplication(t, service, repositoryID, "other_app_a")
		appB := nameTestApplication(t, service, repositoryID, "other_app_b")
		a := nameTestWorkflow(t, service, appA.ID, "shared-name")
		b := nameTestWorkflow(t, service, appB.ID, "different-name")
		result, err := service.SaveApplicationWorkflow(context.Background(), appB.ID, b.ID, "admin", nameTestInput(b, a.Name))
		if err != nil || result.Workflow.Name != a.Name {
			t.Fatalf("不同应用名称被误拒绝: result=%+v err=%v", result, err)
		}
	})
	t.Run("删除后的名称允许新建复用", func(t *testing.T) {
		service, _, _, repositoryID := newPipelineTestService(t)
		app := nameTestApplication(t, service, repositoryID, "deleted_name_create")
		old := nameTestWorkflow(t, service, app.ID, "reusable-name")
		if err := service.DeleteApplicationWorkflow(context.Background(), app.ID, old.ID); err != nil {
			t.Fatal(err)
		}
		fresh := nameTestWorkflow(t, service, app.ID, old.Name)
		if fresh.ID == old.ID {
			t.Fatal("复用名称不能恢复旧记录")
		}
	})
	t.Run("删除后的名称允许改名复用", func(t *testing.T) {
		service, _, _, repositoryID := newPipelineTestService(t)
		app := nameTestApplication(t, service, repositoryID, "deleted_name_rename")
		a := nameTestWorkflow(t, service, app.ID, "deleted-name")
		b := nameTestWorkflow(t, service, app.ID, "new-name")
		if err := service.DeleteApplicationWorkflow(context.Background(), app.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		result, err := service.SaveApplicationWorkflow(context.Background(), app.ID, b.ID, "admin", nameTestInput(b, a.Name))
		if err != nil || result.Workflow.Name != a.Name {
			t.Fatalf("软删除名称被误算为活引用: result=%+v err=%v", result, err)
		}
	})
	t.Run("重复创建仍使用既有冲突", func(t *testing.T) {
		service, _, _, repositoryID := newPipelineTestService(t)
		app := nameTestApplication(t, service, repositoryID, "create_name_conflict")
		w := nameTestWorkflow(t, service, app.ID, "same-name")
		if result, err := service.CreateApplicationWorkflow(context.Background(), app.ID, "admin", WorkflowCreateInput{Name: w.Name}); result != nil || !errors.Is(err, ErrWorkflowExists) {
			t.Fatalf("重复创建错误边界改变: result=%+v err=%v", result, err)
		}
	})
	t.Run("陈旧版本优先返回原CAS冲突", func(t *testing.T) {
		service, _, _, repositoryID := newPipelineTestService(t)
		app := nameTestApplication(t, service, repositoryID, "stale_before_name")
		a := nameTestWorkflow(t, service, app.ID, "occupied-name")
		b := nameTestWorkflow(t, service, app.ID, "old-name")
		if _, err := service.SaveApplicationWorkflow(context.Background(), app.ID, b.ID, "admin", nameTestInput(b, "new-name")); err != nil {
			t.Fatal(err)
		}
		if result, err := service.SaveApplicationWorkflow(context.Background(), app.ID, b.ID, "admin", nameTestInput(b, a.Name)); result != nil || !errors.Is(err, ErrWorkflowRevisionConflict) {
			t.Fatalf("重复名不能掩盖陈旧版本: result=%+v err=%v", result, err)
		}
	})
}
