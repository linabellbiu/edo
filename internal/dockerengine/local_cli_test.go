package dockerengine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestLocalCLIEnvironmentKeepsSDKTargetAndIsolatesContexts(t *testing.T) {
	service, _ := newMonitorTestService(t)
	service.config.DockerBuilderHost = "unix:///fixture-local-docker.sock"
	t.Setenv("DOCKER_HOST", "tcp://unrelated.invalid:2375")
	t.Setenv("DOCKER_CONTEXT", "unrelated-context")
	t.Setenv("K3D_FIXTURE", "must-not-inherit")
	t.Setenv("KUBECONFIG", "/fixture/must-not-read")
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID)})
	env, before, cleanup, err := service.LocalCLIEnvironment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	configDir := ""
	for _, item := range env {
		if strings.Contains(item, "unrelated") || strings.HasPrefix(item, "K3D_") || strings.HasPrefix(item, "KUBECONFIG=") {
			t.Fatal("继承了其他执行上下文")
		}
		if strings.HasPrefix(item, "DOCKER_CONFIG=") {
			configDir = strings.TrimPrefix(item, "DOCKER_CONFIG=")
		}
	}
	if !strings.Contains(strings.Join(env, "\n"), "DOCKER_HOST=unix:///fixture-local-docker.sock") || len(before) != 64 {
		t.Fatal("CLI 与当前配置不一致")
	}
	data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if json.Unmarshal(data, &config) != nil {
		t.Fatal("无效 Docker CLI 配置")
	}
	if _, exists := config["currentContext"]; exists {
		t.Fatal("继承了其他 Docker Context")
	}
	service.config.DockerBuilderHost = "unix:///different-fixture.sock"
	_, after, remove, err := service.LocalCLIEnvironment(ctx)
	if remove != nil {
		remove()
	}
	if err != nil || before == after {
		t.Fatal("Docker 地址变化未进入确认摘要", err)
	}
	foreign := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: "foreign-project"})
	_, _, remove, err = service.LocalCLIEnvironment(foreign)
	if remove != nil {
		remove()
	}
	if !errors.Is(err, ErrEndpointNotFound) {
		t.Fatal("跨项目暴露了本地 CLI 环境", err)
	}
}
func TestLocalCLIEnvironmentRejectsDisabledDockerCapability(t *testing.T) {
	service, db := newMonitorTestService(t)
	service.config.DockerBuilderHost = "unix:///fixture.sock"
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID)})
	if err := db.WithContext(ctx).Model(&model.HostCapability{}).Where("host_id = ? AND kind = ?", model.BuiltinLocalHostID, model.HostCapabilityDocker).Update("status", model.HostCapabilityUnreachable).Error; err != nil {
		t.Fatal(err)
	}
	_, _, cleanup, err := service.LocalCLIEnvironment(ctx)
	if cleanup != nil {
		cleanup()
	}
	if !errors.Is(err, ErrEndpointNotFound) {
		t.Fatal("停用 Docker 能力后仍获得 CLI 连接", err)
	}
}
