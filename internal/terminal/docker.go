package terminal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/moby/moby/client"
)

type dockerSession struct {
	client    *client.Client
	attach    client.ExecAttachResult
	execID    string
	closeOnce sync.Once
	closeErr  error
}

func (s *Service) OpenDocker(
	ctx context.Context,
	endpointID, containerID, shell string,
	size Size,
) (Session, error) {
	if strings.TrimSpace(endpointID) == "" || strings.TrimSpace(containerID) == "" || !validSize(size) {
		return nil, ErrInvalidRequest
	}
	command, err := normalizeShell(shell)
	if err != nil {
		return nil, err
	}
	apiClient, err := s.docker.Client(ctx, endpointID)
	if err != nil {
		return nil, fmt.Errorf("创建 Docker 终端客户端失败: %w", err)
	}
	inspect, err := apiClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		apiClient.Close()
		return nil, fmt.Errorf("读取 Docker 容器状态失败: %w", err)
	}
	if inspect.Container.State == nil || !inspect.Container.State.Running || inspect.Container.State.Paused {
		apiClient.Close()
		return nil, ErrTargetNotReady
	}
	consoleSize := client.ConsoleSize{Height: uint(size.Rows), Width: uint(size.Columns)}
	created, err := apiClient.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		TTY: true, ConsoleSize: consoleSize, AttachStdin: true, AttachStderr: true,
		AttachStdout: true, Privileged: false, Cmd: command,
	})
	if err != nil {
		apiClient.Close()
		return nil, fmt.Errorf("创建 Docker 容器终端失败: %w", err)
	}
	attached, err := apiClient.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true, ConsoleSize: consoleSize})
	if err != nil {
		apiClient.Close()
		return nil, fmt.Errorf("连接 Docker 容器终端失败: %w", err)
	}
	return &dockerSession{client: apiClient, attach: attached, execID: created.ID}, nil
}

func (s *dockerSession) Read(buffer []byte) (int, error) {
	return s.attach.Reader.Read(buffer)
}

func (s *dockerSession) Write(buffer []byte) (int, error) {
	return s.attach.Conn.Write(buffer)
}

func (s *dockerSession) Resize(ctx context.Context, size Size) error {
	if !validSize(size) {
		return ErrInvalidRequest
	}
	_, err := s.client.ExecResize(ctx, s.execID, client.ExecResizeOptions{
		Height: uint(size.Rows), Width: uint(size.Columns),
	})
	if err != nil {
		return fmt.Errorf("调整 Docker 终端尺寸失败: %w", err)
	}
	return nil
}

func (s *dockerSession) Close() error {
	s.closeOnce.Do(func() {
		s.attach.Close()
		if err := s.client.Close(); err != nil && !errors.Is(err, context.Canceled) {
			s.closeErr = fmt.Errorf("关闭 Docker 终端客户端失败: %w", err)
		}
	})
	return s.closeErr
}
