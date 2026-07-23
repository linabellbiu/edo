package terminal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTerminalOnlyAllowsKnownShellsAndSafeSizes(t *testing.T) {
	for _, shell := range []string{"", "sh", "bash", "ash"} {
		if _, err := normalizeShell(shell); err != nil {
			t.Fatalf("允许的 Shell %q 被拒绝: %v", shell, err)
		}
	}
	if _, err := normalizeShell("sh -c whoami"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("任意终端命令未被拒绝: %v", err)
	}
	if validSize(Size{Columns: 10, Rows: 30}) || validSize(Size{Columns: 120, Rows: 300}) {
		t.Fatal("越界终端尺寸未被拒绝")
	}
}

func TestTerminalRejectsInvalidTargetsBeforeConnecting(t *testing.T) {
	service := NewService(nil, nil, time.Hour)
	if _, err := service.OpenDocker(context.Background(), "", "container", "sh", Size{Columns: 120, Rows: 30}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("无效 Docker 终端目标未被拒绝: %v", err)
	}
	if _, err := service.OpenKubernetes(
		context.Background(), "cluster", "bad_namespace", "pod", "container", "sh", Size{Columns: 120, Rows: 30},
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("无效 Kubernetes 终端目标未被拒绝: %v", err)
	}
}
