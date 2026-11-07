package pipeline

import (
	"context"
	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/database"
	"errors"
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
	if strings.Contains(content+writes, "literal$token") || !strings.Contains(content, "${EDO_APP_CONFIG_PATH}/config/.prod.env") || !strings.Contains(content, "format: raw") || !strings.Contains(content, "existing: preserved") {
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
	if !strings.Contains(script, `--env-file "${EDO_APP_CONFIG_PATH:?缺少应用配置目录}/"'config/.prod.env'`) || strings.Contains(script, original) || strings.Contains(script, changed) {
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

func TestComposeConfigurationPreservesExplicitAbsoluteAndHomePaths(t *testing.T) {
	abs := "/srv/shared/.prod.env"
	home := "~/settings/app.env"
	bindings := []model.DeploymentConfigBinding{
		{DeliveryVariableBinding: model.DeliveryVariableBinding{VariableID: "absolute", Kind: "config_env"}, Path: abs},
		{DeliveryVariableBinding: model.DeliveryVariableBinding{VariableID: "home", Kind: "config_env"}, Path: home},
	}
	set := &deployment.ConfigurationSet{Directory: "/srv/app", Bindings: bindings}
	content, _, err := composeConfiguration("services:\n  api:\n    image: alpine:3.22\n", set)
	if err != nil || !strings.Contains(content, abs) || !strings.Contains(content, "${EDO_CONFIGURATION_HOME}/settings/app.env") || strings.Contains(content, "${EDO_CONFIGURATION_DIRECTORY}//") {
		t.Fatal("Compose 改变了指定文件位置", err)
	}
	args, err := configurationComposeArguments(model.WorkflowNodeConfig{
		ConfigurationFiles:   []model.DeploymentConfigFile{{ID: "absolute", Path: abs}, {ID: "home", Path: home}},
		DeploymentParameters: "--env-file " + abs + " --env-file " + home,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-se")
	cmd.Env = append(os.Environ(), "HOME=/home/target", "EDO_CONFIGURATION_DIRECTORY=/srv/app")
	cmd.Stdin = strings.NewReader("set -- " + args + "\nprintf '%s\\n' \"$@\"\n")
	output, err := cmd.Output()
	if err != nil || string(output) != "--env-file\n/srv/shared/.prod.env\n--env-file\n/home/target/settings/app.env\n" {
		t.Fatal("Compose 参数未在目标主机解析主目录", err)
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
	if err != nil || snapshot.ApplicationConfigDirectory != "~/edo/"+f.application.Name+"/config" || !snapshot.DirectConfigurationPaths || len(snapshot.ConfigurationBindings["deploy"]) != 1 || snapshot.ConfigurationBindings["deploy"][0].Version != 1 || strings.Contains(run.WorkflowSnapshot, value) {
		t.Fatal("配置未正确冻结或泄漏正文", err)
	}
	renamed := *f.application
	renamed.Name = "renamed_application"
	renamedRun, err := f.service.newResolvedWorkflowRun(ctx, &renamed, &workflow, workflow.Source, "manual", "refs/heads/main", strings.Repeat("c", 40), actor.ID, "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	renamedSnapshot, err := parseWorkflowSnapshot(renamedRun)
	if err != nil || renamedSnapshot.ApplicationConfigDirectory != "~/edo/renamed_application/config" || snapshot.ApplicationConfigDirectory == renamedSnapshot.ApplicationConfigDirectory {
		t.Fatal("下一次部署未采用新应用名称", err)
	}
	for _, invalid := range []string{"/srv/app/.prod.env", "~/config/.prod.env", "../.prod.env"} {
		bad := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{ConfigurationFiles: []model.DeploymentConfigFile{{ID: saved.ID, Path: invalid}}}}
		if err := sanitizeWorkflowNode(&bad); err == nil {
			t.Fatal("接受新配置的非相对路径", invalid)
		}
		badWorkflow := workflow
		badWorkflow.Stages = cloneWorkflowStages(workflow.Stages)
		for i := range badWorkflow.Stages {
			for j := range badWorkflow.Stages[i].Tasks {
				if badWorkflow.Stages[i].Tasks[j].Type == model.WorkflowNodeDeploy {
					badWorkflow.Stages[i].Tasks[j].Config.ConfigurationFiles[0].Path = invalid
				}
			}
		}
		if badRun, err := f.service.newResolvedWorkflowRun(ctx, f.application, &badWorkflow, badWorkflow.Source, "manual", "refs/heads/main", strings.Repeat("d", 40), actor.ID, "", time.Now().UTC()); badRun != nil || !errors.Is(err, ErrInvalidWorkflow) {
			t.Fatal("开始部署时未拒绝非相对路径，或错误未归为配置校验失败", invalid, err)
		}
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
	if err != nil || nextSnapshot.ApplicationConfigDirectory != "" || len(nextSnapshot.ConfigurationBindings["deploy"]) != 0 || len(snapshot.ConfigurationBindings["deploy"]) != 1 {
		t.Fatal("新旧运行配置互相影响", err)
	}
}
