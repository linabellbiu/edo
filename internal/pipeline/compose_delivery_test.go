package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"edo/internal/artifact"
	"edo/internal/model"
)

func TestComposeDeliveryCreatesTwoStageDraftAndSharedImageBuild(t *testing.T) {
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
	input := ComposeDeliveryInput{ProjectName: "example-stack", ComposeYAML: "services:\n  api:\n    image: registry.example.com/engine:${IMAGE_TAG}\n  jobs:\n    image: registry.example.com/engine:${IMAGE_TAG}\n", EnvFile: "settings.env", Services: []ComposeServiceBuild{{Service: "api", BuildPlanID: build.ID, ImageName: "engine"}, {Service: "jobs", BuildPlanID: build.ID, ImageName: "engine"}}, Target: *target}
	result, err := s.CreateComposeDeliveryWorkflow(ctx, app.ID, "admin", input)
	if err != nil || !result.Valid || result.Workflow.IsActive {
		t.Fatalf("未生成有效独立草稿: err=%v result=%+v", err, result)
	}
	stages := result.Workflow.Stages
	if len(stages) != 2 || len(stages[0].Tasks) != 1 || len(stages[1].Tasks[0].Config.ComposeImages) != 2 {
		t.Fatal("应直接从并行构建进入部署，共享镜像只构建一次")
	}
	if stages[0].Tasks[0].Config.ImageTag != "${EDO_GIT_TAG}" {
		t.Fatal("未保存可配置的镜像 Tag")
	}
	plan, err := deliveryTestPlan(t, s, ctx, stages[1].Tasks[0].Config.DeploymentPlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != model.DeploymentPlanComposeStack || plan.Script != "" || !strings.Contains(plan.ComposeYAML, "${IMAGE_TAG}") {
		t.Fatal("Compose 应保存在 EDO 部署方案，不生成专用脚本或清单阶段")
	}
	for _, value := range []any{&model.PipelineRun{}, &model.DeploymentRecord{}, &model.Job{}} {
		var count int64
		if err := db.Model(value).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("创建草稿不应执行部署")
		}
	}
	var before int64
	db.Model(&model.DeploymentPlan{}).Count(&before)
	input.Target.HostID = "unavailable-host"
	if _, err := s.CreateComposeDeliveryWorkflow(ctx, app.ID, "admin", input); err == nil {
		t.Fatal("无效主机未被拒绝")
	}
	var after int64
	db.Model(&model.DeploymentPlan{}).Count(&after)
	if before != after {
		t.Fatal("失败配置留下了部分方案")
	}
}

func TestComposeDeliveryValidatesInlineYAMLAndTagBindings(t *testing.T) {
	valid := ComposeDeliveryInput{ProjectName: "my-app", ComposeYAML: "services:\n  api:\n    image: registry.example.com/api:${IMAGE_TAG}\n", Services: []ComposeServiceBuild{{Service: "api", BuildPlanID: "build"}}}
	valid.Target.WorkingDirectory = "/srv/app"
	for _, change := range []func(*ComposeDeliveryInput){func(v *ComposeDeliveryInput) { v.ProjectName = "$(id)" }, func(v *ComposeDeliveryInput) { v.ComposeYAML = "services: [" }, func(v *ComposeDeliveryInput) { v.EnvFile = "../prod.env" }, func(v *ComposeDeliveryInput) { v.TagVariable = "EDO_COMMIT_SHA" }, func(v *ComposeDeliveryInput) { v.HealthURL = "https://user:pass@example.com/health" }, func(v *ComposeDeliveryInput) {
		v.ComposeYAML = "services:\n  api:\n    build: .\n    image: app:${IMAGE_TAG}\n"
	}} {
		input := valid
		change(&input)
		if _, err := normalizeComposeDelivery(input); !errors.Is(err, ErrInvalidComposeDelivery) {
			t.Fatalf("非法配置未拒绝: %v", err)
		}
	}
}

