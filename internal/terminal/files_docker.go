package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"edo/internal/terminalfiles"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

func (s *Service) OpenDockerFiles(ctx context.Context, endpointID, containerID string) (terminalfiles.Backend, error) {
	if s == nil || s.docker == nil || strings.TrimSpace(endpointID) == "" || strings.TrimSpace(containerID) == "" {
		return nil, ErrInvalidRequest
	}
	revision, err := s.docker.TerminalRevision(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	api, err := s.docker.Client(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	inspect, err := api.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		api.Close()
		return nil, fmt.Errorf("读取 Docker 文件目标失败: %w", err)
	}
	fullID := inspect.Container.ID
	if !validFileDigest(fullID) || inspect.Container.State == nil || !inspect.Container.State.Running || inspect.Container.State.Paused || inspect.Container.State.Restarting {
		api.Close()
		return nil, ErrTargetNotReady
	}
	fileCtx, cancel := context.WithCancel(ctx)
	f := &containerFiles{ctx: fileCtx, cancel: cancel, close: api.Close, identity: "docker:" + fullID}
	f.check = func(op context.Context) error {
		current, err := s.docker.TerminalRevision(op, endpointID)
		if err != nil {
			return err
		}
		if current != revision {
			return terminalfiles.ErrConflict
		}
		status, err := api.ContainerInspect(op, fullID, client.ContainerInspectOptions{})
		if err != nil {
			return fmt.Errorf("核验 Docker 文件目标失败: %w", err)
		}
		if status.Container.ID != fullID || status.Container.State == nil || !status.Container.State.Running || status.Container.State.Paused || status.Container.State.Restarting {
			return ErrTargetNotReady
		}
		return nil
	}
	f.run = func(op context.Context, command []string, input io.Reader, output io.Writer) error {
		created, err := api.ExecCreate(op, fullID, client.ExecCreateOptions{Cmd: command, AttachStdin: input != nil, AttachStdout: true, AttachStderr: true, Privileged: false, TTY: false})
		if err != nil {
			return fmt.Errorf("创建 Docker 文件操作失败: %w", err)
		}
		attached, err := api.ExecAttach(op, created.ID, client.ExecAttachOptions{TTY: false})
		if err != nil {
			return fmt.Errorf("连接 Docker 文件操作失败: %w", err)
		}
		defer attached.Close()
		stop := context.AfterFunc(op, attached.Close)
		defer stop()
		inputDone := make(chan error, 1)
		if input != nil {
			go func() {
				_, err := io.Copy(attached.Conn, input)
				closeErr := attached.CloseWrite()
				inputDone <- errors.Join(err, closeErr)
			}()
		} else {
			inputDone <- nil
		}
		_, copyErr := stdcopy.StdCopy(output, io.Discard, attached.Reader)
		if copyErr != nil {
			attached.Close()
		}
		// 目标可能在 ready 前退出，关闭等待许可的输入管道，不能等待其
		// 永远读不到的控制帧后才释放上传协程。
		if pending, ok := input.(interface{ closePendingInput() }); ok {
			pending.closePendingInput()
		}
		inputErr := <-inputDone
		if op.Err() != nil {
			return op.Err()
		}
		if err := errors.Join(copyErr, inputErr); err != nil {
			return err
		}
		result, err := api.ExecInspect(op, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return fmt.Errorf("读取 Docker 文件操作结果失败: %w", err)
		}
		if result.Running || result.ContainerID != fullID {
			return terminalfiles.ErrUnavailable
		}
		if result.ExitCode != 0 {
			return fileExecError(fileExecExitError(result.ExitCode))
		}
		return nil
	}
	if err := f.check(ctx); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
