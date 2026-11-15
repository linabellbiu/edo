package pipeline

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
)

func overrideTestPointer[T any](value T) *T { return &value }

func newReusableKubernetesOverrideFixture(t *testing.T) (*Service, *model.Application, *model.BuildPlan, *model.DeploymentPlan, model.WorkflowNode, []model.WorkflowStage) {
	t.Helper()
	s, _, _, repoID := newPipelineTestService(t)
	authorizeRegistryTestActors(t, s, "admin")
	ctx := deliveryTestContext()
	registry := createTestImageRegistry(t, s, "覆盖测试仓库")
	build, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "覆盖测试构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ImageRegistryID: registry.ID})
	if err != nil {
		t.Fatal(err)
	}
	deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "覆盖测试集群", Platform: model.DeploymentKubernetes, RuntimeID: "override-cluster", Namespace: "default", WorkloadName: "fixture", ContainerName: "app", RolloutTimeout: 120})
	base, err := s.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{Name: "通用 Kubernetes 方案", Kind: model.DeploymentPlanKubernetes, Reusable: true, TimeoutSeconds: 120, ExecutionConfig: &model.DeploymentExecutionConfig{Kubernetes: model.DefaultKubernetesDeploymentSettings()}})
	if err != nil {
		t.Fatal(err)
	}
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "override_app", RepositoryID: repoID, PollIntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	source, stages := testStageWorkflowGraph(build.ID, base.ID)
	stages[len(stages)-1].Tasks[0].Config.DeploymentOverride = &model.WorkflowDeploymentOverride{DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentKubernetes, RuntimeID: "override-cluster", Namespace: "default", WorkloadName: "api-a", ContainerName: "api"}, Kubernetes: json.RawMessage(`{"replicas":2}`), ArtifactNodeID: overrideTestPointer("build"), TimeoutSeconds: overrideTestPointer(180)}
	return s, app, build, base, source, stages
}

