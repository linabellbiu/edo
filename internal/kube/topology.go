package kube

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"edo/internal/hostidentity"
	"edo/internal/model"
)

var ErrTopologyIncomplete = errors.New("无法读取完整节点拓扑，请稍后重试")

type TopologyResource struct {
	Capacity    *float64 `json:"capacity"`
	Allocatable *float64 `json:"allocatable"`
	Used        *float64 `json:"used"`
}

type TopologyPods struct {
	Count    *int64 `json:"count"`
	Capacity *int64 `json:"capacity"`
	State    string `json:"state"`
}

type TopologyNode struct {
	Node             map[string]any   `json:"node"`
	Roles            []string         `json:"roles"`
	CPU              TopologyResource `json:"cpu"`
	Memory           TopologyResource `json:"memory"`
	Pods             TopologyPods     `json:"pods"`
	Hosts            []LinkedHost     `json:"hosts"`
	MetricsTimestamp *string          `json:"metrics_timestamp"`
}

type TopologyHost struct {
	LinkedHost
	NodeUIDs []string `json:"node_uids"`
	Roles    []string `json:"roles"`
	State    string   `json:"state"`
}

type NodeTopology struct {
	ClusterID    string         `json:"cluster_id"`
	Nodes        []TopologyNode `json:"nodes"`
	Hosts        []TopologyHost `json:"hosts"`
	HostsState   string         `json:"hosts_state"`
	MetricsState string         `json:"metrics_state"`
	CollectedAt  time.Time      `json:"collected_at"`
}

// NodeRoles follows the actual node-role labels. A control-plane node is not
// assumed to be a worker merely because it also runs Pods.
func NodeRoles(node *corev1.Node) []string {
	roles := []string{}
	if node == nil {
		return roles
	}
	_, control := node.Labels["node-role.kubernetes.io/control-plane"]
	_, master := node.Labels["node-role.kubernetes.io/master"]
	_, worker := node.Labels["node-role.kubernetes.io/worker"]
	if control || master {
		roles = append(roles, "control-plane")
	}
	if worker || len(roles) == 0 {
		roles = append(roles, "worker")
	}
	return roles
}

