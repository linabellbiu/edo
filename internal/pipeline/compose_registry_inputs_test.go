package pipeline

import (
	"context"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestComposeDeploymentOnlyPinsSelectedRegistryImages(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	source := "services:\n  api:\n    image: registry.example.com/team/app:v1\n    environment:\n      SECRET: ${PRIVATE_VALUE}\n  worker:\n    image: ${EDO_INPUT_IMAGE_1}\n  db:\n    image: postgres:17\n"
	inputs := []workflowRegistryInput{{Index: 0, RegistryID: "registry", ImageRef: "registry.example.com/team/app@" + digest, Tag: "v1"}}
	text, err := pinRegistryComposeImages(source, inputs, map[string]string{"EDO_INPUT_IMAGE_1": inputs[0].ImageRef, "PRIVATE_VALUE": "must-not-expand"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseComposeStack(text)
	if err != nil {
		t.Fatal(err)
	}
	services := doc["services"].(map[string]any)
	for _, name := range []string{"api", "worker"} {
		if services[name].(map[string]any)["image"] != inputs[0].ImageRef {
			t.Fatalf("无构建阶段的 %s 镜像没有固定 Digest", name)
		}
	}
	if services["db"].(map[string]any)["image"] != "postgres:17" || strings.Contains(text, "must-not-expand") || !strings.Contains(text, "${PRIVATE_VALUE}") {
		t.Fatal("修改了未选择的镜像或提前展开了容器变量")
	}
	inputs[0].ImageRef = "registry.example.com/team/app:v1"
	if _, err := pinRegistryComposeImages(source, inputs, nil); err == nil {
		t.Fatal("没有 Digest 的镜像进入了执行配置")
	}
}

func TestDockerDeploymentLoadsRegistryImageWithoutAnyBuildArtifact(t *testing.T) {
	input := workflowRegistryInput{Index: 0, RegistryID: "registry", ImageRef: "registry.example.com/team/app@sha256:" + strings.Repeat("a", 64), Tag: "v1"}
	node := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: "plan", DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "registry", RegistryID: input.RegistryID, Image: "app", Tag: "v1"}}}}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "deploy-stage", Tasks: []model.WorkflowNode{node}}}, RegistryInputs: map[string][]workflowRegistryInput{node.ID: {input}}, DeploymentPlans: map[string]workflowDeploymentPlanSnapshot{node.ID: {ID: "plan", Kind: model.DeploymentPlanDocker, TimeoutSeconds: 120}}, DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{node.ID: {ID: "target", Name: "目标主机", Platform: model.DeploymentDocker, RuntimeID: "runtime", HostID: "host", EnvironmentID: "env"}}}
	s, db, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	if err := db.Create(&model.ImageRegistry{ID: input.RegistryID, Name: "独立部署仓库", Endpoint: "https://registry.example.com", Namespace: "team", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	s.artifacts = nil
	prepared, err := s.loadExecution(context.Background(), DeployTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, run.ExecutionJobID)
	if err != nil {
		t.Fatal("没有构建记录或制品存储时无法加载独立部署", err)
	}
	image, _, err := s.resolveDeploymentImage(context.Background(), prepared)
	if err != nil || image != input.ImageRef || prepared.registry.ID != input.RegistryID || prepared.artifact.ID != "" {
		t.Fatal("独立部署没有使用所选仓库的固定镜像", image, err)
	}
}

func TestDeploymentOnlyRegistrySourceDoesNotRequireUpstreamBuild(t *testing.T) {
	for _, kind := range []model.DeploymentPlanKind{model.DeploymentPlanDocker, model.DeploymentPlanCompose, model.DeploymentPlanComposeStack} {
		t.Run(string(kind), func(t *testing.T) {
			node := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: "plan", DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "registry", RegistryID: "registry", Image: "app", Tag: "v1"}}}}
			if kind == model.DeploymentPlanComposeStack {
				node.Config.Compose = &model.ComposeDeploymentSettings{ProjectName: "app", TagVariable: "${EDO_IMAGE_TAG}"}
			}
			if err := sanitizeDeploymentInputs(&node); err != nil {
				t.Fatal(err)
			}
			plan := model.DeploymentPlan{ID: "plan", Kind: kind, ComposeYAML: "services:\n  api:\n    image: registry.example.com/team/app:v1\n"}
			if issues := validateWorkflowArtifactFlow([]model.WorkflowNode{node}, nil, map[string]model.DeploymentPlan{"plan": plan}, nil); len(issues) > 0 {
				t.Fatal("独立部署错误要求上游构建", issues)
			}
			stages := []model.WorkflowStage{{ID: "deploy-stage", Tasks: []model.WorkflowNode{node}}}
			if sources, err := artifactSources(stages); err != nil || sources[node.ID] != "" {
				t.Fatal("独立部署错误借用了历史产物", sources, err)
			}
		})
	}
}
