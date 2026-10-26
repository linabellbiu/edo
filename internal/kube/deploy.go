package kube

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

var (
	ErrDeploymentConfigInvalid     = errors.New("Kubernetes 发布配置无效")
	ErrDeploymentContainerNotFound = errors.New("未找到目标 Kubernetes 容器")
	ErrDeploymentUpdateFailed      = errors.New("更新 Kubernetes 工作负载失败")
	ErrDeploymentStatusUnavailable = errors.New("无法读取 Kubernetes 工作负载状态")
	ErrDeploymentRolloutTimeout    = errors.New("等待 Kubernetes 工作负载就绪超时")
	ErrDeploymentProgressDeadline  = errors.New("Kubernetes 工作负载已超过发布进度期限")
)

func (s *Service) DeployImage(
	ctx context.Context,
	clusterID, namespace, deploymentName, containerName, image, deploymentID string,
	timeout time.Duration,
) (string, error) {
	return s.deployImage(ctx, clusterID, namespace, deploymentName, containerName, image, deploymentID, timeout, nil)
}

func (s *Service) deployImage(
	ctx context.Context,
	clusterID, namespace, deploymentName, containerName, image, deploymentID string,
	timeout time.Duration,
	expected *DeploymentImageIdentity,
) (previousImage string, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.ErrorContext(ctx, "Kubernetes 镜像发布未完成", "operation", "kubernetes_deploy_image",
				"cluster_id", clusterID, "namespace", namespace, "workload_name", deploymentName,
				"container_name", containerName, "deployment_id", deploymentID, "err", resultErr)
		}
	}()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	namespace, err := normalizeNamespace(namespace)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDeploymentConfigInvalid, err)
	}
	operationContext, query, err := s.authorizeResourceOperation(ctx, clusterID, "deployments", namespace, "update", false)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	ctx = operationContext
	defer func() {
		if !query.authorization.enforced() {
			return
		}
		if err := s.validateQueryAuthorization(ctx, query); err != nil {
			query.stop()
			previousImage = ""
			if resultErr == nil {
				resultErr = err
			}
			return
		}
		// The old image is an implementation dependency of the update. It is
		// not a public read result for an actor who only has update permission.
		definition, _ := ResourceType("deployments")
		if !query.authorization.permitsResourceOperation(definition, namespace, "read", false) {
			previousImage = ""
		}
	}()
	clientset := query.typed
	var generation int64
	var targetUID, targetContainer string
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := clientset.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		containerIndex := -1
		for index := range current.Spec.Template.Spec.Containers {
			if current.Spec.Template.Spec.Containers[index].Name == containerName ||
				(containerName == "" && len(current.Spec.Template.Spec.Containers) == 1) {
				containerIndex = index
				break
			}
		}
		if containerIndex < 0 {
			return ErrDeploymentContainerNotFound
		}
		if expected != nil && (string(current.UID) != expected.UID ||
			current.Spec.Template.Spec.Containers[containerIndex].Image != expected.Image ||
			current.Spec.Template.Annotations["edo.io/deployment-id"] != expected.DeploymentID) {
			return ErrDeploymentTargetChanged
		}
		if err := applyReleaseVariables(ctx, clientset, current, containerIndex, deploymentID); err != nil {
			return err
		}
		previousImage = current.Spec.Template.Spec.Containers[containerIndex].Image
		current.Spec.Template.Spec.Containers[containerIndex].Image = image
		if current.Spec.Template.Annotations == nil {
			current.Spec.Template.Annotations = map[string]string{}
		}
		current.Spec.Template.Annotations["edo.io/deployment-id"] = deploymentID
		updated, err := clientset.AppsV1().Deployments(namespace).Update(ctx, current, metav1.UpdateOptions{
			FieldManager: "edo",
		})
		if err == nil {
			generation = updated.Generation
			targetUID = string(updated.UID)
			targetContainer = current.Spec.Template.Spec.Containers[containerIndex].Name
		}
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return previousImage, ctx.Err()
		}
		if errors.Is(err, ErrDeploymentContainerNotFound) || errors.Is(err, ErrDeploymentTargetChanged) {
			return previousImage, err
		}
		return previousImage, fmt.Errorf("%w: %w", ErrDeploymentUpdateFailed, err)
	}
	if targetUID == "" || generation < 1 {
		return previousImage, ErrDeploymentStatusUnavailable
	}

	var lastReadErr error
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(waitContext context.Context) (bool, error) {
		current, err := clientset.AppsV1().Deployments(namespace).Get(waitContext, deploymentName, metav1.GetOptions{})
		if err != nil {
			if errors.Is(err, ErrRBACForbidden) || errors.Is(err, ErrRBACSyncPending) {
				return false, err
			}
			if apierrors.IsNotFound(err) {
				return false, ErrDeploymentTargetChanged
			}
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsBadRequest(err) {
				return false, fmt.Errorf("%w: %w", ErrDeploymentStatusUnavailable, err)
			}
			lastReadErr = err
			return false, nil
		}
		lastReadErr = nil
		if string(current.UID) != targetUID || current.Generation != generation ||
			current.Spec.Template.Annotations["edo.io/deployment-id"] != deploymentID {
			return false, ErrDeploymentTargetChanged
		}
		matchingContainer := false
		for _, container := range current.Spec.Template.Spec.Containers {
			if container.Name == targetContainer {
				matchingContainer = container.Image == image
				break
			}
		}
		if !matchingContainer {
			return false, ErrDeploymentTargetChanged
		}
		if current.Status.ObservedGeneration < generation {
			return false, nil
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == appsv1.DeploymentProgressing && condition.Reason == "ProgressDeadlineExceeded" {
				return false, ErrDeploymentProgressDeadline
			}
		}
		desired := int32(1)
		if current.Spec.Replicas != nil {
			desired = *current.Spec.Replicas
		}
		// 与 kubectl rollout status 一致，旧副本退出且全部新副本可用后才完成。
		// 精确数量校验同时防止不一致的状态快照被误判为成功，缩容为零仍需等待副本清空。
		ready := current.Status.UpdatedReplicas == desired &&
			current.Status.Replicas == current.Status.UpdatedReplicas &&
			current.Status.AvailableReplicas == current.Status.UpdatedReplicas &&
			current.Status.UnavailableReplicas == 0
		return ready, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return previousImage, ctx.Err()
		}
		if errors.Is(err, ErrDeploymentTargetChanged) || errors.Is(err, ErrDeploymentProgressDeadline) || errors.Is(err, ErrDeploymentStatusUnavailable) || errors.Is(err, ErrRBACForbidden) || errors.Is(err, ErrRBACSyncPending) {
			return previousImage, err
		}
		if lastReadErr != nil {
			return previousImage, fmt.Errorf("%w: %w", ErrDeploymentStatusUnavailable, lastReadErr)
		}
		return previousImage, ErrDeploymentRolloutTimeout
	}
	return previousImage, nil
}
