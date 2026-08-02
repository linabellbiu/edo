package dockerengine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	distributionreference "github.com/distribution/reference"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	maximumScriptSize          = 256 * 1024
	maximumScriptSourceFiles   = 100_000
	maximumScriptArtifactFiles = 100_000
	scriptContainerUser        = "10001:10001"
	scriptWorkspace            = "/workspace"
)

var (
	ErrInvalidScriptContainer = errors.New("脚本执行配置无效")
	ErrScriptRuntimeImage     = errors.New("脚本运行镜像不可用")
	ErrScriptArtifact         = errors.New("脚本构建产物无效")
)

var scriptEnvironmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

var reservedScriptContainerEnvironment = map[string]struct{}{
	"CI": {}, "HOME": {}, "TMPDIR": {},
	"EDO_PIPELINE_RUN_ID": {}, "EDO_APPLICATION_ID": {}, "EDO_GIT_REF": {}, "EDO_COMMIT_SHA": {},
	"EDO_TARGET_PLATFORM": {}, "EDO_TARGET_ARCH": {}, "GOOS": {}, "GOARCH": {},
}

// ScriptContainerInput 描述一次隔离的非交互脚本执行。源码通过 Docker archive API
// 写入匿名卷，不允许调用方传入宿主机挂载或 Docker 连接凭据。
type ScriptContainerInput struct {
	Image             string
	Platform          string
	Script            string
	SourceDirectory   string
	WorkingDirectory  string
	ArtifactPath      string
	OutputDirectory   string
	Environment       map[string]string
	SystemEnvironment map[string]string
	Labels            map[string]string
	Timeout           time.Duration
	Stdout            io.Writer
	Stderr            io.Writer
}

type ScriptContainerResult struct {
	ExitCode     int64
	ImageID      string
	ArtifactPath string
}

// ScriptRuntimeImageStatus 表示语言构建镜像在当前构建运行时中的状态。
// 本地二进制与 Compose 会分别查询宿主机 Docker 和独立 DinD。
type ScriptRuntimeImageStatus struct {
	Image     string `json:"image"`
	ImageID   string `json:"image_id,omitempty"`
	Installed bool   `json:"installed"`
}

type ScriptContainerExitError struct {
	ExitCode int64
}

func (e *ScriptContainerExitError) Error() string {
	return fmt.Sprintf("脚本执行失败，退出码为 %d", e.ExitCode)
}

