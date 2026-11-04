package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestComposeDeliveryCreatesGenericDraftAndDeduplicatesBuilds(t *testing.T) {
	s, db, _, repoID := newPipelineTestService(t)
	ctx := deliveryTestContext()
	registry := model.ImageRegistry{ID: "compose-test-registry", Name: "通用镜像仓库", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", IsActive: true}
	if err := db.WithContext(ctx).Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	build, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "自选构建", Kind: model.BuildPlanDockerfile, DockerfilePath: "container/Appfile", ContextPath: ".", ImageRegistryID: registry.ID})
	if err != nil {
		t.Fatal(err)
	}
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "generic_compose", RepositoryID: repoID, EmptyWorkflow: true})
	if err != nil {
		t.Fatal(err)
	}
	target := deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "自选目标", Platform: model.DeploymentSSH, EnvironmentID: "compose-env", HostID: "compose-host", WorkingDirectory: "/srv/apps/example", RolloutTimeout: 600})
	if err := db.WithContext(ctx).Create(&model.HostCapability{HostID: target.HostID, Kind: model.HostCapabilityDocker, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	input := ComposeDeliveryInput{ProjectName: "example-stack", ComposeFile: "deploy/production.yml", EnvFile: "settings.env", Services: []ComposeServiceBuild{{Service: "api", BuildPlanID: build.ID}, {Service: "jobs", BuildPlanID: build.ID}}, Target: *target}
	result, err := s.CreateComposeDeliveryWorkflow(ctx, app.ID, "admin", input)
	if err != nil || !result.Valid || result.Workflow.IsActive {
		t.Fatalf("未生成有效独立草稿: err=%v result=%+v", err, result)
	}
	stages := result.Workflow.Stages
	if len(stages) != 3 || len(stages[0].Tasks) != 1 || len(stages[1].Tasks[0].Config.ArtifactNodeIDs) != 1 {
		t.Fatal("共享构建方案不应重复构建")
	}
	plan, err := deliveryTestPlan(t, s, ctx, stages[2].Tasks[0].Config.DeploymentPlanID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Script, "example-stack") || !strings.Contains(plan.Script, "production.yml") || strings.Contains(plan.Script, "dev-plat") || strings.Contains(plan.Script, "turing") {
		t.Fatal("通用脚本没有使用页面配置或包含业务专用值")
	}
	for _, value := range []any{&model.PipelineRun{}, &model.DeploymentRecord{}, &model.Job{}} {
		var count int64
		if err := db.Model(value).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("创建草稿不应执行部署")
		}
	}
	// 配置失败必须整体回滚，不能留下汇总构建方案、部署位置或半个工作流。
	var before int64
	db.Model(&model.BuildPlan{}).Count(&before)
	input.Target.HostID = "unavailable-host"
	if _, err := s.CreateComposeDeliveryWorkflow(ctx, app.ID, "admin", input); err == nil {
		t.Fatal("无效主机未被拒绝")
	}
	var after int64
	db.Model(&model.BuildPlan{}).Count(&after)
	if before != after {
		t.Fatal("失败配置留下了部分构建方案")
	}
}

func TestComposeDeliveryValidatesPathsServicesAndHealthURL(t *testing.T) {
	valid := ComposeDeliveryInput{ProjectName: "my-app", ComposeFile: "compose.yaml", Services: []ComposeServiceBuild{{Service: "api", BuildPlanID: "build"}}}
	valid.Target.WorkingDirectory = "/srv/app"
	for _, change := range []func(*ComposeDeliveryInput){
		func(v *ComposeDeliveryInput) { v.ProjectName = "$(id)" },
		func(v *ComposeDeliveryInput) { v.ComposeFile = "../prod.yml" },
		func(v *ComposeDeliveryInput) { v.EnvFile = "prod.env\ncommand" },
		func(v *ComposeDeliveryInput) {
			v.Services = []ComposeServiceBuild{{Service: "api", BuildPlanID: "build"}, {Service: "api", BuildPlanID: "build"}}
		},
		func(v *ComposeDeliveryInput) { v.HealthURL = "https://user:pass@example.com/health" },
		func(v *ComposeDeliveryInput) { v.HealthURL = "https://example.com/health?token=private" },
	} {
		input := valid
		change(&input)
		if _, err := normalizeComposeDelivery(input); !errors.Is(err, ErrInvalidComposeDelivery) {
			t.Fatalf("非法配置未拒绝: %v", err)
		}
	}
}

func TestComposeImageManifestRequiresExactCommitAndCompleteDigests(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("当前环境未安装 Node.js")
	}
	dir := t.TempDir()
	script := strings.ReplaceAll(composeImagesTemplate, "__EDO_BINDINGS__", `[{"service":"api","node_id":"image-one"}]`)
	file := filepath.Join(dir, "generate.cjs")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	image := map[string]string{"node_id": "image-one", "kind": "oci_image", "storage_kind": "registry", "digest": "sha256:" + strings.Repeat("b", 64), "image_ref": "registry.example.com/apps/service@sha256:" + strings.Repeat("b", 64), "commit_sha": commit}
	run := func() error {
		raw, _ := json.Marshal([]any{image})
		cmd := exec.CommandContext(context.Background(), "node", file)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "EDO_COMMIT_SHA="+commit, "EDO_PIPELINE_RUN_ID=run-one", "EDO_UPSTREAM_ARTIFACTS_JSON="+string(raw))
		return cmd.Run()
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "compose.images.json"))
	if err != nil || !strings.Contains(string(content), image["image_ref"]) {
		t.Fatal("未生成固定摘要清单")
	}
	image["commit_sha"] = strings.Repeat("c", 40)
	if err := run(); err == nil {
		t.Fatal("接受了其他提交的制品")
	}
	image["commit_sha"] = commit
	image["image_ref"] = "registry.example.com/apps/service:latest"
	if err := run(); err == nil {
		t.Fatal("接受了可变标签")
	}
}

