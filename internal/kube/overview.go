package kube

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

type ResourceCount struct {
	Count int    `json:"count"`
	State string `json:"state"`
}
type ResourceUsage struct {
	CPU       float64 `json:"cpu"`
	Memory    float64 `json:"memory"`
	Timestamp string  `json:"timestamp"`
}
type NodeStatistics struct {
	Name   string  `json:"name"`
	Ready  bool    `json:"ready"`
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
}
type Overview struct {
	Counts       map[string]ResourceCount            `json:"counts"`
	Nodes        []NodeStatistics                    `json:"nodes"`
	Phases       map[string]int                      `json:"phases"`
	Usage        map[string]map[string]ResourceUsage `json:"usage"`
	MetricsState map[string]string                   `json:"metrics_state"`
	CollectedAt  time.Time                           `json:"collected_at"`
	Errors       map[string]error                    `json:"-"`
}

func quantity(value any) float64 {
	text, ok := value.(string)
	if !ok {
		return 0
	}
	q, err := resource.ParseQuantity(text)
	if err != nil {
		return 0
	}
	return q.AsApproximateFloat64()
}

// allowed 由 HTTP 层按当前项目权限生成；不会为统计跨过资源的读取权限。
func (s *Service) Overview(ctx context.Context, clusterID, namespace string, allowed map[string]bool) (*Overview, error) {
	if namespace == "" {
		namespace = "*"
	}
	if namespace != "*" && len(validation.IsDNS1123Label(namespace)) != 0 {
		return nil, ErrInvalidResource
	}
	// 先检查项目内集群归属，即使当前没有可查询的资源也不能绕过。
	cfg, err := s.RESTConfig(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	result := &Overview{Counts: map[string]ResourceCount{}, Nodes: []NodeStatistics{}, Phases: map[string]int{}, Usage: map[string]map[string]ResourceUsage{}, MetricsState: map[string]string{}, CollectedAt: time.Now().UTC(), Errors: map[string]error{}}
	for _, def := range resourceDefinitions {
		if !allowed[def.Permission+".read"] {
			continue
		}
		ns := ""
		if def.Namespaced && namespace != "*" {
			ns = namespace
		}
		var api dynamic.ResourceInterface = client.Resource(def.gvr())
		if def.Namespaced {
			api = client.Resource(def.gvr()).Namespace(ns)
		}
		count := ResourceCount{State: "ok"}
		continuation := ""
		for page := 0; ; page++ {
			items, listErr := api.List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
			if listErr != nil {
				count.State = "unavailable"
				result.Errors[def.Resource] = resourceError(def.Resource, listErr)
				break
			}
			count.Count += len(items.Items)
			for _, item := range items.Items {
				if def.Resource == "nodes" {
					alloc, _, _ := unstructured.NestedMap(item.Object, "status", "allocatable")
					ready := false
					conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
					for _, value := range conditions {
						if condition, ok := value.(map[string]any); ok && condition["type"] == "Ready" {
							ready = condition["status"] == "True"
						}
					}
					result.Nodes = append(result.Nodes, NodeStatistics{Name: item.GetName(), Ready: ready, CPU: quantity(alloc["cpu"]), Memory: quantity(alloc["memory"])})
				}
				if def.Resource == "pods" {
					phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
					if item.GetDeletionTimestamp() != nil {
						phase = "Terminating"
					}
					if phase == "" {
						phase = "Unknown"
					}
					result.Phases[phase]++
				}
			}
			continuation = items.GetContinue()
			if continuation == "" {
				break
			}
			if page >= 19 {
				count.State = "partial"
				break
			}
		}
		if count.State == "unavailable" {
			if def.Resource == "nodes" {
				result.Nodes = nil
			}
			if def.Resource == "pods" {
				result.Phases = nil
			}
		}
		result.Counts[def.Resource] = count
	}
	for _, kind := range []string{"nodes", "pods"} {
		def, _ := ResourceType(kind)
		if !allowed[def.Permission+".read"] {
			continue
		}
		result.Usage[kind] = map[string]ResourceUsage{}
		ns := namespace
		if ns == "*" {
			ns = ""
		}
		var list *unstructured.UnstructuredList
		var metricsErr error
		for _, version := range []string{"v1", "v1beta1"} {
			api := client.Resource(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: version, Resource: kind})
			var reader dynamic.ResourceInterface = api
			if kind == "pods" {
				reader = api.Namespace(ns)
			}
			list, metricsErr = reader.List(ctx, metav1.ListOptions{})
			if !apierrors.IsNotFound(metricsErr) {
				break
			}
		}
		if metricsErr != nil {
			result.MetricsState[kind] = "unavailable"
			result.Errors["metrics/"+kind] = resourceError(kind, metricsErr)
			continue
		}
		result.MetricsState[kind] = "ok"
		for _, item := range list.Items {
			timestamp, _, _ := unstructured.NestedString(item.Object, "timestamp")
			at, parseErr := time.Parse(time.RFC3339Nano, timestamp)
			if parseErr != nil || time.Since(at) > 2*time.Minute || time.Until(at) > time.Minute {
				continue
			}
			usage := ResourceUsage{Timestamp: timestamp}
			values, _, _ := unstructured.NestedMap(item.Object, "usage")
			if kind == "pods" {
				containers, _, _ := unstructured.NestedSlice(item.Object, "containers")
				if len(containers) == 0 {
					continue
				}
				complete := true
				for _, value := range containers {
					if container, ok := value.(map[string]any); ok {
						if data, ok := container["usage"].(map[string]any); ok {
							if !validUsage(data) {
								complete = false
								break
							}
							usage.CPU += quantity(data["cpu"])
							usage.Memory += quantity(data["memory"])
						} else {
							complete = false
						}
					} else {
						complete = false
					}
				}
				if !complete {
					continue
				}
			} else {
				if !validUsage(values) {
					continue
				}
				usage.CPU = quantity(values["cpu"])
				usage.Memory = quantity(values["memory"])
			}
			key := item.GetName()
			if kind == "pods" {
				key = item.GetNamespace() + "/" + key
			}
			result.Usage[kind][key] = usage
		}
	}
	return result, nil
}

func validUsage(values map[string]any) bool {
	for _, key := range []string{"cpu", "memory"} {
		text, ok := values[key].(string)
		if !ok {
			return false
		}
		q, err := resource.ParseQuantity(text)
		if err != nil || q.Sign() < 0 {
			return false
		}
	}
	return true
}
