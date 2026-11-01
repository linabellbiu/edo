package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"gorm.io/gorm"

	"edo/internal/model"
)

func templateNameTestInput(name string, revision uint64) WorkflowTemplateInput {
	return WorkflowTemplateInput{WorkflowInput: WorkflowInput{SchemaVersion: 1, Name: name, Revision: revision, Activate: true,
		Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "同步后的代码源", Config: model.WorkflowNodeConfig{Events: []string{"manual"}}},
		Stages: []model.WorkflowStage{{ID: "hold", Name: "人工阶段", Tasks: []model.WorkflowNode{{ID: "manual", Type: model.WorkflowNodeManual, Name: "人工放行"}}}},
	}}
}

func templateNameTestApp(t *testing.T, s *Service, repositoryID, name string) *model.Application {
	t.Helper()
	a, err := s.CreateApplication(context.Background(), "admin", ApplicationInput{Name: name, RepositoryID: repositoryID, EmptyWorkflow: true})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func templateNameTestTemplate(t *testing.T, s *Service, name string) *model.ReleaseWorkflowTemplate {
	t.Helper()
	input := templateNameTestInput(name, 0)
	input.Source.Name = "创建时代码源"
	r, err := s.CreateWorkflowTemplate(context.Background(), "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	return r.WorkflowTemplate
}

func templateNameTestLinked(t *testing.T, s *Service, app, template string) *model.ReleaseWorkflow {
	t.Helper()
	r, err := s.CreateApplicationWorkflow(context.Background(), app, "admin", WorkflowCreateInput{WorkflowTemplateID: template})
	if err != nil {
		t.Fatal(err)
	}
	return r.Workflow
}

func templateNameTestIndependent(t *testing.T, s *Service, app, name string) *model.ReleaseWorkflow {
	t.Helper()
	r, err := s.CreateApplicationWorkflow(context.Background(), app, "admin", WorkflowCreateInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return r.Workflow
}

func templateNameTestStored(t *testing.T, s *Service, app, id string) *model.ReleaseWorkflow {
	t.Helper()
	w, err := s.FindApplicationWorkflow(context.Background(), app, id)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWorkflowTemplateSyncNameUniqueness(t *testing.T) {
	t.Run("同模板两次创建后原名同步不制造同名", func(t *testing.T) {
		s, _, _, repo := newPipelineTestService(t)
		a, template := templateNameTestApp(t, s, repo, "same_template_twice"), templateNameTestTemplate(t, s, "T")
		first, second := templateNameTestLinked(t, s, a.ID, template.ID), templateNameTestLinked(t, s, a.ID, template.ID)
		if first.Name != "T" || second.Name != "T 2" {
			t.Fatalf("创建前提不是原后缀语义: %q/%q", first.Name, second.Name)
		}
		for revision := uint64(1); revision <= 2; revision++ {
			if _, err := s.SaveWorkflowTemplate(context.Background(), template.ID, "admin", templateNameTestInput("T", revision)); err != nil {
				t.Fatal(err)
			}
			for i, w := range []*model.ReleaseWorkflow{first, second} {
				stored := templateNameTestStored(t, s, a.ID, w.ID)
				want := []string{"T", "T 2"}[i]
				if stored.Name != want || stored.WorkflowTemplateID != template.ID || stored.WorkflowTemplateRevision != revision+1 || stored.Revision != w.Revision+revision || stored.Source.Name != "同步后的代码源" {
					t.Errorf("模板同步没有保留独立名称且传播新版本: want=%q workflow=%+v", want, stored)
				}
			}
		}
	})
	t.Run("关联记录遇独立记录占名继续分配后缀", func(t *testing.T) {
		s, _, _, repo := newPipelineTestService(t)
		a, template := templateNameTestApp(t, s, repo, "linked_independent"), templateNameTestTemplate(t, s, "Old")
		one, two := templateNameTestIndependent(t, s, a.ID, "New"), templateNameTestIndependent(t, s, a.ID, "New 2")
		linked := templateNameTestLinked(t, s, a.ID, template.ID)
		if _, err := s.SaveWorkflowTemplate(context.Background(), template.ID, "admin", templateNameTestInput("New", 1)); err != nil {
			t.Fatal(err)
		}
		stored := templateNameTestStored(t, s, a.ID, linked.ID)
		if stored.Name != "New 3" || stored.WorkflowTemplateRevision != 2 || stored.Revision != linked.Revision+1 {
			t.Errorf("同步撞名没有沿用后缀分配: %+v", stored)
		}
		for _, w := range []*model.ReleaseWorkflow{one, two} {
			if got := templateNameTestStored(t, s, a.ID, w.ID); got.Name != w.Name || got.Revision != w.Revision || got.WorkflowTemplateID != "" {
				t.Errorf("独立记录被同步改写: %+v", got)
			}
		}
	})
	t.Run("不同应用非冲突仍同步模板原名", func(t *testing.T) {
		s, _, _, repo := newPipelineTestService(t)
		a, b, template := templateNameTestApp(t, s, repo, "template_app_a"), templateNameTestApp(t, s, repo, "template_app_b"), templateNameTestTemplate(t, s, "Old")
		wa, wb := templateNameTestLinked(t, s, a.ID, template.ID), templateNameTestLinked(t, s, b.ID, template.ID)
		if _, err := s.SaveWorkflowTemplate(context.Background(), template.ID, "admin", templateNameTestInput("New", 1)); err != nil {
			t.Fatal(err)
		}
		for _, w := range []*model.ReleaseWorkflow{wa, wb} {
			if got := templateNameTestStored(t, s, w.ApplicationID, w.ID); got.Name != "New" || got.WorkflowTemplateRevision != 2 {
				t.Fatalf("无冲突却擅加后缀或停同步: %+v", got)
			}
		}
	})
	t.Run("软删除不占用候选名且解除关联不再同步", func(t *testing.T) {
		s, _, _, repo := newPipelineTestService(t)
		a, template := templateNameTestApp(t, s, repo, "template_deleted_names"), templateNameTestTemplate(t, s, "Old")
		templateNameTestIndependent(t, s, a.ID, "New")
		deleted := templateNameTestIndependent(t, s, a.ID, "New 2")
		if err := s.DeleteApplicationWorkflow(context.Background(), a.ID, deleted.ID); err != nil {
			t.Fatal(err)
		}
		linked, detached := templateNameTestLinked(t, s, a.ID, template.ID), templateNameTestLinked(t, s, a.ID, template.ID)
		input := templateNameTestInput("Custom", 0).WorkflowInput
		input.Revision = detached.Revision
		custom, err := s.SaveApplicationWorkflow(context.Background(), a.ID, detached.ID, "admin", input)
		if err != nil || custom.Workflow.WorkflowTemplateID != "" {
			t.Fatalf("解除关联前提失败: %+v %v", custom, err)
		}
		if _, err := s.SaveWorkflowTemplate(context.Background(), template.ID, "admin", templateNameTestInput("New", 1)); err != nil {
			t.Fatal(err)
		}
		if got := templateNameTestStored(t, s, a.ID, linked.ID); got.Name != "New 2" {
			t.Errorf("软删除候选名仍被占用: %+v", got)
		}
		if got := templateNameTestStored(t, s, a.ID, detached.ID); got.Name != "Custom" || got.WorkflowTemplateID != "" || got.Revision != custom.Workflow.Revision {
			t.Errorf("解除关联后仍被模板覆盖: %+v", got)
		}
	})
	t.Run("128字符冲突保留数字后缀并完整候选查重", func(t *testing.T) {
		s, _, _, repo := newPipelineTestService(t)
		a := templateNameTestApp(t, s, repo, "template_name_limit")
		base, two, three := strings.Repeat("界", 128), strings.Repeat("界", 126)+" 2", strings.Repeat("界", 126)+" 3"
		template := templateNameTestTemplate(t, s, base)
		templateNameTestLinked(t, s, a.ID, template.ID)
		templateNameTestIndependent(t, s, a.ID, two)
		last := templateNameTestLinked(t, s, a.ID, template.ID)
		if last.Name != three || utf8.RuneCountInString(last.Name) != 128 || !utf8.ValidString(last.Name) {
			t.Errorf("自动后缀不是合法128rune完整候选: runes=%d name=%q", utf8.RuneCountInString(last.Name), last.Name)
		}
		if _, err := s.SaveWorkflowTemplate(context.Background(), template.ID, "admin", templateNameTestInput(base, 1)); err != nil {
			t.Fatal(err)
		}
		if got := templateNameTestStored(t, s, a.ID, last.ID); got.Name != three || utf8.RuneCountInString(got.Name) != 128 {
			t.Errorf("同步覆盖了受限后缀候选: %+v", got)
		}
	})
}

func TestWorkflowTemplateSyncNameRollback(t *testing.T) {
	s, db, _, repo := newPipelineTestService(t)
	a, template := templateNameTestApp(t, s, repo, "template_name_rollback"), templateNameTestTemplate(t, s, "Old")
	first, second := templateNameTestLinked(t, s, a.ID, template.ID), templateNameTestLinked(t, s, a.ID, template.ID)
	before := []*model.ReleaseWorkflow{templateNameTestStored(t, s, a.ID, first.ID), templateNameTestStored(t, s, a.ID, second.ID)}
	type markerKey struct{}
	marker := errors.New("本例第二条同步写失败")
	count := 0
	const callback = "test:workflow_name_sync_rollback"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(markerKey{}) == true && tx.Statement.Table == "release_workflows" {
			count++
			if count == 2 {
				tx.AddError(marker)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Update().Remove(callback) })
	result, err := s.SaveWorkflowTemplate(context.WithValue(context.Background(), markerKey{}, true), template.ID, "admin", templateNameTestInput("New", 1))
	if result != nil || !errors.Is(err, marker) || count != 2 {
		t.Fatalf("失败注入没有真实命中第二次写: result=%+v err=%v count=%d", result, err, count)
	}
	stored, err := s.GetWorkflowTemplate(context.Background(), template.ID)
	if err != nil || stored.WorkflowTemplate.Revision != 1 || stored.WorkflowTemplate.Name != "Old" {
		t.Errorf("模板CAS没有原子回滚: %+v %v", stored, err)
	}
	for i, w := range []*model.ReleaseWorkflow{first, second} {
		if got := templateNameTestStored(t, s, a.ID, w.ID); !reflect.DeepEqual(got, before[i]) {
			t.Errorf("同步前面的写没有回滚: before=%+v after=%+v", before[i], got)
		}
	}
}
