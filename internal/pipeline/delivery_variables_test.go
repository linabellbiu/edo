package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

func TestPipelineInjectsFrozenVariablesAndRedactsActualValue(t *testing.T) {
	node := parallelShell("test")
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "ci", Tasks: []model.WorkflowNode{node}}}}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	actor := model.User{ID: "delivery-authority", Username: "delivery_authority", IsSuperuser: true, IsActive: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureDeliveryAccess(permissions)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, UserID: actor.ID})
	value := "private-actual-variable"
	input := cicd.VariableInput{Name: "TOKEN_WITHOUT_HINT", Kind: "env", Usage: "build", Value: &value, IsSecret: true, IsActive: true}
	variable, err := s.Delivery().SaveVariable(ctx, actor.ID, "", input)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := s.Delivery().FreezeVariables(ctx, run.ProjectID, run.ApplicationID, "", "build", run.Ref, []string{input.Name})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.AuthorityID = actor.ID
	snapshot.VariableBindings = map[string][]model.DeliveryVariableBinding{node.ID: bindings}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), value) {
		t.Fatal("运行快照保存了密钥明文")
	}
	if err = db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("workflow_snapshot", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	replacement := "replacement-private-value"
	input.Value = &replacement
	input.ExpectedVersion = variable.Version
	if _, err = s.Delivery().SaveVariable(ctx, actor.ID, variable.ID, input); err != nil {
		t.Fatal(err)
	}
	s.scriptRunner = parallelScriptRunner(func(_ context.Context, in dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
		if in.Environment["TOKEN_WITHOUT_HINT"] != value {
			t.Fatal("没有注入运行创建时的变量版本")
		}
		_, _ = in.Stdout.Write([]byte("value=" + value + "\n"))
		return dockerengine.ScriptContainerResult{}, nil
	})
	if _, err = s.AdvanceRun(ctx, run.ID, actor.ID, ""); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 1 {
		t.Fatal(err)
	}
	if err = s.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, nodes[0].JobID); err != nil {
		t.Fatal(err)
	}
	var logs []model.PipelineRunLog
	if err = db.Where("pipeline_run_id = ?", run.ID).Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(logs)
	if strings.Contains(string(encoded), value) || !strings.Contains(string(encoded), "已脱敏") {
		t.Fatal("日志没有按密钥实际值脱敏")
	}
}
