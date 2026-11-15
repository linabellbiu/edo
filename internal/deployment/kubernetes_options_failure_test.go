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
