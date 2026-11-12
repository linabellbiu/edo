package deployment

import (
	"context"
	"edo/internal/access"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/sshdeploy"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCustomRuntimeOnlyReadsExplicitApplicationServices(t *testing.T) {
	f := newContainerControlFixture(t)
	f.record.DeploymentPlanKind = model.DeploymentPlanScript
	f.record.CommandScript = "# EDO 用户部署脚本 v1\nprintf done"
	if err := f.db.Save(&f.record).Error; err != nil {
		t.Fatal(err)
	}
	bind := []model.WorkflowRuntimeService{{Name: "owned_api", RestartScript: "docker restart owned_api", RecreateScript: "docker compose up -d --force-recreate api"}}
	save := func(bindings []model.WorkflowRuntimeService) {
		payload, _ := json.Marshal(map[string]any{"stages": []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{{ID: f.record.WorkflowNodeID, Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: f.record.DeploymentPlanID, RuntimeServices: bindings}}}}}})
		if err := f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.record.PipelineRunID).Update("workflow_snapshot", string(payload)).Error; err != nil {
			t.Fatal(err)
		}
	}
	save(bind)
	var reads, lists atomic.Int32
	id := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.54")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			lists.Add(1)
			http.Error(w, "不得枚举主机容器", 500)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/owned_api/json") {
			reads.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"Id": id, "Name": "/owned_api", "Image": "sha256:" + strings.Repeat("b", 64), "Config": map[string]any{"Image": "alpine:3.22", "Labels": map[string]string{}}, "State": map[string]any{"Status": "running", "Running": true, "StartedAt": "2026-11-08T00:00:01Z"}, "RestartCount": 2, "Created": "2026-11-07T00:00:00Z"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	var capability model.HostCapability
	if err := f.db.Where("host_id = ? AND kind = ?", f.record.HostID, model.HostCapabilityDocker).First(&capability).Error; err != nil {
		t.Fatal(err)
	}
	f.record.RuntimeID = capability.RuntimeID
	if err := f.db.Save(&f.record).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.DockerEndpoint{}).Where("uuid = ?", capability.RuntimeID).Update("host", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	state, err := f.service.RuntimeState(f.ctx, f.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Kind != "custom" || len(state.Services) != 1 || state.Services[0].Name != "owned_api" || state.Services[0].Containers[0].RestartCount != 2 || lists.Load() != 0 || reads.Load() != 1 {
		t.Fatal("应用关联范围或真实信息错误", state)
	}
	configureDeploymentAuthorizationTestActor(t, f.db, f.record.ProjectID, f.record.DepartmentID, "operator", access.PermissionDeploymentRead, access.PermissionContainerRestart)
	plan, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, id, "operator", "restart")
	if err != nil || plan.Kind != "custom" || len(plan.Containers) != 1 {
		t.Fatal("绑定服务预览失败", err)
	}
	if _, err = f.service.ContainerControlPreview(f.ctx, f.record.ID, strings.Repeat("c", 64), "operator", "restart"); err != dockerengine.ErrContainerControlChanged {
		t.Fatal("未关联的容器不应可操作", err)
	}
	save(nil)
	before := reads.Load()
	state, err = f.service.RuntimeState(f.ctx, f.record.ID)
	if err != nil || state.Kind != "script" || len(state.Services) > 0 || reads.Load() != before {
		t.Fatal("未绑定的脚本不应检测 Docker", state, err)
	}
	save(bind)
	if err := f.db.Model(&model.HostCapability{}).Where("host_id = ? AND kind = ?", f.record.HostID, model.HostCapabilityDocker).Update("runtime_id", "replacement-runtime").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RuntimeState(f.ctx, f.record.ID); err == nil {
		t.Fatal("运行时换绑后不得自动切换到新 Docker")
	}
	if err := f.db.Model(&model.HostCapability{}).Where("host_id = ? AND kind = ?", f.record.HostID, model.HostCapabilityDocker).Update("runtime_id", capability.RuntimeID).Error; err != nil {
		t.Fatal(err)
	}
	other := f.record
	other.ID = uuid.NewString()
	other.RowID = 0
	other.ApplicationID = uuid.NewString()
	other.DeploymentPlanID = uuid.NewString()
	other.WorkloadName = "owned_api"
	other.CommandScript = ""
	other.DeploymentPlanKind = model.DeploymentPlanDocker
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RuntimeState(f.ctx, f.record.ID); err == nil {
		t.Fatal("不能把其他应用已有容器关联进来")
	}
}

