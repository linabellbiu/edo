package deployment

import (
	"encoding/json"
	"fmt"
	"testing"

	"edo/internal/model"
)

func TestKubernetesLibraryStageOverrideRollbackUsesSourceAndStableTarget(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(fmt.Sprint("fixed=", fixed), func(t *testing.T) {
			f := newKubernetesRollbackFixture(t, model.DeploymentSucceeded)
			ctx := deploymentExecutionTestContext("project-a")
			const workflowID = "library-workflow"
			planID, targetID := model.InlineDeploymentIdentity(workflowID, f.source.WorkflowNodeID)
			var base model.DeploymentPlan
			if err := f.db.First(&base, "uuid = ?", f.source.DeploymentPlanID).Error; err != nil {
				t.Fatal(err)
			}
			sourceID := base.ID
			if fixed {
				targetID = base.DeploymentTargetID
			} else {
				base.DeploymentTargetID = ""
			}
			base.ExecutionConfig = &model.DeploymentExecutionConfig{Kubernetes: model.DefaultKubernetesDeploymentSettings()}
			base.TimeoutSeconds = 120
			if err := f.db.Save(&base).Error; err != nil {
				t.Fatal(err)
			}
			binding := &model.WorkflowDeploymentOverride{DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentKubernetes, RuntimeID: f.source.RuntimeID, Namespace: f.source.Namespace, WorkloadName: f.source.WorkloadName, ContainerName: f.source.ContainerName}, Kubernetes: json.RawMessage(`{"replicas":2}`)}
			workflow := model.ReleaseWorkflow{ID: workflowID, ApplicationID: f.source.ApplicationID, ProjectID: f.source.ProjectID, DepartmentID: f.source.DepartmentID, Name: "库方案覆盖回退", SchemaVersion: model.WorkflowSchemaVersion, Stages: []model.WorkflowStage{{ID: "deploy", Name: "部署", Tasks: []model.WorkflowNode{{ID: f.source.WorkflowNodeID, Type: model.WorkflowNodeDeploy, Name: "部署", Config: model.WorkflowNodeConfig{DeploymentPlanID: sourceID, DeploymentSource: "plan", DeploymentOverride: binding}}}}}}
			if err := f.db.Create(&workflow).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := json.Marshal(map[string]any{"authority_id": "operator", "deployment_plans": map[string]any{f.source.WorkflowNodeID: map[string]any{"id": planID, "source_plan_id": sourceID}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.source.PipelineRunID).Updates(map[string]any{"workflow_id": workflowID, "workflow_snapshot": string(snapshot)}).Error; err != nil {
				t.Fatal(err)
			}
			if err = f.db.Model(&model.DeploymentRecord{}).Where("uuid = ?", f.source.ID).Updates(map[string]any{"deployment_plan_id": planID, "target_id": targetID}).Error; err != nil {
				t.Fatal(err)
			}
			f.source.DeploymentPlanID, f.source.TargetID = planID, targetID
			preview, err := f.service.RollbackPreview(ctx, f.source.ID)
			if err != nil || !preview.CanRollback {
				t.Fatalf("通用方案覆盖的真实目标无法预检回退: %+v %v", preview, err)
			}
			binding.DeploymentTarget.WorkloadName = "changed-target"
			if err = f.db.Save(&workflow).Error; err != nil {
				t.Fatal(err)
			}
			preview, err = f.service.RollbackPreview(ctx, f.source.ID)
			if err != nil || preview.CanRollback {
				t.Fatalf("阶段目标变更后仍允许回退原位置: %+v %v", preview, err)
			}
			if f.writes != 0 {
				t.Fatal("回退预览产生集群写入")
			}

		})
	}
}

