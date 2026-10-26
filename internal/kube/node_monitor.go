package kube

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"github.com/redis/go-redis/v9"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	resourcehelper "k8s.io/component-helpers/resource"
)

const NodeSampleInterval = 30 * time.Second
const NodeHistoryRetention = 72 * time.Hour

var ErrNodeHistoryUnavailable = errors.New("节点监控暂不可用，请稍后重试")
var ErrNodeHistoryRange = errors.New("请选择支持的节点监控时间范围")

type NodeAllocation struct {
	CPURequests    float64 `json:"cpu_requests"`
	CPULimits      float64 `json:"cpu_limits"`
	MemoryRequests float64 `json:"memory_requests"`
	MemoryLimits   float64 `json:"memory_limits"`
}

type nodePodAllocation struct {
	count      int64
	allocation NodeAllocation
}

type NodeSample struct {
	CollectedAt       time.Time       `json:"collected_at"`
	MetricsAt         string          `json:"metrics_at"`
	CPU               *float64        `json:"cpu"`
	Memory            *float64        `json:"memory"`
	CPUAllocatable    *float64        `json:"cpu_allocatable"`
	MemoryAllocatable *float64        `json:"memory_allocatable"`
	PodCapacity       *int64          `json:"pod_capacity"`
	Pods              *int64          `json:"pods"`
	Allocation        *NodeAllocation `json:"allocation"`
	MetricsState      string          `json:"metrics_state"`
	PodsState         string          `json:"pods_state"`
}
type NodeHistory struct {
	UID             string       `json:"uid"`
	From            time.Time    `json:"from"`
	To              time.Time    `json:"to"`
	IntervalSeconds int          `json:"interval_seconds"`
	Points          []NodeSample `json:"points"`
}
type nodeMonitor struct {
	client *redis.Client
	prefix string
	logger *slog.Logger
}

func (s *Service) ConfigureNodeMonitoring(client *redis.Client, prefix string, logger *slog.Logger) {
	s.nodeMonitor = &nodeMonitor{client: client, prefix: prefix, logger: logger}
}

