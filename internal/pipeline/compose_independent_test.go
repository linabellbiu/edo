package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/model"
)

func TestIndependentComposeRunDoesNotBindUpstreamImages(t *testing.T) {
	for _, kind := range []model.BuildPlanKind{"", model.BuildPlanScript, model.BuildPlanDockerfile} {
		t.Run(string(kind), func(t *testing.T) {
			s, db, _, repo := newPipelineTestService(t)
			ctx := deliveryTestContext()
			authorizeRegistryTestActors(t, s, "admin")
			app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "independent_images", RepositoryID: repo})
			if err != nil {
				t.Fatal(err)
			}
			target := deploymentPlanTargetInput(t, s, model.DeploymentTarget{Name: "配置主机", Platform: model.DeploymentSSH, EnvironmentID: "compose-env", HostID: "compose-host", WorkingDirectory: "/srv/example"})
			if err = db.WithContext(ctx).Create(&model.HostCapability{HostID: target.HostID, Kind: model.HostCapabilityDocker, Status: model.HostCapabilityReady}).Error; err != nil {
				t.Fatal(err)
			}
			execution := savedRegistryExecutionForTest(t, s, "")
			execution.Compose = &model.ComposeDeploymentSettings{TagVariable: "${EDO_IMAGE_TAG}"}
			plan, err := s.CreateDeploymentPlan(ctx, "admin", DeploymentPlanInput{Name: "独立部署", Kind: model.DeploymentPlanComposeStack, ComposeYAML: "services:\n  frontend:\n    image: another.example.com/team/unrelated:v2\n", ExecutionConfig: execution, DeploymentTarget: target})
			if err != nil {
				t.Fatal(err)
			}
			stages := []model.WorkflowStage{}
			if kind != "" {
				build, err := s.CreateBuildPlan(ctx, "admin", BuildPlanInput{Name: "无关构建", Kind: kind, Script: "printf ok", DockerfilePath: "Dockerfile"})
				if err != nil {
					t.Fatal(err)
				}
				stages = append(stages, model.WorkflowStage{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{{ID: "image", Name: "构建 A", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: build.ID}}}})
			}
			stages = append(stages, model.WorkflowStage{ID: "deploy", Name: "部署", Tasks: []model.WorkflowNode{{ID: "publish", Name: "部署 B", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: plan.ID}}}})
			workflow, err := s.GetWorkflow(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			source := model.WorkflowNode{ID: "source", Name: "代码源", Type: model.WorkflowNodeTrigger, Config: model.WorkflowNodeConfig{Events: []string{"manual"}, Branch: "main"}}
			saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: true, Source: source, Stages: stages})
			if err != nil {
				t.Fatal(err)
			}
			run, err := s.newResolvedWorkflowRun(ctx, app, saved.Workflow, source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := parseWorkflowSnapshot(run)
			if err != nil {
				t.Fatal(err)
			}
			if !snapshot.IndependentDeploymentImages {
				t.Fatal("新运行未使用独立镜像模式")
			}
			node := snapshot.Stages[len(snapshot.Stages)-1].Tasks[0]
			if len(node.Config.ComposeImages) != 0 || len(node.Config.ArtifactNodeIDs) != 0 || node.Config.ArtifactNodeID != "" {
				t.Fatalf("仍然绑定上游镜像: %+v", node.Config)
			}
			if !strings.Contains(snapshot.DeploymentPlans[node.ID].ComposeYAML, "unrelated:v2") {
				t.Fatal("用户镜像被替换")
			}
		})
	}
}