// RunScriptContainer 在当前构建运行时创建一次性 Linux 容器执行脚本。
// 容器使用匿名卷承载源码，根文件系统只读，退出后强制移除容器及匿名卷。
func (s *Service) RunScriptContainer(ctx context.Context, input ScriptContainerInput) (ScriptContainerResult, error) {
	if s == nil {
		return ScriptContainerResult{}, ErrInvalidScriptContainer
	}
	input, err := normalizeScriptContainerInput(input)
	if err != nil {
		return ScriptContainerResult{}, err
	}
	archive, err := createScriptSourceArchive(ctx, input.SourceDirectory)
	if err != nil {
		return ScriptContainerResult{}, err
	}
	defer func() {
		name := archive.Name()
		_ = archive.Close()
		_ = os.Remove(name)
	}()

	apiClient, err := s.builderExecutionClient()
	if err != nil {
		return ScriptContainerResult{}, err
	}
	defer apiClient.Close()
	executionContext, cancel := context.WithTimeout(ctx, input.Timeout)
	defer cancel()

	ping, err := apiClient.Ping(executionContext, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return ScriptContainerResult{}, fmt.Errorf("连接脚本构建运行时失败: %w", err)
	}
	if !strings.EqualFold(ping.OSType, "linux") {
		return ScriptContainerResult{}, ErrScriptRuntimeImage
	}
	imageID, err := ensureScriptRuntimeImage(executionContext, apiClient, input.Image, input.Platform)
	if err != nil {
		return ScriptContainerResult{}, err
	}

	created, err := apiClient.ContainerCreate(executionContext, scriptContainerCreateOptions(input, imageID))
	if err != nil {
		return ScriptContainerResult{}, fmt.Errorf("创建脚本执行容器失败: %w", err)
	}
	containerID := created.ID
	defer removeScriptContainer(apiClient, containerID)

	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return ScriptContainerResult{}, fmt.Errorf("读取脚本源码归档失败: %w", err)
	}
	if _, err := apiClient.CopyToContainer(executionContext, containerID, client.CopyToContainerOptions{
		DestinationPath: scriptWorkspace,
		Content:         archive,
		CopyUIDGID:      true,
	}); err != nil {
		return ScriptContainerResult{}, fmt.Errorf("传入脚本源码失败: %w", err)
	}
	if _, err := apiClient.CopyToContainer(executionContext, containerID, client.CopyToContainerOptions{
		DestinationPath: scriptWorkspace,
		Content:         scriptArchive(input.Script),
		CopyUIDGID:      true,
	}); err != nil {
		return ScriptContainerResult{}, fmt.Errorf("传入执行脚本失败: %w", err)
	}

	waitResult := apiClient.ContainerWait(executionContext, containerID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNextExit,
	})
	attached, err := apiClient.ContainerAttach(executionContext, containerID, client.ContainerAttachOptions{
		Stream: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		return ScriptContainerResult{}, fmt.Errorf("连接脚本输出失败: %w", err)
	}
	outputComplete := make(chan error, 1)
	go func() {
		_, copyErr := stdcopy.StdCopy(input.Stdout, input.Stderr, attached.Reader)
		outputComplete <- copyErr
	}()
	if _, err := apiClient.ContainerStart(executionContext, containerID, client.ContainerStartOptions{}); err != nil {
		attached.Close()
		waitForScriptOutput(outputComplete)
		return ScriptContainerResult{}, fmt.Errorf("启动脚本执行容器失败: %w", err)
	}

	waitResponse, err := waitForScriptContainer(executionContext, apiClient, containerID, waitResult)
	if err != nil {
		attached.Close()
		waitForScriptOutput(outputComplete)
		return ScriptContainerResult{}, err
	}
	if outputErr := finishScriptOutput(&attached.HijackedResponse, outputComplete); outputErr != nil {
		return ScriptContainerResult{}, fmt.Errorf("读取脚本输出失败: %w", outputErr)
	}
	result := ScriptContainerResult{ExitCode: waitResponse.StatusCode, ImageID: imageID}
	if waitResponse.Error != nil {
		return result, errors.New("脚本执行容器异常退出")
	}
	if waitResponse.StatusCode != 0 {
		return result, &ScriptContainerExitError{ExitCode: waitResponse.StatusCode}
	}
	if input.ArtifactPath == "" {
		return result, nil
	}
	artifactPath, err := copyScriptArtifact(executionContext, apiClient, containerID, input)
	if err != nil {
		return result, err
	}
	result.ArtifactPath = artifactPath
	return result, nil
}

// InspectScriptRuntimeImage 只检查当前构建运行时，不会触发拉取。
func (s *Service) InspectScriptRuntimeImage(ctx context.Context, image string) (ScriptRuntimeImageStatus, error) {
	image = strings.TrimSpace(image)
	status := ScriptRuntimeImageStatus{Image: image}
	if s == nil || !validPinnedRuntimeImage(image) {
		return status, ErrScriptRuntimeImage
	}
	apiClient, err := s.builderExecutionClient()
	if err != nil {
		return status, fmt.Errorf("连接构建运行时失败: %w", err)
	}
	defer apiClient.Close()
	ping, err := apiClient.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return status, fmt.Errorf("检查构建运行时失败: %w", err)
	}
	if !strings.EqualFold(ping.OSType, "linux") {
		return status, ErrScriptRuntimeImage
	}
	inspect, err := apiClient.ImageInspect(ctx, image)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return status, nil
		}
		return status, fmt.Errorf("检查脚本运行镜像失败: %w", err)
	}
	if inspect.ID == "" || !strings.EqualFold(inspect.Os, "linux") {
		return status, ErrScriptRuntimeImage
	}
	status.ImageID, status.Installed = inspect.ID, true
	return status, nil
}