// RunNodeMonitoring 与服务同生命周期；采样不依赖浏览器，集群之间最多并发四个。
func (s *Service) RunNodeMonitoring(ctx context.Context) error {
	if s.nodeMonitor == nil {
		return nil
	}
	ticker := time.NewTicker(NodeSampleInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		s.collectNodeMonitoring(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (s *Service) collectNodeMonitoring(ctx context.Context) {
	var clusters []model.KubernetesCluster
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Find(&clusters).Error; err != nil {
		s.nodeMonitor.logger.Error("读取节点监控集群失败", "operation", "kubernetes_node_monitor_clusters", "err", err)
		return
	}
	slots := make(chan struct{}, 4)
	var pending sync.WaitGroup
	for _, cluster := range clusters {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			pending.Wait()
			return
		}
		pending.Add(1)
		go func(cluster model.KubernetesCluster) {
			defer pending.Done()
			defer func() { <-slots }()
			scoped := database.WithDepartmentScope(ctx, database.DepartmentScope{DepartmentID: cluster.DepartmentID, ProjectID: cluster.ProjectID})
			collectCtx, cancel := context.WithTimeout(scoped, 25*time.Second)
			defer cancel()
			if err := s.collectClusterNodes(collectCtx, cluster.ID); err != nil && ctx.Err() == nil {
				s.nodeMonitor.logger.Warn("采集节点监控失败", "operation", "kubernetes_node_monitor_collect", "cluster_id", cluster.ID, "err", err)
			}
		}(cluster)
	}
	pending.Wait()
}
func (s *Service) collectClusterNodes(ctx context.Context, clusterID string) error {
	var nodes corev1.NodeList
	if err := s.monitorResourcePage(ctx, clusterID, "nodes", metav1.ListOptions{Limit: 500}, &nodes); err != nil {
		return err
	}
	for next := nodes.Continue; next != ""; {
		if len(nodes.Items) >= 10000 {
			return errors.New("节点监控超过数量上限")
		}
		var page corev1.NodeList
		if err := s.monitorResourcePage(ctx, clusterID, "nodes", metav1.ListOptions{Limit: 500, Continue: next}, &page); err != nil {
			return err
		}
		nodes.Items = append(nodes.Items, page.Items...)
		next = page.Continue
	}
	metrics, metricsErr := s.readNodeMetrics(ctx, clusterID)
	podsByNode, podsErr := readNodePodPages(ctx, func(ctx context.Context, options metav1.ListOptions) (*corev1.PodList, error) {
		var page corev1.PodList
		err := s.monitorResourcePage(ctx, clusterID, "pods", options, &page)
		return &page, err
	})
	now := time.Now().UTC()
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if node.UID == "" {
			continue
		}
		sample := buildNodeSample(node, podsByNode[node.Name], metrics[node.Name], now)
		if metricsErr != nil {
			sample.MetricsState = "unavailable"
			if apierrors.IsForbidden(metricsErr) {
				sample.MetricsState = "forbidden"
			}
		}
		if podsErr != nil {
			sample.Pods = nil
			sample.Allocation = nil
			sample.PodsState = "unavailable"
			if apierrors.IsForbidden(podsErr) {
				sample.PodsState = "forbidden"
			}
		}
		if err := s.nodeMonitor.append(ctx, clusterID, string(node.UID), sample); err != nil {
			return err
		}
	}
	// 错误只进入日志；响应使用状态码和稳定提示，不回传 Kubernetes 原始错误。
	if metricsErr != nil {
		s.nodeMonitor.logger.Warn("节点 CPU/内存采样不可用", "operation", "kubernetes_node_metrics", "cluster_id", clusterID, "err", metricsErr)
	}
	if podsErr != nil {
		s.nodeMonitor.logger.Warn("节点 Pod 资源统计不可用", "operation", "kubernetes_node_pods", "cluster_id", clusterID, "err", podsErr)
	}
	return nil
}

func (s *Service) monitorResourcePage(ctx context.Context, clusterID, resource string, options metav1.ListOptions, result any) error {
	def, ok := ResourceType(resource)
	if !ok {
		return ErrInvalidResource
	}
	page, err := s.listResourceSnapshotPage(ctx, clusterID, def, "", options)
	if err != nil {
		return err
	}
	return runtime.DefaultUnstructuredConverter.FromUnstructured(page.UnstructuredContent(), result)
}

func (s *Service) readNodeMetrics(ctx context.Context, clusterID string) (map[string]ResourceUsage, error) {
	result := map[string]ResourceUsage{}
	list, err := s.listMetrics(ctx, clusterID, "nodes", "")
	if err != nil {
		return result, err
	}
	for _, node := range list.Items {
		timestamp, _, _ := unstructured.NestedString(node.Object, "timestamp")
		values, _, _ := unstructured.NestedMap(node.Object, "usage")
		if validUsage(values) {
			result[node.GetName()] = ResourceUsage{CPU: quantity(values["cpu"]), Memory: quantity(values["memory"]), Timestamp: timestamp}
		}
	}
	return result, nil
}
func readNodePods(ctx context.Context, client kubernetes.Interface) (map[string]nodePodAllocation, error) {
	return readNodePodPages(ctx, client.CoreV1().Pods("").List)
}

// Both direct-client tests and production observation use the same bounded,
// streaming aggregation; only the official typed page reader differs.
func readNodePodPages(ctx context.Context, list func(context.Context, metav1.ListOptions) (*corev1.PodList, error)) (map[string]nodePodAllocation, error) {
	allocations := map[string]nodePodAllocation{}
	options := resourcehelper.PodResourcesOptions{UseStatusResources: true, Reuse: corev1.ResourceList{}}
	next := ""
	count := 0
	for range 20 {
		page, err := list(ctx, metav1.ListOptions{Limit: 500, Continue: next})
		if err != nil {
			return nil, err
		}
		count += len(page.Items)
		if count > 10000 {
			return nil, errors.New("节点 Pod 统计超过数量上限")
		}
		// 每页只保留节点计数和资源总量，避免跨分页持有全部 Pod 及其配置。
		for i := range page.Items {
			accumulateNodePod(allocations, &page.Items[i], options)
		}
		next = page.Continue
		if next == "" {
			return allocations, nil
		}
	}
	return nil, errors.New("节点 Pod 统计超过数量上限")
}

func accumulateNodePod(allocations map[string]nodePodAllocation, pod *corev1.Pod, options resourcehelper.PodResourcesOptions) {
	if pod.Spec.NodeName == "" {
		return
	}
	total := allocations[pod.Spec.NodeName]
	total.count++
	// 已结束记录仍计入节点 Pod 数量，但不再占调度请求。
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		// 官方实现处理 Init、Sidecar、Overhead 和 Pod 级资源；提取数值后才能复用资源表。
		requests := resourcehelper.PodRequests(pod, options)
		total.allocation.CPURequests += requests.Cpu().AsApproximateFloat64()
		total.allocation.MemoryRequests += requests.Memory().AsApproximateFloat64()
		limits := resourcehelper.PodLimits(pod, options)
		total.allocation.CPULimits += limits.Cpu().AsApproximateFloat64()
		total.allocation.MemoryLimits += limits.Memory().AsApproximateFloat64()
	}
	allocations[pod.Spec.NodeName] = total
}

