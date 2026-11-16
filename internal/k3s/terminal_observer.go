package k3s

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"edo/internal/model"
	"edo/internal/sshclient"
)

// 每个终端流先组成完整行，避免并行 stdout/stderr 或节点日志拼接破坏脱敏边界。
type terminalLineWriter struct {
	mu        sync.Mutex
	output    io.Writer
	prefix    string
	line      []byte
	discarded bool
	redactor  *installationLogRedactor
}

func (w *terminalLineWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range value {
		if b == '\n' || b == '\r' {
			if err := w.flushLocked(); err != nil {
				return 0, err
			}
		} else if len(w.line) < 32*1024 && !w.discarded {
			w.line = append(w.line, b)
		} else {
			w.discarded = true
		}
	}
	return len(value), nil
}
func (w *terminalLineWriter) flushLocked() error {
	line := string(w.line)
	if w.discarded {
		line = "一条终端日志过长，已省略"
	}
	w.line, w.discarded = w.line[:0], false
	if line == "" {
		return nil
	}
	if w.redactor != nil {
		line = w.redactor.line(line)
		if line == "" {
			return nil
		}
	}
	_, err := io.WriteString(w.output, w.prefix+line+"\n")
	return err
}
func (w *terminalLineWriter) flush() error { w.mu.Lock(); defer w.mu.Unlock(); return w.flushLocked() }

type terminalObservation struct{ name, script string }

// 观察器只读取官方服务或本次任务容器的日志，取消 SSH 只终止观察，不管理安装生命周期。
func (s *Service) withTerminalObservations(ctx context.Context, host model.Host, bundle sshclient.Bundle, observations []terminalObservation, output io.Writer, operation func(context.Context) error) error {
	observeCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	for _, observation := range observations {
		workers.Add(1)
		go func(source terminalObservation) {
			defer workers.Done()
			writer := &terminalLineWriter{output: output, prefix: "[" + source.name + "] ", redactor: newInstallationLogRedactor(installationLogSecrets(Input{}, bundle))}
			err := s.transport.Run(observeCtx, host, bundle, source.script, writer)
			_ = writer.flush()
			if err != nil && observeCtx.Err() == nil {
				slog.WarnContext(ctx, "读取安装终端日志失败", "operation", "k3s_terminal_observe", "host_id", host.ID, "source", source.name, "err", err)
				_, _ = fmt.Fprintf(output, "[%s] 日志读取中断，安装结果以实际执行和就绪校验为准。\n", source.name)
			}
		}(observation)
	}
	err := operation(ctx)
	cancel()
	workers.Wait()
	return err
}

func nativeTerminalObservation(role string, input Input) terminalObservation {
	service := installedServiceName(role, input.Installer)
	script := "set -eu\n# EDO_TERMINAL_OBSERVER\nif [ -d /run/systemd/system ] && command -v journalctl >/dev/null 2>&1; then\nexec journalctl --unit=" + shellQuote(service+".service") + " --no-pager --follow --lines=0 --output=short-iso\nelse\nprintf '当前服务管理器未提供 journalctl 日志流，继续展示官方安装器输出。\\n'\nfi\n"
	return terminalObservation{name: service, script: script}
}

func k3dTerminalObservations(input Input, id string, follow bool) []terminalObservation {
	cluster := k3dResourceName(id)
	if cluster == "" || input.K3d == nil {
		return nil
	}
	result := []terminalObservation{}
	for index := -1; index < k3dAgents(input.K3d); index++ {
		role, ordinal := "agent", index
		if index < 0 {
			role, ordinal = "server", 0
		}
		name := fmt.Sprintf("k3d-%s-%s-%d", cluster, role, ordinal)
		script := "set -eu\n# EDO_TERMINAL_OBSERVER\n" + k3dLocalEnvironment
		script += "edo_container=$(edo_docker inspect --format '{{.Id}} {{index .Config.Labels \"edo.dev/installation\"}} {{index .Config.Labels \"k3d.cluster\"}}' " + shellQuote(name) + ") || exit 47\n[ \"${edo_container#* }\" = " + shellQuote(id+" "+cluster) + " ] || exit 47\nedo_container_id=${edo_container%% *}\n[ \"${#edo_container_id}\" = 64 ] || exit 47\ncase \"$edo_container_id\" in *[!a-f0-9]*) exit 47;; esac\n"
		flags := " --timestamps --tail 200"
		if follow {
			flags += " --follow"
		}
		// 使用 exec，SSH 取消不在远端遗留后台管道或采集进程。
		script += "exec env -i PATH=\"$edo_path\" HOME=/var/lib/edo/k3d DOCKER_HOST=unix:///var/run/docker.sock \"$edo_docker_binary\" --host=unix:///var/run/docker.sock logs" + flags + " \"$edo_container_id\"\n"
		result = append(result, terminalObservation{name: name, script: script})
	}
	return result
}

func (s *Service) readTerminalSnapshots(ctx context.Context, host model.Host, bundle sshclient.Bundle, sources []terminalObservation, output io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for _, source := range sources {
		workers.Add(1)
		go func(source terminalObservation) {
			defer workers.Done()
			writer := &terminalLineWriter{output: output, prefix: "[" + source.name + "] ", redactor: newInstallationLogRedactor(installationLogSecrets(Input{}, bundle))}
			err := s.transport.Run(ctx, host, bundle, source.script, writer)
			_ = writer.flush()
			if err != nil {
				slog.WarnContext(ctx, "读取失败安装的节点日志失败", "operation", "k3d_terminal_snapshot", "host_id", host.ID, "source", source.name, "err", err)
			}
		}(source)
	}
	workers.Wait()
}

// 后续阶段按容器名称、真实时间戳和内容去重，不改写终端文本。

type terminalDeduplicator struct {
	mu     sync.Mutex
	output io.Writer
	seen   map[string]bool
	order  []string
	bytes  int
}

func (w *terminalDeduplicator) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := string(value)
	if strings.Contains(line, "[k3d-") {
		if w.seen[line] {
			return len(value), nil
		}
		if w.seen == nil {
			w.seen = map[string]bool{}
		}
		w.seen[line] = true
		w.order = append(w.order, line)
		w.bytes += len(line)
		for len(w.order) > 4096 || w.bytes > installationLogLimit {
			w.bytes -= len(w.order[0])
			delete(w.seen, w.order[0])
			w.order = w.order[1:]
		}
	}
	_, err := w.output.Write(value)
	return len(value), err
}
