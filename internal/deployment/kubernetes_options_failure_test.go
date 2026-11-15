package deployment

import (
	"errors"
	"strings"
	"testing"

	"edo/internal/kube"
)

func TestKubernetesOptionsReferenceFailureKeepsSafeActionableMessage(t *testing.T) {
	cause := errors.Join(kube.ErrDeploymentTemplateReference, errors.New("private-infrastructure-location"))
	code, message := kubernetesFailureDetails(cause)
	if code != "kubernetes_template_reference_unavailable" || message != kube.ErrDeploymentTemplateReference.Error() || strings.Contains(message, "private-infrastructure-location") {
		t.Fatal("扩展引用错误没有保留安全的操作提示", code, message)
	}
}

func TestKubernetesServiceTypeFailureKeepsSafeActionableMessage(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{kube.ErrDeploymentTemplateHeadlessType, "kubernetes_service_type_change_blocked"},
		{kube.ErrDeploymentTemplateExternalNamePorts, "kubernetes_service_ports_required"},
	} {
		cause := errors.Join(tc.err, errors.New("private-infrastructure-location"))
		code, message := kubernetesFailureDetails(cause)
		if code != tc.code || message != tc.err.Error() || strings.Contains(message, "private-infrastructure-location") {
			t.Fatal("服务类型失败没有保留安全的直接操作引导", code, message)
		}
	}
}
