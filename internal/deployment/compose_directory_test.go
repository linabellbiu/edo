package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"edo/internal/model"
	"edo/internal/sshdeploy"
)

type composeHomeProbeStub struct {
	home  string
	err   error
	calls int
	input sshdeploy.Input
}

func (r *composeHomeProbeStub) RunHostDeploymentScript(_ context.Context, input sshdeploy.Input) (sshdeploy.Result, error) {
	r.calls++
	r.input = input
	_, _ = io.WriteString(input.Stdout, r.home)
	return sshdeploy.Result{Started: true, ExitCode: 0}, r.err
}

func TestComposeDirectoryUsesTargetHomeWithoutWritingFiles(t *testing.T) {
	runner := &composeHomeProbeStub{home: "/home/deploy user"}
	s := NewService(nil, nil, nil, runner, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	target := model.DeploymentTarget{ID: "target", Platform: model.DeploymentSSH, HostID: "host", EnvironmentID: "env"}
	directory, err := s.ResolveComposeWorkingDirectory(context.Background(), target, "orders_app")
	if err != nil || directory != "/home/deploy user/edo/orders_app/config" {
		t.Fatalf("自动目录错误：%q %v", directory, err)
	}
	if runner.calls != 1 || runner.input.HostID != "host" || runner.input.EnvironmentID != "env" || runner.input.Script != `printf '%s' "${HOME:?}"` || len(runner.input.Files) != 0 || len(runner.input.Environment) != 0 {
		t.Fatal("目录检测应只在准确主机读取 HOME，不得创建目录或注入用户文本")
	}
	target.WorkingDirectory = "/srv/existing"
	directory, err = s.ResolveComposeWorkingDirectory(context.Background(), target, "orders_app")
	if err != nil || directory != target.WorkingDirectory || runner.calls != 1 {
		t.Fatal("已有目录不应改变或额外探测")
	}
}

func TestComposeDirectoryRejectsInvalidOrFailedProbe(t *testing.T) {
	for _, home := range []string{"", "relative", "/home/user\n", "/home/../user", "/" + strings.Repeat("a", 1025)} {
		runner := &composeHomeProbeStub{home: home}
		s := NewService(nil, nil, nil, runner, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, err := s.ResolveComposeWorkingDirectory(context.Background(), model.DeploymentTarget{Platform: model.DeploymentSSH, HostID: "host", EnvironmentID: "env"}, "orders_app")
		if !errors.Is(err, ErrComposeDirectoryUnavailable) {
			t.Fatalf("无效目录未拒绝：%q %v", home, err)
		}
	}
	runner := &composeHomeProbeStub{home: "/home/user", err: errors.New("private connection details")}
	s := NewService(nil, nil, nil, runner, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := s.ResolveComposeWorkingDirectory(context.Background(), model.DeploymentTarget{Platform: model.DeploymentSSH, HostID: "host", EnvironmentID: "env"}, "orders_app")
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("原始连接信息不能对外返回")
	}
}

func TestManagedComposeDirectoryRestoreRequiresOriginalSnapshotMarker(t *testing.T) {
	source := &model.DeploymentRecord{TargetID: "target", WorkflowNodeID: "deploy", DeploymentPlanKind: model.DeploymentPlanComposeStack, WorkingDirectory: "/home/user/edo/app/config"}
	for _, managed := range []bool{false, true} {
		data, _ := json.Marshal(map[string]any{"deployment_targets": map[string]any{"deploy": map[string]any{"id": "target", "working_directory": source.WorkingDirectory, "managed_working_directory": managed}}})
		target := &model.DeploymentTarget{ID: "target"}
		restoreManagedComposeDirectory(source, model.PipelineRun{WorkflowSnapshot: string(data)}, target)
		if (target.WorkingDirectory == source.WorkingDirectory) != managed {
			t.Fatal("仅可恢复原运行固定的自动目录")
		}
		target = &model.DeploymentTarget{ID: "target", WorkingDirectory: "/srv/changed"}
		restoreManagedComposeDirectory(source, model.PipelineRun{WorkflowSnapshot: string(data)}, target)
		if target.WorkingDirectory != "/srv/changed" {
			t.Fatal("不能覆盖修改后的显式目录")
		}
	}
}

func TestComposeDirectoryFailureKeepsSafeCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  error
	}{
		{"deadline", context.DeadlineExceeded, ErrComposeDirectoryTimeout},
		{"canceled", context.Canceled, ErrComposeDirectoryCanceled},
		{"host", fmt.Errorf("%w: private host password", sshdeploy.ErrHostUnavailable), ErrComposeDirectoryHostUnavailable},
		{"network-timeout", &net.DNSError{Err: "private dns password", Name: "private.example", IsTimeout: true}, ErrComposeDirectoryTimeout},
		{"unknown-not-guessed", errors.New("connection refused timeout forbidden password"), ErrComposeDirectoryUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &composeHomeProbeStub{home: "/home/user", err: tc.cause}
			s := NewService(nil, nil, nil, runner, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
			_, err := s.ResolveComposeWorkingDirectory(context.Background(), model.DeploymentTarget{Platform: model.DeploymentSSH, HostID: "host", EnvironmentID: "env"}, "app")
			if !errors.Is(err, tc.want) || !errors.Is(err, ErrComposeDirectoryUnavailable) {
				t.Fatalf("未保留稳定错误分类: %v", err)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "password") {
				t.Fatal("内部错误泄漏")
			}
		})
	}
}