func buildNodeSample(node *corev1.Node, pods nodePodAllocation, usage ResourceUsage, now time.Time) NodeSample {
	allocation := pods.allocation
	sample := NodeSample{CollectedAt: now, MetricsState: "missing", PodsState: "ok", Allocation: &allocation}
	for key, target := range map[corev1.ResourceName]**float64{corev1.ResourceCPU: &sample.CPUAllocatable, corev1.ResourceMemory: &sample.MemoryAllocatable} {
		if q, ok := node.Status.Allocatable[key]; ok && q.Sign() > 0 {
			v := q.AsApproximateFloat64()
			*target = &v
		}
	}
	if q, ok := node.Status.Allocatable[corev1.ResourcePods]; ok && q.Sign() > 0 {
		v := q.Value()
		sample.PodCapacity = &v
	}
	at, err := time.Parse(time.RFC3339Nano, usage.Timestamp)
	if err == nil && !at.Before(node.CreationTimestamp.Time) && now.Sub(at) <= 2*time.Minute && at.Sub(now) <= time.Minute {
		sample.CPU = &usage.CPU
		sample.Memory = &usage.Memory
		sample.MetricsAt = usage.Timestamp
		sample.MetricsState = "ok"
	}
	sample.Pods = &pods.count
	return sample
}
func (m *nodeMonitor) key(clusterID, uid string) string {
	digest := sha256.Sum256([]byte(clusterID + "\x00" + uid))
	return m.prefix + ":" + fmt.Sprintf("%x", digest[:])
}
func (m *nodeMonitor) append(ctx context.Context, clusterID, uid string, sample NodeSample) error {
	data, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	key := m.key(clusterID, uid)
	at := sample.CollectedAt
	bucket := at.Truncate(time.Minute).UnixMilli()
	_, err = m.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZRemRangeByScore(ctx, key, strconv.FormatInt(bucket, 10), "("+strconv.FormatInt(bucket+60000, 10))
		pipe.ZAdd(ctx, key, redis.Z{Score: float64(at.UnixMilli()), Member: data})
		pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(at.Add(-NodeHistoryRetention).UnixMilli(), 10))
		pipe.ZRemRangeByRank(ctx, key, 0, -4322)
		pipe.Expire(ctx, key, NodeHistoryRetention)
		return nil
	})
	return err
}
func (s *Service) NodeHistory(ctx context.Context, clusterID, name, uid string, window time.Duration, workloads bool) (NodeHistory, error) {
	now := time.Now().UTC()
	result := NodeHistory{UID: uid, From: now.Add(-window), To: now, IntervalSeconds: 60, Points: []NodeSample{}}
	if window != 15*time.Minute && window != time.Hour && window != 6*time.Hour && window != 24*time.Hour && window != NodeHistoryRetention {
		return result, ErrNodeHistoryRange
	}
	node, err := s.GetResource(ctx, clusterID, "nodes", "", name, false)
	if err != nil {
		return result, err
	}
	if uid == "" || len(uid) > 128 || string((&unstructured.Unstructured{Object: node}).GetUID()) != uid {
		return result, ErrResourceConflict
	}
	if s.nodeMonitor == nil {
		return result, ErrNodeHistoryUnavailable
	}
	values, err := s.nodeMonitor.client.ZRangeByScore(ctx, s.nodeMonitor.key(clusterID, uid), &redis.ZRangeBy{Min: strconv.FormatInt(result.From.UnixMilli(), 10), Max: strconv.FormatInt(now.UnixMilli(), 10), Count: 4321}).Result()
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrNodeHistoryUnavailable, err)
	}
	for _, value := range values {
		var point NodeSample
		if err := json.Unmarshal([]byte(value), &point); err != nil {
			return result, fmt.Errorf("%w: %w", ErrNodeHistoryUnavailable, err)
		}
		if !workloads {
			point.Pods = nil
			point.Allocation = nil
			point.PodsState = "no_permission"
		}
		result.Points = append(result.Points, point)
	}
	return result, nil
}