// NodeTopology reads cluster-wide nodes and their current resources. Pod counts
// and host identities are separate permission domains and remain null when the
// current user cannot read them.
func (s *Service) NodeTopology(ctx context.Context, clusterID string, allowed map[string]bool, includeHosts bool) (*NodeTopology, error) {
	if !allowed["kubernetes.node.read"] {
		return nil, ErrResourceForbidden
	}
	ctx, cancel := s.hostValidationContext(ctx)
	defer cancel()
	client, err := s.Clientset(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	nodes, err := listTopologyNodes(ctx, client)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result := &NodeTopology{ClusterID: clusterID, Nodes: []TopologyNode{}, HostsState: "no_permission", MetricsState: "unavailable", CollectedAt: now}
	for i := range nodes {
		node := &nodes[i]
		copy := node.DeepCopy()
		copy.APIVersion, copy.Kind = "v1", "Node"
		copy.ManagedFields = nil
		copy.Annotations = nil
		copy.Status.NodeInfo.BootID, copy.Status.NodeInfo.MachineID, copy.Status.NodeInfo.SystemUUID = "", "", ""
		object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(copy)
		if err != nil {
			return nil, ErrTopologyIncomplete
		}
		result.Nodes = append(result.Nodes, TopologyNode{
			Node: object, Roles: NodeRoles(node),
			CPU:    TopologyResource{Capacity: topologyResourceQuantity(node.Status.Capacity, corev1.ResourceCPU), Allocatable: topologyResourceQuantity(node.Status.Allocatable, corev1.ResourceCPU)},
			Memory: TopologyResource{Capacity: topologyResourceQuantity(node.Status.Capacity, corev1.ResourceMemory), Allocatable: topologyResourceQuantity(node.Status.Allocatable, corev1.ResourceMemory)},
			Pods:   TopologyPods{Capacity: topologyPodCapacity(node.Status.Allocatable), State: "no_permission"},
		})
	}
	if allowed["kubernetes.workload.read"] {
		counts, podErr := listTopologyPodCounts(ctx, client)
		for i := range result.Nodes {
			result.Nodes[i].Pods.State = "unavailable"
			if podErr == nil {
				count := counts[nodes[i].Name]
				result.Nodes[i].Pods.Count, result.Nodes[i].Pods.State = &count, "ok"
			}
		}
	}
	s.fillTopologyMetrics(ctx, clusterID, nodes, result)
	if includeHosts {
		s.fillTopologyHosts(ctx, clusterID, nodes, result)
	}
	return result, nil
}

func listTopologyNodes(ctx context.Context, client kubernetes.Interface) ([]corev1.Node, error) {
	nodes := []corev1.Node{}
	continuation := ""
	seen := map[string]bool{}
	for {
		page, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil {
			return nil, resourceError("nodes", err)
		}
		nodes = append(nodes, page.Items...)
		continuation = page.Continue
		if continuation == "" {
			break
		}
		if seen[continuation] {
			return nil, ErrTopologyIncomplete
		}
		seen[continuation] = true
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}

func listTopologyPodCounts(ctx context.Context, client kubernetes.Interface) (map[string]int64, error) {
	counts := map[string]int64{}
	continuation := ""
	seen := map[string]bool{}
	for {
		page, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil {
			return nil, err
		}
		for _, pod := range page.Items {
			if pod.Spec.NodeName != "" {
				counts[pod.Spec.NodeName]++
			}
		}
		continuation = page.Continue
		if continuation == "" {
			return counts, nil
		}
		if seen[continuation] {
			return nil, ErrTopologyIncomplete
		}
		seen[continuation] = true
	}
}

func topologyResourceQuantity(values corev1.ResourceList, key corev1.ResourceName) *float64 {
	value, found := values[key]
	if !found {
		return nil
	}
	amount := value.AsApproximateFloat64()
	if amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return nil
	}
	return &amount
}

func topologyPodCapacity(values corev1.ResourceList) *int64 {
	quantity, ok := values[corev1.ResourcePods]
	if !ok {
		return nil
	}
	capacity, exact := quantity.AsInt64()
	if !exact || capacity < 0 {
		return nil
	}
	return &capacity
}

func (s *Service) fillTopologyMetrics(ctx context.Context, clusterID string, nodes []corev1.Node, result *NodeTopology) {
	if len(nodes) == 0 {
		result.MetricsState = "ok"
		return
	}
	metrics, err := s.topologyMetrics(ctx, clusterID)
	if err != nil {
		return
	}
	indexes := make(map[string]int, len(nodes))
	for i := range nodes {
		indexes[nodes[i].Name] = i
	}
	complete := make(map[string]bool, len(nodes))
	now := time.Now().UTC()
	for _, item := range metrics.Items {
		index, exists := indexes[item.GetName()]
		if !exists {
			continue
		}
		if uid := item.GetUID(); uid != "" && uid != nodes[index].UID {
			continue
		}
		text, _, _ := unstructured.NestedString(item.Object, "timestamp")
		at, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || now.Sub(at) > 2*time.Minute || at.Sub(now) > time.Minute || at.Before(nodes[index].CreationTimestamp.Time) {
			continue
		}
		values, _, _ := unstructured.NestedMap(item.Object, "usage")
		if !validUsage(values) {
			continue
		}
		cpu, memory := quantity(values["cpu"]), quantity(values["memory"])
		result.Nodes[index].CPU.Used, result.Nodes[index].Memory.Used = &cpu, &memory
		stamp := at.UTC().Format(time.RFC3339Nano)
		result.Nodes[index].MetricsTimestamp = &stamp
		complete[item.GetName()] = true
	}
	result.MetricsState = "partial"
	if len(complete) == len(nodes) {
		result.MetricsState = "ok"
	}
}

// Topology reads live metrics independently of overview/watch display caches.
// A partial metrics page is unavailable rather than a set of fabricated zeros.
func (s *Service) topologyMetrics(ctx context.Context, clusterID string) (*unstructured.UnstructuredList, error) {
	config, err := s.RESTConfig(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	for _, version := range []string{"v1", "v1beta1"} {
		result := &unstructured.UnstructuredList{}
		continuation := ""
		seen := map[string]bool{}
		for {
			var page *unstructured.UnstructuredList
			page, err = client.Resource(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: version, Resource: "nodes"}).List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
			if err != nil {
				break
			}
			result.Items = append(result.Items, page.Items...)
			continuation = page.GetContinue()
			if continuation == "" {
				return result, nil
			}
			if seen[continuation] {
				return nil, ErrTopologyIncomplete
			}
			seen[continuation] = true
		}
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	}
	return nil, err
}

func (s *Service) fillTopologyHosts(ctx context.Context, clusterID string, nodes []corev1.Node, result *NodeTopology) {
	result.HostsState = "unavailable"
	ids, err := clusterLinkedHostIDs(s.db.WithContext(ctx), clusterID)
	if err != nil {
		return
	}
	var hosts []model.Host
	if len(ids) > 0 {
		if err := s.db.WithContext(ctx).Where("uuid IN ?", ids).Order("name ASC").Order("uuid ASC").Find(&hosts).Error; err != nil {
			return
		}
	}
	result.Hosts, result.HostsState = []TopologyHost{}, "ok"
	for i := range result.Nodes {
		result.Nodes[i].Hosts = []LinkedHost{}
	}
	type identityResult struct {
		value hostidentity.Identity
		err   error
	}
	identities := make([]identityResult, len(hosts))
	workers := make(chan struct{}, 4)
	var wait sync.WaitGroup
	for i := range hosts {
		if !hosts[i].IsActive || hosts[i].IsBuiltin || hosts[i].Mode != model.HostModeSSH {
			identities[i].err = hostidentity.ErrUnavailable
			continue
		}
		select {
		case workers <- struct{}{}:
		case <-ctx.Done():
			identities[i].err = ctx.Err()
			continue
		}
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			defer func() { <-workers }()
			if s.topologyHosts == nil {
				identities[index].value, identities[index].err = s.readHostIdentity(ctx, hosts[index])
			} else {
				identities[index].value, identities[index].err = s.topologyHosts.read(ctx, hosts[index], s.readHostIdentity)
			}
		}(i)
	}
	wait.Wait()
	for i, host := range hosts {
		item := TopologyHost{LinkedHost: LinkedHost{ID: host.ID, Name: host.Name, Address: host.Address, IsActive: host.IsActive}, NodeUIDs: []string{}, Roles: []string{}, State: "unmatched"}
		if identities[i].err != nil {
			item.State, result.HostsState = "unavailable", "partial"
		} else {
			for index := range nodes {
				if !nodeMatchesHost(topologyNodeIdentity(&nodes[index]), identities[i].value) {
					continue
				}
				item.State = "matched"
				item.NodeUIDs = append(item.NodeUIDs, string(nodes[index].UID))
				item.Roles = append(item.Roles, result.Nodes[index].Roles...)
				result.Nodes[index].Hosts = append(result.Nodes[index].Hosts, item.LinkedHost)
			}
			if item.State == "unmatched" {
				result.HostsState = "partial"
			}
		}
		sort.Strings(item.Roles)
		item.Roles = compactTopologyRoles(item.Roles)
		result.Hosts = append(result.Hosts, item)
	}
}

