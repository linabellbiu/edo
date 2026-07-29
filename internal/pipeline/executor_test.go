package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"zrt/internal/model"
)

func TestExecutionImageFailureMessageDoesNotExposeNetworkAddress(t *testing.T) {
	cause := fmt.Errorf("连接 Docker SSH 主机失败: dial tcp 10.249.0.186:22: %w", context.DeadlineExceeded)
	message := executionImageFailureMessage("transfer", "ci", false, cause)
	if message != "无法连接“ci”，SSH 连接超时" {
		t.Fatalf("传输超时提示不正确: %s", message)
	}
	if strings.Contains(message, "10.249.0.186") {
		t.Fatalf("对外提示泄露了网络地址: %s", message)
	}
}

func TestExecutionImageFailureMessageDistinguishesBuildAndTransfer(t *testing.T) {
	if message := executionImageFailureMessage("build", "ci", false, fmt.Errorf("build failed")); message != "镜像构建失败，请检查任务日志和构建方案" {
		t.Fatalf("本地构建提示不正确: %s", message)
	}
	if message := executionImageFailureMessage("build", "ci", true, fmt.Errorf("push failed")); message != "镜像构建或推送失败，请检查任务日志、构建方案和镜像仓库" {
		t.Fatalf("仓库推送提示不正确: %s", message)
	}
	if message := executionImageFailureMessage("transfer", "ci", false, fmt.Errorf("load failed")); message != "镜像传输到“ci”失败，请检查 SSH 和 Docker" {
		t.Fatalf("SSH 传输提示不正确: %s", message)
	}
}

func TestSSHExecutionSnapshotDetectsAnyScriptMutation(t *testing.T) {
	script := "printf 'deploy'  \n\n"
	component := model.PipelineRunRepository{
		DeploymentPlanKind:           model.DeploymentPlanScript,
		DeploymentPlanScript:         script,
		DeploymentPlanTimeoutSeconds: 120,
		DeploymentPlanDigest: model.DeploymentPlanExecutionDigest(
			model.DeploymentPlanScript, script, 120,
		),
	}
	target := model.DeploymentTarget{
		Platform: model.DeploymentSSH, HostID: "host-1", EnvironmentID: "environment-1",
	}
	if !validSSHExecutionSnapshot(&component, &target) {
		t.Fatal("完整 SSH 部署快照应当有效")
	}
	component.DeploymentPlanScript = strings.TrimSuffix(script, "\n")
	if validSSHExecutionSnapshot(&component, &target) {
		t.Fatal("仅修改脚本末尾换行也必须导致摘要校验失败")
	}
	component.DeploymentPlanScript = script
	component.DeploymentPlanTimeoutSeconds++
	if validSSHExecutionSnapshot(&component, &target) {
		t.Fatal("修改超时参数必须导致摘要校验失败")
	}
}
