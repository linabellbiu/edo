package pipeline

import (
	"edo/internal/model"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestKubernetesPlanSnapshotUpdateKeepsLegacyConsumersAndNewSelectionsIndependent(t *testing.T) {
	s, app, _, base, source, stages := newReusableKubernetesOverrideFixture(t)
	ctx := deliveryTestContext()
	legacy := *app.Workflow
	legacy.Source, legacy.Stages, legacy.IsActive = source, cloneWorkflowStages(stages), true
	if err := s.db.Save(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	template := model.ReleaseWorkflowTemplate{ID: "pin-legacy-template", Name: "引用旧方案的流水线模板", Revision: 1, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: cloneWorkflowStages(stages), ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, CreatedBy: "admin", UpdatedBy: "admin"}
	if err := s.db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}
	next := model.DefaultKubernetesDeploymentSettings()
	next.CPURequest = "400m"
	next.MemoryRequest = "450Mi"
	next.ServiceType = "LoadBalancer"
	_, err := s.UpdateDeploymentPlan(ctx, base.ID, DeploymentPlanInput{Name: "新名称", Kind: base.Kind, Reusable: true, TimeoutSeconds: 300, ExecutionConfig: &model.DeploymentExecutionConfig{Kubernetes: next}})
	if err != nil {
		t.Fatal(err)
	}
	var stored model.ReleaseWorkflow
	if err = s.db.First(&stored, "uuid = ?", legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.db.First(&template, "uuid = ?", template.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, definition := range [][]model.WorkflowStage{stored.Stages, template.Stages} {
		pin := definition[len(definition)-1].Tasks[0].Config.DeploymentOverride.PlanSnapshot
		if pin == nil || pin.Name != base.Name || pin.TimeoutSeconds != 120 || pin.ExecutionConfig.Kubernetes.CPURequest != "200m" || pin.ExecutionConfig.Kubernetes.MemoryRequest != "300Mi" || pin.ExecutionConfig.Kubernetes.EffectiveServiceType() != "ClusterIP" {
			t.Fatal("库方案更新改掉了原引用默认参数", pin)
		}
	}
	if stored.Revision != legacy.Revision || template.Revision != 1 {
		t.Fatal("固定内部配置不应冒充用户编辑版本")
	}
	// 旧客户端重新保存时没有快照字段，也不能把已经固定的配置换成库方案新默认。
	clientStages := cloneWorkflowStages(stored.Stages)
	clientStages[len(clientStages)-1].Tasks[0].Config.DeploymentOverride.PlanSnapshot = nil
	clientStages[len(clientStages)-1].Tasks[0].Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"replicas":3}`)
	saved, err := s.SaveApplicationWorkflow(ctx, app.ID, stored.ID, "admin", WorkflowInput{Name: stored.Name, Revision: stored.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: clientStages, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	app.Workflow = saved.Workflow
	run, err := s.newResolvedWorkflowRun(ctx, app, app.Workflow, source, "manual", "refs/heads/main", strings.Repeat("c", 40), "admin", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	plan := frozen.DeploymentPlans["deploy"]
	if plan.Kubernetes.CPURequest != "200m" || plan.Kubernetes.MemoryRequest != "300Mi" || plan.Kubernetes.Replicas != 3 || plan.TimeoutSeconds != 180 || plan.SourcePlanID != base.ID {
		t.Fatal("实际运行没有使用阶段自己的完整配置", plan)
	}
	// 新任务重新选用方案，才取得这次更新后的默认配置。
	fresh := cloneWorkflowStages(stages)
	fresh[len(fresh)-1].Tasks[0].ID = "new-deploy"
	if err = s.freezeWorkflowPlanDefaults(ctx, s.db, fresh); err != nil {
		t.Fatal(err)
	}
	pin := fresh[len(fresh)-1].Tasks[0].Config.DeploymentOverride.PlanSnapshot
	if pin.ExecutionConfig.Kubernetes.CPURequest != "400m" || pin.TimeoutSeconds != 300 {
		t.Fatal("新选择没有使用当前方案配置", pin)
	}
}

func TestKubernetesPlanSnapshotCannotBeReplacedThroughOmittedOrForgedBase(t *testing.T) {
	s, app, _, _, source, stages := newReusableKubernetesOverrideFixture(t)
	ctx := deliveryTestContext()
	saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []bool{false, true} {
		client := cloneWorkflowStages(saved.Workflow.Stages)
		override := client[len(client)-1].Tasks[0].Config.DeploymentOverride
		if missing {
			override.PlanSnapshot = nil
		} else {
			override.PlanSnapshot.ExecutionConfig.Kubernetes.CPURequest = "999m"
		}
		next, err := s.SaveApplicationWorkflow(ctx, app.ID, saved.Workflow.ID, "admin", WorkflowInput{Name: saved.Workflow.Name, Revision: saved.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: client, Activate: true})
		if err != nil {
			t.Fatal(err)
		}
		if next.Workflow.Stages[len(client)-1].Tasks[0].Config.DeploymentOverride.PlanSnapshot.ExecutionConfig.Kubernetes.CPURequest != "200m" {
			t.Fatal("未显式更换来源就覆盖了初始快照")
		}
		saved = next
	}
}
