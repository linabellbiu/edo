package terminal

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"edo/internal/terminalwire"
)

func (s *Service) OpenDocker(ctx context.Context, endpointID, containerID, shell string, size Size) (Session, error) {
	if s == nil || s.docker == nil || strings.TrimSpace(endpointID) == "" || len(containerID) != 64 || !validSize(size) || s.maxDuration < time.Second || s.maxDuration > 24*time.Hour {
		return nil, ErrInvalidRequest
	}
	command, err := normalizeShell(shell)
	if err != nil {
		return nil, err
	}
	shell = strings.TrimPrefix(command[0], "/bin/")
	revision, err := s.docker.TerminalRevision(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	api, err := s.docker.Client(ctx, endpointID)
	if err != nil {
		return nil, fmt.Errorf("创建 Docker 终端客户端失败: %w", err)
	}
	initial, err := api.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		api.Close()
		return nil, fmt.Errorf("读取 Docker 容器状态失败: %w", err)
	}
	state := initial.Container.State
	if initial.Container.ID != containerID || state == nil || !state.Running || state.Paused || state.Restarting {
		api.Close()
		return nil, ErrTargetNotReady
	}
	host := initial.Container.HostConfig
	if host == nil || host.Init == nil || !*host.Init || !host.PidMode.IsPrivate() {
		slog.WarnContext(ctx, "Docker 目标不满足受控终端运行条件", "operation", "terminal_docker_preflight", "endpoint_id", endpointID, "container_id", containerID)
		api.Close()
		return nil, ErrDockerTerminalUnavailable
	}
	// 外层仅传协议，真正的 PTY、尺寸和 Shell 由预装工具维护；不写入 Env 或提权。
	created, err := api.ExecCreate(ctx, containerID, client.ExecCreateOptions{TTY: false, AttachStdin: true, AttachStderr: true, AttachStdout: true, Privileged: false, Cmd: []string{terminalwire.HelperPath, "controller"}})
	if err != nil {
		api.Close()
		return nil, fmt.Errorf("创建 Docker 受控终端失败: %w", err)
	}
	if _, err := hex.DecodeString(created.ID); err != nil || len(created.ID) != 64 {
		slog.WarnContext(ctx, "Docker 返回的终端执行身份无效", "operation", "terminal_docker_exec_identity", "container_id", containerID)
		api.Close()
		return nil, ErrTargetNotReady
	}
	attached, err := api.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: false})
	if err != nil {
		slog.WarnContext(ctx, "Docker 受控终端工具无法启动", "operation", "terminal_docker_attach", "container_id", containerID, "exec_id", created.ID, "err", err)
		api.Close()
		return nil, ErrDockerTerminalUnavailable
	}
	session := newDockerSession(ctx, attached.Conn, attached.Reader, attached.Close, api.Close, func(probe context.Context) (client.ExecInspectResult, error) {
		return api.ExecInspect(probe, created.ID, client.ExecInspectOptions{})
	}, created.ID, containerID)
	if err := session.start(ctx, terminalwire.Message{Type: "start", Shell: shell, Rows: size.Rows, Cols: size.Columns, MaxMS: s.maxDuration.Milliseconds(), LeaseMS: terminalwire.LeaseMS}); err != nil {
		slog.WarnContext(ctx, "Docker 受控终端工具未就绪", "operation", "terminal_docker_handshake", "endpoint_id", endpointID, "container_id", containerID, "exec_id", created.ID, "err", err)
		if closeErr := session.Close(); closeErr != nil {
			slog.WarnContext(context.WithoutCancel(ctx), "打开失败后的受控终端回收未确认", "operation", "terminal_docker_open_cleanup", "exec_id", created.ID, "err", closeErr)
		}
		return nil, ErrDockerTerminalUnavailable
	}
	opened, err := api.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil || opened.ID != created.ID || opened.ContainerID != containerID || !opened.Running {
		slog.WarnContext(ctx, "打开后的 Docker 终端执行身份不匹配", "operation", "terminal_docker_exec_open_identity", "container_id", containerID, "exec_id", created.ID, "err", err)
		_ = session.Close()
		return nil, ErrTargetNotReady
	}
	check := func(probe context.Context) error {
		current, err := s.docker.TerminalRevision(probe, endpointID)
		if err != nil {
			return err
		}
		if current != revision {
			return ErrTargetNotReady
		}
		now, err := api.ContainerInspect(probe, containerID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		x := now.Container.State
		if now.Container.ID != containerID || x == nil || !x.Running || x.Paused || x.Restarting || x.Pid != state.Pid || x.StartedAt != state.StartedAt || now.Container.RestartCount != initial.Container.RestartCount {
			return ErrTargetNotReady
		}
		return nil
	}
	if err := check(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	return guardResourceSession(ctx, session, check, 2*time.Second), nil
}
