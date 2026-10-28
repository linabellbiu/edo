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

const installationLogLimit = 64 * 1024
const installationLogTruncated = "较早的安装日志已截断\n"

func appendInstallationLog(previous, line string) string {
	if len(line) > installationLogLimit-len(installationLogTruncated) {
		line = "一条安装日志过长，已省略\n"
	}
	if len(previous)+len(line) <= installationLogLimit {
		return previous + line
	}
	previous = strings.TrimPrefix(previous, installationLogTruncated)
	for len(installationLogTruncated)+len(previous)+len(line) > installationLogLimit {
		index := strings.IndexByte(previous, '\n')
		if index < 0 {
			previous = ""
			break
		}
		previous = previous[index+1:]
	}
	return installationLogTruncated + previous + line
}

// 只持久化登记的阶段和本进程存活心跳，不保存安装器输出或凭据。
func (s *Service) runHostOperation(ctx context.Context, record *model.K3sInstallation, hosts []Host, index int, stage string, operation func(context.Context, io.Writer) error) error {
	if record == nil || index < 0 || index >= len(hosts) || installationStages[stage] == "" {
		return installationError("record", ErrInvalidInput)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	diagnostics := &installationDiagnostics{onStage: func(stage string, started, updated time.Time, heartbeat bool) (err error) {
		defer func() {
			if err != nil {
				cancel()
			}
		}()
		name := installationStages[stage]
		message := name
		if heartbeat {
			message = fmt.Sprintf("%s仍在执行，已持续 %d 秒", name, int(updated.Sub(started).Seconds()))
		}
		line := updated.In(time.Local).Format("15:04:05") + " " + hosts[index].Name + "：" + message + "\n"
		logs := appendInstallationLog(record.Logs, line)
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
		return nil
	}}
	if err := diagnostics.start(stage); err != nil {
		return err
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(30 * time.Second)
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
