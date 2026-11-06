package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"edo/internal/database"
	"edo/internal/model"
)

func TestComposeStackRuntimeUsesFrozenHostScope(t *testing.T) {
	s, db, endpoint := newDeploymentTestService(t)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), DepartmentID: database.DefaultDepartmentID, UserID: "operator"})
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			writes.Add(1)
			http.Error(w, "不允许写入", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		labels := func(name string) map[string]string {
			return map[string]string{"com.docker.compose.project": "frozen-project", "com.docker.compose.service": name, "com.docker.compose.project.working_dir": "/srv/app"}
		}
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.54")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"Id": "api-id", "Labels": labels("api")}, map[string]any{"Id": "setup-id", "Labels": labels("setup")}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/api-id/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "api-id", "Config": map[string]any{"Labels": labels("api")}, "State": map[string]any{"Status": "running", "Running": true}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/setup-id/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "setup-id", "Config": map[string]any{"Labels": labels("setup")}, "State": map[string]any{"Status": "exited", "Running": false, "ExitCode": 0}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	if err := db.Model(&model.DockerEndpoint{}).Where("uuid = ?", endpoint).Update("host", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	repo := model.GitRepository{ID: uuid.NewString(), Name: "Compose 运行测试仓库", CloneURL: "https://example.test/compose.git", IsActive: true}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: uuid.NewString(), RepositoryID: repo.ID, Name: "compose_runtime", IsActive: true}
	if err := db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	record := model.DeploymentRecord{ID: uuid.NewString(), ApplicationID: app.ID, PipelineRunID: uuid.NewString(), DeploymentPlanID: uuid.NewString(), WorkflowNodeID: "deploy", TargetID: uuid.NewString(), TargetName: "测试 Compose", Platform: model.DeploymentSSH, HostID: dockerTestHostID, EnvironmentID: dockerTestEnvironmentID, WorkingDirectory: "/srv/app", DeploymentPlanKind: model.DeploymentPlanComposeStack, Status: model.DeploymentSucceeded, Operation: model.DeploymentRelease}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]any{"schema_version": model.WorkflowSchemaVersion, "stages": []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: record.DeploymentPlanID, Compose: &model.ComposeDeploymentSettings{ProjectName: "frozen-project", Services: []string{"api"}}}}}}}, "deployment_plans": map[string]any{"deploy": map[string]any{"id": record.DeploymentPlanID, "kind": "compose_stack", "compose_yaml": "services:\n  api:\n    image: alpine:3.22\n  unselected:\n    image: alpine:3.22\n"}}, "deployment_targets": map[string]any{"deploy": map[string]any{"id": record.TargetID, "project_id": record.ProjectID, "department_id": record.DepartmentID, "host_id": record.HostID, "environment_id": record.EnvironmentID, "working_directory": record.WorkingDirectory, "platform": "ssh"}}}
	encoded, _ := json.Marshal(snapshot)
	run := model.PipelineRun{ID: record.PipelineRunID, ApplicationID: app.ID, WorkflowSnapshot: string(encoded)}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	// 完成条件只从当次冻结的 Compose 读取，当前方案不存在也不影响成功记录。
	snapshot["deployment_plans"].(map[string]any)["deploy"].(map[string]any)["compose_yaml"] = "services:\n  api:\n    image: alpine:3.22\n    depends_on:\n      setup:\n        condition: service_completed_successfully\n  setup:\n    image: alpine:3.22\n  unselected:\n    image: alpine:3.22\n"
	encoded, _ = json.Marshal(snapshot)
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("workflow_snapshot", string(encoded)).Error; err != nil {
		t.Fatal(err)
	}
	state, err := s.RuntimeState(ctx, record.ID)
	if err != nil || state.RuntimeID != endpoint || state.Kind != "compose" || !state.Running || state.Name != "frozen-project" || len(state.Services) != 2 || state.Services[0].Name != "api" || state.Services[1].State != "completed" || !state.Services[1].RunToCompletion || len(state.Services[0].Containers) != 1 || state.Services[0].Containers[0].ID != "api-id" {
		t.Fatal("未从冻结的主机和服务范围读取状态", state, err)
	}
	if _, err = s.RuntimeConfiguration(ctx, record.ID); !errors.Is(err, ErrRuntimeControlUnsupported) {
		t.Fatal("Compose 被当成 Shell 配置命令", err)
	}
	// 冻结目标被破坏时拒绝继续解析，不使用可编辑方案或其他主机兜底。
	target := snapshot["deployment_targets"].(map[string]any)["deploy"].(map[string]any)
	target["host_id"] = "other-host"
	changed, _ := json.Marshal(snapshot)
	if err = db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("workflow_snapshot", string(changed)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.RuntimeState(ctx, record.ID); !errors.Is(err, ErrRuntimeStateUnavailable) {
		t.Fatal("损坏的冻结目标未被拒绝", err)
	}
	if err = db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("workflow_snapshot", string(encoded)).Error; err != nil {
		t.Fatal(err)
	}
	// 撤权后不能利用新增 Compose 分支绕过原有运行控制鉴权。
	if err = db.Create(&model.UserPermission{UserID: "operator", Permission: "deployment.execute", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.ControlRuntime(ctx, record.ID, "operator", RuntimeControlInput{Action: "stop"}); !errors.Is(err, ErrExecutionAuthorization) || writes.Load() != 0 {
		t.Fatal("Compose 运行控制绕过撤权", err)
	}
	if err = db.Model(&model.HostCapability{}).Where("host_id = ? AND kind = ?", record.HostID, model.HostCapabilityDocker).Update("status", model.HostCapabilityUnreachable).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.RuntimeState(ctx, record.ID); !errors.Is(err, ErrRuntimeStateUnavailable) {
		t.Fatal("失效 Docker 接入仍返回旧状态", err)
	}
}