func TestComposeSummaryPropagatesDownstreamArchitecture(t *testing.T) {
	build := model.WorkflowNode{ID: "image", Type: model.WorkflowNodeBuild}
	bundle := model.WorkflowNode{ID: "bundle", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{ArtifactNodeIDs: []string{"image"}}}
	deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{ArtifactNodeID: "bundle"}}
	snapshot := workflowSnapshot{ExecutionVersion: 2, Stages: []model.WorkflowStage{{ID: "images", Tasks: []model.WorkflowNode{build}}, {ID: "bundle", Tasks: []model.WorkflowNode{bundle}}, {ID: "deploy", Tasks: []model.WorkflowNode{deploy}}}, DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{"deploy": {Architecture: model.HostArchitectureARM64}}}
	platform, err := workflowBuildPlatform(snapshot, build.ID, workflowBuildPlanSnapshot{Kind: model.BuildPlanDockerfile, ImageRegistryID: "registry"})
	if err != nil || platform != "linux/arm64" {
		t.Fatalf("汇总后未正确推导目标架构: %s %v", platform, err)
	}
	if len(validateUpstreamArtifactStages([]model.WorkflowStage{{ID: "same", Tasks: []model.WorkflowNode{build, bundle}}})) == 0 {
		t.Fatal("不应允许同阶段隐式依赖")
	}
}

func TestComposeExistingScriptDoesNotUpdateOnPullFailureAndRestoresFailedUpdate(t *testing.T) {
	for _, mode := range []string{"success", "pull-fails", "update-fails"} {
		t.Run(mode, func(t *testing.T) {
			work := t.TempDir()
			bin := filepath.Join(work, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			fake := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$TRACE"
if [ "$1" = inspect ]; then
 case "$3" in *working_dir*) pwd;; *) cat "$STATE";; esac
 exit 0
fi
if [ "$1" = image ]; then printf 'sha256:%s\n' "$NEW_ID"; exit 0; fi
for arg in "$@"; do
 case "$arg" in
 version) exit 0;;
 config)
   case "$*" in *--services*) echo web;; *--images*) echo "registry.example.com/service@sha256:$NEW_ID";; esac
   exit 0;;
 ps) echo test-container; exit 0;;
 pull) [ "$MODE" != pull-fails ]; exit $?;;
 up)
   if [ "$MODE" = update-fails ] && [ ! -f "$FAILED" ]; then touch "$FAILED"; echo broken > "$STATE"; exit 9; fi
   if grep -q "$NEW_ID" .edo/compose/sample/images.json; then echo "sha256:$NEW_ID" > "$STATE"; else echo "sha256:$OLD_ID" > "$STATE"; fi
   exit 0;;
 esac
done
exit 99
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			oldID, newID := strings.Repeat("a", 64), strings.Repeat("b", 64)
			state, trace := filepath.Join(work, "state"), filepath.Join(work, "trace")
			if err := os.WriteFile(state, []byte("sha256:"+oldID+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			artifactPath := filepath.Join(work, "images.json")
			if err := os.WriteFile(artifactPath, []byte(`{"services":{"web":{"image":"registry.example.com/service@sha256:`+newID+`"}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			input := ComposeDeliveryInput{ProjectName: "sample", ComposeFile: "compose.yaml", TimeoutSeconds: 120, Services: []ComposeServiceBuild{{Service: "web"}}}
			input.Target.WorkingDirectory = work
			cmd := exec.Command("sh", "-se")
			cmd.Dir = work
			cmd.Stdin = strings.NewReader(composeExistingScript(input))
			cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "TRACE="+trace, "STATE="+state, "OLD_ID="+oldID, "NEW_ID="+newID, "MODE="+mode, "FAILED="+filepath.Join(work, "failed"), "EDO_ARTIFACT_PATH="+artifactPath, "EDO_DEPLOYMENT_ID=deployment-one", "EDO_DEPLOYMENT_TARGET_ID=target-one", "EDO_COMMIT_SHA="+strings.Repeat("c", 40))
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mode == "success") {
				t.Fatalf("发布结果错误: mode=%s err=%v output=%s", mode, err, output)
			}
			actual, _ := os.ReadFile(state)
			expected := "sha256:" + oldID
			if mode == "success" {
				expected = "sha256:" + newID
			}
			if strings.TrimSpace(string(actual)) != expected {
				t.Fatalf("镜像状态或恢复错误: mode=%s actual=%s", mode, actual)
			}
			commands, _ := os.ReadFile(trace)
			if mode == "pull-fails" && strings.Contains(string(commands), " up ") {
				t.Fatal("拉取失败后不应更新容器")
			}
			if strings.Contains(string(commands), " down") || strings.Contains(string(commands), "--build") {
				t.Fatal("已有项目脚本不应删除项目或重新构建")
			}
		})
	}
}
