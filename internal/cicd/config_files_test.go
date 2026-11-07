package cicd

import (
	"edo/internal/model"
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigurationFilesFreezeOriginalVersionAndRemainDecoupled(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	original := "server:\n  token: literal$token\n"
	in := VariableInput{Name: "application.yaml", Kind: "config_yaml", Usage: "deploy", Value: &original, IsSecret: true, IsActive: true}
	saved, err := s.SaveVariable(ctx, "owner", "", in)
	if err != nil {
		t.Fatal(err)
	}
	refs := []model.DeploymentConfigFile{{ID: saved.ID, Path: "config/application.yaml"}}
	bindings, err := s.FreezeConfigFiles(ctx, scope.ProjectID, "app", "prod", "refs/heads/main", refs)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(bindings)
	if strings.Contains(string(encoded), "literal$") {
		t.Fatal("快照包含正文")
	}
	in.Value = pointer("server: updated\n")
	in.ExpectedVersion = saved.Version
	if _, err = s.SaveVariable(ctx, "owner", saved.ID, in); err != nil {
		t.Fatal(err)
	}
	files, _, err := s.ResolveConfigFiles(ctx, scope.ProjectID, "app", "prod", "refs/heads/main", bindings)
	if err != nil || files[refs[0].Path] != original {
		t.Fatal("没有恢复原始版本", err)
	}
	automatic, err := s.FreezeDeploymentVariables(ctx, scope.ProjectID, "app", "prod", "refs/heads/main", nil)
	if err != nil || len(automatic) != 0 {
		t.Fatal("配置文件被自动关联", err)
	}
	if _, err = s.FreezeVariables(ctx, scope.ProjectID, "app", "prod", "deploy", "refs/heads/main", []string{saved.Name}); err == nil {
		t.Fatal("配置被当作变量引用")
	}
	if err = s.DeleteVariable(ctx, saved.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ResolveConfigFiles(ctx, scope.ProjectID, "app", "prod", "refs/heads/main", bindings); err == nil {
		t.Fatal("删除后仍可使用")
	}
}
func TestConfigurationFilesRejectScopeAndPathConflicts(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	text := "PORT=1234\n"
	saved, err := s.SaveVariable(ctx, "owner", "", VariableInput{Name: ".env", Kind: "config_env", Usage: "deploy", Value: &text, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../escape", "/etc/../config", "~/../config", "a/../b", "a//b", ".edo/lock", "--flag", "a\nfile"} {
		if _, err = s.FreezeConfigFiles(ctx, scope.ProjectID, "app", "env", "main", []model.DeploymentConfigFile{{ID: saved.ID, Path: path}}); err == nil {
			t.Fatal("接受非法路径", path)
		}
	}
	for _, path := range []string{"/srv/app/.prod.env", "~/config/.prod.env", "config/.prod.env"} {
		if _, err = s.FreezeConfigFiles(ctx, scope.ProjectID, "app", "env", "main", []model.DeploymentConfigFile{{ID: saved.ID, Path: path}}); err != nil {
			t.Fatal("拒绝有效配置文件路径", path, err)
		}
	}
	for _, refs := range [][]model.DeploymentConfigFile{{{ID: saved.ID, Path: "a"}, {ID: saved.ID, Path: "a/b"}}, {{ID: saved.ID, Path: "a"}, {ID: saved.ID, Path: "a"}}} {
		if _, err = s.FreezeConfigFiles(ctx, scope.ProjectID, "app", "env", "main", refs); err == nil {
			t.Fatal("接受重叠映射")
		}
	}
	if _, err = s.FreezeConfigFiles(ctx, "other-project", "app", "env", "main", []model.DeploymentConfigFile{{ID: saved.ID, Path: ".env"}}); err == nil {
		t.Fatal("跨项目可用")
	}
	if err = s.db.Model(&model.DeliveryVariable{}).Where("uuid = ?", saved.ID).Updates(map[string]any{"application_id": "app-a", "environment_id": "prod"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, target := range [][2]string{{"app-b", "prod"}, {"app-a", "test"}} {
		if _, err = s.FreezeConfigFiles(ctx, scope.ProjectID, target[0], target[1], "main", []model.DeploymentConfigFile{{ID: saved.ID, Path: ".env"}}); err == nil {
			t.Fatal("跨应用或环境可用")
		}
	}
}

func TestConfigurationLatestUpdatesContentWithoutChangingDeployedSelection(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	original := "TOKEN=before\nREMOVED=yes\n"
	input := VariableInput{Name: ".prod.env", Kind: "config_env", Usage: "deploy", Value: &original, IsSecret: true, IsActive: true}
	saved, err := s.SaveVariable(ctx, "owner", "", input)
	if err != nil {
		t.Fatal(err)
	}
	refs := []model.DeploymentConfigFile{{ID: saved.ID, Path: "config/.prod.env"}}
	frozen, err := s.FreezeConfigFiles(ctx, scope.ProjectID, "app", "prod", "main", refs)
	if err != nil {
		t.Fatal(err)
	}
	changed := "TOKEN=after\nADDED=yes\n"
	input.Value = &changed
	input.ExpectedVersion = saved.Version
	if _, err = s.SaveVariable(ctx, "owner", saved.ID, input); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestConfigFiles(ctx, scope.ProjectID, "app", "prod", "main", frozen)
	if err != nil || latest[0].Version != 2 || latest[0].VariableID != saved.ID || latest[0].Path != refs[0].Path {
		t.Fatal("引用或路径改变", err)
	}
	files, redactions, err := s.ResolveConfigFiles(ctx, scope.ProjectID, "app", "prod", "main", latest)
	if err != nil || files[refs[0].Path] != changed || len(redactions) == 0 {
		t.Fatal("未采用最新正文", err)
	}
	old, _, err := s.ResolveConfigFiles(ctx, scope.ProjectID, "app", "prod", "main", frozen)
	if err != nil || old[refs[0].Path] != original {
		t.Fatal("改写了原发布版本", err)
	}
	if err = s.DeleteVariable(ctx, saved.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LatestConfigFiles(ctx, scope.ProjectID, "app", "prod", "main", frozen); err == nil {
		t.Fatal("撤销后仍刷新正文")
	}
}
