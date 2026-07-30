package dockerengine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
)

// TestDockerBuildAndComposeIntegration 覆盖“Dockerfile 构建 OCI 制品 → 同一运行时
// Docker/Compose 发布”的真实路径，并确认两种执行器都直接使用固定 Image ID。
// 默认跳过，开发机或 CI 明确提供 Docker 后以 ZRT_TEST_DOCKER_INTEGRATION=1 启用。
func TestDockerBuildAndComposeIntegration(t *testing.T) {
	if os.Getenv("ZRT_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker/Compose 集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:" + uuid.NewString() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开 Docker 集成测试数据库失败: %v", err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移 Docker 集成测试数据库失败: %v", err)
	}
	service := NewService(db, nil, config.Runtime{ConnectTimeout: 10 * time.Second, RequestTimeout: 30 * time.Second})
	if err := service.PingBuilder(ctx); err != nil {
		t.Fatalf("Docker 构建运行时不可用: %v", err)
	}

	buildContext := t.TempDir()
	dockerfile := "FROM busybox:1.37.0\nARG APP_VERSION\nLABEL io.zrt.integration.version=$APP_VERSION\nCMD [\"sh\",\"-c\",\"while true; do sleep 1; done\"]\n"
	if err := os.WriteFile(filepath.Join(buildContext, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	image := "zrt.local/integration:" + identity
	alternateImage := "zrt.local/integration-mutated:" + identity
	apiClient, err := service.BuilderClient()
	if err != nil {
		t.Fatal(err)
	}
	defer apiClient.Close()
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = apiClient.ImageRemove(cleanupCtx, image, client.ImageRemoveOptions{Force: true})
		_, _ = apiClient.ImageRemove(cleanupCtx, alternateImage, client.ImageRemoveOptions{Force: true})
	}()

	var buildOutput bytes.Buffer
	imageID, err := service.BuildLocalWithOptions(ctx, buildContext, "Dockerfile", image, BuildOptions{
		Pull: true, BuildArgs: map[string]string{"APP_VERSION": identity},
	}, 3*time.Minute, &buildOutput)
	if err != nil {
		t.Fatalf("真实 Dockerfile 构建失败: %v\n%s", err, buildOutput.String())
	}
	if !IsValidImageID(imageID) {
		t.Fatalf("真实构建没有返回内容寻址镜像 ID: %q", imageID)
	}
	inspect, err := apiClient.ImageInspect(ctx, image)
	if err != nil || inspect.Config == nil || inspect.Config.Labels["io.zrt.integration.version"] != identity {
		t.Fatalf("真实构建参数或镜像标签不正确: inspect=%+v err=%v", inspect.Config, err)
	}
	var alternateBuildOutput bytes.Buffer
	alternateImageID, err := service.BuildLocalWithOptions(ctx, buildContext, "Dockerfile", alternateImage, BuildOptions{
		BuildArgs: map[string]string{"APP_VERSION": "mutated-" + identity},
	}, 3*time.Minute, &alternateBuildOutput)
	if err != nil || alternateImageID == imageID {
		t.Fatalf("构建用于模拟标签漂移的镜像失败: image_id=%q err=%v\n%s", alternateImageID, err, alternateBuildOutput.String())
	}
	if _, err := apiClient.ImageTag(ctx, client.ImageTagOptions{Source: alternateImageID, Target: image}); err != nil {
		t.Fatalf("模拟构建标签在部署前被替换失败: %v", err)
	}

	containerName := "zrt-integration-" + identity
	directTargetID := "docker-integration-" + identity
	logicalVolume := "application-data"
	actualVolume := managedContainerVolumeName(directTargetID, logicalVolume)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = apiClient.ContainerRemove(cleanupCtx, containerName, client.ContainerRemoveOptions{Force: true})
		_, _ = apiClient.VolumeRemove(cleanupCtx, actualVolume, client.VolumeRemoveOptions{Force: true})
	}()
	previousContainer, warning, err := service.DeployPreparedContainer(
		ctx, LocalEndpointID, directTargetID, containerName, image, imageID, "deployment-container-"+identity,
		90*time.Second, model.DockerContainerConfig{VolumeMounts: []model.DockerVolumeMount{{
			Type: "volume", Source: logicalVolume, Target: "/data",
		}}}, RegistryAuth{},
	)
	if err != nil || warning != nil || previousContainer != (ImageSnapshot{}) {
		t.Fatalf("真实 Docker 容器发布失败: previous=%+v warning=%v err=%v", previousContainer, warning, err)
	}
	deployedContainer, err := apiClient.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if err != nil || deployedContainer.Container.Config == nil ||
		deployedContainer.Container.Config.Image != imageID || deployedContainer.Container.Image != imageID {
		t.Fatalf("Docker 没有直接按固定 Image ID 启动: container=%+v err=%v", deployedContainer.Container, err)
	}
	if _, _, err := service.DeployPreparedContainer(
		ctx, LocalEndpointID, "other-"+directTargetID, containerName, image, imageID,
		"deployment-other-"+identity, 90*time.Second, model.DockerContainerConfig{}, RegistryAuth{},
	); err == nil || !strings.Contains(err.Error(), "不属于当前 ZRT 部署目标") {
		t.Fatalf("其他部署目标仍可替换同名容器: %v", err)
	}
	volume, err := apiClient.VolumeInspect(ctx, actualVolume, client.VolumeInspectOptions{})
	if err != nil || volume.Volume.Labels["zrt.managed"] != "true" ||
		volume.Volume.Labels["zrt.deployment.target.id"] != directTargetID ||
		volume.Volume.Labels["zrt.volume.logical_name"] != logicalVolume {
		t.Fatalf("Docker 命名卷未按部署目标隔离: volume=%+v err=%v", volume.Volume, err)
	}
	if _, err := apiClient.ImageTag(ctx, client.ImageTagOptions{Source: imageID, Target: image}); err != nil {
		t.Fatalf("恢复 Compose 校验使用的镜像标签失败: %v", err)
	}

	targetID := "compose-integration-" + identity
	project := composeProjectName(targetID)
	composeYAML := "services:\n  app:\n    image: ${ZRT_IMAGE}\n"
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		command := exec.CommandContext(cleanupCtx, "docker", "compose", "--ansi", "never", "--project-name", project, "--file", "-", "down", "--remove-orphans")
		command.Env = append(os.Environ(), "ZRT_IMAGE="+image)
		command.Stdin = strings.NewReader(composeYAML)
		_ = command.Run()
	}()
	var composeOutput bytes.Buffer
	previous, err := service.DeployCompose(ctx, ComposeDeployInput{
		EndpointID: LocalEndpointID, TargetID: targetID, ServiceName: "app", YAML: composeYAML,
		Image: image, ExpectedImageID: imageID, DeploymentID: "deployment-" + identity,
		Timeout: 90 * time.Second, Stdout: &composeOutput, Stderr: &composeOutput,
	})
	if err != nil {
		t.Fatalf("真实 Docker Compose 发布失败: %v\n%s", err, composeOutput.String())
	}
	if previous != "" {
		t.Fatalf("首次 Compose 发布不应存在上一镜像: %q", previous)
	}
	containers, err := apiClient.ContainerList(ctx, client.ContainerListOptions{
		All: true,
		Filters: client.Filters{"label": {
			"com.docker.compose.project=" + project: true,
			"com.docker.compose.service=app":        true,
		}},
	})
	if err != nil || len(containers.Items) != 1 {
		t.Fatalf("读取真实 Compose 容器失败: containers=%+v err=%v", containers.Items, err)
	}
	deployed, err := apiClient.ContainerInspect(ctx, containers.Items[0].ID, client.ContainerInspectOptions{})
	if err != nil || deployed.Container.Config == nil || deployed.Container.Config.Image != imageID || deployed.Container.Image != imageID {
		t.Fatalf("Compose 没有直接按固定 Image ID 启动: container=%+v err=%v", deployed.Container, err)
	}
}