func TestUnboundHistoricalScriptDoesNotRequireDockerOrRunSnapshot(t *testing.T) {
	s, _, _ := newDeploymentTestService(t)
	state, err := s.readRuntimeState(context.Background(), &model.DeploymentRecord{ID: "legacy-script", Platform: model.DeploymentSSH, DeploymentPlanKind: model.DeploymentPlanScript})
	if err != nil || state.Kind != "script" || len(state.Services) > 0 {
		t.Fatal("历史纯脚本部署状态读取回归", err)
	}
}

type customRuntimeActionRunner struct{ run func(sshdeploy.Input) }

func (r customRuntimeActionRunner) RunHostDeploymentScript(_ context.Context, input sshdeploy.Input) (sshdeploy.Result, error) {
	r.run(input)
	return sshdeploy.Result{Started: true, ExitCode: 0}, nil
}

func TestCustomRuntimeActionsUseFrozenCommandsAndVerifyContainerGeneration(t *testing.T) {
	for _, independent := range []bool{false, true} {
		t.Run(fmt.Sprint(independent), func(t *testing.T) { testCustomRuntimeFrozenActions(t, independent, false) })
	}
	t.Run("legacy-file-input", func(t *testing.T) { testCustomRuntimeFrozenActions(t, false, true) })
}

