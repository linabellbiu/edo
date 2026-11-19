package kube

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
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
	Name        string  `json:"name"`
	UID         string  `json:"uid"`
	Ready       bool    `json:"ready"`
	CPU         float64 `json:"cpu"`
	Memory      float64 `json:"memory"`
	PodCapacity *int64  `json:"pod_capacity"`
	Pods        *int64  `json:"pods"`
	PodState    string  `json:"pod_state"`
}
type Overview struct {
	Counts          map[string]ResourceCount            `json:"counts"`
	Nodes           []NodeStatistics                    `json:"nodes"`
	Phases          map[string]int                      `json:"phases"`
	Usage           map[string]map[string]ResourceUsage `json:"usage"`
	MetricsState    map[string]string                   `json:"metrics_state"`
	MonitoringState string                              `json:"monitoring_state,omitempty"`
	CollectedAt     time.Time                           `json:"collected_at"`
	Errors          map[string]error                    `json:"-"`
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
	query, err := s.clusterQueries(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if query.authorization.identityBound() && !query.authorization.enforced() && !query.authorization.Administrator {
		// 未启用原生 RBAC 时也使用这次批量加载的数据库权限收紧旧候选权限。
		allowed = maps.Clone(allowed)
		for code := range allowed {
			allowed[code] = allowed[code] && slices.Contains(query.authorization.Permissions[""], code)
		}
	}
	if s.inventoryRead(ctx) {
		result, err := s.collectOverview(ctx, query, namespace, allowed)
		if result != nil {
			result.CollectedAt, _ = InventoryReadStatus(ctx)
			if result.CollectedAt.IsZero() {
				for _, reason := range result.Errors {
					if errors.Is(reason, ErrInventoryPending) {
						return nil, ErrInventoryPending
					}
				}
			}
		}
		return result, err
	}
	return s.cachedOverview(ctx, query, namespace, allowed)
}

func (s *Service) collectOverview(ctx context.Context, query *clusterQueries, namespace string, allowed map[string]bool) (*Overview, error) {
	result := &Overview{Counts: map[string]ResourceCount{}, Nodes: []NodeStatistics{}, Phases: map[string]int{}, Usage: map[string]map[string]ResourceUsage{}, MetricsState: map[string]string{}, CollectedAt: time.Now().UTC(), Errors: map[string]error{}}
	podsByNode := map[string]int64{}
	podState := "no_permission"
	if allowed["kubernetes.workload.read"] {
		podState = "unavailable"
	}
	for _, def := range resourceDefinitions {
		if !allowed[def.Permission+".read"] {
			continue
		}
		ns := ""
		if def.Namespaced && namespace != "*" {
			ns = namespace
		}
		count := ResourceCount{State: "ok"}
		continuation := ""
		for page := 0; ; page++ {
			items, listErr := s.listAuthorizedResourcePage(ctx, query, def, ns, metav1.ListOptions{Limit: 500, Continue: continuation}, 10000)
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
					var podCapacity *int64
					if text, ok := alloc["pods"].(string); ok {
						if q, err := resource.ParseQuantity(text); err == nil {
							if value, ok := q.AsInt64(); ok && value > 0 {
								podCapacity = &value
							}
						}
					}
					result.Nodes = append(result.Nodes, NodeStatistics{Name: item.GetName(), UID: string(item.GetUID()), Ready: ready, CPU: quantity(alloc["cpu"]), Memory: quantity(alloc["memory"]), PodCapacity: podCapacity})
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
					if namespace == "*" {
						countNodePod(podsByNode, &item)
					}
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
		if def.Resource == "pods" && namespace == "*" && count.State == "ok" {
			podState = "ok"
		}
	}
	if allowed["kubernetes.node.read"] && allowed["kubernetes.workload.read"] && namespace != "*" {
		// 节点用量不跟随命名空间筛选，避免把局部数量当成整个节点的占用。
		var err error
		podsByNode, err = s.listNodePodCountsForQuery(ctx, query)
		if err != nil {
			result.Errors["node_pods"] = resourceError("pods", err)
		} else {
			podState = "ok"
		}
	}
	if query.authorization.enforced() {
		// The aggregate contains only authorized namespaces. It is not a count
		// of every Pod on a node and must never be labeled as such.
		podState = "restricted"
	}
	for i := range result.Nodes {
		result.Nodes[i].PodState = podState
		if podState == "ok" {
			count := podsByNode[result.Nodes[i].Name]
			result.Nodes[i].Pods = &count
		}
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
		list, metricsErr := s.listMetricsForQuery(ctx, query, kind, ns)
		if metricsErr != nil {
			result.MetricsState[kind] = "unavailable"
			if apierrors.IsForbidden(metricsErr) || apierrors.IsUnauthorized(metricsErr) || errors.Is(metricsErr, ErrRBACForbidden) {
				result.MetricsState[kind] = "forbidden"
			}
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
	if allowed["kubernetes.node.read"] && missingNodeMetrics(result) {
		result.MonitoringState = "unavailable"
		if result.MetricsState["nodes"] == "forbidden" {
			result.MonitoringState = "forbidden"
		} else if allowed["kubernetes.workload.read"] {
			state, err := s.monitoringState(ctx, query)
			if err != nil {
				result.Errors["monitoring"] = err
			} else {
				result.MonitoringState = state
			}
		}
	}
	if query.authorization.identityBound() {
		// Earlier statistics belong to this exact policy snapshot. A later
		// failed subquery must not turn revoked data into a partial response.
		if err := s.validateQueryAuthorization(ctx, query); err != nil {
			query.stop()
			return nil, err
		}
	}
	return result, nil
}

func countNodePod(counts map[string]int64, pod *unstructured.Unstructured) {
	if node, _, _ := unstructured.NestedString(pod.Object, "spec", "nodeName"); node != "" {
		counts[node]++
	}
}

func (s *Service) listNodePodCounts(ctx context.Context, clusterID string) (map[string]int64, error) {
	query, err := s.clusterQueries(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	return s.listNodePodCountsForQuery(ctx, query)
}

func (s *Service) listNodePodCountsForQuery(ctx context.Context, query *clusterQueries) (map[string]int64, error) {
	if query.authorization.enforced() {
		return nil, ErrRBACForbidden
	}
	counts := map[string]int64{}
	def, _ := ResourceType("pods")
	continuation := ""
	for page := 0; page < 20; page++ {
		list, err := s.listAuthorizedResourcePage(ctx, query, def, "", metav1.ListOptions{Limit: 500, Continue: continuation}, 10000)
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			countNodePod(counts, &list.Items[i])
		}
		continuation = list.GetContinue()
		if continuation == "" {
			return counts, nil
		}
	}
	return nil, errors.New("节点 Pod 统计超过数量上限")
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
