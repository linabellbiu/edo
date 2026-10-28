package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"edo/internal/database"
	"edo/internal/model"
)

// clusterAuthorization is a fresh policy decision. A successful decision never
// grants a wildcard namespace and never derives an identity from HTTP headers.
type clusterAuthorization struct {
	Enforced     bool
	CacheKey     string
	Version      string
	Principal    string
	Namespaces   []string
	ClusterScope bool
	Permissions  map[string][]string
}

type internalClusterAccessKey struct{}

// WithInternalClusterAccess is reserved for trusted control-plane work after
// its own operation-specific authorization. HTTP resource handlers must never
// call it. An absent user identity by itself does not authorize this access.
func WithInternalClusterAccess(ctx context.Context, purpose string) context.Context {
	switch purpose {
	case "node-monitor", "node-bootstrap", "node-bootstrap-cleanup", "cluster-validation":
		return context.WithValue(ctx, internalClusterAccessKey{}, purpose)
	default:
		return ctx
	}
}

func internalClusterAccess(ctx context.Context) bool {
	value, _ := ctx.Value(internalClusterAccessKey{}).(string)
	return value != ""
}

func (s *Service) resolveClusterAuthorization(ctx context.Context, cluster *model.KubernetesCluster, ensure bool) (*clusterAuthorization, error) {
	if internalClusterAccess(ctx) {
		return nil, nil
	}
	if s.authorizationResolver != nil {
		return s.authorizationResolver(ctx, cluster, ensure)
	}
	if ensure {
		return s.authorizationForCluster(ctx, cluster)
	}
	return s.authorizationCurrent(ctx, cluster)
}

func cloneClusterAuthorization(value *clusterAuthorization) *clusterAuthorization {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Namespaces = append([]string(nil), value.Namespaces...)
	copy.Permissions = make(map[string][]string, len(value.Permissions))
	for namespace, codes := range value.Permissions {
		copy.Permissions[namespace] = append([]string(nil), codes...)
	}
	return &copy
}

func authorizationFingerprint(value *clusterAuthorization) [32]byte {
	if value == nil || !value.Enforced {
		return [32]byte{}
	}
	canonical := cloneClusterAuthorization(value)
	slices.Sort(canonical.Namespaces)
	for _, codes := range canonical.Permissions {
		slices.Sort(codes)
	}
	encoded, _ := json.Marshal(canonical)
	return sha256.Sum256(encoded)
}

func authorizedQueryVersion(cluster *model.KubernetesCluster, authorization *clusterAuthorization, internal bool) [32]byte {
	base := clusterQueryVersion(cluster)
	if !authorization.enforced() && !internal {
		return base
	}
	identity := authorizationFingerprint(authorization)
	return sha256.Sum256([]byte(hex.EncodeToString(base[:]) + "\x00" + hex.EncodeToString(identity[:]) + "\x00" + stringBool(internal)))
}

func stringBool(value bool) string {
	if value {
		return "internal"
	}
	return "user"
}

func (s *Service) invalidateClusterActorQueries(ctx context.Context, cluster *model.KubernetesCluster) {
	if s.queries == nil {
		return
	}
	scope := identityScope(ctx, cluster)
	s.queries.mu.Lock()
	defer s.queries.mu.Unlock()
	for key, query := range s.queries.clusters {
		if query.clusterID == cluster.ID && query.scope.UserID == scope.UserID && query.scope.ProjectID == scope.ProjectID && !query.internal {
			query.stop()
			delete(s.queries.clusters, key)
		}
	}
}

func (a *clusterAuthorization) enforced() bool { return a != nil && a.Enforced }

func (a *clusterAuthorization) has(namespace, permission string) bool {
	return !a.enforced() || slices.Contains(a.Permissions[namespace], permission)
}

