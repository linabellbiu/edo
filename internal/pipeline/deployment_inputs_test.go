package pipeline

import (
	"strings"
	"testing"

	"edo/internal/model"
)

func TestDeploymentInputSanitizerKeepsSelectedFileParameters(t *testing.T) {
	node := model.WorkflowNode{Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "configuration", ConfigurationID: "config", Path: "runtime/settings.env"}, {Kind: "upstream", ArtifactNodeID: "files", Path: "output.tar.gz"}, {Kind: "registry", RegistryID: "registry", Image: "team/app", Tag: "v1"}}, DeploymentScript: "docker compose --env-file \"${EDO_APP_CONFIG_PATH}/runtime/settings.env\" up -d", DeploymentParameters: "--env-file runtime/settings.env"}}
	if err := sanitizeDeploymentInputs(&node); err != nil {
		t.Fatal(err)
	}
	if len(node.Config.ConfigurationFiles) != 1 || node.Config.ConfigurationFiles[0].Path != "runtime/settings.env" || node.Config.DeploymentParameters != "--env-file runtime/settings.env" {
		t.Fatal("所选配置引用或自定义参数没有保留")
	}
	for _, input := range []model.DeploymentInput{{Kind: "configuration", ConfigurationID: "config", Path: "../escape"}, {Kind: "registry", RegistryID: "registry", Image: "other.example/app:tag", Tag: "v1"}, {Kind: "registry", RegistryID: "registry", Image: "team/app", Tag: "${IMAGE_TAG}"}} {
		bad := node
		bad.Config.DeploymentInputs = []model.DeploymentInput{input}
		if sanitizeDeploymentInputs(&bad) == nil {
			t.Fatalf("接受了无效来源: %+v", input)
		}
	}
	bad := node
	bad.Config.DeploymentInputs = []model.DeploymentInput{{Kind: "configuration", ConfigurationID: "one", Path: "settings"}, {Kind: "upstream", ArtifactNodeID: "files", Path: "settings/app"}}
	if sanitizeDeploymentInputs(&bad) == nil {
		t.Fatal("文件和父路径冲突未拒绝")
	}
}

func TestDeploymentScriptImageTagIsCurrentBuildNotRegistryOrUserValue(t *testing.T) {
	p := &executionContext{run: model.PipelineRun{ID: "run", Ref: "refs/tags/git-version"}, node: model.WorkflowNode{Config: model.WorkflowNodeConfig{DeploymentScript: "printf '%s' \"${EDO_IMAGE_TAG}\"", EnvironmentVariables: map[string]string{"APP_ENV": "production"}}}, artifact: model.Artifact{Kind: model.ArtifactKindOCIImage, Name: "app:build-v1", PipelineRunID: "run", ImageRef: "registry/app@sha256:" + strings.Repeat("a", 64)}, variables: map[string]string{"EDO_IMAGE_TAG": "wrong", "IMAGE_TAG": "wrong", "EDO_INPUT_IMAGE_1": "registry/other@sha256:" + strings.Repeat("b", 64)}}
	p.node.Config.DeploymentTagReference = "${RELEASE_TAG}"
	values, err := deploymentScriptEnvironment(p)
	if err != nil || values["EDO_IMAGE_TAG"] != "build-v1" || values["IMAGE_TAG"] != "build-v1" || values["RELEASE_TAG"] != "build-v1" || values["APP_ENV"] != "production" || values["EDO_GIT_TAG"] != "git-version" {
		t.Fatalf("脚本变量错误: %+v %v", values, err)
	}
	p.composeCandidates = map[string]model.Artifact{"second": {Name: "other:build-v2"}}
	if _, err := deploymentScriptEnvironment(p); err == nil {
		t.Fatal("多个不同 Tag 被静默选取")
	}
	p.composeCandidates = nil
	p.artifact = model.Artifact{}
	if _, err := deploymentScriptEnvironment(p); err == nil {
		t.Fatal("没有本次构建时借用了用户 EDO_IMAGE_TAG")
	}
	p.node.Config.DeploymentScript = "printf '%s' \"${IMAGE_TAG}\""
	if _, err := deploymentScriptEnvironment(p); err == nil {
		t.Fatal("历史变量引用也不得借用用户值")
	}
}