func composeTestConfig() model.WorkflowNodeConfig {
	return model.WorkflowNodeConfig{ArtifactNodeID: "image", ArtifactNodeIDs: []string{"image"}, ComposeImages: []model.ComposeImageBinding{{Service: "web", ArtifactNodeID: "image"}}, Compose: &model.ComposeDeploymentSettings{ProjectName: "sample", TagVariable: "IMAGE_TAG", AdoptExisting: true}}
}

func TestComposeTagComesFromActualBuildAndExecutionPinsDigest(t *testing.T) {
	config := composeTestConfig()
	digest := "sha256:" + strings.Repeat("b", 64)
	image := model.Artifact{Name: "web:release-one", ImageRef: "registry.example.com/team/web@" + digest, Digest: digest}
	source := "services:\n  web:\n    image: registry.example.com/team/web:${IMAGE_TAG}\n    environment:\n      TOKEN: ${PRIVATE_VALUE}\n"
	text, tag, err := renderComposeStack(source, config, map[string]model.Artifact{"web": image}, map[string]string{"PRIVATE_VALUE": "do-not-serialize"})
	if err != nil || tag != "release-one" || !strings.Contains(text, image.ImageRef) || strings.Contains(text, "do-not-serialize") || !strings.Contains(text, "${PRIVATE_VALUE}") {
		t.Fatalf("未绑定构建结果或泄露变量: tag=%s err=%v", tag, err)
	}
	_, _, err = renderComposeStack(strings.ReplaceAll(source, "web:${IMAGE_TAG}", "other:${IMAGE_TAG}"), config, map[string]model.Artifact{"web": image}, nil)
	if err == nil {
		t.Fatal("接受了与实际构建不同的镜像名称")
	}
	for _, wrong := range []string{"other.example.com/team/web:${IMAGE_TAG}", "registry.example.com/other/web:${IMAGE_TAG}", "web:${IMAGE_TAG}"} {
		if _, _, err := renderComposeStack(strings.Replace(source, "registry.example.com/team/web:${IMAGE_TAG}", wrong, 1), config, map[string]model.Artifact{"web": image}, nil); err == nil {
			t.Fatalf("接受了与固定摘要不同的仓库路径: %s", wrong)
		}
	}
}

func TestPipelineImageNamesAndGitTagAreConfigurable(t *testing.T) {
	p := &executionContext{application: model.Application{ID: "app-one", Name: "my_app"}, run: model.PipelineRun{ID: "run-one", Ref: "refs/tags/release-one", CommitSHA: strings.Repeat("a", 40)}, node: model.WorkflowNode{Config: model.WorkflowNodeConfig{ImageName: "team/service", ImageTag: "${EDO_GIT_TAG}"}}}
	name, err := executionNodeImageName(p)
	if err != nil || name != "team/service" {
		t.Fatalf("镜像名未采用配置: %s %v", name, err)
	}
	tag, err := executionNodeImageTag(p)
	if err != nil || tag != "release-one" {
		t.Fatalf("镜像 Tag 未采用运行引用: %s %v", tag, err)
	}
	p.run.Ref = "refs/heads/main"
	if _, err := executionNodeImageTag(p); err == nil {
		t.Fatal("分支运行不应伪造 Git Tag")
	}
	p.node.Config.ImageName = "https://other.example.com/app"
	if _, err := executionNodeImageName(p); err == nil {
		t.Fatal("镜像名不应改变登记仓库地址")
	}
}