func (a *clusterAuthorization) readNamespaces(def ResourceDefinition) []string {
	if !a.enforced() {
		return nil
	}
	permission := def.Permission + ".read"
	if def.Resource == "secrets" {
		permission = "kubernetes.secret.read"
	}
	result := []string{}
	for _, namespace := range a.Namespaces {
		if a.has(namespace, permission) {
			result = append(result, namespace)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func (a *clusterAuthorization) permitsNamespace(namespace string) bool {
	return !a.enforced() || slices.Contains(a.Namespaces, namespace)
}

func identityScope(ctx context.Context, cluster *model.KubernetesCluster) database.DepartmentScope {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok {
		scope = database.DepartmentScope{DepartmentID: cluster.DepartmentID, ProjectID: cluster.ProjectID}
	}
	return scope
}

func (s *Service) validateQueryAuthorization(ctx context.Context, q *clusterQueries) error {
	if q.ctx.Err() != nil {
		return ErrRBACForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx = database.WithDepartmentScope(ctx, q.scope)
	if q.internal {
		ctx = WithInternalClusterAccess(ctx, "node-monitor")
	}
	cluster, err := s.Find(ctx, q.clusterID)
	if err != nil || !cluster.IsActive || clusterQueryVersion(cluster) != q.connectionVersion {
		return ErrRBACForbidden
	}
	current, err := s.resolveClusterAuthorization(ctx, cluster, false)
	if err != nil {
		return err
	}
	if authorizationFingerprint(current) != authorizationFingerprint(q.authorization) {
		return ErrRBACForbidden
	}
	return nil
}

// RequireClusterPermission is the capability fence for operations whose
// implementation needs a trusted control-plane identity (node bootstrap).
// It deliberately ignores an existing internal marker so callers cannot turn
// the marker itself into authorization to perform the operation.
func (s *Service) RequireClusterPermission(ctx context.Context, clusterID, namespace, permission string) error {
	ctx = context.WithValue(ctx, internalClusterAccessKey{}, "")
	query, err := s.clusterQueries(ctx, clusterID)
	if err != nil {
		return err
	}
	if !query.authorization.has(namespace, permission) {
		return ErrRBACForbidden
	}
	if query.authorization.enforced() {
		attributes, err := nativeCapabilityAttributes(permission, namespace)
		if err != nil {
			return err
		}
		return s.reviewCachedAuthorization(ctx, query, attributes)
	}
	return nil
}

// Every cached identity has a short, actively checked lease. This includes
// legacy clients so enabling enforcement also closes streams created earlier.
func (s *Service) watchQueryAuthorization(q *clusterQueries) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-ticker.C:
			q.mu.Lock()
			active := q.activeRequests > 0 || q.activeLeases > 0 || len(q.subscribers) > 0
			q.mu.Unlock()
			if !active {
				if q.ephemeral && time.Since(time.Unix(0, q.lastUsed.Load())) > 10*time.Second {
					q.stop()
					return
				}
				continue
			}
			q.lastUsed.Store(time.Now().UnixNano())
			ctx, cancel := context.WithTimeout(q.ctx, time.Second)
			err := s.validateQueryAuthorization(ctx, q)
			if err == nil {
				err = s.reviewActiveClusterAccess(ctx, q)
			}
			cancel()
			if err != nil {
				q.stop()
				return
			}
		}
	}
}

// WithClusterAuthorizationLease binds an upgraded connection (such as an exec
// terminal) to the same identity lease used by list, logs and watch requests.
func (s *Service) WithClusterAuthorizationLease(ctx context.Context, clusterID string) (context.Context, context.CancelFunc, error) {
	q, err := s.clusterQueries(ctx, clusterID)
	if err != nil {
		return nil, nil, err
	}
	lease, cancel := context.WithCancel(ctx)
	token := &clusterAuthorizationLease{query: q, checks: map[string]func(){}}
	q.mu.Lock()
	q.activeLeases++
	q.mu.Unlock()
	lease = context.WithValue(lease, clusterAuthorizationLeaseKey{}, token)
	stop := context.AfterFunc(q.ctx, cancel)
	stopLease := context.AfterFunc(lease, token.close)
	return lease, func() { stop(); cancel(); stopLease(); token.close() }, nil
}

type authorizationRoundTripper struct {
	service *Service
	query   *clusterQueries
	next    http.RoundTripper
}

func (t *authorizationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.service.validateQueryAuthorization(request.Context(), t.query); err != nil {
		t.query.stop()
		return nil, err
	}
	if !t.query.permitsRequestNamespace(request) || !t.query.permitsRequestOperation(request) {
		return nil, ErrRBACForbidden
	}
	release, err := t.query.trackRequest(request)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(request.Context())
	stop := context.AfterFunc(t.query.ctx, cancel)
	pinned := request.Clone(ctx)
	pinned.Header = request.Header.Clone()
	for name := range pinned.Header {
		if strings.HasPrefix(strings.ToLower(name), "impersonate-") {
			pinned.Header.Del(name)
		}
	}
	if t.query.authorization.enforced() {
		pinned.Header.Set("Impersonate-User", t.query.authorization.Principal)
	}
	response, err := t.next.RoundTrip(pinned)
	if err != nil {
		release()
		stop()
		cancel()
		// 保留调用方的取消原因，避免连接租约的取消掩盖请求超时。
		if contextErr := request.Context().Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	if response.Body == nil {
		release()
		stop()
		cancel()
		return response, nil
	}
	response.Body = &authorizationReadCloser{ReadCloser: response.Body, stop: stop, cancel: cancel, release: release}
	return response, nil
}

func (t *authorizationRoundTripper) WrappedRoundTripper() http.RoundTripper { return t.next }

type authorizationReadCloser struct {
	io.ReadCloser
	once    sync.Once
	stop    func() bool
	cancel  context.CancelFunc
	release func()
}

func (r *authorizationReadCloser) Close() error {
	r.once.Do(func() {
		r.stop()
		r.cancel()
		if r.release != nil {
			r.release()
		}
	})
	return r.ReadCloser.Close()
}

func (q *clusterQueries) permitsRequestNamespace(request *http.Request) bool {
	auth := q.authorization
	if !auth.enforced() {
		return true
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	for index, part := range parts {
		if part == "namespaces" {
			if index+1 < len(parts) {
				if index+2 == len(parts) {
					switch request.Method {
					case http.MethodGet, http.MethodHead:
						return auth.permitsNamespace(parts[index+1]) || auth.has("", "kubernetes.config.read")
					case http.MethodPatch, http.MethodPut:
						return auth.has("", "kubernetes.config.update")
					case http.MethodDelete:
						return auth.has("", "kubernetes.config.delete")
					default:
						return false
					}
				}
				return auth.permitsNamespace(parts[index+1])
			}
			if request.Method == http.MethodPost {
				return auth.has("", "kubernetes.config.create")
			}
			return auth.has("", "kubernetes.config.read")
		}
	}
	// Namespace-wide collection URLs omit /namespaces/<name>. Reject them at
	// the boundary even when a collector's credential can read the collection.
	resource := ""
	if len(parts) >= 3 && parts[0] == "api" {
		resource = parts[2]
	} else if len(parts) >= 4 && parts[0] == "apis" {
		resource = parts[3]
	}
	if definition, ok := ResourceType(resource); ok && definition.Namespaced {
		return false
	}
	return true
}
