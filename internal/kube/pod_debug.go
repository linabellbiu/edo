package kube

import (
	"context"
	"slices"
	"time"

	"github.com/distribution/reference"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

type PodDebugContainer struct {
	Name      string `json:"name"`
	Image     string `json:"image"`
	Container string `json:"container"`
}

func (s *Service) addPodDebugContainer(ctx context.Context, clusterID, namespace, name string, identity ResourceIdentity, input PodDebugContainer) (map[string]any, error) {
	if len(validation.IsDNS1123Subdomain(name)) != 0 || len(validation.IsDNS1123Label(input.Name)) != 0 ||
		len(validation.IsDNS1123Label(input.Container)) != 0 || len(input.Image) > 512 {
		return nil, ErrInvalidResource
	}
	if _, err := reference.ParseNormalizedNamed(input.Image); err != nil {
		return nil, ErrInvalidResource
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	ctx, query, err := s.authorizeResourceOperation(ctx, clusterID, "pods", namespace, "update", false)
	if err != nil {
		return nil, err
	}
	authorization := query.authorization
	if authorization.identityBound() && !authorization.Administrator {
		scope := ""
		if authorization.enforced() {
			scope = namespace
		}
		// 映射未启用时仍然核对当前项目中的动作权限。
		codes := authorization.Permissions[scope]
		for _, permission := range []string{"kubernetes.workload.read", "kubernetes.workload.update", "kubernetes.workload.execute"} {
			if !slices.Contains(codes, permission) {
				return nil, ErrRBACForbidden
			}
		}
	}
	pods := query.typed.CoreV1().Pods(namespace)
	pod, err := pods.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, resourceError("pods", err)
	}
	if string(pod.UID) != identity.UID || pod.ResourceVersion != identity.ResourceVersion || pod.DeletionTimestamp != nil {
		return nil, ErrResourceConflict
	}
	isTargetRunning := func(status corev1.ContainerStatus) bool {
		return status.Name == input.Container && status.State.Running != nil
	}
	if pod.Status.Phase != corev1.PodRunning || !slices.ContainsFunc(pod.Status.ContainerStatuses, isTargetRunning) {
		return nil, ErrResourceConflict
	}
	// 官方临时容器子资源只新增调试容器，镜像和指定容器名由用户提供。
	allowPrivilegeEscalation := false
	pod.Spec.EphemeralContainers = append(pod.Spec.EphemeralContainers, corev1.EphemeralContainer{
		TargetContainerName: input.Container,
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name: input.Name, Image: input.Image, Command: []string{"sh"}, Stdin: true, TTY: true,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &allowPrivilegeEscalation,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		},
	})
	options := metav1.UpdateOptions{FieldManager: "edo", FieldValidation: "Strict", DryRun: []string{metav1.DryRunAll}}
	if _, err := pods.UpdateEphemeralContainers(ctx, name, pod, options); err != nil {
		return nil, resourceError("pods", err)
	}
	// 写入沿用相同 UID 和版本，干运行与提交之间的竞争由集群拒绝。
	options.DryRun = nil
	updated, err := pods.UpdateEphemeralContainers(ctx, name, pod, options)
	if err != nil {
		return nil, resourceError("pods", err)
	}
	s.invalidateResourceQueries(clusterID, "pods")
	return map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{
		"name": name, "namespace": namespace, "uid": string(updated.UID), "resourceVersion": updated.ResourceVersion,
	}}, nil
}