// PrepareScriptRuntimeImage 将缺失的镜像拉取到当前 EDO 构建运行时，
// 不访问或修改宿主机的 Go、Node.js 和 Python 安装。
func (s *Service) PrepareScriptRuntimeImage(ctx context.Context, image string) (ScriptRuntimeImageStatus, error) {
	image = strings.TrimSpace(image)
	status := ScriptRuntimeImageStatus{Image: image}
	if s == nil || !validPinnedRuntimeImage(image) {
		return status, ErrScriptRuntimeImage
	}
	apiClient, err := s.builderExecutionClient()
	if err != nil {
		return status, fmt.Errorf("连接构建运行时失败: %w", err)
	}
	defer apiClient.Close()
	ping, err := apiClient.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return status, fmt.Errorf("检查构建运行时失败: %w", err)
	}
	if !strings.EqualFold(ping.OSType, "linux") {
		return status, ErrScriptRuntimeImage
	}
	imageID, err := ensureScriptRuntimeImage(ctx, apiClient, image, "")
	if err != nil {
		return status, err
	}
	status.ImageID, status.Installed = imageID, true
	return status, nil
}

func normalizeScriptContainerInput(input ScriptContainerInput) (ScriptContainerInput, error) {
	input.Image = strings.TrimSpace(input.Image)
	input.Platform = strings.ToLower(strings.TrimSpace(input.Platform))
	input.SourceDirectory = strings.TrimSpace(input.SourceDirectory)
	input.WorkingDirectory = strings.TrimSpace(input.WorkingDirectory)
	input.ArtifactPath = strings.TrimSpace(input.ArtifactPath)
	input.OutputDirectory = strings.TrimSpace(input.OutputDirectory)
	if input.WorkingDirectory == "" {
		input.WorkingDirectory = "."
	}
	if input.Stdout == nil {
		input.Stdout = io.Discard
	}
	if input.Stderr == nil {
		input.Stderr = io.Discard
	}
	if input.Script == "" || len(input.Script) > maximumScriptSize || strings.ContainsRune(input.Script, '\x00') ||
		input.Timeout < 30*time.Second || input.Timeout > 2*time.Hour ||
		!validPinnedRuntimeImage(input.Image) || !validScriptPlatform(input.Platform) || !validContainerRelativePath(input.WorkingDirectory) ||
		(input.ArtifactPath != "" && !validContainerRelativePath(input.ArtifactPath)) ||
		(input.ArtifactPath == "") != (input.OutputDirectory == "") || !validScriptEnvironment(input.Environment) ||
		!validScriptSystemEnvironment(input.SystemEnvironment) {
		return input, ErrInvalidScriptContainer
	}
	source, err := filepath.Abs(input.SourceDirectory)
	if err != nil {
		return input, ErrInvalidScriptContainer
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return input, ErrInvalidScriptContainer
	}
	info, err := os.Stat(resolvedSource)
	if err != nil || !info.IsDir() {
		return input, ErrInvalidScriptContainer
	}
	workingDirectory, err := secureScriptSourcePath(resolvedSource, input.WorkingDirectory)
	if err != nil {
		return input, ErrInvalidScriptContainer
	}
	if info, err := os.Stat(workingDirectory); err != nil || !info.IsDir() {
		return input, ErrInvalidScriptContainer
	}
	input.SourceDirectory = resolvedSource
	if input.OutputDirectory != "" {
		output, err := filepath.Abs(input.OutputDirectory)
		if err != nil || !emptyDirectory(output) {
			return input, ErrInvalidScriptContainer
		}
		input.OutputDirectory = output
	}
	return input, nil
}

