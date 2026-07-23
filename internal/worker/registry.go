package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"zrt/internal/task"
)

type Handler func(context.Context, task.Message) error

type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

func (r *Registry) Register(kind string, handler Handler) error {
	if kind == "" || handler == nil {
		return errors.New("任务处理器配置无效")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[kind]; exists {
		return fmt.Errorf("任务处理器 %s 已注册", kind)
	}
	r.handlers[kind] = handler
	return nil
}

func (r *Registry) Handler(kind string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[kind]
	return handler, ok
}
