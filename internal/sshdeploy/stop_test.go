package sshdeploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type cancelOnOutput struct {
	cancel context.CancelFunc
	output strings.Builder
}

func (w *cancelOnOutput) Write(data []byte) (int, error) {
	w.output.Write(data)
	if strings.Contains(string(data), "READY") {
		w.cancel()
	}
	return len(data), nil
}

func TestCancelLocalDeploymentStopsShellChildren(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output := &cancelOnOutput{cancel: cancel}
	result, err := runLocalDeploymentScript(ctx, Input{WorkingDirectory: t.TempDir(), Timeout: 10 * time.Second, Script: "sh -c 'sleep 1; echo SURVIVED' & echo READY; wait", Stdout: output}, "")
	if !errors.Is(err, context.Canceled) || !result.Started {
		t.Fatalf("脚本未取消: %+v %v", result, err)
	}
	if strings.Contains(output.output.String(), "SURVIVED") {
		t.Fatal("取消后子进程仍继续运行")
	}
}