func TestComposeDirectImagesPropagateDownstreamArchitecture(t *testing.T) {
	a := model.WorkflowNode{ID: "one", Type: model.WorkflowNodeBuild}
	b := model.WorkflowNode{ID: "two", Type: model.WorkflowNodeBuild}
	d := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{ArtifactNodeID: "one", ArtifactNodeIDs: []string{"one", "two"}}}
	snapshot := workflowSnapshot{ExecutionVersion: 2, Stages: []model.WorkflowStage{{ID: "build", Tasks: []model.WorkflowNode{a, b}}, {ID: "deploy", Tasks: []model.WorkflowNode{d}}}, DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{"deploy": {Architecture: model.HostArchitectureARM64}}}
	for _, id := range []string{"one", "two"} {
		platform, err := workflowBuildPlatform(snapshot, id, workflowBuildPlanSnapshot{Kind: model.BuildPlanDockerfile, ImageRegistryID: "registry"})
		if err != nil || platform != "linux/arm64" {
			t.Fatalf("目标架构错误: %s %s %v", id, platform, err)
		}
	}
}

func TestComposeInlineScriptFailurePreservesOrRestoresImages(t *testing.T) {
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
if [ "$1" = inspect ]; then case "$3" in *working_dir*) pwd;; *RestartCount*) echo 0;; *State.Status*) echo 'running 0 ';; *) cat "$STATE";; esac; exit 0; fi
if [ "$1" = image ]; then echo "sha256:$NEW_ID"; exit 0; fi
for arg in "$@"; do
 case "$arg" in
 config)
  case "$*" in *--images*) echo "registry.example.com/web@sha256:$NEW_ID";; *--format*) printf '{"services":{"web":{"image":"sha256:%s"}}}\n' "$OLD_ID";; esac
  exit 0;;
 ps) echo test-container; exit 0;;
 pull) [ "$MODE" != pull-fails ]; exit $?;;
 up)
  if [ "$MODE" = update-fails ] && [ ! -f "$FAILED" ]; then touch "$FAILED"; echo broken > "$STATE"; exit 9; fi
  if grep -q "$NEW_ID" .edo/compose/sample/current.yml; then echo "sha256:$NEW_ID" > "$STATE"; else echo "sha256:$OLD_ID" > "$STATE"; fi
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
			content := "services:\n  web:\n    image: registry.example.com/web@sha256:" + newID + "\n"
			script, err := composeStackScript(content, composeTestConfig(), work, 120)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(context.Background(), "sh", "-se")
			cmd.Dir = work
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "TRACE="+trace, "STATE="+state, "OLD_ID="+oldID, "NEW_ID="+newID, "MODE="+mode, "FAILED="+filepath.Join(work, "failed"), "EDO_DEPLOYMENT_ID=deployment-one", "EDO_DEPLOYMENT_TARGET_ID=target-one", "EDO_COMMIT_SHA="+strings.Repeat("c", 40))
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mode == "success") {
				t.Fatalf("结果错误: %s %v %s", mode, err, output)
			}
			actual, _ := os.ReadFile(state)
			expected := "sha256:" + oldID
			if mode == "success" {
				expected = "sha256:" + newID
			}
			if strings.TrimSpace(string(actual)) != expected {
				t.Fatalf("镜像状态错误: %s %s", mode, actual)
			}
			commands, _ := os.ReadFile(trace)
			if mode == "pull-fails" && strings.Contains(string(commands), " up ") {
				t.Fatal("拉取失败后不应更新")
			}
			if strings.Contains(string(commands), " down") || strings.Contains(string(commands), "--build") {
				t.Fatal("不应删除项目或在部署主机构建")
			}
		})
	}
}

