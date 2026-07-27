package dockerengine

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	distributionreference "github.com/distribution/reference"
	"github.com/moby/moby/api/types/build"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

const maximumBuildContextSize int64 = 1024 * 1024 * 1024

type RegistryAuth struct {
	ServerAddress string
	Host          string
	Username      string
	Credential    string
}

func (s *Service) BuildAndPush(
	ctx context.Context,
	contextDirectory, dockerfile, image string,
	registry RegistryAuth,
	timeout time.Duration,
) (string, error, error) {
	apiClient, err := s.BuilderClient()
	if err != nil {
		return "", nil, err
	}
	defer apiClient.Close()
	buildContext, err := createBuildContext(contextDirectory, dockerfile)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		name := buildContext.Name()
		_ = buildContext.Close()
		_ = os.Remove(name)
	}()

	buildContextTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	authConfig := registryAuthConfig(registry)
	encodedAuth, err := encodeRegistryAuth(authConfig)
	if err != nil {
		return "", nil, err
	}
	cacheImage, err := cacheImageName(image)
	if err != nil {
		return "", nil, err
	}
	var cacheWarning error
	cachePull, err := apiClient.ImagePull(buildContextTimeout, cacheImage, client.ImagePullOptions{RegistryAuth: encodedAuth})
	if err != nil {
		cacheWarning = fmt.Errorf("拉取远程构建缓存失败: %w", err)
	} else if err := cachePull.Wait(buildContextTimeout); err != nil {
		cacheWarning = fmt.Errorf("拉取远程构建缓存失败: %w", err)
	}
	inlineCache := "1"
	result, err := apiClient.ImageBuild(buildContextTimeout, buildContext, client.ImageBuildOptions{
		Tags: []string{image, cacheImage}, Dockerfile: filepath.ToSlash(dockerfile),
		PullParent: true, Remove: true, ForceRemove: true,
		Version:     build.BuilderBuildKit,
		AuthConfigs: map[string]registrytypes.AuthConfig{registry.Host: authConfig},
		CacheFrom:   []string{cacheImage},
		BuildArgs:   map[string]*string{"BUILDKIT_INLINE_CACHE": &inlineCache},
	})
	if err != nil {
		return "", cacheWarning, fmt.Errorf("提交 Docker 镜像构建失败: %w", err)
	}
	if err := waitImageBuild(buildContextTimeout, result.Body); err != nil {
		return "", cacheWarning, err
	}

	push, err := apiClient.ImagePush(buildContextTimeout, image, client.ImagePushOptions{RegistryAuth: encodedAuth})
	if err != nil {
		return "", cacheWarning, fmt.Errorf("提交 Docker 镜像推送失败: %w", err)
	}
	if err := push.Wait(buildContextTimeout); err != nil {
		return "", cacheWarning, fmt.Errorf("等待 Docker 镜像推送完成失败: %w", err)
	}
	cachePush, err := apiClient.ImagePush(buildContextTimeout, cacheImage, client.ImagePushOptions{RegistryAuth: encodedAuth})
	if err != nil {
		cacheWarning = errors.Join(cacheWarning, fmt.Errorf("提交远程构建缓存失败: %w", err))
	} else if err := cachePush.Wait(buildContextTimeout); err != nil {
		cacheWarning = errors.Join(cacheWarning, fmt.Errorf("等待远程构建缓存更新完成失败: %w", err))
	}
	inspect, err := apiClient.ImageInspect(buildContextTimeout, image)
	if err != nil {
		return "", cacheWarning, fmt.Errorf("读取已推送镜像摘要失败: %w", err)
	}
	for _, digest := range inspect.RepoDigests {
		if strings.Contains(digest, "@sha256:") {
			return digest, cacheWarning, nil
		}
	}
	return "", cacheWarning, errors.New("镜像仓库没有返回可验证的镜像摘要")
}

// BuildLocal 在独立 Docker-in-Docker 构建节点中构建镜像，并返回内容寻址的镜像 ID。
func (s *Service) BuildLocal(
	ctx context.Context,
	contextDirectory, dockerfile, image string,
	timeout time.Duration,
) (string, error) {
	if !IsZRTLocalImage(image) {
		return "", errors.New("本地构建镜像名称无效")
	}
	apiClient, err := s.BuilderClient()
	if err != nil {
		return "", err
	}
	defer apiClient.Close()
	buildContext, err := createBuildContext(contextDirectory, dockerfile)
	if err != nil {
		return "", err
	}
	defer func() {
		name := buildContext.Name()
		_ = buildContext.Close()
		_ = os.Remove(name)
	}()

	buildContextTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cacheImage, err := cacheImageName(image)
	if err != nil {
		return "", err
	}
	inlineCache := "1"
	result, err := apiClient.ImageBuild(buildContextTimeout, buildContext, client.ImageBuildOptions{
		Tags: []string{image, cacheImage}, Dockerfile: filepath.ToSlash(dockerfile),
		PullParent: true, Remove: true, ForceRemove: true,
		Version:   build.BuilderBuildKit,
		CacheFrom: []string{cacheImage},
		BuildArgs: map[string]*string{"BUILDKIT_INLINE_CACHE": &inlineCache},
	})
	if err != nil {
		return "", fmt.Errorf("提交 Docker 本地镜像构建失败: %w", err)
	}
	if err := waitImageBuild(buildContextTimeout, result.Body); err != nil {
		return "", err
	}
	inspect, err := apiClient.ImageInspect(buildContextTimeout, image)
	if err != nil {
		return "", fmt.Errorf("读取本地构建镜像失败: %w", err)
	}
	if !IsValidImageID(inspect.ID) {
		return "", errors.New("Docker 没有返回可验证的本地镜像 ID")
	}
	return inspect.ID, nil
}