func testCustomRuntimeFrozenActions(t *testing.T, independent, fileInput bool) {
	t.Helper()
	f := newContainerControlFixture(t)
	commit := "0123456789abcdef0123456789abcdef01234567"
	if err := f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.record.PipelineRunID).Updates(map[string]any{"commit_sha": commit, "ref": "refs/tags/git-version"}).Error; err != nil {
		t.Fatal(err)
	}
	image := model.Artifact{ID: uuid.NewString(), ApplicationID: f.record.ApplicationID, ProjectID: f.record.ProjectID, PipelineRunID: f.record.PipelineRunID, Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, Name: "example/app:release-one", ImageRef: "example/app@sha256:" + strings.Repeat("e", 64)}
	if err := f.db.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	f.record.ArtifactID = image.ID
	if fileInput {
		file := model.Artifact{ID: uuid.NewString(), ProjectID: f.record.ProjectID, ApplicationID: f.record.ApplicationID, PipelineRunID: f.record.PipelineRunID, Kind: model.ArtifactKindFileBundle, Status: model.ArtifactStatusAvailable, Name: "files.tar.gz"}
		if err := f.db.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
		f.record.ArtifactID = file.ID
	}
	f.record.DeploymentPlanKind = model.DeploymentPlanScript
	f.record.CommandScript = "# EDO 用户部署脚本 v1\nprintf deployed"
	if err := f.db.Save(&f.record).Error; err != nil {
		t.Fatal(err)
	}
	binding := model.WorkflowRuntimeService{Name: "owned_api", RestartScript: "printf RESTART", RecreateScript: "printf RECREATE"}
	snapshot := map[string]any{"application_config_directory": "~/edo/container_control/config", "stages": []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{{ID: f.record.WorkflowNodeID, Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: f.record.DeploymentPlanID, RuntimeServices: []model.WorkflowRuntimeService{binding}}}}}}}
	if independent {
		snapshot["independent_deployment_images"] = true
		snapshot["build_plans"] = map[string]any{"before": map[string]any{"kind": model.BuildPlanDockerfile}, "later": map[string]any{"kind": model.BuildPlanDockerfile}}
		stages := snapshot["stages"].([]model.WorkflowStage)
		snapshot["stages"] = append([]model.WorkflowStage{{ID: "before", Tasks: []model.WorkflowNode{{ID: "before", Type: model.WorkflowNodeBuild}}}}, append(stages, model.WorkflowStage{ID: "later", Tasks: []model.WorkflowNode{{ID: "later", Type: model.WorkflowNodeBuild}}})...)
		later := model.Artifact{ID: uuid.NewString(), ApplicationID: f.record.ApplicationID, ProjectID: f.record.ProjectID, PipelineRunID: f.record.PipelineRunID, Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, Name: "example/app:future-version", ImageRef: "example/app@sha256:" + strings.Repeat("d", 64)}
		if err := f.db.Create(&later).Error; err != nil {
			t.Fatal(err)
		}
		for _, node := range []model.PipelineNodeRun{{ID: uuid.NewString(), PipelineRunID: f.record.PipelineRunID, ProjectID: f.record.ProjectID, NodeID: "before", ArtifactID: image.ID, Status: model.PipelineRunSucceeded}, {ID: uuid.NewString(), PipelineRunID: f.record.PipelineRunID, ProjectID: f.record.ProjectID, NodeID: "later", ArtifactID: later.ID, Status: model.PipelineRunSucceeded}} {
			if err := f.db.Create(&node).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	encoded, _ := json.Marshal(snapshot)
	if err := f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.record.PipelineRunID).Update("workflow_snapshot", string(encoded)).Error; err != nil {
		t.Fatal(err)
	}
	var capability model.HostCapability
	if err := f.db.Where("host_id = ? AND kind = ?", f.record.HostID, model.HostCapabilityDocker).First(&capability).Error; err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	generation := 1
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.54")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/owned_api/json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"Id": id, "Name": "/owned_api", "Image": "sha256:" + strings.Repeat("f", 64), "Config": map[string]any{"Image": "alpine:3.22", "Labels": map[string]string{}}, "State": map[string]any{"Status": "running", "Running": true, "StartedAt": fmt.Sprintf("2026-11-08T00:00:%02dZ", generation)}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	f.record.RuntimeID = capability.RuntimeID
	if err := f.db.Save(&f.record).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.DockerEndpoint{}).Where("uuid = ?", capability.RuntimeID).Update("host", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	f.service.ssh = customRuntimeActionRunner{run: func(input sshdeploy.Input) {
		if input.Environment["EDO_GIT_TAG"] != "git-version" {
			t.Error("重启、重建缺少首次部署的 Git Tag")
		}
		if input.Environment["EDO_COMMIT_SHA"] != "0123456789ab" {
			t.Errorf("重启、重建应注入 12 位短 Commit: %q", input.Environment["EDO_COMMIT_SHA"])
		}
		if input.Environment["EDO_IMAGE_TAG"] != "release-one" || input.Environment["IMAGE_TAG"] != "release-one" {
			t.Error("重启、重建没有注入本次部署的镜像 Tag")
		}
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, input.Script)
		generation++
		if strings.HasSuffix(input.Script, "printf RECREATE") {
			id = strings.Repeat("b", 64)
		}
	}}
	configureDeploymentAuthorizationTestActor(t, f.db, f.record.ProjectID, f.record.DepartmentID, "operator", access.PermissionDeploymentRead, access.PermissionContainerRestart, access.PermissionContainerRecreate)
	firstID := id
	restart, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, firstID, "operator", "restart")
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.service.ControlContainer(f.ctx, f.record.ID, firstID, "operator", "restart", ContainerControlConfirmation{Digest: restart.Digest})
	if err != nil || state.Runtime.Kind != "custom" {
		t.Fatal("执行绑定重启命令失败", err)
	}
	if _, err = f.service.ControlContainer(f.ctx, f.record.ID, firstID, "operator", "restart", ContainerControlConfirmation{Digest: restart.Digest}); err == nil {
		t.Fatal("已执行的旧确认不得复用")
	}
	recreate, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, firstID, "operator", "recreate")
	if err != nil {
		t.Fatal(err)
	}
	state, err = f.service.ControlContainer(f.ctx, f.record.ID, firstID, "operator", "recreate", ContainerControlConfirmation{Digest: recreate.Digest})
	if err != nil || state.Runtime.Services[0].Containers[0].ID == firstID {
		t.Fatal("重建没有核验新容器身份", err)
	}
	if len(calls) != 2 || !strings.HasSuffix(calls[0], "printf RESTART") || !strings.HasSuffix(calls[1], "printf RECREATE") {
		t.Fatal("执行了错误的服务命令", len(calls))
	}
	var stored model.PipelineRun
	if err := f.db.First(&stored, "uuid = ?", f.record.PipelineRunID).Error; err != nil || stored.CommitSHA != commit {
		t.Fatal("重启、重建改写了完整运行 Commit", err)
	}
}

