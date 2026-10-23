package kube

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeployImageIfCurrentRejectsConfirmationDriftBeforeUpdate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected DeploymentImageIdentity
	}{
		{"对象身份变化", DeploymentImageIdentity{UID: "other-object", Image: readinessPreviousImage}},
		{"目标镜像变化", DeploymentImageIdentity{UID: "original-deployment", Image: readinessTargetImage}},
		{"发布归属变化", DeploymentImageIdentity{UID: "original-deployment", Image: readinessPreviousImage, DeploymentID: "other-run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, id := deployReadinessFixture(t, nil)
			previous, err := service.DeployImageIfCurrent(context.Background(), id, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, time.Second, tc.expected)
			if !errors.Is(err, ErrDeploymentTargetChanged) || previous != "" {
				t.Fatalf("确认后漂移仍更新镜像: previous=%q err=%v", previous, err)
			}
			identity, err := service.DeploymentImageIdentity(context.Background(), id, "acceptance", "api", "api")
			if err != nil || identity.Image != readinessPreviousImage || identity.DeploymentID != "" {
				t.Fatalf("拒绝后目标被修改: %+v %v", identity, err)
			}
		})
	}
}
