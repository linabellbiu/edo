package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"gorm.io/datatypes"

	"edo/internal/model"
)

const installationLogLimit = 2 * 1024 * 1024
const installationLogTruncated = "较早的安装日志已截断\n"

func appendInstallationLog(previous, line string) string {
	return appendBoundedInstallationLog(previous, line, installationLogLimit)
}

func appendBoundedInstallationLog(previous, line string, limit int) string {
	if len(line) > limit-len(installationLogTruncated) {
		line = "一条安装日志过长，已省略\n"
	}
	if len(previous)+len(line) <= limit {
		return previous + line
	}
	previous = strings.TrimPrefix(previous, installationLogTruncated)
	for len(installationLogTruncated)+len(previous)+len(line) > limit {
		index := strings.IndexByte(previous, '\n')
		if index < 0 {
			previous = ""
			break
		}
		previous = previous[index+1:]
	}
	return installationLogTruncated + previous + line
}

// 阶段状态与终端日志分开保存；同一把诊断锁内批量写入，避免逐行更新数据库。
func (s *Service) runHostOperation(ctx context.Context, record *model.K3sInstallation, hosts []Host, index int, stage string, operation func(context.Context, io.Writer) error, secrets ...string) error {
	if record == nil || index < 0 || index >= len(hosts) || installationStages[stage] == "" {
		return installationError("record", ErrInvalidInput)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	redactor := newInstallationLogRedactor(secrets)
	lastFlush := time.Time{}
	var pending strings.Builder
	mergeLogs := func() {
		if pending.Len() > 0 {
			record.Logs = appendInstallationLog(record.Logs, pending.String())
			pending.Reset()
		}
	}
	dirty := false
	flush := func() error {
		if !dirty {
			return nil
		}
		mergeLogs()
		if err := s.persistProgress(record, map[string]any{"logs": record.Logs}); err != nil {
			cancel()
			return err
		}
		dirty, lastFlush = false, time.Now()
		return nil
	}
	diagnostics := &installationDiagnostics{onLine: func(line string) error {
		if line = redactor.line(line); line == "" {
			return nil
		}
		pending.WriteString("[" + hosts[index].Name + "] " + line + "\n")
		if pending.Len() >= 32*1024 {
			mergeLogs()
		}
		dirty = true
		if time.Since(lastFlush) >= 500*time.Millisecond {
			return flush()
		}
		return nil
	}, onFlush: flush, onStage: func(stage string, started, updated time.Time, heartbeat bool) (err error) {
		defer func() {
			if err != nil {
				cancel()
			}
		}()
		name := installationStages[stage]
		message := name
		if heartbeat {
			message = fmt.Sprintf("%s仍在执行，已持续 %d 秒", name, int(updated.Sub(started).Seconds()))
			if record.Action != "uninstall" {
				message = fmt.Sprintf("已持续 %d 秒", int(updated.Sub(started).Seconds()))
			}
		}
		line := updated.In(time.Local).Format("15:04:05") + " " + hosts[index].Name + "：" + message + "\n"
		mergeLogs()
		logs := record.Logs
		if record.Action == "uninstall" {
			logs = appendInstallationLog(logs, line)
		}
		host := hosts[index]
		host.Status = "installing"
		host.Stage, host.StageStartedAt, host.ProgressUpdatedAt, host.Message = stage, &started, &updated, message
		current := append([]Host(nil), hosts...)
		current[index] = host
		encoded, err := json.Marshal(current)
		if err != nil {
			return installationError("record", err)
		}
		writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := s.persistProgressContext(writeCtx, record, map[string]any{"hosts": datatypes.JSON(encoded), "logs": logs}); err != nil {
			return err
		}
		hosts[index], record.Hosts, record.Logs = host, datatypes.JSON(encoded), logs
		dirty, lastFlush = false, time.Now()
		return nil
	}}
	if record.Action == "uninstall" {
		diagnostics.onLine, diagnostics.onFlush = nil, nil
	}
	if err := diagnostics.start(stage); err != nil {
		return err
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if diagnostics.heartbeat(now) != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := operation(ctx, diagnostics)
	close(done)
	<-stopped
	return diagnostics.failure(err)
}
