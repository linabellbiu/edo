package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/model"
	"github.com/google/uuid"
)

type kubernetesRegistryFixture struct {
	s              *Service
	ctx            context.Context
	prepared       *executionContext
	registry       *model.ImageRegistry
	heads          atomic.Int32
	revision       atomic.Int32
	beforeManifest func(*http.Request)
}

func newKubernetesRegistryFixture(t *testing.T, tag string) *kubernetesRegistryFixture {
	t.Helper()
	s, db, _, repositoryID := newPipelineTestService(t)
	authorizeRegistryTestActors(t, s, "admin")
	f := &kubernetesRegistryFixture{s: s, ctx: deliveryTestContext()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v2/team/api/manifests/") {
			f.heads.Add(1)
			if f.beforeManifest != nil {
				f.beforeManifest(r)
			}
			digit := "a"
			if f.revision.Load() > 0 {
				digit = "b"
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat(digit, 64))
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(http.StatusOK)
			return
		}
		t.Errorf("未预期仓库请求: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	registry, err := s.CreateRegistry(f.ctx, "admin", RegistryInput{Name: "Kubernetes 显式仓库", Provider: model.RegistryGeneric, Endpoint: server.URL, Namespace: "team", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	f.registry = registry
	app, err := s.CreateApplication(f.ctx, "admin", ApplicationInput{Name: "kubernetes_registry_app", RepositoryID: repositoryID, PollIntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	node := model.WorkflowNode{ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "集群部署", Config: model.WorkflowNodeConfig{DeploymentPlanID: "plan", DeploymentRegistryID: registry.ID, DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "registry", RegistryID: registry.ID, Image: "api", Tag: tag}}}}
	target := model.DeploymentTarget{ID: "target", ProjectID: registry.ProjectID, DepartmentID: registry.DepartmentID, Platform: model.DeploymentKubernetes, RuntimeID: "cluster", Namespace: "demo", WorkloadName: "api", ContainerName: "api"}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, ExecutionVersion: 2, AuthorityID: "admin", IndependentDeploymentImages: true, ExplicitDeploymentRegistries: true, DeferredKubernetesRegistryImages: true,
		Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{node}}},
		DeploymentPlans: map[string]workflowDeploymentPlanSnapshot{node.ID: {ID: "plan", Kind: model.DeploymentPlanKubernetes}}, DeploymentTargets: map[string]workflowDeploymentTargetSnapshot{node.ID: {ID: target.ID, Platform: target.Platform, RuntimeID: target.RuntimeID, Namespace: target.Namespace, WorkloadName: target.WorkloadName, ContainerName: target.ContainerName}}}
	raw, _ := json.Marshal(snapshot)
	run := model.PipelineRun{ID: uuid.NewString(), ProjectID: registry.ProjectID, DepartmentID: registry.DepartmentID, ApplicationID: app.ID, Status: model.PipelineRunRunning, Stage: "deploy", CurrentNodeID: node.ID, WorkflowSnapshot: string(raw), CreatedBy: "admin", Ref: "refs/tags/git-tag-must-not-be-build-tag", CommitSHA: strings.Repeat("a", 40)}
	if err := db.WithContext(f.ctx).Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	f.prepared = &executionContext{run: run, node: node, snapshot: snapshot, deploymentPlan: model.DeploymentPlan{ID: "plan", Kind: model.DeploymentPlanKubernetes}, target: target, application: *app}
	s.docker = nil
	return f
}