// TransferImageToSSH 将 Docker-in-Docker 中的镜像以 docker save 流传给目标 SSH 主机的 docker load。
// 传输全程不落盘，加载完成后再按镜像 ID 校验，避免只信任可变标签。
func (s *Service) TransferImageToSSH(
	ctx context.Context,
	endpointID, image, expectedImageID string,
	timeout time.Duration,
) error {
	if !IsZRTLocalImage(image) || !IsValidImageID(expectedImageID) {
		return errors.New("待传输的本地 Docker 镜像无效")
	}
	apiClient, err := s.BuilderClient()
	if err != nil {
		return err
	}
	defer apiClient.Close()
	transferContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	archive, err := apiClient.ImageSave(transferContext, []string{image})
	if err != nil {
		return fmt.Errorf("从 Docker-in-Docker 导出镜像失败: %w", err)
	}
	defer archive.Close()
	if err := s.loadImageToSSH(transferContext, endpointID, image, expectedImageID, archive); err != nil {
		return err
	}
	return nil
}

// IsZRTLocalImage 判断镜像是否属于仅在 Docker SSH 目标主机保存的本地命名空间。
func IsZRTLocalImage(value string) bool {
	named, err := distributionreference.ParseNormalizedNamed(strings.TrimSpace(value))
	return err == nil && distributionreference.Domain(named) == "zrt.local"
}

// IsValidImageID 校验 Docker 返回的内容寻址镜像 ID，防止只凭可变标签发布。
func IsValidImageID(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func cacheImageName(image string) (string, error) {
	named, err := distributionreference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("解析构建镜像名称失败: %w", err)
	}
	cache, err := distributionreference.WithTag(distributionreference.TrimNamed(named), "zrt-cache")
	if err != nil {
		return "", fmt.Errorf("生成构建缓存镜像名称失败: %w", err)
	}
	return distributionreference.FamiliarString(cache), nil
}

func registryAuthConfig(input RegistryAuth) registrytypes.AuthConfig {
	result := registrytypes.AuthConfig{Username: input.Username, ServerAddress: input.ServerAddress}
	if input.Username == "" {
		result.IdentityToken = input.Credential
	} else {
		result.Password = input.Credential
	}
	return result
}

func encodeRegistryAuth(config registrytypes.AuthConfig) (string, error) {
	payload, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("编码镜像仓库认证信息失败: %w", err)
	}
	return base64.URLEncoding.EncodeToString(payload), nil
}

func waitImageBuild(ctx context.Context, body io.ReadCloser) error {
	defer body.Close()
	decoder := json.NewDecoder(body)
	for {
		var message struct {
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("读取 Docker 构建结果失败: %w", err)
		}
		if message.Error != "" || (message.ErrorDetail != nil && message.ErrorDetail.Message != "") {
			return errors.New("Docker 镜像构建失败")
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("Docker 镜像构建超时: %w", err)
		}
	}
}

func createBuildContext(root, dockerfile string) (*os.File, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析构建上下文失败: %w", err)
	}
	dockerfile = filepath.Clean(dockerfile)
	if filepath.IsAbs(dockerfile) || dockerfile == ".." || strings.HasPrefix(dockerfile, ".."+string(filepath.Separator)) {
		return nil, errors.New("Dockerfile 必须位于构建上下文中")
	}
	if info, err := os.Stat(filepath.Join(root, dockerfile)); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("构建上下文中找不到 Dockerfile")
	}
	matcher, err := dockerIgnoreMatcher(root)
	if err != nil {
		return nil, err
	}
	archive, err := os.CreateTemp("", "zrt-build-context-*.tar")
	if err != nil {
		return nil, fmt.Errorf("创建临时构建上下文失败: %w", err)
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
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		mustInclude := relative == filepath.ToSlash(dockerfile) || relative == ".dockerignore"
		if !mustInclude && matcher != nil {
			ignored, err := matcher.MatchesOrParentMatches(relative)
			if err != nil {
				return err
			}
			if ignored {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSocket|os.ModeDevice|os.ModeNamedPipe) != 0 {
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = relative
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		totalSize += info.Size()
		if totalSize > maximumBuildContextSize {
			return errors.New("Docker 构建上下文超过 1 GiB 限制")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("打包 Docker 构建上下文失败: %w", err)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("读取 Docker 构建上下文失败: %w", err)
	}
	removeArchive = false
	return archive, nil
}

func dockerIgnoreMatcher(root string) (*patternmatcher.PatternMatcher, error) {
	file, err := os.Open(filepath.Join(root, ".dockerignore"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 .dockerignore 失败: %w", err)
	}
	defer file.Close()
	patterns, err := ignorefile.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("解析 .dockerignore 失败: %w", err)
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, fmt.Errorf("解析 .dockerignore 规则失败: %w", err)
	}
	return matcher, nil
}
