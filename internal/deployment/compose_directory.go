package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"edo/internal/configfile"
	"edo/internal/diagnosticerror"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

var ErrComposeDirectoryUnavailable = errors.New("无法确定应用部署位置，请检查目标主机连接和用户主目录")

// 目录读取保留具体故障类型，对外仅返回稳定提示。
type composeDirectoryFailure struct{ message string }

func (e *composeDirectoryFailure) Error() string { return e.message }
func (e *composeDirectoryFailure) Unwrap() error { return ErrComposeDirectoryUnavailable }

var (
	ErrComposeDirectoryTimeout         = &composeDirectoryFailure{"读取部署主机用户主目录超时，请检查该主机的连接是否可达"}
	ErrComposeDirectoryHostUnavailable = &composeDirectoryFailure{"部署主机不可用，无法读取部署目录，请检查主机状态和连接"}
	ErrComposeDirectoryCanceled        = &composeDirectoryFailure{"部署目录读取已取消"}
	ErrComposeHomeInvalid              = &composeDirectoryFailure{"部署主机返回的用户主目录无效，请检查该账号的主目录配置"}
)

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
	output := &composeProbeOutput{limit: 1024}
	stderr := &composeProbeOutput{limit: diagnosticerror.MaximumBytes}
	var redactions []string
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.ssh.RunHostDeploymentScript(probeCtx, sshdeploy.Input{HostID: target.HostID, EnvironmentID: target.EnvironmentID,
		Script: `printf '%s' "${HOME:?}"`, Timeout: 30 * time.Second, Stdout: output, Stderr: stderr,
		OnDiagnosticRedactions: func(values ...string) { redactions = append(redactions, values...) }})
	home, exceeded := output.snapshot()
	directory := path.Join(home, strings.TrimPrefix(configurationPath, "~/"))
	if err != nil || !result.Started || result.ExitCode != 0 || exceeded || !validWorkingDirectory(home) || !path.IsAbs(home) || path.Clean(home) != home || strings.ContainsFunc(home, unicode.IsControl) || !validWorkingDirectory(directory) {
		var networkError net.Error
		var summary error
		switch {
		case errors.Is(probeCtx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded), errors.As(err, &networkError) && networkError.Timeout():
			summary = ErrComposeDirectoryTimeout
		case errors.Is(probeCtx.Err(), context.Canceled), errors.Is(err, context.Canceled):
			summary = ErrComposeDirectoryCanceled
		case errors.Is(err, sshdeploy.ErrHostUnavailable):
			summary = ErrComposeDirectoryHostUnavailable
		case err == nil && result.Started && result.ExitCode == 0:
			summary = ErrComposeHomeInvalid
		default:
			summary = ErrComposeDirectoryUnavailable
		}
		detail := fmt.Sprintf("主目录探测：Started=%t ExitCode=%d", result.Started, result.ExitCode)
		if err != nil {
			detail += "\n" + err.Error()
		}
		stderrText, stderrExceeded := stderr.snapshot()
		if stderrExceeded {
			// 丢弃截断的末行，避免只留下密码或私钥片段而无法完整匹配脱敏。
			stderrText = stderrText[:strings.LastIndexByte(stderrText, '\n')+1]
		}
		if stderrText != "" {
			detail += "\nstderr:\n" + stderrText
		}
		if stderrExceeded {
			detail += "\nstderr 超出采集上限，末尾已截断"
		}
		// HOME 仅用于路径校验，即使错误输出重复该值也不能带入诊断。
		failure := diagnosticerror.New(summary, detail, append(redactions, home)...)
		var diagnostic interface{ Diagnostic() string }
		var safeDetail string
		if errors.As(failure, &diagnostic) {
			safeDetail = diagnostic.Diagnostic()
		}
		s.logger.ErrorContext(ctx, "读取部署主机用户目录失败", "operation", "compose_directory_resolve", "target_id", target.ID, "host_id", target.HostID, "started", result.Started, "exit_code", result.ExitCode, "err", failure, "diagnostic", safeDetail)
		return "", failure
	}
	return directory, nil
}

// 标准输出与错误输出分别限量采集；超出上限的字节丢弃，不进入内存。
type composeProbeOutput struct {
	mu       sync.Mutex
	content  strings.Builder
	exceeded bool
	limit    int
}

func (b *composeProbeOutput) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(value)
	if n > b.limit-b.content.Len() {
		b.exceeded = true
		value = value[:max(0, b.limit-b.content.Len())]
	}
	_, _ = b.content.Write(value)
	return n, nil
}

func (b *composeProbeOutput) snapshot() (string, bool) {
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
