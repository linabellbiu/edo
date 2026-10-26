package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// At most 256 informer watches and 64 other distinct streaming operations can
// be reviewed concurrently across this service. Duplicate streams under one
// principal share a review, and ordinary requests never create review traffic.
const maxNativeAuthorizationChecks = maxResourceWatches + 64

type activeClusterAccess struct {
	attributes authorizationv1.ResourceAttributes
	references int
}

type clusterAuthorizationLeaseKey struct{}

type clusterAuthorizationLease struct {
	mu     sync.Mutex
	query  *clusterQueries
	checks map[string]func()
	closed bool
}

func (lease *clusterAuthorizationLease) track(attributes authorizationv1.ResourceAttributes) error {
	key := nativeAccessKey(attributes)
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return ErrRBACForbidden
	}
	if _, exists := lease.checks[key]; exists {
		return nil
	}
	release, err := lease.query.addNativeAccess(attributes)
	if err != nil {
		return err
	}
	lease.checks[key] = release
	return nil
}

func (lease *clusterAuthorizationLease) close() {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return
	}
	lease.closed = true
	for _, release := range lease.checks {
		release()
	}
	lease.checks = nil
	lease.query.mu.Lock()
	lease.query.activeLeases--
	lease.query.mu.Unlock()
}

func nativeAccessKey(attributes authorizationv1.ResourceAttributes) string {
	encoded, _ := json.Marshal(attributes)
	return string(encoded)
}

func (q *clusterQueries) addNativeAccess(attributes authorizationv1.ResourceAttributes) (func(), error) {
	key := nativeAccessKey(attributes)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx.Err() != nil {
		return nil, ErrRBACForbidden
	}
	entry := q.activeAccess[key]
	if entry == nil {
		select {
		case q.authorizationSlots <- struct{}{}:
		default:
			return nil, ErrResourceWatchUnavailable
		}
		entry = &activeClusterAccess{attributes: attributes}
		q.activeAccess[key] = entry
	}
	entry.references++
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			defer q.mu.Unlock()
			if current := q.activeAccess[key]; current == entry {
				entry.references--
				if entry.references == 0 {
					delete(q.activeAccess, key)
					<-q.authorizationSlots
				}
			}
		})
	}, nil
}

func (q *clusterQueries) trackRequest(request *http.Request) (func(), error) {
	q.mu.Lock()
	if q.ctx.Err() != nil {
		q.mu.Unlock()
		return nil, ErrRBACForbidden
	}
	q.activeRequests++
	q.mu.Unlock()
	var releaseNative func()
	if q.authorization.enforced() {
		if attributes, streaming := streamingAccessAttributes(request); streaming {
			if lease, ok := request.Context().Value(clusterAuthorizationLeaseKey{}).(*clusterAuthorizationLease); ok && lease.query.clusterID == q.clusterID && lease.query.version == q.version {
				if err := lease.track(attributes); err != nil {
					q.mu.Lock()
					q.activeRequests--
					q.mu.Unlock()
					return nil, err
				}
			} else {
				var err error
				releaseNative, err = q.addNativeAccess(attributes)
				if err != nil {
					q.mu.Lock()
					q.activeRequests--
					q.mu.Unlock()
					return nil, err
				}
			}
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if releaseNative != nil {
				releaseNative()
			}
			q.mu.Lock()
			q.activeRequests--
			q.mu.Unlock()
		})
	}, nil
}

func streamingAccessAttributes(request *http.Request) (authorizationv1.ResourceAttributes, bool) {
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	attributes := authorizationv1.ResourceAttributes{}
	var tail []string
	for index, part := range parts {
		if part == "api" && index+2 < len(parts) {
			attributes.Version, tail = parts[index+1], parts[index+2:]
			break
		}
		if part == "apis" && index+3 < len(parts) {
			attributes.Group, attributes.Version, tail = parts[index+1], parts[index+2], parts[index+3:]
			break
		}
	}
	if len(tail) == 0 {
		return attributes, false
	}
	if tail[0] == "namespaces" && len(tail) >= 3 {
		attributes.Namespace, tail = tail[1], tail[2:]
	}
	attributes.Resource = tail[0]
	if len(tail) > 1 {
		attributes.Name = tail[1]
	}
	if len(tail) > 2 {
		attributes.Subresource = tail[2]
	}
	if request.URL.Query().Get("watch") == "true" || request.URL.Query().Get("watch") == "1" {
		attributes.Verb = "watch"
		return attributes, true
	}
	switch attributes.Subresource {
	case "log", "exec", "attach", "portforward":
		attributes.Verb = "get"
		if request.Method == http.MethodPost {
			attributes.Verb = "create"
		}
		return attributes, true
	default:
		return attributes, false
	}
}

