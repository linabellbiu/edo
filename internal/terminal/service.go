package terminal

import (
	"context"
	"errors"
	"io"
	"time"

	"zrt/internal/dockerengine"
	"zrt/internal/kube"
)

var (
	ErrInvalidRequest = errors.New("终端连接参数无效")
	ErrTargetNotReady = errors.New("目标容器当前不可进入终端")
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
	docker      *dockerengine.Service
	kube        *kube.Service
	maxDuration time.Duration
}

func NewService(docker *dockerengine.Service, kubeService *kube.Service, maxDuration time.Duration) *Service {
	return &Service{docker: docker, kube: kubeService, maxDuration: maxDuration}
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