func TestComposeDraftAllowsIncompleteFieldsButActivationChecksBindings(t *testing.T) {
	node := model.WorkflowNode{ID: "deploy", Name: "部署", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{Compose: &model.ComposeDeploymentSettings{}, ComposeImages: []model.ComposeImageBinding{{Service: "", ArtifactNodeID: ""}}}}
	if err := sanitizeWorkflowNode(&node); err != nil {
		t.Fatalf("草稿未填完不应无法保存: %v", err)
	}
	if validateComposeNode(node.Config) == nil {
		t.Fatal("未完成配置不能执行")
	}
	config := composeTestConfig()
	config.ArtifactNodeIDs = append(config.ArtifactNodeIDs, "unmapped")
	if validateComposeNode(config) == nil {
		t.Fatal("未映射的额外制品来源不能被忽略")
	}
	config = composeTestConfig()
	config.ComposeImages = append(config.ComposeImages, model.ComposeImageBinding{Service: "jobs", ArtifactNodeID: "image"})
	if err := validateComposeNode(config); err != nil {
		t.Fatalf("多个服务应能共用同一构建: %v", err)
	}
	if validateComposeBindingsYAML("services:\n  web:\n    image: repo/web:${IMAGE_TAG}\n", config) == nil {
		t.Fatal("不存在的服务必须在构建前拒绝")
	}
}

func TestComposeMixedTagsAndDuplicateBuildOutputsAreRejected(t *testing.T) {
	config := composeTestConfig()
	config.ArtifactNodeIDs = append(config.ArtifactNodeIDs, "other")
	config.ComposeImages = append(config.ComposeImages, model.ComposeImageBinding{Service: "jobs", ArtifactNodeID: "other"})
	images := map[string]model.Artifact{"web": {Name: "registry.example.com/web:one"}, "jobs": {Name: "registry.example.com/jobs:two"}}
	if _, _, err := renderComposeStack("services:\n  web:\n    image: registry.example.com/web:${IMAGE_TAG}\n  jobs:\n    image: registry.example.com/jobs:${IMAGE_TAG}\n", config, images, nil); err == nil {
		t.Fatal("不同实际 Tag 不能混用一个共享变量")
	}
	nodes := []model.WorkflowNode{{ID: "one", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "plan", ImageName: "api", ImageTag: "${EDO_GIT_TAG}"}}, {ID: "two", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "plan", ImageName: "api", ImageTag: "${EDO_GIT_TAG}"}}}
	issues := validateWorkflowArtifactFlow(nodes, map[string]model.BuildPlan{"plan": {Kind: model.BuildPlanDockerfile, ImageRegistryID: "registry"}}, nil, nil)
	if !hasWorkflowIssue(issues, "duplicate_image_output") {
		t.Fatal("并行任务覆盖相同 Tag 未被阻断")
	}
}

