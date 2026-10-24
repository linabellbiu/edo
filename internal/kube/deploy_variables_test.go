package kube

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestReleaseVariablesUseImmutableOwnedSecretAndCanBeRemoved(t *testing.T) {
	client := fake.NewClientset()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "qa", UID: types.UID("workload-id")}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Env: []corev1.EnvVar{{Name: "APP_MODE", Value: "qa"}}}}}}}}
	ctx := WithReleaseVariables(t.Context(), map[string]string{"DB_PASSWORD": "private-value"})
	if err := applyReleaseVariables(ctx, client, deployment, 0, "release-one"); err != nil {
		t.Fatal(err)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].Env) != 2 {
		t.Fatal("应用环境变量被覆盖")
	}
	variable := deployment.Spec.Template.Spec.Containers[0].Env[1]
	if variable.Value != "" || variable.ValueFrom == nil || variable.ValueFrom.SecretKeyRef == nil {
		t.Fatal("密钥明文进入 Pod 配置")
	}
	secret, err := client.CoreV1().Secrets("qa").Get(t.Context(), variable.ValueFrom.SecretKeyRef.Name, metav1.GetOptions{})
	if err != nil || secret.Immutable == nil || !*secret.Immutable || len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].UID != deployment.UID {
		t.Fatal("Secret 没有固定身份或生命周期归属")
	}
	deployment.Spec.Template.Annotations = map[string]string{"edo.io/deployment-id": "release-one"}
	if err = applyReleaseVariables(WithReleaseVariables(t.Context(), map[string]string{}), client, deployment, 0, "release-two"); err != nil {
		t.Fatal(err)
	}
	if len(deployment.Spec.Template.Spec.Containers[0].Env) != 1 || deployment.Spec.Template.Spec.Containers[0].Env[0].Name != "APP_MODE" {
		t.Fatal("取消变量引用后保留了密钥或删除了用户配置")
	}
}
