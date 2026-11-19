package kube

import (
	"context"
	"errors"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func missingNodeMetrics(result *Overview) bool {
	for _, node := range result.Nodes {
		if _, ok := result.Usage["nodes"][node.Name]; !ok {
			return true
		}
	}
	return false
}

// 仅在缺测时读取当前身份可见的组件；无法确认时不推测正在安装。
func (s *Service) monitoringState(ctx context.Context, query *clusterQueries) (string, error) {
	def, _ := ResourceType("deployments")
	if !query.authorization.permitsResourceOperation(def, "kube-system", "read", false) {
		return "unavailable", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx, _, err := authorizeResourceQuery(ctx, query, def, "kube-system", "read", false)
	if err != nil {
		return "unavailable", err
	}
	var deployment *appsv1.Deployment
	if s.inventoryRead(ctx) {
		object, readErr := s.inventoryGet(ctx, query, def, "kube-system", "metrics-server")
		err = readErr
		if err == nil {
			deployment = &appsv1.Deployment{}
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, deployment)
		}
	} else {
		deployment, err = query.typed.AppsV1().Deployments("kube-system").Get(ctx, "metrics-server", metav1.GetOptions{})
	}
	if apierrors.IsNotFound(err) || errors.Is(err, ErrResourceNotFound) {
		// 接入的集群可以使用其他指标实现，未找到并不代表正在安装。
		return "unavailable", nil
	}
	if err != nil {
		return "unavailable", err
	}
	if deployment.UID == "" || deployment.Spec.Selector == nil || deployment.DeletionTimestamp != nil || (deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0) {
		return "unavailable", nil
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil || selector.Empty() {
		return "unavailable", ErrInvalidResource
	}
	options := metav1.ListOptions{LabelSelector: selector.String(), Limit: 100}
	var replicas []appsv1.ReplicaSet
	var continuation string
	if s.inventoryRead(ctx) {
		replicas, continuation, err = inventoryTypedList[appsv1.ReplicaSet](ctx, s, query, inventoryReplicaSets, "kube-system", options)
	} else {
		result, readErr := query.typed.AppsV1().ReplicaSets("kube-system").List(ctx, options)
		err = readErr
		if err == nil {
			replicas, continuation = result.Items, result.Continue
		}
	}
	if err != nil {
		return "unavailable", err
	}
	if continuation != "" {
		return "unavailable", nil
	}
	def, _ = ResourceType("pods")
	ctx, _, err = authorizeResourceQuery(ctx, query, def, "kube-system", "read", false)
	if err != nil {
		return "unavailable", err
	}
	var pods []corev1.Pod
	if s.inventoryRead(ctx) {
		pods, continuation, err = inventoryTypedList[corev1.Pod](ctx, s, query, def, "kube-system", options)
	} else {
		result, readErr := query.typed.CoreV1().Pods("kube-system").List(ctx, options)
		err = readErr
		if err == nil {
			pods, continuation = result.Items, result.Continue
		}
	}
	if err != nil {
		return "unavailable", err
	}
	if continuation != "" {
		return "unavailable", nil
	}
	return currentMonitoringState(deployment, replicas, pods), nil
}

func currentMonitoringState(deployment *appsv1.Deployment, replicas []appsv1.ReplicaSet, pods []corev1.Pod) string {
	current := map[types.UID]bool{}
	for _, rs := range replicas {
		owner := metav1.GetControllerOf(&rs)
		if owner == nil || owner.Kind != "Deployment" || owner.UID != deployment.UID || rs.UID == "" || rs.DeletionTimestamp != nil {
			continue
		}
		template, wanted := rs.Spec.Template.DeepCopy(), deployment.Spec.Template.DeepCopy()
		delete(template.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
		delete(wanted.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
		if apiequality.Semantic.DeepEqual(template, wanted) {
			current[rs.UID] = true
		}
	}
	state := "installing"
	priority := map[string]int{"installing": 0, "startup_failed": 1, "image_pull_failed": 2, "image_pull_timeout": 3}
	ready := false
	for _, pod := range pods {
		owner := metav1.GetControllerOf(&pod)
		if owner == nil || owner.Kind != "ReplicaSet" || !current[owner.UID] || pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if pod.Status.Phase == corev1.PodFailed && priority[state] < priority["startup_failed"] {
			state = "startup_failed"
		}
		for _, container := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
			waiting := container.State.Waiting
			if waiting == nil {
				continue
			}
			candidate := "installing"
			switch waiting.Reason {
			case "ErrImagePull", "ImagePullBackOff":
				candidate = "image_pull_failed"
				// 只分类已确认的拉取故障；不把等待时长或其他请求超时当成镜像超时。
				message := strings.ToLower(waiting.Message)
				for _, marker := range []string{"timeout", "timed out", "context deadline exceeded"} {
					if strings.Contains(message, marker) {
						candidate = "image_pull_timeout"
						break
					}
				}
			case "CrashLoopBackOff", "CreateContainerConfigError", "CreateContainerError", "InvalidImageName":
				candidate = "startup_failed"
			}
			if priority[candidate] > priority[state] {
				state = candidate
			}
		}
	}
	if state != "installing" {
		return state
	}
	if deployment.Status.ObservedGeneration >= deployment.Generation {
		for _, condition := range deployment.Status.Conditions {
			if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded" {
				return "startup_failed"
			}
		}
		if ready && deployment.Status.AvailableReplicas > 0 {
			return "waiting_metrics"
		}
	}
	return state
}
