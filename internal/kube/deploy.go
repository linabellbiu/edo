package kube

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

func (s *Service) DeployImage(
	ctx context.Context,
	clusterID, namespace, deploymentName, containerName, image, deploymentID string,
	timeout time.Duration,
) (string, error) {
	namespace, err := normalizeNamespace(namespace)
	if err != nil {
		return "", err
	}
	clientset, err := s.Clientset(ctx, clusterID)
	if err != nil {
		return "", err
	}
	var previousImage string
	var generation int64
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
			return errors.New("未找到目标 Kubernetes 容器")
		}
		previousImage = current.Spec.Template.Spec.Containers[containerIndex].Image
		current.Spec.Template.Spec.Containers[containerIndex].Image = image
		if current.Spec.Template.Annotations == nil {
			current.Spec.Template.Annotations = map[string]string{}
		}
		current.Spec.Template.Annotations["zrt.io/deployment-id"] = deploymentID
		updated, err := clientset.AppsV1().Deployments(namespace).Update(ctx, current, metav1.UpdateOptions{
			FieldManager: "zrt",
		})
		if err == nil {
			generation = updated.Generation
		}
		return err
	})
	if err != nil {
		return previousImage, fmt.Errorf("更新 Kubernetes Deployment 镜像失败: %w", err)
	}

	var lastReadErr error
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(waitContext context.Context) (bool, error) {
		current, err := clientset.AppsV1().Deployments(namespace).Get(waitContext, deploymentName, metav1.GetOptions{})
		if err != nil {
			lastReadErr = err
			return false, nil
		}
		lastReadErr = nil
		desired := int32(1)
		if current.Spec.Replicas != nil {
			desired = *current.Spec.Replicas
		}
		ready := current.Status.ObservedGeneration >= generation &&
			current.Status.UpdatedReplicas >= desired &&
			current.Status.AvailableReplicas >= desired &&
			current.Status.UnavailableReplicas == 0
		return ready, nil
	})
	if err != nil {
		if lastReadErr != nil {
			return previousImage, fmt.Errorf("等待 Kubernetes Deployment 状态时读取失败: %w", lastReadErr)
		}
		return previousImage, errors.New("等待 Kubernetes Deployment 发布完成超时")
	}
	return previousImage, nil
}
