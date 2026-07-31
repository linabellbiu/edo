package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"gorm.io/gorm"

	"edo/internal/model"
	"edo/internal/task"
)

type Handler func(context.Context, task.Message) error
type TerminalFailureHook func(context.Context, *gorm.DB, model.Job, string, string) error

type Registry struct {
	mu                   sync.RWMutex
	handlers             map[string]Handler
	terminalFailureHooks map[string]TerminalFailureHook
}

func NewRegistry() *Registry {
	return &Registry{
		handlers:             make(map[string]Handler),
		terminalFailureHooks: make(map[string]TerminalFailureHook),
	}
}

func (r *Registry) RegisterTerminalFailureHook(kind string, hook TerminalFailureHook) error {
	if kind == "" || hook == nil {
		return errors.New("任务终止失败处理器配置无效")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.terminalFailureHooks[kind]; exists {
		return fmt.Errorf("任务终止失败处理器 %s 已注册", kind)
	}
	r.terminalFailureHooks[kind] = hook
	return nil
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

func (r *Registry) TerminalFailureHook(kind string) (TerminalFailureHook, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	hook, ok := r.terminalFailureHooks[kind]
	return hook, ok
}