func (s *Service) reviewActiveClusterAccess(ctx context.Context, q *clusterQueries) error {
	if !q.authorization.enforced() {
		return nil
	}
	q.mu.Lock()
	checks := make([]authorizationv1.ResourceAttributes, 0, len(q.activeAccess))
	for _, entry := range q.activeAccess {
		checks = append(checks, entry.attributes)
	}
	q.mu.Unlock()
	if len(checks) == 0 {
		return nil
	}
	return reviewNativeAttributes(ctx, q, checks)
}

func (s *Service) reviewCachedAuthorization(ctx context.Context, q *clusterQueries, attributes []authorizationv1.ResourceAttributes) error {
	if !q.authorization.enforced() {
		return nil
	}
	if err := s.validateQueryAuthorization(ctx, q); err != nil {
		q.stop()
		return err
	}
	if err := reviewNativeAttributes(ctx, q, attributes); err != nil {
		q.stop()
		return err
	}
	return nil
}

// Derive capability checks from the same compiler as managed native Roles.
// In particular EDO node.create means the explicitly delegated bootstrap
// capability, whose native prerequisite is nodes list/get, not nodes:create.
func nativeCapabilityAttributes(permission, namespace string) ([]authorizationv1.ResourceAttributes, error) {
	rules := rbacRules(map[string]bool{permission: true}, namespace, namespace == "")
	checks := []authorizationv1.ResourceAttributes{}
	for _, definition := range ResourceDefinitions() {
		if definition.Namespaced != (namespace != "") || !strings.HasPrefix(permission, definition.Permission+".") && !(definition.Resource == "secrets" && permission == "kubernetes.secret.read") {
			continue
		}
		for _, rule := range rules {
			if !slices.Contains(rule.APIGroups, definition.gvr().Group) || !slices.Contains(rule.Resources, definition.Resource) {
				continue
			}
			for _, verb := range []string{"list", "get", "create", "patch", "update", "delete"} {
				if slices.Contains(rule.Verbs, verb) {
					checks = append(checks, authorizationv1.ResourceAttributes{Namespace: namespace, Group: definition.gvr().Group, Version: definition.gvr().Version, Resource: definition.Resource, Verb: verb})
					break
				}
			}
		}
	}
	if len(checks) == 0 {
		return nil, ErrRBACForbidden
	}
	return checks, nil
}

func reviewNativeAttributes(ctx context.Context, q *clusterQueries, checks []authorizationv1.ResourceAttributes) error {
	if !q.authorization.enforced() {
		return nil
	}
	if q.ctx.Err() != nil || q.reviewClient == nil {
		return ErrRBACForbidden
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(q.ctx, cancel)
	defer stop()
	var workers sync.WaitGroup
	slots := make(chan struct{}, 16)
	var first error
	var once sync.Once
	for _, attributes := range checks {
		workers.Add(1)
		go func(attributes authorizationv1.ResourceAttributes) {
			defer workers.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			result, err := q.reviewClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attributes}}, metav1.CreateOptions{})
			if err == nil && (!result.Status.Allowed || result.Status.Denied || result.Status.EvaluationError != "") {
				err = ErrRBACForbidden
			}
			if err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}(attributes)
	}
	workers.Wait()
	if first != nil {
		if ctx.Err() != nil && q.ctx.Err() == nil && errors.Is(first, context.Canceled) {
			return ctx.Err()
		}
		return ErrRBACForbidden
	}
	if ctx.Err() != nil || q.ctx.Err() != nil {
		return ErrRBACForbidden
	}
	return nil
}
