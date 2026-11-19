package terminal

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"edo/internal/terminalassets"
	"edo/internal/terminalwire"
)

const maximumDockerHelperBytes = 8 << 20

type executionAuthorizationKey struct{}
type dockerPreparationObserverKey struct{}

type DockerPreparation struct {
	Architecture string
	Path         string
	Digest       string
	Succeeded    bool
	Attempts     int
	Volume       string
}

// 准备工具与 Exec 使用同一个当前权限检查，不能从查看或日志权限推导。
func WithExecutionAuthorization(ctx context.Context, check func() bool) context.Context {
	return context.WithValue(ctx, executionAuthorizationKey{}, check)
}

func WithDockerPreparationObserver(ctx context.Context, observer func(DockerPreparation)) context.Context {
	return context.WithValue(ctx, dockerPreparationObserverKey{}, observer)
}

func executionAuthorized(ctx context.Context, required bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	check, ok := ctx.Value(executionAuthorizationKey{}).(func() bool)
	if (!ok && required) || (ok && !check()) {
		return ErrExecAuthorization
	}
	return nil
}

func defaultDockerHelperDirectory() string {
	executable, _ := os.Executable()
	candidates := []string{filepath.Join(filepath.Dir(executable), "terminal-tools"), filepath.Join("bin", "terminal-tools")}
	for _, directory := range candidates {
		if info, err := os.Stat(directory); err == nil && info.IsDir() {
			return directory
		}
	}
	return candidates[0]
}

func readDockerHelper(directory, architecture string) ([]byte, string, error) {
	machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[architecture]
	if machine == elf.EM_NONE {
		return nil, "", errors.New("目标容器架构不受支持")
	}
	file := filepath.Join(directory, "linux-"+architecture, "edo-terminal-session")
	info, err := os.Lstat(file)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumDockerHelperBytes {
		return nil, "", errors.New("随包终端工具无效")
	}
	reader, err := os.Open(file)
	if err != nil {
		return nil, "", err
	}
	defer reader.Close()
	opened, err := reader.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, "", errors.Join(err, errors.New("随包终端工具身份发生变化"))
	}
	content, err := io.ReadAll(io.LimitReader(reader, maximumDockerHelperBytes+1))
	if err != nil || len(content) > maximumDockerHelperBytes {
		return nil, "", errors.Join(err, errors.New("读取随包终端工具失败"))
	}
	binary, err := elf.NewFile(bytes.NewReader(content))
	if err != nil {
		return nil, "", err
	}
	defer binary.Close()
	if binary.Class != elf.ELFCLASS64 || binary.Data != elf.ELFDATA2LSB || binary.Machine != machine || binary.Type != elf.ET_EXEC {
		return nil, "", errors.New("终端工具架构无效")
	}
	for _, program := range binary.Progs {
		if program.Type == elf.PT_INTERP {
			return nil, "", errors.New("终端工具必须使用静态链接")
		}
	}
	digest := sha256.Sum256(content)
	return content, hex.EncodeToString(digest[:]), nil
}

func dockerHelperMatches(ctx context.Context, api *client.Client, id, file, digest string) (bool, error) {
	stat, err := api.ContainerStatPath(ctx, id, client.ContainerStatPathOptions{Path: file})
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !stat.Stat.Mode.IsRegular() || stat.Stat.LinkTarget != "" || stat.Stat.Size > maximumDockerHelperBytes {
		return false, errors.New("目标终端工具不是安全的普通文件")
	}
	if stat.Stat.Size <= 0 {
		return false, nil
	}
	if stat.Stat.Mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return false, errors.New("终端工具不能包含提权位")
	}
	if stat.Stat.Mode.Perm()&0555 != 0555 {
		return false, nil
	}
	archive, err := api.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: file})
	if err != nil {
		return false, err
	}
	defer archive.Content.Close()
	reader := tar.NewReader(io.LimitReader(archive.Content, maximumDockerHelperBytes+64*1024))
	header, err := reader.Next()
	if err != nil {
		return false, err
	}
	if header.Typeflag != tar.TypeReg || path.Base(header.Name) != path.Base(file) || header.Size != stat.Stat.Size || header.Linkname != "" {
		return false, errors.New("目标工具归档身份无效")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return false, err
	}
	return hex.EncodeToString(hash.Sum(nil)) == digest, nil
}

