package dockerengine

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/moby/moby/client"
)

var ErrInvalidContainerLogs = errors.New("Docker 容器日志参数无效")

type ContainerLogOptions struct {
	Tail       int
	Follow     bool
	Timestamps bool
}

type ContainerLogStream struct {
	io.ReadCloser
	TTY       bool
	apiClient *client.Client
}

func (stream *ContainerLogStream) Close() error {
	var closeErr error
	if stream.ReadCloser != nil {
		closeErr = stream.ReadCloser.Close()
	}
	if stream.apiClient != nil {
		if err := stream.apiClient.Close(); closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (s *Service) ContainerLogs(
	ctx context.Context,
	endpointID string,
	containerID string,
	options ContainerLogOptions,
) (*ContainerLogStream, error) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" || len(containerID) > 256 || strings.ContainsAny(containerID, "\r\n\x00") ||
		options.Tail < 1 || options.Tail > 5000 {
		return nil, ErrInvalidContainerLogs
	}
	apiClient, err := s.executionClient(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	inspect, err := apiClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		_ = apiClient.Close()
		return nil, err
	}
	logs, err := apiClient.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(options.Tail),
		Follow:     options.Follow,
		Timestamps: options.Timestamps,
	})
	if err != nil {
		_ = apiClient.Close()
		return nil, err
	}
	tty := inspect.Container.Config != nil && inspect.Container.Config.Tty
	return &ContainerLogStream{ReadCloser: logs, TTY: tty, apiClient: apiClient}, nil
}