func (f *kubernetesRegistryFixture) saveSnapshot(t *testing.T) {
	t.Helper()
	raw, err := json.Marshal(f.prepared.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f.prepared.run.WorkflowSnapshot = string(raw)
	if err = f.s.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("uuid = ?", f.prepared.run.ID).Update("workflow_snapshot", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
}

func (f *kubernetesRegistryFixture) addBuild(t *testing.T, nodeID, tag string) {
	t.Helper()
	node := model.WorkflowNode{ID: nodeID, Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: nodeID + "-plan"}}
	f.prepared.snapshot.Stages = append([]model.WorkflowStage{{ID: nodeID + "-stage", Tasks: []model.WorkflowNode{node}}}, f.prepared.snapshot.Stages...)
	if f.prepared.snapshot.BuildPlans == nil {
		f.prepared.snapshot.BuildPlans = map[string]workflowBuildPlanSnapshot{}
	}
	f.prepared.snapshot.BuildPlans[nodeID] = workflowBuildPlanSnapshot{ID: node.Config.BuildPlanID, Kind: model.BuildPlanDockerfile}
	build := model.BuildRun{ID: uuid.NewString(), PipelineRunID: f.prepared.run.ID, ProjectID: f.prepared.run.ProjectID, DepartmentID: f.prepared.run.DepartmentID, ApplicationID: f.prepared.run.ApplicationID, WorkflowNodeID: nodeID, CommitSHA: f.prepared.run.CommitSHA, Status: model.BuildRunStatusSucceeded}
	image := model.Artifact{ID: uuid.NewString(), BuildRunID: build.ID, PipelineRunID: build.PipelineRunID, ProjectID: build.ProjectID, DepartmentID: build.DepartmentID, ApplicationID: build.ApplicationID, Kind: model.ArtifactKindOCIImage, StorageKind: model.ArtifactStorageKindRegistry, Status: model.ArtifactStatusAvailable, Name: "registry.example.com/team/api:" + tag}
	state := model.PipelineNodeRun{ID: uuid.NewString(), PipelineRunID: build.PipelineRunID, ProjectID: build.ProjectID, DepartmentID: build.DepartmentID, NodeID: nodeID, Type: model.WorkflowNodeBuild, Status: model.PipelineRunSucceeded, ArtifactID: image.ID}
	for _, row := range []any{&build, &image, &state} {
		if err := f.s.db.WithContext(f.ctx).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.saveSnapshot(t)
}

func TestKubernetesRegistryImageFreezesAtDeploymentAndReusesDigestWithoutDocker(t *testing.T) {
	f := newKubernetesRegistryFixture(t, "v1")
	stale := *f.prepared
	// 其他任务及未来快照字段不可被本节点的冻结覆盖。
	var document map[string]json.RawMessage
	_ = json.Unmarshal([]byte(f.prepared.run.WorkflowSnapshot), &document)
	document["future_snapshot_field"] = json.RawMessage(`{"preserved":true}`)
	document["registry_inputs"] = json.RawMessage(`{"another-task":[{"index":0,"registry_id":"other","image_ref":"registry.invalid/other@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tag":"other"}]}`)
	raw, _ := json.Marshal(document)
	if err := f.s.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.prepared.run.ID).Update("workflow_snapshot", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.loadKubernetesRegistryImage(f.ctx, f.prepared); err != nil {
		t.Fatal(err)
	}
	image := f.prepared.artifact
	if image.ID != "" || image.ImageRegistryID != f.registry.ID || image.Digest != "sha256:"+strings.Repeat("a", 64) || !strings.Contains(image.Name, "/team/api:v1") || f.heads.Load() != 1 {
		t.Fatal("未准确冻结显式仓库镜像", image, f.heads.Load())
	}
	if _, _, err := f.s.resolveDeploymentImage(f.ctx, f.prepared); err != nil {
		t.Fatal("仓库镜像仍依赖 Docker", err)
	}
	f.revision.Store(1)
	if err := f.s.loadKubernetesRegistryImage(f.ctx, &stale); err != nil || stale.artifact.ImageRef != image.ImageRef || f.heads.Load() != 1 {
		t.Fatal("重投重新解析了可变 Tag", err, stale.artifact, f.heads.Load())
	}
	var retained model.PipelineRun
	if err := f.s.db.First(&retained, "uuid = ?", f.prepared.run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(retained.WorkflowSnapshot, `"future_snapshot_field":{"preserved":true}`) || !strings.Contains(retained.WorkflowSnapshot, `"another-task"`) {
		t.Fatal("覆盖了其他节点或未知快照字段")
	}
	var count int64
	if err := f.s.db.Model(&model.Artifact{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("仓库部署被伪造为构建制品", count, err)
	}
}

func TestKubernetesRegistryVariableTagRequiresActualUniquePriorBuild(t *testing.T) {
	f := newKubernetesRegistryFixture(t, "${EDO_IMAGE_TAG}")
	if err := f.s.loadKubernetesRegistryImage(f.ctx, f.prepared); err == nil || f.heads.Load() != 0 {
		t.Fatal("没有构建时借用了 Git Tag", err)
	}
	f.addBuild(t, "build-one", "build-v1")
	if err := f.s.loadKubernetesRegistryImage(f.ctx, f.prepared); err != nil || f.prepared.node.Config.DeploymentInputs[0].Tag != "build-v1" {
		t.Fatal("实际前序构建 Tag 无法展开", err)
	}
	other := newKubernetesRegistryFixture(t, "${EDO_IMAGE_TAG}")
	other.addBuild(t, "build-one", "v1")
	other.addBuild(t, "build-two", "v2")
	if err := other.s.loadKubernetesRegistryImage(other.ctx, other.prepared); err == nil || other.heads.Load() != 0 {
		t.Fatal("不同构建 Tag 被静默选取", err)
	}
}

func TestKubernetesRegistryRetryTagOnlyUsesTheAccurateOriginalRun(t *testing.T) {
	for _, accurateSource := range []bool{true, false} {
		t.Run(fmt.Sprint("accurate=", accurateSource), func(t *testing.T) {
			f := newKubernetesRegistryFixture(t, "${EDO_IMAGE_TAG}")
			f.addBuild(t, "build-one", "retry-v1")
			const source = "original-run"
			if err := f.s.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.prepared.run.ID).Updates(map[string]any{"trigger": "retry", "retry_of_id": source}).Error; err != nil {
				t.Fatal(err)
			}
			origin := source
			if !accurateSource {
				origin = "another-run"
			}
			for _, row := range []any{&model.Artifact{}, &model.BuildRun{}} {
				if err := f.s.db.Model(row).Where("pipeline_run_id = ?", f.prepared.run.ID).Update("pipeline_run_id", origin).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := f.s.db.Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ?", f.prepared.run.ID).Update("status", model.PipelineRunSkipped).Error; err != nil {
				t.Fatal(err)
			}
			err := f.s.loadKubernetesRegistryImage(f.ctx, f.prepared)
			if accurateSource && (err != nil || f.prepared.node.Config.DeploymentInputs[0].Tag != "retry-v1") {
				t.Fatal("精准原运行制品不能用于重试Tag", err)
			}
			if !accurateSource && (!errors.Is(err, ErrKubernetesRegistryTag) || f.heads.Load() != 0) {
				t.Fatal("重试借用了其他运行制品", err)
			}
		})
	}
}

func TestKubernetesRegistryJobRestoresScopeAndRejectsForeignScope(t *testing.T) {
	f := newKubernetesRegistryFixture(t, "v1")
	foreign := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "other", DepartmentID: f.prepared.run.DepartmentID})
	if err := f.s.loadKubernetesRegistryImage(foreign, f.prepared); !errors.Is(err, ErrPipelineExecutionConfig) || f.heads.Load() != 0 {
		t.Fatal("复用了其他项目范围", err)
	}
	if err := f.s.loadKubernetesRegistryImage(context.Background(), f.prepared); err != nil {
		t.Fatal("后台任务未恢复运行项目范围", err)
	}
	if err := f.s.db.Model(&model.User{}).Where("uuid = ?", "admin").Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.loadKubernetesRegistryImage(context.Background(), f.prepared); !errors.Is(err, cicd.ErrExecutionAuthorization) {
		t.Fatal("权限撤回没有保留准确错误类型", err)
	}
}

func TestKubernetesRegistryLoadExecutionIgnoresOldArtifactAndHasNoDockerPreparation(t *testing.T) {
	f := newKubernetesRegistryFixture(t, "v1")
	const jobID = "registry-deployment-job"
	if err := f.s.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.prepared.run.ID).Updates(map[string]any{"execution_job_id": jobID, "artifact_id": "unrelated-old-artifact"}).Error; err != nil {
		t.Fatal(err)
	}
	component := model.PipelineRunRepository{ID: uuid.NewString(), PipelineRunID: f.prepared.run.ID, RepositoryID: f.prepared.application.RepositoryID, Ref: f.prepared.run.Ref, CommitSHA: f.prepared.run.CommitSHA}
	if err := f.s.db.WithContext(f.ctx).Create(&component).Error; err != nil {
		t.Fatal(err)
	}
	f.s.artifacts = nil
	loaded, err := f.s.loadExecution(context.Background(), DeployTaskPayload{PipelineRunID: f.prepared.run.ID, WorkflowNodeID: f.prepared.node.ID}, jobID)
	if err != nil {
		t.Fatal("后台执行加载仍需要旧制品、制品服务或主机 Docker", err)
	}
	frozen := loaded.snapshot.RegistryInputs[loaded.node.ID]
	if len(frozen) != 1 || loaded.artifact.ImageRef != frozen[0].ImageRef || !strings.HasSuffix(loaded.artifact.ImageRef, "@sha256:"+strings.Repeat("a", 64)) || loaded.artifact.ID != "" || loaded.run.ArtifactID != "" {
		t.Fatal("执行镜像没有使用准确的快照摘要", loaded.artifact, frozen)
	}
	closers, err := f.s.prepareDeploymentInputs(f.ctx, loaded)
	for _, closer := range closers {
		_ = closer.Close()
	}
	if err != nil || len(loaded.inputFiles) != 0 {
		t.Fatal("Kubernetes 镜像准备触发了宿主机文件或 Docker 流程", err)
	}
	var count int64
	if err := f.s.db.Model(&model.Artifact{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("直接仓库部署创建了伪构建制品记录", count, err)
	}
}

func TestKubernetesRegistryTagVariablesPreserveGitContextAndRejectSecrets(t *testing.T) {
	for _, secret := range []bool{false, true} {
		t.Run(fmt.Sprint("secret=", secret), func(t *testing.T) {
			f := newKubernetesRegistryFixture(t, "${RELEASE_TAG}")
			value := "release-v4"
			if _, err := f.s.Delivery().SaveVariable(f.ctx, "admin", "", cicd.VariableInput{Name: "RELEASE_TAG", Kind: "env", Usage: "deploy", Value: &value, IsSecret: secret, IsActive: true}); err != nil {
				t.Fatal(err)
			}
			bindings, err := f.s.Delivery().FreezeVariables(f.ctx, f.prepared.run.ProjectID, f.prepared.run.ApplicationID, "", "deploy", f.prepared.run.Ref, []string{"RELEASE_TAG"})
			if err != nil {
				t.Fatal(err)
			}
			f.prepared.snapshot.VariableBindings = map[string][]model.DeliveryVariableBinding{"deploy": bindings}
			f.saveSnapshot(t)
			err = f.s.loadKubernetesRegistryImage(f.ctx, f.prepared)
			if secret {
				if err == nil || f.heads.Load() != 0 || strings.Contains(f.prepared.run.WorkflowSnapshot, value) {
					t.Fatal("密钥被用于持久化镜像身份", err)
				}
			} else if err != nil || f.prepared.node.Config.DeploymentInputs[0].Tag != value {
				t.Fatal("授权变量无法展开", err)
			}
		})
	}
	f := newKubernetesRegistryFixture(t, "${EDO_GIT_TAG}")
	if err := f.s.loadKubernetesRegistryImage(f.ctx, f.prepared); err != nil || f.prepared.node.Config.DeploymentInputs[0].Tag != "git-tag-must-not-be-build-tag" {
		t.Fatal("Git 上下文变量丢失", err)
	}
	missing := newKubernetesRegistryFixture(t, "v-${MISSING}")
	if err := missing.s.loadKubernetesRegistryImage(missing.ctx, missing.prepared); err == nil || missing.heads.Load() != 0 {
		t.Fatal("缺失变量被当成空文本", err)
	}
}

func TestKubernetesRegistryImageRejectsRevokedAndChangedFrozenSources(t *testing.T) {
	for _, mutation := range []string{"仓库停用", "身份失效", "摘要篡改", "镜像路径篡改", "旧快照未冻结"} {
		t.Run(mutation, func(t *testing.T) {
			f := newKubernetesRegistryFixture(t, "v1")
			if mutation != "旧快照未冻结" {
				if err := f.s.loadKubernetesRegistryImage(f.ctx, f.prepared); err != nil {
					t.Fatal(err)
				}
			}
			switch mutation {
			case "仓库停用":
				if err := f.s.db.Model(f.registry).Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			case "身份失效":
				if err := f.s.db.Model(&model.User{}).Where("uuid = ?", "admin").Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			case "摘要篡改":
				f.prepared.snapshot.RegistryInputs["deploy"][0].ImageRef = "registry.invalid/team/api@sha256:" + strings.Repeat("b", 64)
				f.saveSnapshot(t)
			case "镜像路径篡改":
				f.prepared.snapshot.RegistryInputs["deploy"][0].ImageRef = strings.Replace(f.prepared.artifact.ImageRef, "/api@", "/another@", 1)
				f.saveSnapshot(t)
			case "旧快照未冻结":
				f.prepared.snapshot.DeferredKubernetesRegistryImages = false
				f.saveSnapshot(t)
			}
			before := f.heads.Load()
			if err := f.s.loadKubernetesRegistryImage(f.ctx, f.prepared); err == nil || f.heads.Load() != before {
				t.Fatal("无效来源仍能执行或重新查询仓库", err)
			}
		})
	}
}