func validScriptPlatform(value string) bool {
	return value == "" || value == "linux/amd64" || value == "linux/arm64"
}

func validPinnedRuntimeImage(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	named, err := distributionreference.ParseNormalizedNamed(value)
	if err != nil {
		return false
	}
	if tagged, ok := named.(distributionreference.Tagged); ok {
		return !strings.EqualFold(tagged.Tag(), "latest")
	}
	_, ok := named.(distributionreference.Digested)
	return ok
}

func validContainerRelativePath(value string) bool {
	if value == "" || len(value) > 512 || strings.ContainsAny(value, "\\\x00") || path.IsAbs(value) {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func validScriptEnvironment(values map[string]string) bool {
	if len(values) > 100 {
		return false
	}
	for name, value := range values {
		if !scriptEnvironmentNamePattern.MatchString(name) || len(value) > 16*1024 || strings.ContainsRune(value, '\x00') {
			return false
		}
		if _, reserved := reservedScriptContainerEnvironment[name]; reserved {
			return false
		}
	}
	return true
}

func validScriptSystemEnvironment(values map[string]string) bool {
	if len(values) > len(reservedScriptContainerEnvironment) {
		return false
	}
	for name, value := range values {
		if _, reserved := reservedScriptContainerEnvironment[name]; !reserved ||
			name == "CI" || name == "HOME" || name == "TMPDIR" || len(value) > 16*1024 || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	return true
}

func emptyDirectory(directory string) bool {
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(directory)
	return err == nil && len(entries) == 0
}

func secureScriptSourcePath(root, relative string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(relative))
	candidate := filepath.Join(root, cleaned)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	relativeToRoot, err := filepath.Rel(root, resolved)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", ErrInvalidScriptContainer
	}
	return resolved, nil
}

func ensureScriptRuntimeImage(ctx context.Context, apiClient *client.Client, image, platform string) (string, error) {
	inspect, err := apiClient.ImageInspect(ctx, image)
	architecture := strings.TrimPrefix(platform, "linux/")
	if err == nil && architecture != "" && !strings.EqualFold(inspect.Architecture, architecture) {
		err = errdefs.ErrNotFound
	}
	if err != nil {
		if !errdefs.IsNotFound(err) {
			return "", fmt.Errorf("检查脚本运行镜像失败: %w", err)
		}
		pullOptions := client.ImagePullOptions{}
		if architecture != "" {
			pullOptions.Platforms = []ocispec.Platform{{OS: "linux", Architecture: architecture}}
		}
		pull, pullErr := apiClient.ImagePull(ctx, image, pullOptions)
		if pullErr != nil {
			return "", fmt.Errorf("拉取脚本运行镜像失败: %w", pullErr)
		}
		if pullErr := pull.Wait(ctx); pullErr != nil {
			return "", fmt.Errorf("等待脚本运行镜像拉取完成失败: %w", pullErr)
		}
		inspect, err = apiClient.ImageInspect(ctx, image)
		if err != nil {
			return "", fmt.Errorf("校验脚本运行镜像失败: %w", err)
		}
	}
	if inspect.ID == "" || !strings.EqualFold(inspect.Os, "linux") ||
		(architecture != "" && !strings.EqualFold(inspect.Architecture, architecture)) {
		return "", ErrScriptRuntimeImage
	}
	return inspect.ID, nil
}

func scriptContainerCreateOptions(input ScriptContainerInput, imageID string) client.ContainerCreateOptions {
	initProcess := true
	pidsLimit := int64(512)
	labels := make(map[string]string, len(input.Labels)+2)
	for name, value := range input.Labels {
		if name != "" && len(name) <= 128 && len(value) <= 512 {
			labels[name] = value
		}
	}
	labels["io.edo.managed"] = "script"
	labels["io.edo.runtime.image"] = input.Image
	options := client.ContainerCreateOptions{
		Name: "edo-script-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		Config: &container.Config{
			Image:        imageID,
			User:         scriptContainerUser,
			Entrypoint:   []string{"/bin/sh"},
			Cmd:          []string{"-e", scriptWorkspace + "/.edo/script.sh"},
			WorkingDir:   scriptWorkspace + "/src/" + path.Clean(input.WorkingDirectory),
			Env:          scriptContainerEnvironment(input.Environment, input.SystemEnvironment),
			AttachStdout: true,
			AttachStderr: true,
			Labels:       labels,
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode("default"),
			ReadonlyRootfs: true,
			Privileged:     false,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			AutoRemove:     false,
			LogConfig:      container.LogConfig{Type: "none"},
			Init:           &initProcess,
			Tmpfs: map[string]string{
				"/tmp":      "rw,noexec,nosuid,nodev,size=268435456,uid=10001,gid=10001,mode=0700",
				"/home/edo": "rw,noexec,nosuid,nodev,size=16777216,uid=10001,gid=10001,mode=0700",
			},
			Mounts: []mount.Mount{{
				Type: mount.TypeVolume, Target: scriptWorkspace,
				VolumeOptions: &mount.VolumeOptions{NoCopy: true, Labels: map[string]string{"io.edo.managed": "script"}},
			}},
			Resources: container.Resources{
				Memory: 2 * 1024 * 1024 * 1024, MemorySwap: 2 * 1024 * 1024 * 1024,
				NanoCPUs: 2_000_000_000, PidsLimit: &pidsLimit,
			},
		},
	}
	if architecture := strings.TrimPrefix(input.Platform, "linux/"); architecture != "" {
		options.Platform = &ocispec.Platform{OS: "linux", Architecture: architecture}
	}
	return options
}

