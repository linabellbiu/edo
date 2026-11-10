package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"edo/internal/configfile"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

var ErrComposeDirectoryUnavailable = errors.New("无法确定应用部署位置，请检查目标主机连接和用户主目录")

// ResolveComposeWorkingDirectory 只读取得执行用户主目录，新方案不要求填写部署目录。
func (s *Service) ResolveComposeWorkingDirectory(ctx context.Context, target model.DeploymentTarget, applicationName string) (string, error) {
	if target.WorkingDirectory != "" {
		return target.WorkingDirectory, nil
	}
	configurationPath, err := configfile.ApplicationDirectory(applicationName)
	if err != nil || s.ssh == nil || target.Platform != model.DeploymentSSH || target.HostID == "" {
		s.logger.ErrorContext(ctx, "自动部署目录配置无效", "operation", "compose_directory_resolve", "target_id", target.ID, "err", errors.Join(err, ErrInvalidTarget))
		return "", ErrComposeDirectoryUnavailable
	}
	output := &composeHomeOutput{}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.ssh.RunHostDeploymentScript(probeCtx, sshdeploy.Input{HostID: target.HostID, EnvironmentID: target.EnvironmentID,
		Script: `printf '%s' "${HOME:?}"`, Timeout: 30 * time.Second, Stdout: output, Stderr: io.Discard})
	home, exceeded := output.snapshot()
	directory := path.Join(home, strings.TrimPrefix(configurationPath, "~/"))
	if err != nil || !result.Started || result.ExitCode != 0 || exceeded || !validWorkingDirectory(home) || !path.IsAbs(home) || path.Clean(home) != home || strings.ContainsFunc(home, unicode.IsControl) || !validWorkingDirectory(directory) {
		s.logger.ErrorContext(ctx, "读取部署主机用户目录失败", "operation", "compose_directory_resolve", "target_id", target.ID, "host_id", target.HostID, "err", errors.Join(err, ErrComposeDirectoryUnavailable))
		return "", ErrComposeDirectoryUnavailable
	}
	return directory, nil
}

// 探测只需要一个路径；超出上限的主机输出丢弃，不进入日志或内存。
type composeHomeOutput struct {
	mu       sync.Mutex
	content  strings.Builder
	exceeded bool
}

func (b *composeHomeOutput) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(value)
	if n > 1024-b.content.Len() {
		b.exceeded = true
		value = value[:max(0, 1024-b.content.Len())]
	}
	_, _ = b.content.Write(value)
	return n, nil
}

func (b *composeHomeOutput) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.content.String(), b.exceeded
}

// 已固定的自动目录属于该次运行；只在原快照明确登记时恢复，不能把清空旧目录当成同一位置。
func restoreManagedComposeDirectory(source *model.DeploymentRecord, run model.PipelineRun, target *model.DeploymentTarget) {
	if source.DeploymentPlanKind != model.DeploymentPlanComposeStack || target.WorkingDirectory != "" {
		return
	}
	var snapshot struct {
		Targets map[string]struct {
			ID               string `json:"id"`
			WorkingDirectory string `json:"working_directory"`
			Managed          bool   `json:"managed_working_directory"`
		} `json:"deployment_targets"`
	}
	if json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot) != nil {
		return
	}
	original := snapshot.Targets[source.WorkflowNodeID]
	if original.Managed && original.ID == source.TargetID && original.ID == target.ID && original.WorkingDirectory == source.WorkingDirectory {
		target.WorkingDirectory = original.WorkingDirectory
	}
}