func topologyNodeIdentity(node *corev1.Node) nodeIdentity {
	result := nodeIdentity{machineID: normalizedIdentityID(node.Status.NodeInfo.MachineID), systemUUID: normalizedIdentityID(node.Status.NodeInfo.SystemUUID), bootID: normalizedIdentityID(node.Status.NodeInfo.BootID)}
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP || address.Type == corev1.NodeExternalIP {
			if ip, ok := identityIP(address.Address); ok {
				result.addresses = append(result.addresses, ip)
			}
		}
	}
	return result
}

func compactTopologyRoles(roles []string) []string {
	result := roles[:0]
	for _, role := range roles {
		if len(result) == 0 || result[len(result)-1] != role {
			result = append(result, role)
		}
	}
	return result
}

type topologyIdentityCacheEntry struct {
	digest   [32]byte
	identity hostidentity.Identity
	err      error
	expires  time.Time
}

type topologyHostIdentityCache struct {
	mu      sync.Mutex
	entries map[string]topologyIdentityCacheEntry
}

func newTopologyHostIdentityCache() *topologyHostIdentityCache {
	return &topologyHostIdentityCache{entries: map[string]topologyIdentityCacheEntry{}}
}

func (cache *topologyHostIdentityCache) read(ctx context.Context, host model.Host, reader hostIdentityReader) (hostidentity.Identity, error) {
	if err := ctx.Err(); err != nil {
		return hostidentity.Identity{}, err
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{host.ID, host.Address, strconv.Itoa(host.SSHPort), host.SSHUsername, string(host.SSHAuthType), host.SSHCredentialCiphertext, host.SSHHostKeyFingerprint, host.ProjectID, host.DepartmentID, host.UpdatedAt.UTC().Format(time.RFC3339Nano)}, "\x00")))
	now := time.Now()
	cache.mu.Lock()
	entry, found := cache.entries[host.ID]
	if found && entry.digest == digest && now.Before(entry.expires) {
		cache.mu.Unlock()
		return entry.identity, entry.err
	}
	cache.mu.Unlock()
	identity, err := reader(ctx, host)
	if ctx.Err() != nil {
		return hostidentity.Identity{}, ctx.Err()
	}
	ttl := 30 * time.Second
	if err != nil {
		ttl = 5 * time.Second
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) >= 512 {
		for id, value := range cache.entries {
			if !now.Before(value.expires) {
				delete(cache.entries, id)
			}
		}
		if len(cache.entries) >= 512 {
			for id := range cache.entries {
				delete(cache.entries, id)
				break
			}
		}
	}
	cache.entries[host.ID] = topologyIdentityCacheEntry{digest: digest, identity: identity, err: err, expires: time.Now().Add(ttl)}
	return identity, err
}
