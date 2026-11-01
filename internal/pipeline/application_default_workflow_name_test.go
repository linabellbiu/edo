package pipeline

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCreateApplicationDefaultWorkflowNameWithinLimit(t *testing.T) {
	service, _, _, repositoryID := newPipelineTestService(t)
	name := strings.Repeat("a", 128)
	app, err := service.CreateApplication(context.Background(), "admin", ApplicationInput{Name: name, RepositoryID: repositoryID})
	if err != nil {
		t.Fatalf("合法128 ASCII应用名被拒绝: %v", err)
	}
	if app.Name != name || len(app.Workflows) != 1 {
		t.Fatalf("正常默认创建前提不符: app=%+v", app)
	}
	w, err := service.FindApplicationWorkflow(context.Background(), app.ID, app.Workflows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("正常入口已持久化 application_runes=%d workflow_runes=%d workflow_bytes=%d workflow_active=%t", utf8.RuneCountInString(app.Name), utf8.RuneCountInString(w.Name), len(w.Name), w.IsActive)
	if utf8.RuneCountInString(w.Name) > 128 || !utf8.ValidString(w.Name) {
		t.Errorf("默认WF名称超过正常保存的128rune边界: got=%d", utf8.RuneCountInString(w.Name))
	}
	result, err := service.SaveApplicationWorkflow(context.Background(), app.ID, w.ID, "admin", WorkflowInput{SchemaVersion: w.SchemaVersion, Name: w.Name, Revision: w.Revision, Source: w.Source, Stages: w.Stages})
	if err != nil || result == nil {
		t.Errorf("新建默认草稿必须能以自己的原名保存: err=%v", err)
	}
}
