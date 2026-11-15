package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func kubernetesServiceTypeEditor(t *testing.T, s *Service, projectID, departmentID string) context.Context {
	t.Helper()
	for _, row := range []any{
		&model.User{ID: "service-type-editor", Username: "service_type_editor", IsActive: true, DepartmentID: departmentID},
		&model.Role{ID: "service-type-role", Name: "service-type-role", DisplayName: "服务配置编辑"},
		&model.ProjectMember{ProjectID: projectID, UserID: "service-type-editor", RoleID: "service-type-role"},
		&model.RolePermission{RoleID: "service-type-role", Permission: "workflow.update"},
		&model.RolePermission{RoleID: "service-type-role", Permission: "kubernetes.workload.update"},
	} {
		if err := s.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: projectID, DepartmentID: departmentID, UserID: "service-type-editor"})
}

func TestKubernetesServiceTypePatchRequiresCurrentNetworkPermission(t *testing.T) {
	s, _, _, _ := newPipelineTestService(t)
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	ctx := kubernetesServiceTypeEditor(t, s, projectID, database.DefaultDepartmentID)
	for _, patch := range []string{`{"service_type":"LoadBalancer"}`, `{"service_type":"ClusterIP"}`, `{"service_type":null}`, `{"external_name":"api.example.com"}`, `{"external_name":null}`, `{"ports":[]}`} {
		if err := s.validateDeploymentOverridePatchPermission(ctx, s.db, &model.WorkflowDeploymentOverride{Kubernetes: json.RawMessage(patch)}); !errors.Is(err, ErrKubernetesTemplatePermission) {
			t.Fatal("服务类型、DNS 或删除补丁绕过网络修改权限", patch, err)
		}
	}
	if err := s.db.Create(&model.RolePermission{RoleID: "service-type-role", Permission: "kubernetes.network.update"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.validateDeploymentOverridePatchPermission(ctx, s.db, &model.WorkflowDeploymentOverride{Kubernetes: json.RawMessage(`{"service_type":"ExternalName","external_name":"api.example.com"}`)}); err != nil {
		t.Fatal("有网络修改权限仍无法编辑服务类型", err)
	}
	if err := s.db.Create(&model.UserPermission{UserID: "service-type-editor", Permission: "kubernetes.network.update", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.validateDeploymentOverridePatchPermission(ctx, s.db, &model.WorkflowDeploymentOverride{Kubernetes: json.RawMessage(`{"external_name":"other.example.com"}`)}); !errors.Is(err, ErrKubernetesTemplatePermission) {
		t.Fatal("个人拒绝后仍沿用旧权限", err)
	}
}

func TestKubernetesServiceTypeNetworkChangesAreExcludedFromWorkloadComparison(t *testing.T) {
	before := &model.DeploymentPlan{ExecutionConfig: &model.DeploymentExecutionConfig{Kubernetes: model.DefaultKubernetesDeploymentSettings()}}
	after := &model.DeploymentPlan{ExecutionConfig: cloneDeploymentExecutionConfig(before.ExecutionConfig)}
	after.ExecutionConfig.Kubernetes.ServiceType = "ExternalName"
	after.ExecutionConfig.Kubernetes.ExternalName = "api.example.com"
	after.ExecutionConfig.Kubernetes.Ports = nil
	if kubernetesWorkloadSettingsDiffer(*before, after) || !kubernetesNetworkSettingsDiffer(*before, after) {
		t.Fatal("服务类型被混作工作负载参数或没有识别网络变化")
	}
	after.ExecutionConfig.Kubernetes.Replicas = 2
	if !kubernetesWorkloadSettingsDiffer(*before, after) {
		t.Fatal("服务类型字段剔除同时忽略了真实副本变化")
	}
}

func TestKubernetesServiceTypeSaveCannotRestoreRemoveOrSwitchAfterNetworkRevocation(t *testing.T) {
	s, app, _, _, source, stages := newReusableKubernetesOverrideFixture(t)
	adminCtx := deliveryTestContext()
	stages[len(stages)-1].Tasks[0].Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"service_type":"ExternalName","external_name":"api.example.com"}`)
	saved, err := s.SaveWorkflow(adminCtx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := kubernetesServiceTypeEditor(t, s, app.ProjectID, app.DepartmentID)
	for _, mutation := range []string{"恢复默认", "清除覆盖", "切回ClusterIP", "更换DNS", "删除端口并改类型", "删除任务", "切换来源"} {
		t.Run(mutation, func(t *testing.T) {
			next := cloneWorkflowStages(saved.Workflow.Stages)
			task := &next[len(next)-1].Tasks[0]
			switch mutation {
			case "恢复默认":
				task.Config.DeploymentOverride.Kubernetes = nil
			case "清除覆盖":
				task.Config.DeploymentOverride = nil
			case "切回ClusterIP":
				task.Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"service_type":"ClusterIP"}`)
			case "更换DNS":
				task.Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"service_type":"ExternalName","external_name":"other.example.com"}`)
			case "删除端口并改类型":
				task.Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"service_type":"ClusterIP","ports":[]}`)
			case "删除任务":
				next = next[:len(next)-1]
			case "切换来源":
				target := *task.Config.DeploymentOverride.DeploymentTarget
				task.Config.DeploymentPlanID, task.Config.DeploymentSource = "", "inline"
				task.Config.DeploymentOverride = nil
				task.Config.InlineDeploymentPlan = &model.WorkflowDeploymentPlan{Name: task.Name, Kind: model.DeploymentPlanKubernetes, TimeoutSeconds: 120, DeploymentTarget: &target, ExecutionConfig: &model.DeploymentExecutionConfig{Kubernetes: model.DefaultKubernetesDeploymentSettings()}}
			}
			if _, err := s.SaveApplicationWorkflow(ctx, app.ID, saved.Workflow.ID, "service-type-editor", WorkflowInput{Name: saved.Workflow.Name, Revision: saved.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: next, Activate: false}); !errors.Is(err, ErrKubernetesTemplatePermission) {
				t.Fatal("服务类型网络撤权后仍可保存变更", mutation, err)
			}
		})
	}
	var retained model.ReleaseWorkflow
	if err := s.db.First(&retained, "uuid = ?", saved.Workflow.ID).Error; err != nil || retained.Revision != saved.Workflow.Revision || string(retained.Stages[len(retained.Stages)-1].Tasks[0].Config.DeploymentOverride.Kubernetes) != `{"service_type":"ExternalName","external_name":"api.example.com"}` {
		t.Fatal("权限拒绝后仍更新了流水线", retained, err)
	}
}