func TestComposeRecreateRestoresBuiltinImageContext(t *testing.T) {
	for _, independent := range []bool{false, true} {
		t.Run(fmt.Sprint(independent), func(t *testing.T) { testComposeRecreateBuiltinContext(t, independent) })
	}
}

func testComposeRecreateBuiltinContext(t *testing.T, independent bool) {
	t.Helper()
	f := newContainerControlFixture(t)
	var run model.PipelineRun
	if err := f.db.First(&run, "uuid = ?", f.record.PipelineRunID).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["independent_deployment_images"] = independent
	wantTag := "v2"
	if !independent {
		image := model.Artifact{ID: uuid.NewString(), ProjectID: f.record.ProjectID, ApplicationID: f.record.ApplicationID, PipelineRunID: run.ID, Name: "example/api:built-version", Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, ImageRef: "example/api@sha256:" + strings.Repeat("e", 64)}
		if err := f.db.Create(&image).Error; err != nil {
			t.Fatal(err)
		}
		f.record.ArtifactID = image.ID
		if err := f.db.Save(&f.record).Error; err != nil {
			t.Fatal(err)
		}
		encodedStages, _ := json.Marshal(snapshot["stages"])
		var stages []model.WorkflowStage
		if err := json.Unmarshal(encodedStages, &stages); err != nil {
			t.Fatal(err)
		}
		stages[0].Tasks[0].Config.Compose.TagVariable = "${RELEASE_TAG}"
		snapshot["stages"] = stages
		wantTag = "built-version"
	}
	encoded, _ := json.Marshal(snapshot)
	if err := f.db.Model(&run).Updates(map[string]any{"workflow_snapshot": string(encoded), "ref": "refs/tags/v2", "commit_sha": strings.Repeat("a", 40)}).Error; err != nil {
		t.Fatal(err)
	}
	f.service.ssh = customRuntimeActionRunner{run: func(input sshdeploy.Input) {
		if input.Environment["EDO_GIT_TAG"] != "v2" || input.Environment["EDO_IMAGE_TAG"] != wantTag || input.Environment["EDO_COMMIT_SHA"] != strings.Repeat("a", 12) {
			t.Error("Compose 重建没有恢复与首次部署相同的内置版本")
		}
		if !sshdeploy.ValidEnvironmentVariables(input.Environment) || !independent && input.Environment["RELEASE_TAG"] != wantTag {
			t.Error("镜像 Tag 引用未转换为可传给执行主机的变量名")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.states, strings.Repeat("a", 64))
		delete(f.states, strings.Repeat("b", 64))
		f.states[strings.Repeat("e", 64)] = "running"
		f.states[strings.Repeat("f", 64)] = "running"
	}}
	configureDeploymentAuthorizationTestActor(t, f.db, f.record.ProjectID, f.record.DepartmentID, "operator", access.PermissionDeploymentRead, access.PermissionContainerRecreate)
	plan, err := f.service.ContainerControlPreview(f.ctx, f.record.ID, strings.Repeat("a", 64), "operator", "recreate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ControlContainer(f.ctx, f.record.ID, strings.Repeat("a", 64), "operator", "recreate", ContainerControlConfirmation{Digest: plan.Digest}); err != nil {
		t.Fatal(err)
	}
}