func scriptContainerEnvironment(configured, system map[string]string) []string {
	keys := make([]string, 0, len(configured))
	for key := range configured {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys)+3)
	for _, key := range keys {
		result = append(result, key+"="+configured[key])
	}
	keys = keys[:0]
	for key := range system {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+system[key])
	}
	// 受控目录最后写入，避免方案变量把非 root 用户重新指向只读或敏感路径。
	result = append(result, "CI=true", "HOME=/home/edo", "TMPDIR=/tmp")
	return result
}

func scriptArchive(script string) io.Reader {
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	content := []byte(script)
	_ = writer.WriteHeader(&tar.Header{
		Name: ".edo/", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 10001, Gid: 10001,
	})
	_ = writer.WriteHeader(&tar.Header{
		Name: ".edo/script.sh", Typeflag: tar.TypeReg, Mode: 0o500,
		Size: int64(len(content)), Uid: 10001, Gid: 10001,
	})
	_, _ = writer.Write(content)
	_ = writer.Close()
	return bytes.NewReader(output.Bytes())
}

func createScriptSourceArchive(ctx context.Context, sourceDirectory string) (*os.File, error) {
	return createScriptSourceArchiveWithLimits(ctx, sourceDirectory, maximumBuildContextSize, maximumScriptSourceFiles)
}