func TestKubernetesServiceTypeDeletingAllPortsRequiresNetworkPermission(t *testing.T) {
	s, app, _, base, _, stages := newReusableKubernetesOverrideFixture(t)
	ctx := kubernetesServiceTypeEditor(t, s, app.ProjectID, app.DepartmentID)
	before := cloneWorkflowStages(stages)
	after := cloneWorkflowStages(stages)
	before[len(before)-1].Tasks[0].Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"service_type":"ClusterIP"}`)
	after[len(after)-1].Tasks[0].Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"service_type":"ClusterIP","ports":[]}`)
	if err := s.validateWorkflowNetworkChanges(ctx, s.db, before, after); !errors.Is(err, ErrKubernetesTemplatePermission) {
		t.Fatal("ClusterIP 删除全部端口被当作默认值而绕过权限", err)
	}
	plan := &model.DeploymentPlan{ExecutionConfig: cloneDeploymentExecutionConfig(base.ExecutionConfig)}
	plan.ExecutionConfig.Kubernetes.ServiceType = "NodePort"
	legacy := &model.DeploymentPlan{ExecutionConfig: cloneDeploymentExecutionConfig(base.ExecutionConfig)}
	legacy.ExecutionConfig.Kubernetes.NodePort = true
	if err := s.validateDeploymentOverrideNetworkPermission(ctx, s.db, *legacy, plan); err != nil {
		t.Fatal("显式 NodePort 与旧开关同义仍要求重新授权", err)
	}
}