func TestIndependentComposePullsConfiguredImageAndPreservesUnrelatedRegistry(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	var requested atomic.Int32
	var authenticated atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "selected-user" || password != "selected-private-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="selected-registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Add(1)
		if r.URL.Path == "/v2/" {
			w.WriteHeader(200)
			return
		}
		if r.URL.Path != "/v2/team/deployed-b/manifests/v2" {
			t.Errorf("读取了用户没有部署的镜像: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("b", 64))
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(200)
	}))
	defer server.Close()
	registry := model.ImageRegistry{ID: "selected", Name: "部署仓库", ProjectID: project, DepartmentID: database.DefaultDepartmentID, Endpoint: server.URL, Namespace: "team", Provider: model.RegistryGeneric, AllowInsecureHTTP: true, IsActive: true}
	var err error
	registry.Username = "selected-user"
	registry.CredentialCiphertext, err = s.secrets.Encrypt("selected-private-password", []byte("image_registry:"+registry.Name+":credential"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(server.URL, "http://")
	source := "services:\n  frontend:\n    image: " + host + "/team/deployed-b:${DEPLOY_TAG:-v2}\n    depends_on: [external]\n  external:\n    image: other.example.com/public/web:v3\n  unused:\n    image: " + host + "/team/not-selected:v9\n"
	p := &executionContext{run: model.PipelineRun{ID: "run", ProjectID: project}, node: model.WorkflowNode{ID: "deploy", Config: model.WorkflowNodeConfig{DeploymentRegistryID: registry.ID, Compose: &model.ComposeDeploymentSettings{TagVariable: "${EDO_IMAGE_TAG}", Services: []string{"frontend"}}}}, variables: map[string]string{}, configuration: &deployment.ConfigurationSet{Values: map[string]string{"DEPLOY_TAG": "v2"}}}
	content, err := s.resolveConfiguredComposeImages(context.Background(), p, source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "deployed-b@sha256:"+strings.Repeat("b", 64)) || !strings.Contains(content, "other.example.com/public/web:v3") || len(p.composeArtifacts) != 1 {
		t.Fatalf("未严格使用用户配置的镜像: %s %+v", content, p.composeArtifacts)
	}
	if requested.Load() == 0 || authenticated.Load() == 0 {
		t.Fatal("没有使用所选仓库认证解析实际镜像")
	}
	if _, injected := p.variables["DEPLOY_TAG"]; injected {
		t.Fatal("env-file 插值被错误注入所有容器")
	}
	before := requested.Load()
	frozen := "sha256:" + strings.Repeat("a", 64)
	p.snapshot.RegistryInputs = map[string][]workflowRegistryInput{"deploy": {{RegistryID: registry.ID, ImageRef: host + "/team/deployed-b@" + frozen, Tag: "v2"}}}
	content, err = s.resolveConfiguredComposeImages(context.Background(), p, source)
	if err != nil || !strings.Contains(content, "deployed-b@"+frozen) || requested.Load() != before {
		t.Fatalf("已冻结的显式镜像被重新解析: %s %v", content, err)
	}
	p.run.ProjectID = "another-project"
	if _, err := s.resolveConfiguredComposeImages(context.Background(), p, source); err == nil || requested.Load() != before {
		t.Fatal("使用了其他项目的部署仓库")
	}
}

func TestIndependentComposeWarnsAboutOtherRegistryWithoutRewritingOrSendingCredentials(t *testing.T) {
	s, prepared, ctx := newComposeParameterTest(t, map[string]string{".env": "IMAGE_NAMESPACE=edo-demo\nREGISTRY_PASSWORD=registry-private-value\n"})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	registry := model.ImageRegistry{ID: "selected-registry", Name: "所选仓库", ProjectID: prepared.run.ProjectID, DepartmentID: prepared.run.DepartmentID, Endpoint: server.URL, Namespace: "team", Provider: model.RegistryGeneric, AllowInsecureHTTP: true, IsActive: true}
	registry.Username = "selected-user"
	var err error
	registry.CredentialCiphertext, err = s.secrets.Encrypt("registry-private-value", []byte("image_registry:"+registry.Name+":credential"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	prepared.node.Config.DeploymentRegistryID = registry.ID
	prepared.node.Config.Compose = &model.ComposeDeploymentSettings{}
	if err := s.prepareComposeParameterFiles(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	source := "services:\n  web:\n    image: ${IMAGE_NAMESPACE}/edo-demo-web:v0.0.1\n"
	result, err := s.resolveConfiguredComposeImages(ctx, prepared, source)
	if err != nil || requests.Load() != 0 || len(prepared.composeArtifacts) != 0 || !strings.Contains(result, "${IMAGE_NAMESPACE}/edo-demo-web:v0.0.1") {
		t.Fatal("其他仓库镜像被改写或使用了所选仓库认证", err)
	}
	logs, _, err := s.ListNodeRunLogs(ctx, prepared.run.ID, prepared.node.ID, 0, 100)
	if err != nil || len(logs) != 1 || logs[0].Level != "warning" || !strings.Contains(logs[0].Message, "edo-demo/edo-demo-web:v0.0.1 将从 docker.io 拉取") || !strings.Contains(logs[0].Message, strings.TrimPrefix(server.URL, "http://")+"/team") || strings.Contains(logs[0].Message, "registry-private-value") {
		t.Fatal("任务日志未准确解释镜像实际仓库和认证范围", err)
	}
}

func TestIndependentComposeConfigKeepsExplicitFiles(t *testing.T) {
	input := model.WorkflowNodeConfig{ArtifactNodeID: "old", ArtifactNodeIDs: []string{"old"}, ComposeImages: []model.ComposeImageBinding{{Service: "old", ArtifactNodeID: "old"}}, DeploymentInputs: []model.DeploymentInput{{Kind: "upstream", ArtifactNodeID: "files", Path: "dist"}}, ConfigurationFiles: []model.DeploymentConfigFile{{ID: "config", Path: ".env"}}}
	result := independentComposeConfig(input)
	if len(result.ComposeImages) != 0 || len(result.ArtifactNodeIDs) != 0 || result.ArtifactNodeID != "" {
		t.Fatal("自动镜像绑定没有移除")
	}
	if result.DeploymentInputs[0].ArtifactNodeID != "files" || result.ConfigurationFiles[0].Path != ".env" {
		t.Fatal("显式文件来源被清空")
	}
	if input.ArtifactNodeID != "old" {
		t.Fatal("历史配置被改写")
	}
}

func TestIndependentComposeDoesNotReadUnreferencedBuildMetadata(t *testing.T) {
	p := &executionContext{run: model.PipelineRun{ID: "run", Ref: "refs/tags/v1"}, deploymentPlan: model.DeploymentPlan{ComposeYAML: "services:\n  app:\n    image: unrelated/app:${MY_TAG}\n"}, variables: map[string]string{"MY_TAG": "user-tag"}, snapshot: workflowSnapshot{Stages: []model.WorkflowStage{{Tasks: []model.WorkflowNode{{ID: "build", Type: model.WorkflowNodeBuild}, {ID: "deploy", Type: model.WorkflowNodeDeploy}}}}, BuildPlans: map[string]workflowBuildPlanSnapshot{"build": {Kind: model.BuildPlanDockerfile}}}, node: model.WorkflowNode{ID: "deploy"}}
	// 没有数据库或制品服务，未引用上游变量也必须可以独立准备部署。
	if err := (&Service{}).addDeploymentPipelineVariables(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if p.variables["MY_TAG"] != "user-tag" || p.variables["EDO_GIT_TAG"] != "v1" {
		t.Fatal("用户变量被构建结果覆盖")
	}
}

func TestIndependentComposeBuildPlatformStillUsesDeploymentHosts(t *testing.T) {
	builds := []model.WorkflowNode{{ID: "one", Type: model.WorkflowNodeBuild}, {ID: "two", Type: model.WorkflowNodeBuild}}
	deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{Compose: &model.ComposeDeploymentSettings{}}}
	snapshot := workflowSnapshot{IndependentDeploymentImages: true, ExecutionVersion: 2, Stages: []model.WorkflowStage{{ID: "build", Tasks: builds}, {ID: "deploy", Tasks: []model.WorkflowNode{deploy}}}, DeploymentPlans: map[string]workflowDeploymentPlanSnapshot{"deploy": {Kind: model.DeploymentPlanComposeStack}}, DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{"deploy": {Architectures: []model.HostArchitecture{model.HostArchitectureARM64, model.HostArchitectureAMD64}}}}
	for _, build := range builds {
		platform, err := workflowBuildPlatform(snapshot, build.ID, workflowBuildPlanSnapshot{Kind: model.BuildPlanDockerfile, ImageRegistryID: "registry"})
		if err != nil || platform != "linux/amd64,linux/arm64" {
			t.Fatalf("取消镜像绑定后丢失目标主机架构: %s %q %v", build.ID, platform, err)
		}
	}
	platform, err := workflowBuildPlatform(snapshot, "one", workflowBuildPlanSnapshot{Kind: model.BuildPlanScript})
	if err != nil || platform != "" {
		t.Fatalf("文件构建不应被当作 Compose 镜像来源: %q %v", platform, err)
	}
	var historical workflowSnapshot
	if err := json.Unmarshal([]byte(`{"execution_version":2}`), &historical); err != nil || historical.IndependentDeploymentImages {
		t.Fatal("历史快照不应启用新部署规则", err)
	}
}

func TestIndependentComposeTagContextDoesNotBindImageOrRegistry(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	images := []model.Artifact{
		{ID: "one", Name: "unrelated:v1", ProjectID: project, ApplicationID: "app", Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, ImageRegistryID: "build-registry"},
		{ID: "foreign", Name: "unrelated:wrong", ProjectID: "other", ApplicationID: "app", Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable},
		{ID: "same-stage", Name: "other:wrong", ProjectID: project, ApplicationID: "app", Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable},
	}
	for _, image := range images {
		if err := db.Create(&image).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, node := range []model.PipelineNodeRun{
		{ID: "one", PipelineRunID: "run", NodeID: "build", ProjectID: project, ArtifactID: "one", Status: model.PipelineRunSucceeded},
		{ID: "foreign", PipelineRunID: "run", NodeID: "foreign", ProjectID: "other", ArtifactID: "foreign", Status: model.PipelineRunSucceeded},
		{ID: "same-stage", PipelineRunID: "run", NodeID: "parallel", ProjectID: project, ArtifactID: "same-stage", Status: model.PipelineRunSucceeded},
	} {
		if err := db.Create(&node).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &executionContext{
		run:            model.PipelineRun{ID: "run", ProjectID: project, ApplicationID: "app"},
		node:           model.WorkflowNode{ID: "deploy", Config: model.WorkflowNodeConfig{DeploymentRegistryID: "different-registry", DeploymentTagReference: "${MY_TAG}"}},
		deploymentPlan: model.DeploymentPlan{Kind: model.DeploymentPlanComposeStack, ComposeYAML: "services:\n  api:\n    image: independent.example.com/different:${EDO_IMAGE_TAG:-fallback}\n"},
		variables:      map[string]string{"MY_TAG": "user-tag"},
		snapshot: workflowSnapshot{IndependentDeploymentImages: true, Stages: []model.WorkflowStage{
			{Tasks: []model.WorkflowNode{{ID: "build", Type: model.WorkflowNodeBuild}, {ID: "foreign", Type: model.WorkflowNodeBuild}}},
			{Tasks: []model.WorkflowNode{{ID: "parallel", Type: model.WorkflowNodeBuild}, {ID: "deploy", Type: model.WorkflowNodeDeploy}}},
		}, BuildPlans: map[string]workflowBuildPlanSnapshot{"build": {Kind: model.BuildPlanDockerfile}, "foreign": {Kind: model.BuildPlanDockerfile}, "parallel": {Kind: model.BuildPlanDockerfile}}},
	}
	if err := s.addDeploymentPipelineVariables(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	values, err := deploymentScriptEnvironment(p)
	if err != nil || values["EDO_IMAGE_TAG"] != "v1" || values["MY_TAG"] != "user-tag" || len(p.composeCandidates) != 0 || p.artifact.ID != "" {
		t.Fatalf("Tag 上下文错误绑定了镜像或覆盖用户变量: %+v %v", values, err)
	}
	p.deploymentPlan = model.DeploymentPlan{Kind: model.DeploymentPlanScript, Script: "printf '%s' \"${EDO_IMAGE_TAG}\""}
	if err := s.addDeploymentPipelineVariables(context.Background(), p); err != nil || p.variables["EDO_IMAGE_TAG"] != "v1" {
		t.Fatalf("部署方案脚本缺少主动引用的流水线 Tag: %+v %v", p.variables, err)
	}
	if err := db.Model(&model.Artifact{}).Where("uuid = ?", "foreign").Update("project_id", project).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.PipelineNodeRun{}).Where("uuid = ?", "foreign").Update("project_id", project).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.addDeploymentPipelineVariables(context.Background(), p); err != nil || p.variables["EDO_IMAGE_TAG"] != "" {
		t.Fatalf("不同 Tag 时不能猜测上游镜像: %+v %v", p.variables, err)
	}
}

func TestIndependentDeploymentDoesNotAutomaticallyConsumeLastImage(t *testing.T) {
	build := model.WorkflowNode{ID: "build", Type: model.WorkflowNodeBuild}
	deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentInputVersion: 1, DeploymentScript: "printf independent"}}
	stages := []model.WorkflowStage{{ID: "build", Tasks: []model.WorkflowNode{build}}, {ID: "deploy", Tasks: []model.WorkflowNode{deploy}}}
	sources, err := configuredArtifactSources(stages)
	if err != nil || sources[deploy.ID] != "" {
		t.Fatalf("部署脚本借用了未选择的镜像: %+v %v", sources, err)
	}
	snapshot := workflowSnapshot{IndependentDeploymentImages: true, Stages: stages}
	states := map[string]model.PipelineNodeRun{build.ID: {Type: model.WorkflowNodeBuild, ArtifactID: "upstream", Status: model.PipelineRunSucceeded}}
	if id, err := parallelUpstreamArtifact(&snapshot, deploy, states, "old-parent-artifact"); err != nil || id != "" {
		t.Fatalf("独立部署仍从父运行或上游获取镜像: %q %v", id, err)
	}
	deploy.Config.ArtifactNodeID = build.ID
	if id, err := parallelUpstreamArtifact(&snapshot, deploy, states, "old-parent-artifact"); err != nil || id != "upstream" {
		t.Fatalf("用户显式选择的来源没有保留: %q %v", id, err)
	}
	sources, err = artifactSources(stages)
	if err != nil || sources["deploy"] != "build" {
		t.Fatal("历史快照的来源规则被改写", sources, err)
	}
}

func TestIndependentDeploymentScriptIgnoresParentImageArtifact(t *testing.T) {
	node := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: "plan", DeploymentInputVersion: 1, DeploymentScript: "printf independent"}}
	snapshot := workflowSnapshot{IndependentDeploymentImages: true, SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "deploy", Tasks: []model.WorkflowNode{node}}}, DeploymentPlans: map[string]workflowDeploymentPlanSnapshot{node.ID: {ID: "plan", Kind: model.DeploymentPlanDocker, TimeoutSeconds: 120}}, DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{node.ID: {ID: "target", Platform: model.DeploymentDocker}}}
	s, db, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("artifact_id", "unselected-image").Error; err != nil {
		t.Fatal(err)
	}
	s.artifacts = nil
	p, err := s.loadExecution(context.Background(), DeployTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, run.ExecutionJobID)
	if err != nil || p.artifact.ID != "" || p.run.ArtifactID != "" {
		t.Fatalf("自定义部署继承了未选择的父运行镜像: %+v %v", p, err)
	}
}

