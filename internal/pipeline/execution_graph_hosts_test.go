package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/model"
)

func TestDeploymentHostDisplayMetadataPreservesLongAndDelimitedNames(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	taskName := "docker-compose · " + strings.Repeat("长", 110)
	hosts := []model.Host{
		{ID: "display-host-a", Name: "内网 · " + strings.Repeat("甲", 100), IsActive: true},
		{ID: "display-host-b", Name: "本地 · 备用", IsActive: true},
	}
	if err := db.Create(&hosts).Error; err != nil {
		t.Fatal(err)
	}
	compose := model.WorkflowNode{ID: "compose", Name: taskName, Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{
		InlineDeploymentPlan: &model.WorkflowDeploymentPlan{Kind: model.DeploymentPlanCompose, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentDocker, HostIDs: []string{hosts[0].ID, hosts[1].ID}}},
	}}
	kubernetes := model.WorkflowNode{ID: "kubernetes", Name: "Kubernetes", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{
		InlineDeploymentPlan: &model.WorkflowDeploymentPlan{Kind: model.DeploymentPlanKubernetes, DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentKubernetes}},
	}}
	workflow := model.ReleaseWorkflow{Stages: []model.WorkflowStage{{ID: "deploy", Tasks: []model.WorkflowNode{compose, kubernetes}}}}
	if err := s.expandDeploymentHosts(context.Background(), db, &workflow); err != nil {
		t.Fatal(err)
	}
	tasks := workflow.Stages[0].Tasks
	if len(tasks) != 3 {
		t.Fatalf("两个配置任务应保留三份实际执行: %+v", tasks)
	}
	for index, host := range hosts {
		task := tasks[index]
		if task.ID != model.DeploymentHostNodeID(compose.ID, host.ID) || task.Config.ExecutionHostID != host.ID || task.Config.ExecutionTaskName != taskName || task.Config.ExecutionHostName != host.Name {
			t.Fatalf("展开任务没有冻结准确的独立名称和主机身份: %+v", task)
		}
		legacyName := []rune(taskName + " · " + host.Name)
		if len(legacyName) > 128 {
			legacyName = legacyName[:128]
		}
		if task.Name != string(legacyName) {
			t.Fatalf("已有执行名称或长度边界被改变: %q", task.Name)
		}
	}
	if tasks[2].ID != kubernetes.ID || tasks[2].Config.ExecutionTaskName != "" || tasks[2].Config.ExecutionHostName != "" || tasks[2].Config.ExecutionHostID != "" {
		t.Fatalf("Kubernetes 任务不应展开或获得物理主机展示信息: %+v", tasks[2])
	}
}

func TestWorkflowSaveClearsClientExecutionDisplayMetadata(t *testing.T) {
	s, _, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "execution_display_input", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.GetWorkflow(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := result.Workflow.Source
	source.Config.ExecutionHostID, source.Config.ExecutionTaskName, source.Config.ExecutionHostName = "forged-host", "forged-task", "forged-name"
	task := model.WorkflowNode{ID: "check", Name: "检查", Type: model.WorkflowNodeShell, Config: model.WorkflowNodeConfig{
		Script: "echo ok", ExecutionHostID: "forged-host", ExecutionTaskName: "forged-task", ExecutionHostName: "forged-name",
	}}
	saved, err := s.SaveApplicationWorkflow(ctx, app.ID, result.Workflow.ID, "admin", WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: result.Workflow.Name, Revision: result.Workflow.Revision,
		Source: source, Stages: []model.WorkflowStage{{ID: "stage", Name: "检查", Tasks: []model.WorkflowNode{task}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []model.WorkflowNode{saved.Workflow.Source, saved.Workflow.Stages[0].Tasks[0]} {
		if node.Config.ExecutionHostID != "" || node.Config.ExecutionTaskName != "" || node.Config.ExecutionHostName != "" {
			t.Fatalf("客户端伪造的执行展示信息被保存: %+v", node)
		}
	}
}

func TestRunGraphUsesOnlyFrozenHostDisplayMetadataAndPreservesLegacyNames(t *testing.T) {
	s, db, _, repositoryID := newPipelineTestService(t)
	ctx := context.Background()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "execution_display_graph", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	frozenTask, frozenHost := "docker-compose · 原任务", "原主机 · "+strings.Repeat("长", 140)
	fullLegacyName := "docker-compose · 名称中有分隔符 · 主机名"
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源"}, Stages: []model.WorkflowStage{{ID: "deploy", Name: "部署", Tasks: []model.WorkflowNode{
		{ID: "frozen", Type: model.WorkflowNodeDeploy, Name: "已有截断组合名称", Config: model.WorkflowNodeConfig{ExecutionTaskName: frozenTask, ExecutionHostName: frozenHost, ExecutionHostID: "current-host", Script: "不应返回的配置"}},
		{ID: "legacy", Type: model.WorkflowNodeDeploy, Name: fullLegacyName, Config: model.WorkflowNodeConfig{ExecutionHostID: "current-host"}},
	}}}}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{ID: "execution-display-run", ApplicationID: app.ID, WorkflowID: "old-workflow", Status: model.PipelineRunFailed, WorkflowSnapshot: string(encoded), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Host{ID: "current-host", Name: "后来修改的主机名称", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("execution_display_no_current_host", func(tx *gorm.DB) {
		if tx.Statement.Table == "hosts" {
			tx.AddError(errors.New("运行图不得读取当前主机补造冻结信息"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	found, err := s.FindRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.ExecutionGraph == nil || len(found.ExecutionGraph.Stages) != 1 || len(found.ExecutionGraph.Stages[0].Tasks) != 2 {
		t.Fatalf("运行图结构改变: %+v", found.ExecutionGraph)
	}
	tasks := found.ExecutionGraph.Stages[0].Tasks
	if tasks[0].Name != "已有截断组合名称" || tasks[0].ExecutionTaskName != frozenTask || tasks[0].ExecutionHostName != frozenHost || tasks[0].ExecutionHostID != "current-host" {
		t.Fatalf("展示信息没有沿用冻结快照: %+v", tasks[0])
	}
	if tasks[1].Name != fullLegacyName || tasks[1].ExecutionTaskName != "" || tasks[1].ExecutionHostName != "" || tasks[1].ExecutionHostID != "current-host" {
		t.Fatalf("历史名称被猜测拆分或从当前主机回填: %+v", tasks[1])
	}
	graphJSON, err := json.Marshal(found.ExecutionGraph)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(graphJSON), "后来修改的主机名称") || strings.Contains(string(graphJSON), "不应返回的配置") {
		t.Fatal("运行图泄露当前主机数据或执行配置")
	}
	var fields struct {
		Stages []struct {
			Tasks []map[string]any `json:"tasks"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(graphJSON, &fields); err != nil {
		t.Fatal(err)
	}
	if fields.Stages[0].Tasks[0]["execution_task_name"] != frozenTask || fields.Stages[0].Tasks[0]["execution_host_name"] != frozenHost || fields.Stages[0].Tasks[0]["execution_host_id"] != "current-host" {
		t.Fatal("运行图接口缺少明确的任务及执行主机字段")
	}
	for _, key := range []string{"execution_task_name", "execution_host_name"} {
		if _, exists := fields.Stages[0].Tasks[1][key]; exists {
			t.Fatalf("历史运行缺少的字段不能补造: %s", key)
		}
	}
}
