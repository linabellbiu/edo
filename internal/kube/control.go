package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

var (
	ErrRuntimeControlInvalid   = errors.New("Kubernetes 运行控制参数无效")
	ErrRuntimeResourceMissing  = errors.New("Kubernetes 工作负载不存在")
	ErrRuntimeControlFailed    = errors.New("Kubernetes 工作负载操作失败")
	ErrRuntimeStateUnavailable = errors.New("Kubernetes 工作负载状态不可用")
)

const stoppedReplicasAnnotation = "edo.io/stopped-replicas"
const restartedAtAnnotation = "edo.io/restarted-at"

type RuntimeState struct {
	Kind              string `json:"kind"`
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	State             string `json:"state"`
	Running           bool   `json:"running"`
	Replicas          int32  `json:"replicas"`
	ReadyReplicas     int32  `json:"ready_replicas"`
	AvailableReplicas int32  `json:"available_replicas"`
}

func (s *Service) DeploymentRuntimeState(ctx context.Context, clusterID, namespace, name string) (RuntimeState, error) {
	namespace, err := normalizeNamespace(namespace)
	if err != nil || strings.TrimSpace(clusterID) == "" || strings.TrimSpace(name) == "" {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	ctx, query, err := s.authorizeResourceOperation(ctx, clusterID, "deployments", namespace, "read", false)
	if err != nil {
		return RuntimeState{}, err
	}
	current, err := query.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %w", ErrRuntimeResourceMissing, err)
	}
	return deploymentRuntimeState(current.Namespace, current.Name, current.Spec.Replicas, current.Status.ReadyReplicas, current.Status.AvailableReplicas), nil
}

func (s *Service) ControlDeployment(
	ctx context.Context,
	clusterID, namespace, name, action string,
	replicas int32,
	timeout time.Duration,
) (RuntimeState, error) {
	namespace, err := normalizeNamespace(namespace)
	if err != nil || strings.TrimSpace(clusterID) == "" || strings.TrimSpace(name) == "" ||
		(action != "restart" && action != "stop" && action != "scale") || replicas < 0 || replicas > 1000 ||
		timeout < 30*time.Second || timeout > time.Hour {
		return RuntimeState{}, ErrRuntimeControlInvalid
	}
	ctx, query, err := s.authorizeResourceOperation(ctx, clusterID, "deployments", namespace, "execute", false)
	if err != nil {
		return RuntimeState{}, err
	}
	clientset := query.typed
	var generation int64
	var desired int32
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !(ResourceIdentity{UID: string(current.UID), ResourceVersion: current.ResourceVersion}).valid() {
			return ErrResourceConflict
		}
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		currentReplicas := int32(1)
		if current.Spec.Replicas != nil {
			currentReplicas = *current.Spec.Replicas
		}
		switch action {
		case "stop":
			if currentReplicas > 0 {
				current.Annotations[stoppedReplicasAnnotation] = strconv.FormatInt(int64(currentReplicas), 10)
			}
			desired = 0
		case "restart":
			desired = currentReplicas
			if desired == 0 {
				desired = 1
				if saved, parseErr := strconv.ParseInt(current.Annotations[stoppedReplicasAnnotation], 10, 32); parseErr == nil && saved > 0 && saved <= 1000 {
					desired = int32(saved)
				}
			}
			delete(current.Annotations, stoppedReplicasAnnotation)
			if current.Spec.Template.Annotations == nil {
				current.Spec.Template.Annotations = map[string]string{}
			}
			// 修改 Pod 模板才会创建新的 ReplicaSet；只改 Deployment 元数据不会触发滚动重启。
			current.Spec.Template.Annotations[restartedAtAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
		case "scale":
			desired = replicas
			if desired == 0 && currentReplicas > 0 {
				current.Annotations[stoppedReplicasAnnotation] = strconv.FormatInt(int64(currentReplicas), 10)
			} else if desired > 0 {
				delete(current.Annotations, stoppedReplicasAnnotation)
			}
		}
		// Execute authorizes these reviewed fields, rather than an unrestricted
		// Deployment update. Native execute grants patch for this narrow action.
		annotations := map[string]any{}
		if value, exists := current.Annotations[stoppedReplicasAnnotation]; exists {
			annotations[stoppedReplicasAnnotation] = value
		} else if action == "restart" || action == "scale" && desired > 0 {
			annotations[stoppedReplicasAnnotation] = nil
		}
		metadata := map[string]any{"uid": string(current.UID), "resourceVersion": current.ResourceVersion}
		if len(annotations) > 0 {
			metadata["annotations"] = annotations
		}
		spec := map[string]any{"replicas": desired}
		if action == "restart" {
			spec["template"] = map[string]any{"metadata": map[string]any{"annotations": map[string]any{restartedAtAnnotation: current.Spec.Template.Annotations[restartedAtAnnotation]}}}
		}
		patch, err := json.Marshal(map[string]any{"metadata": metadata, "spec": spec})
		if err != nil {
			return err
		}
		updated, err := clientset.AppsV1().Deployments(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "edo"})
		if err == nil {
			generation = updated.Generation
		}
		return err
	})
	if err != nil {
		return RuntimeState{}, fmt.Errorf("%w: %w", ErrRuntimeControlFailed, err)
	}

	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastState RuntimeState
	var lastReadErr error
	err = wait.PollUntilContextTimeout(waitContext, time.Second, timeout, true, func(pollContext context.Context) (bool, error) {
		current, err := clientset.AppsV1().Deployments(namespace).Get(pollContext, name, metav1.GetOptions{})
		if err != nil {
			if errors.Is(err, ErrRBACForbidden) || errors.Is(err, ErrRBACSyncPending) {
				return false, err
			}
			lastReadErr = err
			return false, nil
		}
		lastReadErr = nil
		lastState = deploymentRuntimeState(current.Namespace, current.Name, current.Spec.Replicas, current.Status.ReadyReplicas, current.Status.AvailableReplicas)
		observed := current.Status.ObservedGeneration >= generation
		if desired == 0 {
			return observed && current.Status.ReadyReplicas == 0 && current.Status.AvailableReplicas == 0, nil
		}
		return observed && current.Status.UpdatedReplicas >= desired && current.Status.ReadyReplicas >= desired &&
			current.Status.AvailableReplicas >= desired && current.Status.UnavailableReplicas == 0, nil
	})
	if err != nil {
		if lastReadErr != nil {
			return RuntimeState{}, fmt.Errorf("%w: %w", ErrRuntimeStateUnavailable, lastReadErr)
		}
		return RuntimeState{}, fmt.Errorf("%w: %w", ErrRuntimeControlFailed, err)
	}
	return lastState, nil
}

func deploymentRuntimeState(namespace, name string, replicas *int32, ready, available int32) RuntimeState {
	desired := int32(1)
	if replicas != nil {
		desired = *replicas
	}
	state := "running"
	if desired == 0 {
		state = "stopped"
	} else if ready < desired || available < desired {
		state = "progressing"
	}
	return RuntimeState{
		Kind: "kubernetes", Name: name, Namespace: namespace, State: state,
		Running:  desired > 0 && ready >= desired && available >= desired,
		Replicas: desired, ReadyReplicas: ready, AvailableReplicas: available,
	}
}
