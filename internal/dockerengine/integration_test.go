package dockerengine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
)

// TestDockerBuildAndComposeIntegration 覆盖“Dockerfile 构建 OCI 制品 → 同一运行时
// Docker/Compose 发布”的真实路径，并确认两种执行器都直接使用固定 Image ID。
// 默认跳过，开发机或 CI 明确提供 Docker 后以 EDO_TEST_DOCKER_INTEGRATION=1 启用。
func TestDockerBuildAndComposeIntegration(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker/Compose 集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(ctx, config.Database{
		Driver: "sqlite", DSN: "file:" + uuid.NewString() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: 10 * time.Minute,
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
	baseImage := strings.TrimSpace(os.Getenv("EDO_TEST_DOCKER_BASE_IMAGE"))
	if baseImage == "" {
		baseImage = "busybox:1.37.0"
	}
	dockerfile := fmt.Sprintf("# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89\nFROM %s\nARG APP_VERSION\nCOPY fixture.txt /edo-integration-fixture.txt\nLABEL io.edo.integration.version=$APP_VERSION\nCMD [\"sh\",\"-c\",\"while true; do echo EDO_INTEGRATION_HEARTBEAT; sleep 1; done\"]\n", baseImage)
	if err := os.WriteFile(filepath.Join(buildContext, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildContext, "fixture.txt"), []byte("local build context\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	image := "edo.local/integration:" + identity
	alternateImage := "edo.local/integration-mutated:" + identity
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
	builtImage, err := service.BuildLocalArtifactWithOptions(ctx, buildContext, "Dockerfile", image, BuildOptions{
		CacheEnabled: true, BuildArgs: map[string]string{"APP_VERSION": identity},
	}, 3*time.Minute, &buildOutput)
	if err != nil {
		t.Fatalf("真实 Dockerfile 构建失败: %v\n%s", err, buildOutput.String())
	}
	imageID := builtImage.ImageID
	if strings.Contains(buildOutput.String(), "load remote build context") ||
		!strings.Contains(buildOutput.String(), "load build context") {
		t.Fatalf("Buildx 没有直接读取本地版本工作区:\n%s", buildOutput.String())
	}
	if !IsValidImageID(imageID) {
		t.Fatalf("真实构建没有返回内容寻址镜像 ID: %q", imageID)
	}
	if builtImage.SizeBytes <= 0 {
		t.Fatalf("真实构建没有返回镜像大小: %+v", builtImage)
	}
	inspect, err := apiClient.ImageInspect(ctx, image)
	if err != nil || inspect.Config == nil || inspect.Config.Labels["io.edo.integration.version"] != identity {
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

	containerName := "edo-integration-" + identity
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
		ctx, LocalEndpointID, directTargetID, containerName, image, "integration:"+identity,
		imageID, "deployment-container-"+identity,
		90*time.Second, model.DockerContainerConfig{VolumeMounts: []model.DockerVolumeMount{{
			Type: "volume", Source: logicalVolume, Target: "/data",
		}}}, RegistryAuth{}, io.Discard, io.Discard,
	)
	if err != nil || warning != nil || previousContainer != (ImageSnapshot{}) {
		t.Fatalf("真实 Docker 容器发布失败: previous=%+v warning=%v err=%v", previousContainer, warning, err)
	}
	deployedContainer, err := apiClient.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if err != nil || deployedContainer.Container.Config == nil ||
		deployedContainer.Container.Config.Image != imageID || deployedContainer.Container.Image != imageID {
		t.Fatalf("Docker 没有直接按固定 Image ID 启动: container=%+v err=%v", deployedContainer.Container, err)
	}
	if deployedContainer.Container.Config.Labels[managedImageDisplayLabel] != "integration:"+identity {
		t.Fatalf("Docker 容器没有保存简短展示版本: %+v", deployedContainer.Container.Config.Labels)
	}
	replacementDeploymentID := "deployment-container-replacement-" + identity
	previousContainer, warning, err = service.DeployPreparedContainer(
		ctx, LocalEndpointID, directTargetID, containerName, image, "integration:"+identity,
		imageID, replacementDeploymentID,
		90*time.Second, model.DockerContainerConfig{VolumeMounts: []model.DockerVolumeMount{{
			Type: "volume", Source: logicalVolume, Target: "/data",
		}}}, RegistryAuth{}, io.Discard, io.Discard,
	)
	if err != nil || warning != nil || previousContainer.ID != imageID {
		t.Fatalf("重复发布 Docker 容器失败: previous=%+v warning=%v err=%v", previousContainer, warning, err)
	}
	replacedContainer, err := apiClient.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if err != nil || replacedContainer.Container.State == nil || !replacedContainer.Container.State.Running ||
		replacedContainer.Container.Config == nil ||
		replacedContainer.Container.Config.Labels["edo.deployment.id"] != replacementDeploymentID {
		t.Fatalf("重复发布后新容器没有稳定运行: container=%+v err=%v", replacedContainer.Container, err)
	}
	controlled, err := service.ControlContainer(ctx, LocalEndpointID, containerName, "stop")
	if err != nil || controlled.Running || controlled.State == "running" {
		t.Fatalf("真实 Docker 容器停止失败: state=%+v err=%v", controlled, err)
	}
	controlled, err = service.ControlContainer(ctx, LocalEndpointID, containerName, "restart")
	if err != nil || !controlled.Running || controlled.State != "running" {
		t.Fatalf("真实 Docker 容器重启失败: state=%+v err=%v", controlled, err)
	}
	shortRequestService := NewService(db, nil, config.Runtime{
		ConnectTimeout: 10 * time.Second,
		RequestTimeout: 250 * time.Millisecond,
	})
	logContext, logCancel := context.WithTimeout(ctx, 5*time.Second)
	logStream, err := shortRequestService.ContainerLogs(logContext, LocalEndpointID, containerName, ContainerLogOptions{
		Tail: 1, Follow: true,
	})
	if err != nil {
		logCancel()
		t.Fatalf("打开 Docker 容器持续日志失败: %v", err)
	}
	if _, err := readDockerLogFrame(logStream); err != nil {
		_ = logStream.Close()
		logCancel()
		t.Fatalf("读取 Docker 容器历史日志失败: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	frame, err := readDockerLogFrame(logStream)
	_ = logStream.Close()
	logCancel()
	if err != nil || !strings.Contains(string(frame), "EDO_INTEGRATION_HEARTBEAT") {
		t.Fatalf("Docker 容器日志流被普通请求超时截断: frame=%q err=%v", frame, err)
	}
	failedDeploymentID := "deployment-container-failed-" + identity
	previousContainer, warning, err = service.DeployPreparedContainer(
		ctx, LocalEndpointID, directTargetID, containerName, image, "integration:"+identity,
		imageID, failedDeploymentID, 90*time.Second,
		model.DockerContainerConfig{Command: []string{"sh", "-c", "exit 23"}},
		RegistryAuth{}, io.Discard, io.Discard,
	)
	if !errors.Is(err, ErrContainerRestarted) || warning != nil || previousContainer.ID != imageID {
		t.Fatalf("未保持运行的新容器没有触发可分类失败: previous=%+v warning=%v err=%v", previousContainer, warning, err)
	}
	restoredContainer, err := apiClient.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if err != nil || restoredContainer.Container.ID != replacedContainer.Container.ID ||
		restoredContainer.Container.State == nil || !restoredContainer.Container.State.Running ||
		restoredContainer.Container.Config == nil ||
		restoredContainer.Container.Config.Labels["edo.deployment.id"] != replacementDeploymentID {
		t.Fatalf("新容器启动失败后没有恢复旧容器: container=%+v err=%v", restoredContainer.Container, err)
	}
	if _, _, err := service.DeployPreparedContainer(
		ctx, LocalEndpointID, "other-"+directTargetID, containerName, image, "integration:"+identity, imageID,
		"deployment-other-"+identity, 90*time.Second, model.DockerContainerConfig{}, RegistryAuth{}, io.Discard, io.Discard,
	); err == nil || !strings.Contains(err.Error(), "不属于当前 EDO 部署目标") {
		t.Fatalf("其他部署目标仍可替换同名容器: %v", err)
	}
	volume, err := apiClient.VolumeInspect(ctx, actualVolume, client.VolumeInspectOptions{})
	if err != nil || volume.Volume.Labels["edo.managed"] != "true" ||
		volume.Volume.Labels["edo.deployment.target.id"] != directTargetID ||
		volume.Volume.Labels["edo.volume.logical_name"] != logicalVolume {
		t.Fatalf("Docker 命名卷未按部署目标隔离: volume=%+v err=%v", volume.Volume, err)
	}
	if _, err := apiClient.ImageTag(ctx, client.ImageTagOptions{Source: imageID, Target: image}); err != nil {
		t.Fatalf("恢复 Compose 校验使用的镜像标签失败: %v", err)
	}

	targetID := "compose-integration-" + identity
	project := composeProjectName(targetID)
	composeYAML := "services:\n  app:\n    image: ${EDO_IMAGE}\n"
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		command := exec.CommandContext(cleanupCtx, "docker", "compose", "--ansi", "never", "--project-name", project, "--file", "-", "down", "--remove-orphans")
		command.Env = append(os.Environ(), "EDO_IMAGE="+image)
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
	composeState, err := service.ControlCompose(ctx, LocalEndpointID, targetID, "app", "stop")
	if err != nil || composeState.Running {
		t.Fatalf("真实 Docker Compose 服务停止失败: state=%+v err=%v", composeState, err)
	}
	composeState, err = service.ControlCompose(ctx, LocalEndpointID, targetID, "app", "restart")
	if err != nil || !composeState.Running {
		t.Fatalf("真实 Docker Compose 服务重启失败: state=%+v err=%v", composeState, err)
	}
	composeStopTimeout := 2
	if _, err := apiClient.ContainerStop(ctx, deployed.Container.ID, client.ContainerStopOptions{Timeout: &composeStopTimeout}); err != nil {
		t.Fatalf("准备已停止 Compose 服务的恢复测试失败: %v", err)
	}
	composeOutput.Reset()
	previous, err = service.DeployCompose(ctx, ComposeDeployInput{
		EndpointID: LocalEndpointID, TargetID: targetID, ServiceName: "app", YAML: composeYAML,
		Image: image, ExpectedImageID: imageID, DeploymentID: "deployment-recovery-" + identity,
		Timeout: 90 * time.Second, Stdout: &composeOutput, Stderr: &composeOutput,
	})
	if err != nil || previous != imageID {
		t.Fatalf("已停止的 Docker Compose 服务无法重新发布: previous=%q err=%v\n%s", previous, err, composeOutput.String())
	}
	recoveredCompose, err := apiClient.ContainerInspect(ctx, deployed.Container.ID, client.ContainerInspectOptions{})
	if err != nil || recoveredCompose.Container.State == nil || !recoveredCompose.Container.State.Running {
		t.Fatalf("Docker Compose 重新发布后服务没有保持运行: container=%+v err=%v", recoveredCompose.Container, err)
	}
}

func readDockerLogFrame(reader io.Reader) ([]byte, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[4:])
	if size > 1024*1024 {
		return nil, fmt.Errorf("Docker 日志帧过大: %d", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func TestDockerBuildCacheAcrossIsolatedContexts(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker 缓存集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	service := NewService(nil, nil, config.Runtime{ConnectTimeout: 10 * time.Second, RequestTimeout: 30 * time.Second})
	if err := service.PingBuilder(ctx); err != nil {
		t.Fatalf("Docker 构建运行时不可用: %v", err)
	}
	baseImage := strings.TrimSpace(os.Getenv("EDO_TEST_DOCKER_BASE_IMAGE"))
	if baseImage == "" {
		baseImage = "busybox:1.37.0"
	}
	dockerfile := fmt.Sprintf("# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89\nFROM %s\nCOPY fixture.txt /fixture.txt\nRUN sha256sum /fixture.txt > /fixture.sha256\nCMD [\"sh\"]\n", baseImage)
	contexts := []string{t.TempDir(), t.TempDir()}
	for _, directory := range contexts {
		if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "fixture.txt"), []byte("same content across tags\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	identity := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	images := []string{"edo.local/cache-test-a:" + identity, "edo.local/cache-test-b:" + identity}
	apiClient, err := service.BuilderClient()
	if err != nil {
		t.Fatal(err)
	}
	defer apiClient.Close()
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, image := range images {
			_, _ = apiClient.ImageRemove(cleanupCtx, image, client.ImageRemoveOptions{Force: true})
		}
	}()
	var firstOutput, secondOutput bytes.Buffer
	firstID, err := service.BuildLocalWithOptions(
		ctx, contexts[0], "Dockerfile", images[0], BuildOptions{CacheEnabled: true}, 2*time.Minute, &firstOutput,
	)
	if err != nil {
		t.Fatalf("首次隔离工作区构建失败: %v\n%s", err, firstOutput.String())
	}
	secondID, err := service.BuildLocalWithOptions(
		ctx, contexts[1], "Dockerfile", images[1], BuildOptions{CacheEnabled: true}, 2*time.Minute, &secondOutput,
	)
	if err != nil {
		t.Fatalf("第二个隔离工作区构建失败: %v\n%s", err, secondOutput.String())
	}
	if firstID != secondID {
		t.Fatalf("内容相同但目录不同的构建结果不一致: first=%s second=%s", firstID, secondID)
	}
	if !strings.Contains(secondOutput.String(), "CACHED") || strings.Contains(secondOutput.String(), "load remote build context") {
		t.Fatalf("不同 Tag/Commit 目录没有复用本地 BuildKit 缓存:\n%s", secondOutput.String())
	}
}

// TestScriptContainerIntegration 覆盖“上传固定源码 → 隔离容器执行 → Docker API 回收制品”的真实路径。
func TestScriptContainerIntegration(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
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
		Labels:      map[string]string{"io.edo.integration.id": identity},
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
	var noArtifactStdout bytes.Buffer
	result, err = service.RunScriptContainer(ctx, ScriptContainerInput{
		Image: "alpine:3.22", Script: "test \"$(cat input.txt)\" = source\nprintf no-artifact-ok\n",
		SourceDirectory: source, WorkingDirectory: "src",
		Labels:  map[string]string{"io.edo.integration.id": identity},
		Timeout: 2 * time.Minute, Stdout: &noArtifactStdout,
	})
	if err != nil {
		t.Fatalf("不保存制品的真实脚本容器执行失败: %v\nstdout=%s", err, noArtifactStdout.String())
	}
	if !IsValidImageID(result.ImageID) || result.ExitCode != 0 || result.ArtifactPath != "" || noArtifactStdout.String() != "no-artifact-ok" {
		t.Fatalf("不保存制品的真实脚本执行结果不正确: result=%+v stdout=%q", result, noArtifactStdout.String())
	}
	apiClient, err := service.BuilderClient()
	if err != nil {
		t.Fatal(err)
	}
	defer apiClient.Close()
	containers, err := apiClient.ContainerList(ctx, client.ContainerListOptions{
		All: true, Filters: client.Filters{"label": {"io.edo.integration.id=" + identity: true}},
	})
	if err != nil {
		t.Fatalf("检查脚本容器清理结果失败: %v", err)
	}
	if len(containers.Items) != 0 {
		t.Fatalf("脚本执行结束后仍残留容器: %+v", containers.Items)
	}
}