func TestWorkflowLibraryPlanRejectsChangingTemplateModeAndKeepsBaseUntouched(t *testing.T) {
	f := newKubernetesRollbackFixture(t, model.DeploymentSucceeded)
	ctx := deploymentExecutionTestContext("project-a")
	base := model.DeploymentPlan{ID: "base", Name: "库方案", Kind: model.DeploymentPlanKubernetes, TimeoutSeconds: 120, ExecutionConfig: &model.DeploymentExecutionConfig{Kubernetes: model.DefaultKubernetesDeploymentSettings()}}
	target := &model.DeploymentTarget{Platform: model.DeploymentKubernetes, RuntimeID: f.source.RuntimeID, Namespace: f.source.Namespace, WorkloadName: f.source.WorkloadName, ContainerName: f.source.ContainerName}
	_, _, err := f.service.ResolveWorkflowLibraryPlan(ctx, base, nil, &model.WorkflowDeploymentOverride{DeploymentTarget: target, Kubernetes: json.RawMessage(`{"template":"other"}`)}, "workflow", "node")
	if err == nil {
		t.Fatal("阶段能够改变方案模板模式")
	}
	if base.ExecutionConfig.Kubernetes.Replicas != 1 || base.DeploymentTargetID != "" {
		t.Fatal("错误解析污染了原方案")
	}
	plain := base
	plain.ExecutionConfig = nil
	if _, _, err = f.service.ResolveWorkflowLibraryPlan(ctx, plain, nil, &model.WorkflowDeploymentOverride{DeploymentTarget: target, Kubernetes: json.RawMessage(`{"replicas":2}`)}, "workflow", "node"); err == nil {
		t.Fatal("旧镜像更新方案被阶段自动转换成模板")
	}
}

func TestFixedKubernetesLibraryOverrideKeepsExistingTargetAndApprovalScope(t *testing.T) {
	f := newKubernetesRollbackFixture(t, model.DeploymentSucceeded)
	ctx := deploymentExecutionTestContext("project-a")
	var base model.DeploymentPlan
	if err := f.db.First(&base, "uuid = ?", f.source.DeploymentPlanID).Error; err != nil {
		t.Fatal(err)
	}
	var target model.DeploymentTarget
	if err := f.db.First(&target, "uuid = ?", base.DeploymentTargetID).Error; err != nil {
		t.Fatal(err)
	}
	environment := model.Environment{ID: "guard-environment", Name: "原审批范围", IsActive: true}
	if err := f.db.WithContext(ctx).Create(&environment).Error; err != nil {
		t.Fatal(err)
	}
	target.EnvironmentID = environment.ID
	base.ExecutionConfig = &model.DeploymentExecutionConfig{Kubernetes: model.DefaultKubernetesDeploymentSettings()}
	base.TimeoutSeconds = 120
	binding := &model.DeploymentTarget{Platform: target.Platform, RuntimeID: target.RuntimeID, Namespace: target.Namespace, WorkloadName: target.WorkloadName, ContainerName: target.ContainerName}
	override := &model.WorkflowDeploymentOverride{DeploymentTarget: binding, Kubernetes: json.RawMessage(`{"replicas":2}`)}
	effective, resolved, err := f.service.ResolveWorkflowLibraryPlan(ctx, base, &target, override, "fixed-workflow", "deploy")
	if err != nil || resolved.ID != target.ID || effective.ID == base.ID || effective.DeploymentTargetID != target.ID {
		t.Fatal("仅修改副本却改掉已部署目标身份", effective, resolved, err)
	}
	if resolved.EnvironmentID != target.EnvironmentID || resolved.HostID != target.HostID {
		t.Fatal("固定方案原审批及变量范围丢失")
	}
	binding.WorkloadName = "another-workload"
	_, moved, err := f.service.ResolveWorkflowLibraryPlan(ctx, base, &target, override, "fixed-workflow", "deploy")
	if err != nil || moved.ID == target.ID {
		t.Fatal("实际工作负载改变却保留旧目标身份", moved, err)
	}
	if target.WorkloadName != f.source.WorkloadName || base.ExecutionConfig.Kubernetes.Replicas != 1 {
		t.Fatal("解析阶段覆盖污染固定方案")
	}
}