func (s *Service) prepareDockerHelper(ctx context.Context, api *client.Client, id, imageID string, check func(context.Context) error, target container.InspectResponse) (string, error) {
	if err := executionAuthorized(ctx, true); err != nil {
		return "", err
	}
	// 单独编译的源码二进制仍可使用已预装的受控工具；正式安装包和 Mage 均携带工具目录。
	if _, err := os.Stat(s.helperDirectory); errors.Is(err, os.ErrNotExist) {
		return terminalwire.HelperPath, nil
	}
	select {
	case s.helperGate <- struct{}{}:
		defer func() { <-s.helperGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := check(ctx); err != nil {
		return "", err
	}
	image, err := api.ImageInspect(ctx, imageID)
	if err != nil {
		return "", err
	}
	if image.Os != "linux" {
		return "", errors.New("容器终端仅支持 Linux 镜像")
	}
	content, digest, err := readDockerHelper(s.helperDirectory, image.Architecture)
	if err != nil {
		return "", err
	}
	if matches, err := dockerHelperMatches(ctx, api, id, terminalwire.HelperPath, digest); err != nil || matches {
		return terminalwire.HelperPath, err
	}
	file := terminalwire.HelperPath + "-" + digest[:24]
	toolsVolume, err := dockerHelperVolume(ctx, api, target)
	if err != nil {
		return "", err
	}
	if toolsVolume != "" {
		file = path.Join(terminalassets.Directory, path.Base(file))
	}
	matches, err := dockerHelperMatches(ctx, api, id, file, digest)
	if err != nil || matches {
		return file, err
	}
	if toolsVolume == "" && target.HostConfig != nil && target.HostConfig.ReadonlyRootfs && !helperPathOnWritableMount(target, file) {
		return "", ErrDockerHelperReadOnly
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	parents := []string{"/usr", "/usr/local", "/usr/local/libexec"}
	archiveFile := strings.TrimPrefix(file, "/")
	if toolsVolume != "" {
		parents = []string{"/run", terminalassets.Directory}
		archiveFile = path.Base(file)
	}
	for _, parent := range parents {
		stat, err := api.ContainerStatPath(ctx, id, client.ContainerStatPathOptions{Path: parent})
		if err == nil {
			if !stat.Stat.Mode.IsDir() || stat.Stat.LinkTarget != "" {
				return "", errors.New("终端工具目录不能是符号链接或文件")
			}
			continue
		}
		if !errdefs.IsNotFound(err) {
			return "", err
		}
		if toolsVolume != "" {
			return "", errors.New("终端工具卷挂载目录不存在")
		}
		if err := writer.WriteHeader(&tar.Header{Name: strings.TrimPrefix(parent, "/"), Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
			return "", err
		}
	}
	if err := writer.WriteHeader(&tar.Header{Name: archiveFile, Typeflag: tar.TypeReg, Mode: 0555, Size: int64(len(content))}); err != nil {
		return "", err
	}
	if _, err := writer.Write(content); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	if err := check(ctx); err != nil {
		return "", err
	}
	event := DockerPreparation{Architecture: image.Architecture, Path: file, Digest: digest, Volume: toolsVolume}
	defer func() {
		if observer, ok := ctx.Value(dockerPreparationObserverKey{}).(func(DockerPreparation)); ok {
			observer(event)
		}
	}()
	copyID, destination := id, "/"
	var cleanup func() error
	if toolsVolume != "" {
		copyID, cleanup, err = prepareDockerHelperVolume(ctx, api, imageID, toolsVolume, check)
		if err != nil {
			return "", err
		}
		destination = terminalassets.Directory
		defer func() {
			if cleanup != nil {
				if err := cleanup(); err != nil {
					slog.ErrorContext(context.WithoutCancel(ctx), "清理终端工具准备容器失败", "operation", "terminal_helper_prepare_cleanup", "container_id", id, "err", err)
				}
			}
		}()
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := check(ctx); err != nil {
			return "", err
		}
		event.Attempts = attempt
		// 上传使用新连接，避免复用已被 SSH/转发端关闭的空闲字节流。
		_ = api.Close()
		_, err := api.CopyToContainer(ctx, copyID, client.CopyToContainerOptions{DestinationPath: destination, Content: bytes.NewReader(buffer.Bytes())})
		if err == nil {
			break
		}
		if errdefs.IsPermissionDenied(err) {
			return "", errors.Join(ErrDockerHelperWriteDenied, err)
		}
		if ctx.Err() != nil || !dockerHelperTransportInterrupted(err) || attempt == 2 {
			return "", err
		}
		// 仅重传同一摘要的工具；先重新核实授权、容器身份及可能已完成的写入。
		slog.WarnContext(ctx, "上传终端工具时连接中断，重新核验后最多重传一次", "operation", "terminal_helper_install_retry", "container_id", id, "attempt", attempt, "err", err)
		// 官方 Client.Close 只清理空闲连接，后续请求会建立新连接。
		_ = api.Close()
		if err := check(ctx); err != nil {
			return "", err
		}
		matches, err := dockerHelperMatches(ctx, api, id, file, digest)
		if err != nil {
			return "", err
		}
		if matches {
			break
		}
	}
	if err := check(ctx); err != nil {
		return "", err
	}
	matches, err = dockerHelperMatches(ctx, api, id, file, digest)
	if err != nil || !matches {
		return "", errors.Join(err, errors.New("安装后的终端工具校验失败"))
	}
	if cleanup != nil {
		remove := cleanup
		cleanup = nil
		if err := remove(); err != nil {
			return "", err
		}
		if err := check(ctx); err != nil {
			return "", err
		}
	}
	event.Succeeded = true
	slog.InfoContext(ctx, "已自动准备容器终端工具", "operation", "terminal_helper_install", "container_id", id, "architecture", image.Architecture, "digest", digest, "attempts", event.Attempts)
	return file, nil
}

func dockerHelperTransportInterrupted(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

func (s *Service) dockerPreparationError(ctx context.Context, id string, err error) error {
	slog.ErrorContext(ctx, "自动准备容器终端工具失败", "operation", "terminal_helper_install", "container_id", id, "err", err)
	if errors.Is(err, ErrExecAuthorization) || errors.Is(err, ErrTargetNotReady) {
		return err
	}
	if errors.Is(err, ErrDockerHelperReadOnly) {
		return ErrDockerHelperReadOnly
	}
	if errors.Is(err, ErrDockerHelperWriteDenied) {
		return ErrDockerHelperWriteDenied
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrDockerHelperTimeout
	}
	if dockerHelperTransportInterrupted(err) {
		return ErrDockerHelperConnection
	}
	return ErrDockerHelperUnavailable
}
