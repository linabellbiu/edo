package pipeline

import (
	"context"
	"errors"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestKubernetesTemplateConfigurationPermissionRevocationAndDefaults(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	cfg := model.DefaultKubernetesDeploymentSettings()
	if err := s.validateKubernetesTemplateConfiguration(context.Background(), db, cfg); !errors.Is(err, ErrKubernetesTemplatePermission) {
		t.Fatal("无操作者仍能提交模板配置", err)
	}
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	for _, row := range []any{&model.User{ID: "template-editor", Username: "template-editor", IsActive: true, DepartmentID: database.DefaultDepartmentID}, &model.Role{ID: "template-role", Name: "template-role", DisplayName: "模板配置"}, &model.ProjectMember{ProjectID: project, UserID: "template-editor", RoleID: "template-role"}, &model.RolePermission{RoleID: "template-role", Permission: "kubernetes.workload.update"}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project, DepartmentID: database.DefaultDepartmentID, UserID: "template-editor"})
	if err := s.validateKubernetesTemplateConfiguration(ctx, db, cfg); err != nil {
		t.Fatal("工作负载修改权限不能保存默认模板", err)
	}
	cfg.NodePort = true
	cfg.Ports[0].Enabled = true
	if err := s.validateKubernetesTemplateConfiguration(ctx, db, cfg); !errors.Is(err, ErrKubernetesTemplatePermission) {
		t.Fatal("缺少网络权限仍开启 NodePort", err)
	}
	if err := db.Create(&model.RolePermission{RoleID: "template-role", Permission: "kubernetes.network.update"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.validateKubernetesTemplateConfiguration(ctx, db, cfg); err != nil {
		t.Fatal("拥有两个权限仍被阻断", err)
	}
	if err := db.Create(&model.UserPermission{UserID: "template-editor", Permission: "kubernetes.workload.update", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.validateKubernetesTemplateConfiguration(ctx, db, cfg); !errors.Is(err, ErrKubernetesTemplatePermission) {
		t.Fatal("显式拒绝后仍能修改模板", err)
	}
}
func TestKubernetesTemplateRejectsDuplicateWorkloadsWithinPipeline(t *testing.T) {
	stages := []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{{ID: "a", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: "one"}}, {ID: "b", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: "two"}}}}}
	plans := map[string]model.DeploymentPlan{"one": {ID: "one", DeploymentTargetID: "first"}, "two": {ID: "two", DeploymentTargetID: "second"}}
	target := model.DeploymentTarget{Platform: model.DeploymentKubernetes, RuntimeID: "cluster", Namespace: "application", WorkloadName: "api"}
	targets := map[string]model.DeploymentTarget{"first": target, "second": target}
	if issues := checkDuplicateKubernetesTargets(stages, plans, targets); len(issues) != 2 || issues[0].Code != "duplicate_kubernetes_target" || issues[1].Code != "duplicate_kubernetes_target" || issues[0].NodeID != "a" || issues[1].NodeID != "b" {
		t.Fatal("未拒绝重复集群目标", issues)
	}
	target.Namespace = "other"
	targets["second"] = target
	if issues := checkDuplicateKubernetesTargets(stages, plans, targets); len(issues) != 0 {
		t.Fatal("不同命名空间被当成重复", issues)
	}
}
