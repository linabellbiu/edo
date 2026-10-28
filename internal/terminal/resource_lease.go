package terminal

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// resourceSession 固定运行时及容器身份，不把原连接复用到同名新资源。
type resourceSession struct {
	Session
	ctx    context.Context
	cancel context.CancelFunc
	check  func(context.Context) error
	once   sync.Once
}

func guardResourceSession(ctx context.Context, session Session, check func(context.Context) error, interval time.Duration) Session {
	lease, cancel := context.WithCancel(ctx)
	guard := &resourceSession{Session: session, ctx: lease, cancel: cancel, check: check}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-lease.Done():
				_ = guard.Close()
				return
			case <-ticker.C:
				probe, stop := context.WithTimeout(lease, 3*time.Second)
				err := check(probe)
				stop()
				if err != nil {
					slog.WarnContext(lease, "终端目标状态核验失败，关闭原会话", "operation", "terminal_resource_revalidate", "err", err)
					_ = guard.Close()
					return
				}
			}
		}
	}()
	return guard
}

func (s *resourceSession) Write(data []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	return s.Session.Write(data)
}
func (s *resourceSession) Resize(ctx context.Context, size Size) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if err := s.check(ctx); err != nil {
		slog.WarnContext(ctx, "调整终端尺寸前目标核验失败", "operation", "terminal_resource_resize", "err", err)
		_ = s.Close()
		return err
	}
	return s.Session.Resize(ctx, size)
}
func (s *resourceSession) Close() error {
	var err error
	s.once.Do(func() { s.cancel(); err = s.Session.Close() })
	return err
}