func createScriptSourceArchiveWithLimits(
	ctx context.Context,
	sourceDirectory string,
	maximumBytes int64,
	maximumFiles int,
) (*os.File, error) {
	if maximumBytes < 1 || maximumFiles < 1 {
		return nil, ErrInvalidScriptContainer
	}
	resolvedSource, err := filepath.EvalSymlinks(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("读取脚本源码目录失败: %w", err)
	}
	sourceDirectory = resolvedSource
	archive, err := os.CreateTemp("", "edo-script-source-*.tar")
	if err != nil {
		return nil, fmt.Errorf("创建脚本源码归档失败: %w", err)
	}
	removeArchive := true
	defer func() {
		if removeArchive {
			name := archive.Name()
			_ = archive.Close()
			_ = os.Remove(name)
		}
	}()
	writer := tar.NewWriter(archive)
	var totalSize int64
	entryCount := 0
	err = filepath.WalkDir(sourceDirectory, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(sourceDirectory, filePath)
		if err != nil {
			return err
		}
		if relative == "." {
			return writeScriptSourceHeader(writer, filePath, "src", sourceDirectory, &totalSize, maximumBytes)
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entryCount++
		if entryCount > maximumFiles {
			return ErrInvalidScriptContainer
		}
		return writeScriptSourceHeader(writer, filePath, "src/"+relative, sourceDirectory, &totalSize, maximumBytes)
	})
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("打包脚本源码失败: %w", err)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("读取脚本源码归档失败: %w", err)
	}
	removeArchive = false
	return archive, nil
}

func writeScriptSourceHeader(
	writer *tar.Writer,
	filePath, archivePath, root string,
	totalSize *int64,
	maximumBytes int64,
) error {
	info, err := os.Lstat(filePath)
	if err != nil {
		return err
	}
	mode := info.Mode()
	if !mode.IsDir() && !mode.IsRegular() && mode&os.ModeSymlink == 0 {
		return ErrInvalidScriptContainer
	}
	linkTarget := ""
	if mode&os.ModeSymlink != 0 {
		linkTarget, err = os.Readlink(filePath)
		if err != nil || filepath.IsAbs(linkTarget) {
			return ErrInvalidScriptContainer
		}
		resolved, resolveErr := filepath.EvalSymlinks(filePath)
		if resolveErr != nil {
			return ErrInvalidScriptContainer
		}
		relative, relativeErr := filepath.Rel(root, resolved)
		if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrInvalidScriptContainer
		}
		linkTarget = filepath.ToSlash(linkTarget)
	}
	header, err := tar.FileInfoHeader(info, linkTarget)
	if err != nil {
		return err
	}
	header.Name = archivePath
	header.Uid, header.Gid = 10001, 10001
	header.Uname, header.Gname = "", ""
	header.ModTime, header.AccessTime, header.ChangeTime = time.Time{}, time.Time{}, time.Time{}
	if mode.IsDir() {
		header.Mode = 0o755
	} else if mode.IsRegular() {
		header.Mode = int64(mode.Perm() & 0o755)
		*totalSize += info.Size()
		if *totalSize > maximumBytes {
			return ErrInvalidScriptContainer
		}
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	if !mode.IsRegular() {
		return nil
	}
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(writer, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func waitForScriptContainer(
	ctx context.Context,
	apiClient *client.Client,
	containerID string,
	waitResult client.ContainerWaitResult,
) (container.WaitResponse, error) {
	select {
	case response, ok := <-waitResult.Result:
		if !ok {
			return container.WaitResponse{}, errors.New("Docker 未返回脚本执行结果")
		}
		return response, nil
	case err, ok := <-waitResult.Error:
		if !ok || err == nil {
			err = errors.New("Docker 未返回脚本执行结果")
		}
		return container.WaitResponse{}, fmt.Errorf("等待脚本执行完成失败: %w", err)
	case <-ctx.Done():
		stopTimeout := 0
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = apiClient.ContainerStop(cleanupContext, containerID, client.ContainerStopOptions{Timeout: &stopTimeout})
		return container.WaitResponse{}, ctx.Err()
	}
}

func finishScriptOutput(attached *client.HijackedResponse, complete <-chan error) error {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-complete:
		attached.Close()
		return err
	case <-timer.C:
		attached.Close()
		return waitForScriptOutput(complete)
	}
}

func waitForScriptOutput(complete <-chan error) error {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-complete:
		return err
	case <-timer.C:
		return errors.New("等待脚本输出结束超时")
	}
}

func removeScriptContainer(apiClient *client.Client, containerID string) {
	cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = apiClient.ContainerRemove(cleanupContext, containerID, client.ContainerRemoveOptions{
		Force: true, RemoveVolumes: true,
	})
}