func TestReusableKubernetesDeploymentOverridesStayLocalAndFreezePerTask(t *testing.T) {
	s, app, _, base, source, stages := newReusableKubernetesOverrideFixture(t)
	ctx := deliveryTestContext()
	second := cloneWorkflowStages(stages)[len(stages)-1].Tasks[0]
	second.ID = "deploy-b"
	second.Name = "第二部署"
	second.Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"replicas":3,"node_port":false}`)
	second.Config.DeploymentOverride.DeploymentTarget.WorkloadName = "api-b"
	stages[len(stages)-1].Tasks = append(stages[len(stages)-1].Tasks, second)
	result, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true})
	if err != nil {
		t.Fatalf("通用方案阶段覆盖不能保存启用: %v %+v", err, result)
	}
	for _, task := range result.Workflow.Stages[len(stages)-1].Tasks {
		if task.Config.DeploymentPlanID != base.ID || task.Config.InlineDeploymentPlan != nil || task.Config.DeploymentOverride == nil {
			t.Fatal("保存覆盖丢失了通用方案来源", task)
		}
	}
	app, err = s.FindApplication(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.newResolvedWorkflowRun(ctx, app, app.Workflow, app.Workflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	a, b := frozen.DeploymentPlans["deploy"], frozen.DeploymentPlans["deploy-b"]
	if a.ID == b.ID || a.SourcePlanID != base.ID || b.SourcePlanID != base.ID || a.Kubernetes.Replicas != 2 || b.Kubernetes.Replicas != 3 || a.TimeoutSeconds != 180 {
		t.Fatal("同一通用方案的任务覆盖发生串扰", a, b)
	}
	ta, tb := frozen.DeploymentTargets["deploy"], frozen.DeploymentTargets["deploy-b"]
	if ta.ID == tb.ID || ta.WorkloadName != "api-a" || tb.WorkloadName != "api-b" || ta.EnvironmentID != "" {
		t.Fatal("任务目标没有独立冻结", ta, tb)
	}
	var current model.DeploymentPlan
	if err = s.db.First(&current, "uuid = ?", base.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.DeploymentTargetID != "" || current.ExecutionConfig.Kubernetes.Replicas != 1 || current.TimeoutSeconds != 120 {
		t.Fatal("阶段修改反向写入通用方案", current)
	}
	for _, task := range workflowTasks(frozen.Stages) {
		if task.Type == model.WorkflowNodeDeploy && task.Config.ArtifactNodeID != "build" {
			t.Fatal("阶段上游镜像未固定到前序准确任务", task.Config)
		}
	}
	current.ExecutionConfig.Kubernetes.CPURequest = "250m"
	if err = s.db.Save(&current).Error; err != nil {
		t.Fatal(err)
	}
	next, err := s.newResolvedWorkflowRun(ctx, app, app.Workflow, app.Workflow.Source, "manual", "refs/heads/main", strings.Repeat("b", 40), "admin", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nextSnapshot, _ := parseWorkflowSnapshot(next)
	if nextSnapshot.DeploymentPlans["deploy"].Kubernetes.CPURequest != "200m" || nextSnapshot.DeploymentPlans["deploy"].Kubernetes.Replicas != 2 || nextSnapshot.DeploymentPlans["deploy"].ID != a.ID {
		t.Fatal("库方案修改影响已选用的阶段配置或任务身份漂移")
	}
	old, _ := parseWorkflowSnapshot(run)
	if old.DeploymentPlans["deploy"].Kubernetes.CPURequest != "200m" {
		t.Fatal("历史运行重新读取了新方案")
	}
}

func TestReusableKubernetesDeploymentOverrideRejectsForeignAndRevokedChanges(t *testing.T) {
	s, app, _, base, source, stages := newReusableKubernetesOverrideFixture(t)
	ctx := deliveryTestContext()
	for _, mutation := range []string{"伪造目标范围", "非前序镜像", "同名目标"} {
		t.Run(mutation, func(t *testing.T) {
			copy := cloneWorkflowStages(stages)
			task := &copy[len(copy)-1].Tasks[0]
			switch mutation {
			case "伪造目标范围":
				task.Config.DeploymentOverride.DeploymentTarget.ProjectID = "other"
			case "非前序镜像":
				task.Config.DeploymentOverride.ArtifactNodeID = overrideTestPointer("not-before")
			case "同名目标":
				other := *task
				other.ID = "same-target"
				copy[len(copy)-1].Tasks = append(copy[len(copy)-1].Tasks, other)
			}
			result, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: copy, Activate: true})
			if err == nil {
				t.Fatal("不合法覆盖可启用", mutation, result)
			}
		})
	}
	foreign := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: "admin", ProjectID: "other", DepartmentID: app.DepartmentID, AllDepartments: true})
	_, _, err := s.resolveWorkflowLibraryPlan(foreign, &model.Application{ProjectID: "other", DepartmentID: app.DepartmentID}, app.Workflow.ID, stages[len(stages)-1].Tasks[0], *base, nil, s.db)
	if err == nil {
		t.Fatal("跨项目通用方案被接受")
	}
	if err = s.db.Create(&model.UserPermission{UserID: "admin", Permission: "kubernetes.workload.update", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	// 管理员显式拒绝语义由现有权限服务决定，此处改为失效真实管理员身份，确保不借缓存沿用。
	if err = s.db.Model(&model.User{}).Where("uuid = ?", "admin").Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err = s.resolveWorkflowLibraryPlan(ctx, app, app.Workflow.ID, stages[len(stages)-1].Tasks[0], *base, nil, s.db)
	if !errors.Is(err, ErrKubernetesTemplatePermission) {
		t.Fatal("身份失效后仍解析覆盖", err)
	}
}

func TestKubernetesStageCannotDisableInheritedNetworkWithoutPermission(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	ctx := deliveryTestContext()
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.NodePort = true
	cfg.Ports[0].Enabled = true
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	for _, row := range []any{&model.User{ID: "stage-editor", Username: "stage-editor", IsActive: true, DepartmentID: database.DefaultDepartmentID}, &model.Role{ID: "stage-role", Name: "stage-role", DisplayName: "阶段配置"}, &model.ProjectMember{ProjectID: project, UserID: "stage-editor", RoleID: "stage-role"}, &model.RolePermission{RoleID: "stage-role", Permission: "kubernetes.workload.update"}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: project, DepartmentID: database.DefaultDepartmentID, UserID: "stage-editor"})
	base := model.DeploymentPlan{ID: "base", ExecutionConfig: &model.DeploymentExecutionConfig{Kubernetes: cfg}}
	after := model.DeploymentPlan{ExecutionConfig: &model.DeploymentExecutionConfig{Kubernetes: model.DefaultKubernetesDeploymentSettings()}}
	if err := s.validateDeploymentOverrideNetworkPermission(ctx, db, base, &after); !errors.Is(err, ErrKubernetesTemplatePermission) {
		t.Fatal("缺少网络权限仍可关闭继承的 NodePort 和端口", err)
	}
	if err := db.Create(&model.RolePermission{RoleID: "stage-role", Permission: "kubernetes.network.update"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.validateDeploymentOverrideNetworkPermission(ctx, db, base, &after); err != nil {
		t.Fatal("有效网络权限不能关闭继承配置", err)
	}
	if err := db.Create(&model.UserPermission{UserID: "stage-editor", Permission: "kubernetes.network.update", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.validateDeploymentOverrideNetworkPermission(ctx, db, base, &after); !errors.Is(err, ErrKubernetesTemplatePermission) {
		t.Fatal("个人拒绝后仍能修改网络", err)
	}
}

func TestWorkflowSaveCannotRemoveNetworkOverridesAfterRevocation(t *testing.T) {
	s, app, _, base, source, stages := newReusableKubernetesOverrideFixture(t)
	adminCtx := deliveryTestContext()
	stages[len(stages)-1].Tasks[0].Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"node_port":true}`)
	saved, err := s.SaveWorkflow(adminCtx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	project := app.ProjectID
	for _, row := range []any{&model.User{ID: "revoked-network-editor", Username: "revoked-network-editor", IsActive: true, DepartmentID: app.DepartmentID}, &model.Role{ID: "revoked-network-role", Name: "revoked-network-role", DisplayName: "网络已撤权"}, &model.ProjectMember{ProjectID: project, UserID: "revoked-network-editor", RoleID: "revoked-network-role"}, &model.RolePermission{RoleID: "revoked-network-role", Permission: "workflow.update"}, &model.RolePermission{RoleID: "revoked-network-role", Permission: "kubernetes.workload.update"}} {
		if err := s.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := database.WithDepartmentScope(adminCtx, database.DepartmentScope{ProjectID: project, DepartmentID: app.DepartmentID, UserID: "revoked-network-editor"})
	for _, mutation := range []string{"恢复默认", "移除任务", "切换自定义"} {
		t.Run(mutation, func(t *testing.T) {
			next := cloneWorkflowStages(saved.Workflow.Stages)
			task := &next[len(next)-1].Tasks[0]
			switch mutation {
			case "恢复默认":
				task.Config.DeploymentOverride.Kubernetes = nil
			case "移除任务":
				next = next[:len(next)-1]
			case "切换自定义":
				target := *task.Config.DeploymentOverride.DeploymentTarget
				task.Config.DeploymentPlanID = ""
				task.Config.DeploymentSource = "inline"
				task.Config.DeploymentOverride = nil
				task.Config.InlineDeploymentPlan = &model.WorkflowDeploymentPlan{Name: task.Name, Kind: model.DeploymentPlanKubernetes, TimeoutSeconds: 120, DeploymentTarget: &target, ExecutionConfig: &model.DeploymentExecutionConfig{Kubernetes: model.DefaultKubernetesDeploymentSettings()}}
			}
			_, err := s.SaveApplicationWorkflow(ctx, app.ID, saved.Workflow.ID, "revoked-network-editor", WorkflowInput{Name: saved.Workflow.Name, Revision: saved.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: next, Activate: false})
			if !errors.Is(err, ErrKubernetesTemplatePermission) {
				t.Fatal("网络撤权后仍可清除既有覆盖", mutation, err)
			}
		})
	}
	template, templateErr := s.CreateWorkflowTemplate(adminCtx, "admin", WorkflowTemplateInput{WorkflowInput: WorkflowInput{Name: "网络权限模板", SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: cloneWorkflowStages(saved.Workflow.Stages)}})
	if templateErr != nil {
		t.Fatal(templateErr)
	}
	templateStages := cloneWorkflowStages(template.WorkflowTemplate.Stages)
	templateStages[len(templateStages)-1].Tasks[0].Config.DeploymentOverride.Kubernetes = nil
	if _, templateErr = s.SaveWorkflowTemplate(ctx, template.WorkflowTemplate.ID, "revoked-network-editor", WorkflowTemplateInput{WorkflowInput: WorkflowInput{Name: template.WorkflowTemplate.Name, Revision: template.WorkflowTemplate.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: templateStages}}); !errors.Is(templateErr, ErrKubernetesTemplatePermission) {
		t.Fatal("公共流水线模板恢复网络默认绕过权限", templateErr)
	}
	if err = s.db.Model(&base).Update("execution_config", `{"kubernetes":{"template":"web","replicas":1,"cpu_request":"200m","memory_request":"300Mi","cpu_limit":"1","memory_limit":"1Gi","node_port":true,"ports":[{"name":"http","enabled":false,"port":8080,"target_port":8080},{"name":"tcp","enabled":false,"port":8888,"target_port":8888}]}}`).Error; err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateDeploymentPlan(ctx, base.ID, DeploymentPlanInput{Name: base.Name, Kind: base.Kind, Reusable: true, TimeoutSeconds: base.TimeoutSeconds, ExecutionConfig: &model.DeploymentExecutionConfig{Kubernetes: model.DefaultKubernetesDeploymentSettings()}})
	if !errors.Is(err, ErrKubernetesTemplatePermission) {
		t.Fatal("修改共享方案把网络恢复默认绕过了撤权", err)
	}
	var retained model.DeploymentPlan
	if err = s.db.First(&retained, "uuid = ?", base.ID).Error; err != nil || !retained.ExecutionConfig.Kubernetes.NodePort {
		t.Fatal("权限拒绝后仍写入共享方案", err)
	}
}

func TestWorkflowStageResourceOverrideCannotBeSavedWithoutWorkloadPermission(t *testing.T) {
	s, app, _, _, source, stages := newReusableKubernetesOverrideFixture(t)
	adminCtx := deliveryTestContext()
	saved, err := s.SaveWorkflow(adminCtx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{&model.User{ID: "workflow-only-editor", Username: "workflow-only-editor", IsActive: true, DepartmentID: app.DepartmentID}, &model.Role{ID: "workflow-only-role", Name: "workflow-only-role", DisplayName: "仅流水线编辑"}, &model.ProjectMember{ProjectID: app.ProjectID, UserID: "workflow-only-editor", RoleID: "workflow-only-role"}, &model.RolePermission{RoleID: "workflow-only-role", Permission: "workflow.update"}} {
		if err := s.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := database.WithDepartmentScope(adminCtx, database.DepartmentScope{ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, UserID: "workflow-only-editor"})
	next := cloneWorkflowStages(saved.Workflow.Stages)
	next[len(next)-1].Tasks[0].Config.DeploymentOverride.Kubernetes = json.RawMessage(`{"replicas":3}`)
	denied, err := s.SaveApplicationWorkflow(ctx, app.ID, saved.Workflow.ID, "workflow-only-editor", WorkflowInput{Name: saved.Workflow.Name, Revision: saved.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: next, Activate: false})
	if !errors.Is(err, ErrKubernetesTemplatePermission) || denied == nil || !hasWorkflowIssue(denied.Issues, "deployment_override_permission_denied") {
		t.Fatal("无工作负载修改权限仍可持久化副本覆盖草稿", denied, err)
	}
	next[len(next)-1].Tasks[0].Config.DeploymentOverride.DeploymentTarget.RuntimeID = ""
	if _, invalidTargetErr := s.SaveApplicationWorkflow(ctx, app.ID, saved.Workflow.ID, "workflow-only-editor", WorkflowInput{Name: saved.Workflow.Name, Revision: saved.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: next, Activate: false}); !errors.Is(invalidTargetErr, ErrKubernetesTemplatePermission) {
		t.Fatal("借未填写目标绕过资源修改权限", invalidTargetErr)
	}
	next[len(next)-1].Tasks[0].Config.DeploymentOverride = nil
	if _, removeErr := s.SaveApplicationWorkflow(ctx, app.ID, saved.Workflow.ID, "workflow-only-editor", WorkflowInput{Name: saved.Workflow.Name, Revision: saved.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: next, Activate: false}); !errors.Is(removeErr, ErrKubernetesTemplatePermission) {
		t.Fatal("清空整个覆盖时绕过原资源参数修改权限", removeErr)
	}
	var retained model.ReleaseWorkflow
	if err = s.db.First(&retained, "uuid = ?", saved.Workflow.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retained.Revision != saved.Workflow.Revision || string(retained.Stages[len(retained.Stages)-1].Tasks[0].Config.DeploymentOverride.Kubernetes) != `{"replicas":2}` {
		t.Fatal("权限拒绝后仍更新流水线", retained)
	}
	incomplete := cloneWorkflowStages(saved.Workflow.Stages)
	incomplete[len(incomplete)-1].Tasks[0].Config.DeploymentOverride.DeploymentTarget.RuntimeID = ""
	draft, err := s.SaveApplicationWorkflow(adminCtx, app.ID, saved.Workflow.ID, "admin", WorkflowInput{Name: saved.Workflow.Name, Revision: saved.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: incomplete, Activate: false})
	if err != nil || draft.Valid {
		t.Fatal("有权限的缺目标草稿应保留为无效可编辑状态", draft, err)
	}
}
