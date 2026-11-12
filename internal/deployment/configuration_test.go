package deployment

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/configfile"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/secret"
)

func attachConfigurationFixture(t *testing.T, f *containerControlFixture, custom ...cicd.VariableInput) (*cicd.VariableView, cicd.VariableInput) {
	t.Helper()
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("c", 32))))
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.service.ConfigureReleaseVariables(manager, permissions)
	configureDeploymentAuthorizationTestActor(t, f.db, f.record.ProjectID, f.record.DepartmentID, "operator", access.PermissionContainerRestart, access.PermissionContainerRecreate, "delivery_variable.execute")
	value := "TOKEN=original$token\nREMOVED=true\n"
	input := cicd.VariableInput{Name: ".prod.env", Kind: "config_env", Usage: "deploy", Value: &value, IsSecret: true, IsActive: true}
	if len(custom) > 0 {
		input = custom[0]
	}
	saved, err := f.service.variables.service.SaveVariable(f.ctx, "operator", "", input)
	if err != nil {
		t.Fatal(err)
	}
	refs := []model.DeploymentConfigFile{{ID: saved.ID, Path: "config/.prod.env"}}
	bindings, err := f.service.variables.service.FreezeConfigFiles(f.ctx, f.record.ProjectID, f.record.ApplicationID, f.record.EnvironmentID, "main", refs)
	if err != nil {
		t.Fatal(err)
	}
	var run model.PipelineRun
	if err = f.db.First(&run, "uuid = ?", f.record.PipelineRunID).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]json.RawMessage
	if err = json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["configuration_bindings"], _ = json.Marshal(map[string]any{"deploy": bindings})
	var stages []model.WorkflowStage
	if err = json.Unmarshal(snapshot["stages"], &stages); err != nil {
		t.Fatal(err)
	}
	stages[0].Tasks[0].Config.ConfigurationFiles = refs
	stages[0].Tasks[0].Config.DeploymentParameters = "--env-file config/.prod.env"
	snapshot["stages"], _ = json.Marshal(stages)
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "original$token") {
		t.Fatal("运行快照包含密钥")
	}
	if err = f.db.Model(&run).Update("workflow_snapshot", string(encoded)).Error; err != nil {
		t.Fatal(err)
	}
	f.record.UsesDeliveryVariables = true
	if err = f.db.Save(&f.record).Error; err != nil {
		t.Fatal(err)
	}
	return saved, input
}

func TestConfigurationLogRedactionsDistinguishOrdinaryValuesAndSecrets(t *testing.T) {
	for _, isSecret := range []bool{false, true} {
		name := "ordinary"
		if isSecret {
			name = "secret"
		}
		t.Run(name, func(t *testing.T) {
			f := newContainerControlFixture(t)
			content := "IMAGE_NAMESPACE=edo-demo\nIMAGE_VERSION=v0.0.1\nDB_PASSWORD=configuration-private-value\n"
			attachConfigurationFixture(t, f, cicd.VariableInput{Name: ".prod.env", Kind: "config_env", Usage: "deploy", Value: &content, IsSecret: isSecret, IsActive: true})
			set, err := f.service.ResolveDeploymentConfiguration(f.ctx, &f.record, false)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(set.Redactions, "edo-demo") != isSecret || slices.Contains(set.Redactions, "v0.0.1") != isSecret {
				t.Fatal("普通镜像信息与显式密钥未区分")
			}
			if !slices.Contains(set.Redactions, "configuration-private-value") || !slices.Contains(set.Redactions, content) || !slices.Contains(set.Redactions, "IMAGE_NAMESPACE=edo-demo") {
				t.Fatal("敏感变量或配置正文未纳入脱敏")
			}
		})
	}
}