func TestIndependentDockerDeploymentRequiresConfiguredImage(t *testing.T) {
	build := model.WorkflowNode{ID: "build", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "build-plan"}}
	deploy := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: "deploy-plan", DeploymentInputVersion: 1}}
	issues := validateWorkflowArtifactFlow([]model.WorkflowNode{build, deploy}, map[string]model.BuildPlan{"build-plan": {Kind: model.BuildPlanDockerfile}}, map[string]model.DeploymentPlan{"deploy-plan": {Kind: model.DeploymentPlanDocker}}, nil)
	if !hasWorkflowIssue(issues, "missing_deployment_image") {
		t.Fatal("部署未配置镜像时不能自动使用上游镜像", issues)
	}
	deploy.Config.DeploymentInputs = []model.DeploymentInput{{Kind: "registry", RegistryID: "different-registry", Image: "different-name", Tag: "different-tag"}}
	issues = validateWorkflowArtifactFlow([]model.WorkflowNode{build, deploy}, map[string]model.BuildPlan{"build-plan": {Kind: model.BuildPlanDockerfile}}, map[string]model.DeploymentPlan{"deploy-plan": {Kind: model.DeploymentPlanDocker}}, nil)
	if len(issues) != 0 {
		t.Fatal("明确选择的部署镜像不能要求匹配上游", issues)
	}
}

func TestIndependentComposeInvalidImageRedactsSecretDiagnostics(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	registry := model.ImageRegistry{ID: "selected", ProjectID: project, Endpoint: "https://registry.example.com", IsActive: true}
	if err := db.Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&logs, nil))
	secret := "PrivateImageValue"
	p := &executionContext{run: model.PipelineRun{ProjectID: project}, node: model.WorkflowNode{ID: "deploy", Config: model.WorkflowNodeConfig{DeploymentRegistryID: registry.ID, Compose: &model.ComposeDeploymentSettings{}}}, variables: map[string]string{"SECRET": secret}, variableRedactions: []string{secret}}
	_, err := s.resolveConfiguredComposeImages(context.Background(), p, "services:\n  api:\n    image: registry.example.com/${SECRET}:v1\n")
	if err != ErrDeploymentInputs || strings.Contains(logs.String(), secret) || !strings.Contains(logs.String(), "compose_configured_image") {
		t.Fatalf("镜像诊断泄露密钥或缺少操作上下文: %v %s", err, logs.String())
	}
}