// TestScriptContainerIntegration 覆盖“上传固定源码 → 隔离容器执行 → Docker API 回收制品”的真实路径。
func TestScriptContainerIntegration(t *testing.T) {
	if os.Getenv("ZRT_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker 脚本执行集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	service := NewService(nil, nil, config.Runtime{ConnectTimeout: 10 * time.Second, RequestTimeout: 30 * time.Second})
	if err := service.PingBuilder(ctx); err != nil {
		t.Fatalf("Docker 构建运行时不可用: %v", err)
	}
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "src", "input.txt"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	identity := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	var stdout, stderr bytes.Buffer
	result, err := service.RunScriptContainer(ctx, ScriptContainerInput{
		Image:           "alpine:3.22",
		Script:          "mkdir -p dist\nprintf '%s|%s' \"$(cat input.txt)\" \"$BUILD_VALUE\" > dist/result.txt\nprintf stdout-log\nprintf stderr-log >&2\n",
		SourceDirectory: source, WorkingDirectory: "src", ArtifactPath: "src/dist", OutputDirectory: output,
		Environment: map[string]string{"BUILD_VALUE": identity},
		Labels:      map[string]string{"io.zrt.integration.id": identity},
		Timeout:     2 * time.Minute, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("真实脚本容器执行失败: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !IsValidImageID(result.ImageID) || result.ExitCode != 0 || result.ArtifactPath == "" {
		t.Fatalf("真实脚本执行结果不完整: %+v", result)
	}
	content, err := os.ReadFile(filepath.Join(result.ArtifactPath, "result.txt"))
	if err != nil || string(content) != "source|"+identity {
		t.Fatalf("真实脚本制品不正确: content=%q err=%v", content, err)
	}
	if stdout.String() != "stdout-log" || stderr.String() != "stderr-log" {
		t.Fatalf("真实脚本输出没有正确解复用: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(source, "src", "dist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("脚本容器修改了宿主机检出目录: %v", err)
	}
	apiClient, err := service.BuilderClient()
	if err != nil {
		t.Fatal(err)
	}
	defer apiClient.Close()
	containers, err := apiClient.ContainerList(ctx, client.ContainerListOptions{
		All: true, Filters: client.Filters{"label": {"io.zrt.integration.id=" + identity: true}},
	})
	if err != nil {
		t.Fatalf("检查脚本容器清理结果失败: %v", err)
	}
	if len(containers.Items) != 0 {
		t.Fatalf("脚本执行结束后仍残留容器: %+v", containers.Items)
	}
}
