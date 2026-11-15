package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/cicd"
	"edo/internal/model"
	"github.com/google/uuid"
)

func TestKubernetesRegistryExecutionNormalizesExplicitImageWithoutUpstream(t *testing.T) {
	config := &model.DeploymentExecutionConfig{DeploymentRegistryID: "registry", DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "registry", RegistryID: "registry", Image: "api", Tag: "${EDO_IMAGE_TAG}"}}, ArtifactBuildPlanID: "hidden-old-build", Kubernetes: model.DefaultKubernetesDeploymentSettings()}
	if err := normalizeDeploymentExecution(model.DeploymentPlanKubernetes, "", config); err != nil {
		t.Fatal("显式仓库镜像不能保存", err)
	}
	if config.ArtifactBuildPlanID != "" || config.ArtifactNodeID != "" || config.DeploymentInputs[0].Tag != "${EDO_IMAGE_TAG}" {
		t.Fatal("显式镜像仍然依赖隐藏的旧构建来源", config)
	}
	for _, mutate := range []func(*model.DeploymentExecutionConfig){
		func(c *model.DeploymentExecutionConfig) { c.DeploymentInputs[0].RegistryID = "other" },
		func(c *model.DeploymentExecutionConfig) {
			c.DeploymentInputs = append(c.DeploymentInputs, c.DeploymentInputs[0])
		},
		func(c *model.DeploymentExecutionConfig) { c.DeploymentInputs[0].Tag = "${IMAGE_TAG}" },
		func(c *model.DeploymentExecutionConfig) { c.DeploymentScript = "echo custom" },
		func(c *model.DeploymentExecutionConfig) {
			c.DeploymentInputs[0] = model.DeploymentInput{Kind: "upstream", ArtifactNodeID: "files", Path: "app.tar.gz"}
		},
	} {
		copy := cloneDeploymentExecutionConfig(config)
		mutate(copy)
		if err := normalizeDeploymentExecution(model.DeploymentPlanKubernetes, "", copy); err == nil {
			t.Fatal("无效的 Kubernetes 镜像输入被接受", copy)
		}
	}
	legacy := &model.DeploymentExecutionConfig{ArtifactNodeID: "build", Kubernetes: model.DefaultKubernetesDeploymentSettings()}
	if err := normalizeDeploymentExecution(model.DeploymentPlanKubernetes, "", legacy); err != nil || legacy.ArtifactNodeID != "build" {
		t.Fatal("旧 Kubernetes 上游配置被转换", legacy, err)
	}
}

func TestKubernetesRegistryExecutionFailureKeepsActionableSafeReason(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "缺少Tag变量", true: "执行权限撤回"}[revoked], func(t *testing.T) {
			f := newKubernetesRegistryFixture(t, "${EDO_IMAGE_TAG}")
			jobID := uuid.NewString()
			if err := f.s.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.prepared.run.ID).Update("execution_job_id", jobID).Error; err != nil {
				t.Fatal(err)
			}
			component := model.PipelineRunRepository{ID: uuid.NewString(), PipelineRunID: f.prepared.run.ID, RepositoryID: f.prepared.application.RepositoryID, Ref: f.prepared.run.Ref, CommitSHA: f.prepared.run.CommitSHA}
			if err := f.s.db.WithContext(f.ctx).Create(&component).Error; err != nil {
				t.Fatal(err)
			}
			want, cause := "填写固定 Tag", error(ErrKubernetesRegistryTag)
			if revoked {
				want, cause = "授权已失效", cicd.ErrExecutionAuthorization
				if err := f.s.db.Model(&model.User{}).Where("uuid = ?", "admin").Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := f.s.executeDeployTask(context.Background(), DeployTaskPayload{PipelineRunID: f.prepared.run.ID, WorkflowNodeID: f.prepared.node.ID}, jobID)
			if !errors.Is(err, cause) || f.heads.Load() != 0 {
				t.Fatal("执行边界丢失错误身份或仍访问仓库", err)
			}
			var run model.PipelineRun
			if err := f.s.db.First(&run, "uuid = ?", f.prepared.run.ID).Error; err != nil || run.Status != model.PipelineRunFailed || !strings.Contains(run.Message, want) || strings.Contains(run.Message, "构建方案") {
				t.Fatal("失败记录未给出准确下一步", run.Message, err)
			}
			var logs []model.PipelineRunLog
			if err := f.s.db.Where("pipeline_run_id = ? AND stage = ? AND level = ?", run.ID, "failed", "error").Find(&logs).Error; err != nil {
				t.Fatal(err)
			}
			found := false
			for _, log := range logs {
				found = found || log.Message == run.Message
			}
			if !found {
				t.Fatal("任务日志没有保留安全的失败引导", logs)
			}
		})
	}
}

func TestKubernetesRegistryStageOverrideIsIndependentAndNeedsNoBuild(t *testing.T) {
	s, app, _, base, source, stages := newReusableKubernetesOverrideFixture(t)
	ctx := deliveryTestContext()
	registry := createTestImageRegistry(t, s, "阶段直接镜像仓库")
	last := stages[len(stages)-1]
	stages = []model.WorkflowStage{last}
	override := stages[0].Tasks[0].Config.DeploymentOverride
	override.DeploymentRegistryID = overrideTestPointer(registry.ID)
	override.DeploymentInputVersion = overrideTestPointer(1)
	override.DeploymentInputs = overrideTestPointer([]model.DeploymentInput{{Kind: "registry", RegistryID: registry.ID, Image: "api", Tag: "v1"}})
	// 显式选择镜像后，继承的旧绑定不得变成隐藏依赖。
	override.ArtifactNodeID = overrideTestPointer("removed-build")
	saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true})
	if err != nil || !saved.Valid {
		t.Fatal("直接镜像部署错误要求构建阶段", saved, err)
	}
	app, err = s.FindApplication(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.newResolvedWorkflowRun(ctx, app, app.Workflow, source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := parseWorkflowSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	config := frozen.Stages[0].Tasks[0].Config
	if !frozen.DeferredKubernetesRegistryImages || config.DeploymentRegistryID != registry.ID || config.ArtifactNodeID != "" || config.DeploymentInputs[0].Image != "api" || len(frozen.RegistryInputs) != 0 {
		t.Fatal("显式镜像未冻结配置或提前依赖仓库网络", frozen)
	}
	var unchanged model.DeploymentPlan
	if err = s.db.First(&unchanged, "uuid = ?", base.ID).Error; err != nil || unchanged.ExecutionConfig.DeploymentRegistryID != "" || len(unchanged.ExecutionConfig.DeploymentInputs) != 0 {
		t.Fatal("阶段镜像改动反写共享方案", unchanged, err)
	}
	invalid := cloneWorkflowStages(saved.Workflow.Stages)
	invalid[0].Tasks[0].Config.DeploymentOverride.DeploymentInputs = overrideTestPointer([]model.DeploymentInput{})
	issues := s.validateWorkflow(ctx, app, model.WorkflowSchemaVersion, source, invalid)
	if !hasWorkflowIssue(issues, "missing_deployment_image") {
		t.Fatal("没有镜像仍可启用", issues)
	}
	invalid[0].Tasks[0].Config.DeploymentOverride.DeploymentRegistryID = overrideTestPointer("")
	app.Workflow.Stages = invalid
	if next, runErr := s.newResolvedWorkflowRun(ctx, app, app.Workflow, source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now()); next != nil || !errors.Is(runErr, ErrDeploymentRegistryRequired) {
		t.Fatal("未选择镜像仓库仍可创建执行", next, runErr)
	}
}
