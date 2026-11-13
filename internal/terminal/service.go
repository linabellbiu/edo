package terminal

import (
	"context"
	"errors"
	"io"
	"time"

	"edo/internal/dockerengine"
	"edo/internal/kube"
)

var (
	ErrInvalidRequest            = errors.New("终端连接参数无效")
	ErrTargetNotReady            = errors.New("目标容器当前不可进入终端")
	ErrDockerTerminalUnavailable = errors.New("目标容器需要启用 init 并使用独立 PID 命名空间，终端工具由 EDO 自动准备")
	ErrDockerHelperUnavailable   = errors.New("自动准备终端工具失败，请查看系统日志中的具体原因")
	ErrDockerHelperConnection    = errors.New("准备终端工具时 Docker 连接中断，请重新连接；详情请查看系统日志")
	ErrDockerHelperTimeout       = errors.New("准备终端工具超时，请检查目标连接后重试")
	ErrDockerHelperWriteDenied   = errors.New("目标 Docker 拒绝写入终端工具，请检查容器只读设置及写入权限")
	ErrExecAuthorization         = errors.New("终端 Exec 授权已失效")
	ErrDockerTerminalRelease     = errors.New("终端会话回收未确认，请联系管理员")
)

type Size struct {
	Columns uint16
	Rows    uint16
}

type Session interface {
	io.Reader
	io.Writer
	Resize(context.Context, Size) error
	Close() error
}

type Service struct {
	docker          *dockerengine.Service
	kube            *kube.Service
	maxDuration     time.Duration
	helperDirectory string
	helperGate      chan struct{}
}

type Option func(*Service)

func WithDockerHelperDirectory(directory string) Option {
	return func(s *Service) { s.helperDirectory = directory }
}

func NewService(docker *dockerengine.Service, kubeService *kube.Service, maxDuration time.Duration, options ...Option) *Service {
	s := &Service{docker: docker, kube: kubeService, maxDuration: maxDuration, helperDirectory: defaultDockerHelperDirectory(), helperGate: make(chan struct{}, 1)}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *Service) MaxDuration() time.Duration { return s.maxDuration }

func normalizeShell(value string) ([]string, error) {
	switch value {
	case "", "sh":
		return []string{"/bin/sh"}, nil
	case "bash":
		return []string{"/bin/bash"}, nil
	case "ash":
		return []string{"/bin/ash"}, nil
	default:
		return nil, ErrInvalidRequest
	}
}

func validSize(size Size) bool {
	return size.Columns >= 20 && size.Columns <= 500 && size.Rows >= 5 && size.Rows <= 200
}
