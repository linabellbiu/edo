package deployment

import (
	"context"
	"fmt"
	"testing"

	"edo/internal/kube"
)

func TestKubernetesProgressDeadlineHasStablePublicFailure(t *testing.T) {
	for _, tt := range []struct {
		err     error
		code    string
		message string
	}{
		{kube.ErrDeploymentProgressDeadline, "kubernetes_progress_deadline_exceeded", "Kubernetes 部署失败：工作负载已超过发布进度期限，请检查 Pod 状态和事件"},
		{context.DeadlineExceeded, "kubernetes_deployment_timeout", "Kubernetes 部署超时：执行任务已超过规定期限，请核对工作负载状态"},
		{context.Canceled, "kubernetes_deployment_canceled", "Kubernetes 部署已取消"},
		{fmt.Errorf("%w: private server error", kube.ErrDeploymentStatusUnavailable), "kubernetes_status_unavailable", "Kubernetes 部署失败：更新后无法读取工作负载状态，请检查集群连接"},
	} {
		code, message := kubernetesFailureDetails(tt.err)
		if code != tt.code || message != tt.message {
			t.Fatalf("发布失败没有明确的安全分类: code=%q message=%q", code, message)
		}
	}
}