func copyScriptArtifact(
	ctx context.Context,
	apiClient *client.Client,
	containerID string,
	input ScriptContainerInput,
) (string, error) {
	containerPath := scriptWorkspace + "/src/" + path.Clean(input.ArtifactPath)
	archive, err := apiClient.CopyFromContainer(ctx, containerID, client.CopyFromContainerOptions{SourcePath: containerPath})
	if err != nil {
		return "", fmt.Errorf("读取脚本构建产物失败: %w", err)
	}
	defer archive.Content.Close()
	if !archive.Stat.Mode.IsRegular() && !archive.Stat.Mode.IsDir() {
		return "", ErrScriptArtifact
	}
	artifactPath, err := extractScriptArtifact(archive.Content, input.OutputDirectory, archive.Stat.Name, maximumBuildContextSize)
	if err != nil {
		return "", err
	}
	return artifactPath, nil
}

func extractScriptArtifact(source io.Reader, outputDirectory, sourceName string, maximumBytes int64) (string, error) {
	if !emptyDirectory(outputDirectory) || maximumBytes < 1 {
		return "", ErrScriptArtifact
	}
	reader := tar.NewReader(source)
	entryCount := 0
	topLevels := make(map[string]struct{})
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("解包脚本构建产物失败: %w", err)
		}
		entryCount++
		if entryCount > maximumScriptArtifactFiles || header.Size < 0 || strings.ContainsAny(header.Name, "\\\x00") {
			return "", ErrScriptArtifact
		}
		cleaned, ok := safeArchivePath(header.Name)
		if !ok {
			return "", ErrScriptArtifact
		}
		if cleaned == "." {
			if header.Typeflag != tar.TypeDir {
				return "", ErrScriptArtifact
			}
			continue
		}
		topLevel := strings.SplitN(cleaned, "/", 2)[0]
		topLevels[topLevel] = struct{}{}
		destination := filepath.Join(outputDirectory, filepath.FromSlash(cleaned))
		relative, err := filepath.Rel(outputDirectory, destination)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", ErrScriptArtifact
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, safeArtifactMode(header.Mode, true)); err != nil {
				return "", fmt.Errorf("创建脚本构建产物目录失败: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size > maximumBytes {
				return "", ErrScriptArtifact
			}
			maximumBytes -= header.Size
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return "", fmt.Errorf("创建脚本构建产物目录失败: %w", err)
			}
			file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, safeArtifactMode(header.Mode, false))
			if err != nil {
				return "", fmt.Errorf("创建脚本构建产物文件失败: %w", err)
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return "", fmt.Errorf("写入脚本构建产物失败: %w", copyErr)
			}
			if closeErr != nil {
				return "", fmt.Errorf("关闭脚本构建产物失败: %w", closeErr)
			}
		default:
			return "", ErrScriptArtifact
		}
	}
	if entryCount == 0 || len(topLevels) != 1 {
		return "", ErrScriptArtifact
	}
	name := strings.TrimSpace(sourceName)
	if name == "" || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		for topLevel := range topLevels {
			name = topLevel
		}
	}
	if _, exists := topLevels[name]; !exists {
		return "", ErrScriptArtifact
	}
	result := filepath.Join(outputDirectory, filepath.FromSlash(name))
	if _, err := os.Lstat(result); err != nil {
		return "", ErrScriptArtifact
	}
	return result, nil
}

func safeArchivePath(value string) (string, bool) {
	if value == "" || path.IsAbs(value) {
		return "", false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(value)
	return cleaned, cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func safeArtifactMode(value int64, directory bool) os.FileMode {
	mode := os.FileMode(value) & 0o777
	if directory {
		if mode == 0 {
			return 0o700
		}
		return mode
	}
	if mode == 0 {
		return 0o600
	}
	return mode
}
