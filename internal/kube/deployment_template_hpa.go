package kube

import (
	"context"
	"errors"
	"fmt"

	"edo/internal/model"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var ErrDeploymentTemplateHPA = errors.New("无法配置 HPA，请检查集群自动扩缩容能力和工作负载权限")

// 只对启用过或显式配置的目标核对 HPA，不让旧固定副本方案新增权限依赖。
func (s *Service) deploymentTemplateHPAState(ctx context.Context, t model.DeploymentTarget, appID, targetID string, cfg *model.KubernetesDeploymentSettings, workload map[string]any) (map[string]any, error) {
	managed, _, _ := unstructured.NestedString(workload, "metadata", "annotations", "edo.io/hpa-managed")
	if cfg.HPA == nil && managed != "true" {
		return nil, nil
	}
	readCtx, query, err := s.authorizeResourceOperation(ctx, t.RuntimeID, "horizontalpodautoscalers", t.Namespace, "read", false)
	if err != nil {
		return nil, err
	}
	var current map[string]any
	continuation := ""
	for {
		list, err := query.typed.AutoscalingV2().HorizontalPodAutoscalers(t.Namespace).List(readCtx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrDeploymentTemplateHPA, err)
		}
		for _, item := range list.Items {
			ref := item.Spec.ScaleTargetRef
			matches := ref.APIVersion == "apps/v1" && ref.Kind == "Deployment" && ref.Name == t.WorkloadName
			if !matches && item.Name != t.WorkloadName {
				continue
			}
			object := templateObject(&item)
			if !matches || item.Name != t.WorkloadName || current != nil || !templateOwned(object, t.ProjectID, appID, targetID) {
				return nil, ErrDeploymentTemplateConflict
			}
			current = object
		}
		continuation = list.Continue
		if continuation == "" {
			break
		}
	}
	if !cfg.AutoscalingEnabled() && current == nil {
		return nil, nil
	}
	action := "create"
	if current != nil {
		action = "update"
		if !cfg.AutoscalingEnabled() {
			action = "delete"
		}
	}
	if err := s.RequireResourcePermission(ctx, t.RuntimeID, "horizontalpodautoscalers", t.Namespace, action, false); err != nil {
		return nil, err
	}
	return current, nil
}

func deploymentTemplateHPA(current map[string]any, cfg *model.KubernetesDeploymentSettings, metadata metav1.ObjectMeta) map[string]any {
	if !cfg.AutoscalingEnabled() {
		return nil
	}
	settings := cfg.HPA
	hpa := &autoscalingv2.HorizontalPodAutoscaler{TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"}, ObjectMeta: metadata, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: metadata.Name}, MinReplicas: &settings.MinReplicas, MaxReplicas: settings.MaxReplicas}}
	for _, metric := range []struct {
		name  corev1.ResourceName
		value *int32
	}{{corev1.ResourceCPU, settings.CPUUtilization}, {corev1.ResourceMemory, settings.MemoryUtilization}} {
		if metric.value != nil {
			hpa.Spec.Metrics = append(hpa.Spec.Metrics, autoscalingv2.MetricSpec{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: metric.name, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: metric.value}}})
		}
	}
	object := templateObject(hpa)
	bindTemplateIdentity(object, current)
	return object
}
