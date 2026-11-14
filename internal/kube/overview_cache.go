package kube

import (
	"context"
	"maps"
	"slices"
	"time"

	"edo/internal/database"
	authorizationv1 "k8s.io/api/authorization/v1"
)

const overviewCacheTTL = 5 * time.Second
const maxOverviewEntries = 64

// 总览仅缓存有界统计，不缓存授权决定；复用 client-go 的 LRU 和 singleflight。
func (s *Service) cachedOverview(ctx context.Context, query *clusterQueries, namespace string, allowed map[string]bool) (*Overview, error) {
	if query.ephemeral || query.overviews == nil {
		return s.collectOverview(ctx, query, namespace, allowed)
	}
	allowed = maps.Clone(allowed)
	codes := []string{}
	for code, enabled := range allowed {
		if enabled {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	scope, _ := database.DepartmentScopeFromContext(ctx)
	epoch := query.overviewEpoch.Load()
	fresh := freshResourceRead(ctx)
	if fresh {
		// 手动刷新不加入此前的旧查询，旧查询也不能随后回填当前缓存。
		epoch = query.overviewEpoch.Add(1)
		query.metricsEpoch.Add(1)
	}
	key := rbacHash([]any{scope, namespace, codes, epoch})
	if !fresh {
		if value, ok := query.overviews.Get(key); ok {
			return s.returnOverviewSnapshot(ctx, query, namespace, allowed, value.(*Overview))
		}
	}
	result := query.overviewRequests.DoChan(key, func() (any, error) {
		if !fresh {
			if value, ok := query.overviews.Get(key); ok {
				return value, nil
			}
		}
		// 共享采样不随第一个等待者离开而取消，但受集群连接和请求上限约束。
		timeout := s.config.RequestTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		request, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		stop := context.AfterFunc(query.ctx, cancel)
		defer stop()
		value, err := s.collectOverview(request, query, namespace, allowed)
		if err == nil && request.Err() == nil && query.overviewEpoch.Load() == epoch && cacheableOverview(value) {
			query.overviews.Add(key, value, overviewCacheTTL)
		}
		return value, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-result:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if value.Err != nil {
			return nil, value.Err
		}
		return s.returnOverviewSnapshot(ctx, query, namespace, allowed, value.Val.(*Overview))
	}
}

func cacheableOverview(value *Overview) bool {
	if value == nil || len(value.Errors) != 0 || value.MonitoringState != "" {
		return false
	}
	for _, count := range value.Counts {
		if count.State != "ok" {
			return false
		}
	}
	for _, state := range value.MetricsState {
		if state != "ok" {
			return false
		}
	}
	return true
}

func (s *Service) returnOverviewSnapshot(ctx context.Context, query *clusterQueries, namespace string, allowed map[string]bool, value *Overview) (*Overview, error) {
	if err := s.reviewCachedAuthorization(ctx, query, overviewAccessAttributes(query.authorization, namespace, allowed, value)); err != nil {
		return nil, err
	}
	// 数据库身份、项目、连接及权限版本在响应前再次核对，不以 TTL 延迟撤权。
	if err := s.validateQueryAuthorization(ctx, query); err != nil {
		query.stop()
		return nil, err
	}
	return cloneOverview(value), nil
}

func overviewAccessAttributes(authorization *clusterAuthorization, namespace string, allowed map[string]bool, value *Overview) []authorizationv1.ResourceAttributes {
	if !authorization.enforced() {
		return nil
	}
	checks := []authorizationv1.ResourceAttributes{}
	for _, def := range resourceDefinitions {
		count, present := value.Counts[def.Resource]
		if !allowed[def.Permission+".read"] || !present || count.State == "unavailable" {
			continue
		}
		base := authorizationv1.ResourceAttributes{Group: def.gvr().Group, Version: def.gvr().Version, Resource: def.Resource, Verb: "list"}
		if def.Resource == "namespaces" && !authorization.has("", "kubernetes.config.read") {
			base.Verb = "get"
			for _, name := range authorization.Namespaces {
				item := base
				item.Name = name
				checks = append(checks, item)
			}
		} else if def.Namespaced && namespace == "*" {
			for _, ns := range authorization.readNamespaces(def) {
				item := base
				item.Namespace = ns
				checks = append(checks, item)
			}
		} else {
			if def.Namespaced {
				base.Namespace = namespace
			}
			checks = append(checks, base)
		}
	}
	for _, kind := range []string{"nodes", "pods"} {
		def, _ := ResourceType(kind)
		if allowed[def.Permission+".read"] && value.MetricsState[kind] == "ok" {
			ns := namespace
			if ns == "*" {
				ns = ""
			}
			checks = append(checks, metricAccessAttributes(authorization, kind, ns)...)
		}
	}
	return checks
}

func cloneOverview(value *Overview) *Overview {
	copy := *value
	copy.Counts = maps.Clone(value.Counts)
	copy.Phases = maps.Clone(value.Phases)
	copy.MetricsState = maps.Clone(value.MetricsState)
	copy.Errors = maps.Clone(value.Errors)
	copy.Nodes = slices.Clone(value.Nodes)
	for i := range copy.Nodes {
		if pointer := copy.Nodes[i].Pods; pointer != nil {
			count := *pointer
			copy.Nodes[i].Pods = &count
		}
		if pointer := copy.Nodes[i].PodCapacity; pointer != nil {
			capacity := *pointer
			copy.Nodes[i].PodCapacity = &capacity
		}
	}
	copy.Usage = make(map[string]map[string]ResourceUsage, len(value.Usage))
	for kind, usage := range value.Usage {
		copy.Usage[kind] = maps.Clone(usage)
	}
	return &copy
}
