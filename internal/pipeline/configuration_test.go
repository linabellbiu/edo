package pipeline

import (
	"context"
	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/database"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/configfile"
	"edo/internal/deployment"
	"edo/internal/model"
)

func TestComposeConfigurationUsesStablePathsAndOpaqueTransport(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := "TOKEN=literal$token\nREMOVED=true\n"
	binding := model.DeploymentConfigBinding{DeliveryVariableBinding: model.DeliveryVariableBinding{VariableID: "config", Name: ".prod.env", Kind: "config_env", Version: 1}, Path: "config/.prod.env"}
	set := &deployment.ConfigurationSet{Bindings: []model.DeploymentConfigBinding{binding}, Files: map[string]string{binding.Path: original}, Directory: directory + "/configuration"}
	source := "services:\n  web:\n    image: alpine:3.22\n    labels: [existing=preserved]\n"
	content, writes, err := composeConfiguration(source, set)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content+writes, "literal$token") || !strings.Contains(content, "${EDO_CONFIGURATION_DIRECTORY}/config/.prod.env") || !strings.Contains(content, "format: raw") || !strings.Contains(content, "existing: preserved") {
		t.Fatal("正文泄漏、未生成受控引用或原标签丢失")
	}
	execute := func(value string) {
		t.Helper()
		cmd := exec.Command("sh", "-se")
		cmd.Dir = directory
		cmd.Stdin = strings.NewReader(writes)
		cmd.Env = append(os.Environ(), configfile.TransportKey(binding.Path)+"="+value)
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(data))
		}
	}
	execute(original)
	changed := "TOKEN=after\nADDED=true\n"
	execute(changed)
	data, err := os.ReadFile(filepath.Join(set.Directory, binding.Path))
	if err != nil || string(data) != changed {
		t.Fatal("没有使用原路径替换最新正文", err)
	}
	config := composeTestConfig()
	config.ConfigurationFiles = []model.DeploymentConfigFile{{ID: "config", Path: binding.Path}}
	config.DeploymentParameters = "--env-file config/.prod.env"
	script, err := composeStackScript(content, config, directory, 120, writes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `--env-file "${EDO_CONFIGURATION_DIRECTORY:?缺少配置目录}/"'config/.prod.env'`) || strings.Contains(script, original) || strings.Contains(script, changed) {
		t.Fatal("部署参数丢失或正文写入命令快照")
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if err = cmd.Run(); err != nil {
		t.Fatal("生成脚本语法无效", err)
	}
	empty, emptyWrites, err := composeConfiguration(source, nil)
	if err != nil || empty != source || emptyWrites != "" {
		t.Fatal("未配置时改变了原行为", err)
	}
}

func TestConfigurationWorkflowSnapshotPinsSelectionUntilNextDeployment(t *testing.T) {
	f := newInactiveEnvironmentGuardFixture(t)
	actor := model.User{ID: "configuration-authority", Username: "configuration_authority", DepartmentID: f.application.DepartmentID, IsSuperuser: true, IsActive: true}
	if err := f.db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.service.ConfigureDeliveryAccess(permissions)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: f.application.ProjectID, DepartmentID: f.application.DepartmentID, UserID: actor.ID})
	value := "TOKEN=configuration-snapshot-private\n"
	input := cicd.VariableInput{Name: ".prod.env", Kind: "config_env", Usage: "deploy", Value: &value, IsSecret: true, IsActive: true, ApplicationID: f.application.ID, EnvironmentID: f.environmentID}
	saved, err := f.service.Delivery().SaveVariable(ctx, actor.ID, "", input)
	if err != nil {
		t.Fatal(err)
	}
	workflow := *f.activeWorkflow
	workflow.Stages = cloneWorkflowStages(workflow.Stages)
	for i := range workflow.Stages {
		for j := range workflow.Stages[i].Tasks {
			node := &workflow.Stages[i].Tasks[j]
			if node.Type == model.WorkflowNodeDeploy {
				node.Config.ConfigurationFiles = []model.DeploymentConfigFile{{ID: saved.ID, Path: "config/.prod.env"}}
				node.Config.DeploymentParameters = "--env-file config/.prod.env"
			}
		}
	}
	run, err := f.service.newResolvedWorkflowRun(ctx, f.application, &workflow, workflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), actor.ID, "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil || len(snapshot.ConfigurationBindings["deploy"]) != 1 || snapshot.ConfigurationBindings["deploy"][0].Version != 1 || strings.Contains(run.WorkflowSnapshot, value) {
		t.Fatal("配置未正确冻结或泄漏正文", err)
	}
	// 只改可编辑流程不能改写已创建运行；下次运行才采用移除引用和参数。
	for i := range workflow.Stages {
		for j := range workflow.Stages[i].Tasks {
			node := &workflow.Stages[i].Tasks[j]
			if node.Type == model.WorkflowNodeDeploy {
				node.Config.ConfigurationFiles = nil
				node.Config.DeploymentParameters = ""
			}
		}
	}
	next, err := f.service.newResolvedWorkflowRun(ctx, f.application, &workflow, workflow.Source, "manual", "refs/heads/main", strings.Repeat("b", 40), actor.ID, "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	nextSnapshot, err := parseWorkflowSnapshot(next)
	if err != nil || len(nextSnapshot.ConfigurationBindings["deploy"]) != 0 || len(snapshot.ConfigurationBindings["deploy"]) != 1 {
		t.Fatal("新旧运行配置互相影响", err)
	}
}