func TestComposeChecksEveryArtifactProducerAndRun(t *testing.T) {
	s, db, _, repoID := newPipelineTestService(t)
	ctx := deliveryTestContext()
	store, err := artifact.NewService(db, t.TempDir(), 1024*1024, s.logger)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureArtifacts(store)
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "compose_identity", RepositoryID: repoID, EmptyWorkflow: true})
	if err != nil {
		t.Fatal(err)
	}
	registry := model.ImageRegistry{ID: "identity-registry", Name: "身份测试仓库", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", IsActive: true}
	if err := db.WithContext(ctx).Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	config := composeTestConfig()
	config.ArtifactNodeIDs = append(config.ArtifactNodeIDs, "other")
	config.ComposeImages = append(config.ComposeImages, model.ComposeImageBinding{Service: "jobs", ArtifactNodeID: "other"})
	prepared := &executionContext{run: model.PipelineRun{ID: "identity-run", ApplicationID: app.ID, ProjectID: app.ProjectID, CommitSHA: strings.Repeat("a", 40), Trigger: "manual"}, node: model.WorkflowNode{Config: config}, snapshot: workflowSnapshot{BuildPlans: map[string]workflowBuildPlanSnapshot{}}}
	for _, id := range []string{"image", "other"} {
		build := model.BuildRun{ID: "build-" + id, ApplicationID: app.ID, ProjectID: app.ProjectID, PipelineRunID: prepared.run.ID, WorkflowNodeID: id, BuildPlanID: "plan-" + id, CommitSHA: prepared.run.CommitSHA, Status: model.BuildRunStatusSucceeded}
		image := model.Artifact{ID: "artifact-" + id, ApplicationID: app.ID, ProjectID: app.ProjectID, PipelineRunID: prepared.run.ID, BuildRunID: build.ID, Name: "registry.example.com/" + id + ":release", ImageRef: "registry.example.com/" + id + "@sha256:" + strings.Repeat("b", 64), Digest: "sha256:" + strings.Repeat("b", 64), Kind: model.ArtifactKindOCIImage, StorageKind: model.ArtifactStorageKindRegistry, ImageRegistryID: registry.ID, Status: model.ArtifactStatusAvailable}
		node := model.PipelineNodeRun{ID: "node-" + id, ProjectID: app.ProjectID, PipelineRunID: prepared.run.ID, NodeID: id, Status: model.PipelineRunSucceeded, ArtifactID: image.ID}
		for _, row := range []any{&build, &image, &node} {
			if err := db.WithContext(ctx).Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
		prepared.snapshot.BuildPlans[id] = workflowBuildPlanSnapshot{ID: build.BuildPlanID, ImageRegistryID: registry.ID}
	}
	if err := s.loadComposeStackArtifacts(ctx, prepared); err != nil {
		t.Fatalf("完整制品链应通过: %v", err)
	}
	if len(prepared.composeArtifacts) != 2 {
		t.Fatal("未读取所有服务镜像")
	}
	db.WithContext(ctx).Model(&model.BuildRun{}).Where("uuid = ?", "build-other").Update("commit_sha", strings.Repeat("c", 40))
	if s.loadComposeStackArtifacts(ctx, prepared) == nil {
		t.Fatal("非主镜像的其他提交未被拒绝")
	}
	db.WithContext(ctx).Model(&model.BuildRun{}).Where("uuid = ?", "build-other").Update("commit_sha", prepared.run.CommitSHA)
	db.WithContext(ctx).Model(&model.Artifact{}).Where("uuid = ?", "artifact-other").Update("pipeline_run_id", "foreign-run")
	if s.loadComposeStackArtifacts(ctx, prepared) == nil {
		t.Fatal("手动运行不能绕过多镜像的运行归属")
	}
}

func TestComposeRegisteredImageUsesDigestRepository(t *testing.T) {
	s, db, _, repoID := newPipelineTestService(t)
	ctx := deliveryTestContext()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "compose_registered", RepositoryID: repoID, EmptyWorkflow: true})
	if err != nil {
		t.Fatal(err)
	}
	registry := model.ImageRegistry{ID: "registered-registry", Name: "登记镜像仓库", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", Namespace: "team", IsActive: true}
	if err := db.WithContext(ctx).Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	store, err := artifact.NewService(db, t.TempDir(), 1024*1024, s.logger)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	image, err := store.CreateImage(ctx, artifact.ImageInput{BuildMetadata: artifact.BuildMetadata{ApplicationID: app.ID, PipelineRunID: "registered-run", RepositoryID: repoID, WorkflowNodeID: "image", BuildPlanID: "build-plan", Ref: "refs/tags/release-one", CommitSHA: strings.Repeat("a", 40), PlanSnapshot: "{}", PlanDigest: sha256Digest([]byte("{}")), CreatedBy: "admin"}, Name: "registry.example.com/team/web:release-one", StorageKind: model.ArtifactStorageKindRegistry, ImageRegistryID: registry.ID, ImageRef: "registry.example.com/team/web@" + digest})
	if err != nil {
		t.Fatal(err)
	}
	if image.Name != "web:release-one" {
		t.Fatalf("需覆盖真实登记后的显示名: %s", image.Name)
	}
	content, tag, err := renderComposeStack("services:\n  web:\n    image: registry.example.com/team/web:${IMAGE_TAG}\n", composeTestConfig(), map[string]model.Artifact{"web": *image}, nil)
	if err != nil || tag != "release-one" || !strings.Contains(content, image.ImageRef) {
		t.Fatalf("真实登记制品无法用于 Compose: tag=%s err=%v", tag, err)
	}
}