func TestConfigurationRestartUsesLatestContentsAndInvalidatesConfirmation(t *testing.T) {
	f := newContainerControlFixture(t)
	saved, input := attachConfigurationFixture(t, f)
	id := strings.Repeat("a", 64)
	frozen, err := f.service.ResolveDeploymentConfiguration(f.ctx, &f.record, false)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Values["TOKEN"] != "original$token" || !strings.HasPrefix(frozen.Directory, "~/edo/deply/") {
		t.Fatal("原配置不正确")
	}
	first, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, id, "operator", "restart")
	if err != nil || first.EffectiveAction != "recreate" || len(first.Containers) != 2 {
		t.Fatal("未确认全部受影响副本", first, err)
	}
	changed := "TOKEN=new$token\nADDED=true\n"
	input.Value = &changed
	input.ExpectedVersion = saved.Version
	if _, err = f.service.variables.service.SaveVariable(f.ctx, "operator", saved.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.ControlContainer(f.ctx, f.record.ID, id, "operator", "restart", ContainerControlConfirmation{Digest: first.Digest}); !errors.Is(err, dockerengine.ErrContainerControlChanged) || f.writes.Load() != 0 {
		t.Fatal("正文变化后复用了旧确认", err)
	}
	latest, err := f.service.ResolveDeploymentConfiguration(f.ctx, &f.record, true)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Values["TOKEN"] != "new$token" || latest.Values["ADDED"] != "true" || latest.Values["REMOVED"] != "" || latest.Directory != frozen.Directory {
		t.Fatal("最新正文或原路径不正确")
	}
	if latest.Bindings[0].VariableID != frozen.Bindings[0].VariableID || latest.Bindings[0].Version != 2 {
		t.Fatal("采用了其他配置引用")
	}
	if err = f.service.authorizeConfigurationControl(f.ctx, &f.record, "operator", "recreate", frozen); !errors.Is(err, dockerengine.ErrContainerControlChanged) {
		t.Fatal("执行前没有复核最新版本", err)
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "original$token") {
		t.Fatal("确认接口泄漏了正文")
	}
	if configfile.RevisionDigest(latest.Bindings) == configfile.RevisionDigest(frozen.Bindings) {
		t.Fatal("版本摘要未变化")
	}
}
func TestConfigurationRestartRequiresRecreatePermissionWhenContentChanged(t *testing.T) {
	f := newContainerControlFixture(t)
	attachConfigurationFixture(t, f)
	if err := f.db.Create(&model.UserPermission{UserID: "operator", Permission: access.PermissionContainerRecreate, Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, strings.Repeat("a", 64), "operator", "restart"); !errors.Is(err, ErrExecutionAuthorization) || f.writes.Load() != 0 {
		t.Fatal("重启绕过了重建权限", err)
	}
}

func TestConfigurationUnchangedRestartKeepsSingleContainerAndUsesCurrentPermission(t *testing.T) {
	f := newContainerControlFixture(t)
	attachConfigurationFixture(t, f)
	latest, err := f.service.ResolveDeploymentConfiguration(f.ctx, &f.record, true)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	for id := range f.states {
		f.configurationRevisions[id] = configfile.RevisionDigest(latest.Bindings)
	}
	f.mu.Unlock()
	// 已应用最新内容后，只拥有重启权限仍可执行普通重启，不必再次重建。
	if err = f.db.Create(&model.UserPermission{UserID: "operator", Permission: access.PermissionContainerRecreate, Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	plan, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, id, "operator", "restart")
	if err != nil || plan.EffectiveAction != "" || plan.ConfigurationChanged || len(plan.Containers) != 1 {
		t.Fatal("未修改内容也触发重建", plan, err)
	}
	result, err := f.service.ControlContainer(f.ctx, f.record.ID, id, "operator", "restart", ContainerControlConfirmation{Digest: plan.Digest})
	if err != nil || result.Runtime == nil || f.writes.Load() != 1 {
		t.Fatal("普通单容器重启未执行", err)
	}
}

func TestConfigurationEmptyContentsRemovePreviouslyProvidedKeys(t *testing.T) {
	f := newContainerControlFixture(t)
	saved, input := attachConfigurationFixture(t, f)
	empty := "# all keys removed\n"
	input.Value = &empty
	input.ExpectedVersion = saved.Version
	if _, err := f.service.variables.service.SaveVariable(f.ctx, "operator", saved.ID, input); err != nil {
		t.Fatal(err)
	}
	latest, err := f.service.ResolveDeploymentConfiguration(f.ctx, &f.record, true)
	if err != nil || len(latest.Values) != 0 || latest.Files["config/.prod.env"] != empty {
		t.Fatal("空 ENV 不能清除旧键", err)
	}
}
func TestConfigurationLegacyVariableRestartDoesNotGainNewPermissionRequirement(t *testing.T) {
	f := newContainerControlFixture(t)
	f.record.UsesDeliveryVariables = true
	if err := f.db.Save(&f.record).Error; err != nil {
		t.Fatal(err)
	}
	configureDeploymentAuthorizationTestActor(t, f.db, f.record.ProjectID, f.record.DepartmentID, "operator", access.PermissionContainerRestart)
	plan, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, strings.Repeat("a", 64), "operator", "restart")
	if err != nil || len(plan.Containers) != 1 || plan.EffectiveAction != "" {
		t.Fatal("旧变量实例的普通重启行为改变", err)
	}
}

func TestConfigurationDirectPathsUseFrozenDeploymentDirectory(t *testing.T) {
	f := newContainerControlFixture(t)
	attachConfigurationFixture(t, f)
	var run model.PipelineRun
	if err := f.db.First(&run, "uuid = ?", f.record.PipelineRunID).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["direct_configuration_paths"] = json.RawMessage("true")
	data, _ := json.Marshal(snapshot)
	if err := f.db.Model(&run).Update("workflow_snapshot", string(data)).Error; err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{f.record.WorkingDirectory, ""} {
		record := f.record
		record.WorkingDirectory = directory
		set, err := f.service.ResolveDeploymentConfiguration(f.ctx, &record, false)
		if err != nil {
			t.Fatal(err)
		}
		expected := directory
		if expected == "" {
			expected = "~"
		}
		if set.Directory != expected || set.Bindings[0].Path != "config/.prod.env" {
			t.Fatal("实际目录或文件路径被额外拼接", set.Directory)
		}
		latest, err := f.service.ResolveDeploymentConfiguration(f.ctx, &record, true)
		if err != nil || latest.Directory != set.Directory {
			t.Fatal("重启重建改变了配置路径", err)
		}
	}
}

func TestApplicationConfigurationPathSurvivesRenameAndRejectsAbsoluteReferences(t *testing.T) {
	f := newContainerControlFixture(t)
	attachConfigurationFixture(t, f)
	var run model.PipelineRun
	if err := f.db.First(&run, "uuid = ?", f.record.PipelineRunID).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["application_config_directory"], _ = json.Marshal("~/edo/container_control/config")
	save := func() {
		t.Helper()
		data, _ := json.Marshal(snapshot)
		if err := f.db.Model(&run).Update("workflow_snapshot", string(data)).Error; err != nil {
			t.Fatal(err)
		}
	}
	save()
	if err := f.db.Model(&model.Application{}).Where("uuid = ?", f.record.ApplicationID).Update("name", "renamed_application").Error; err != nil {
		t.Fatal(err)
	}
	record := f.record
	record.WorkingDirectory = "/different/workspace"
	for _, latest := range []bool{false, true} {
		set, err := f.service.ResolveDeploymentConfiguration(f.ctx, &record, latest)
		if err != nil || set.Directory != "~/edo/container_control/config" || set.Bindings[0].Path != "config/.prod.env" {
			t.Fatal("改名或工作目录改变了原配置位置", set, err)
		}
	}
	// 新目录模式必须二次核对冻结引用，不能用绝对路径绕过新流程的输入校验。
	var stages []model.WorkflowStage
	var bindings map[string][]model.DeploymentConfigBinding
	if err := json.Unmarshal(snapshot["stages"], &stages); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(snapshot["configuration_bindings"], &bindings); err != nil {
		t.Fatal(err)
	}
	stages[0].Tasks[0].Config.ConfigurationFiles[0].Path = "/outside/.prod.env"
	bindings["deploy"][0].Path = "/outside/.prod.env"
	snapshot["stages"], _ = json.Marshal(stages)
	snapshot["configuration_bindings"], _ = json.Marshal(bindings)
	save()
	if set, err := f.service.ResolveDeploymentConfiguration(f.ctx, &record, true); err == nil || set != nil {
		t.Fatal("接受应用配置目录外的引用")
	}
}
