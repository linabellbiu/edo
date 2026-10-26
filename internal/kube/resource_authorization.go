package kube

import (
	"context"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

// A native verb can serve different EDO actions (execute needs patch for a
// restart). This private intent preserves the reviewed action through those
// implementation dependencies without granting a public read/update action.
type resourceOperationIntent struct {
	clusterID string
	resource  string
	namespace string
	action    string
	all       bool
}

type resourceOperationIntentKey struct{}

func resourcePermissionCode(def ResourceDefinition, action string) string {
	if def.Resource == "secrets" && action == "read" {
		return "kubernetes.secret.read"
	}
	return def.Permission + "." + action
}

func (a *clusterAuthorization) permitsResourceOperation(def ResourceDefinition, namespace, action string, all bool) bool {
	if !a.enforced() {
		return true
	}
	code := resourcePermissionCode(def, action)
	if def.Resource == "namespaces" && action == "read" {
		if a.has("", code) {
			return true
		}
		if namespace == "" || namespace == "*" {
			return len(a.Namespaces) > 0
		}
		return a.permitsNamespace(namespace)
	}
	if !def.Namespaced {
		return a.has("", code)
	}
	if all && action == "read" && (namespace == "" || namespace == "*") {
		return len(a.readNamespaces(def)) > 0
	}
	return a.has(namespace, code)
}

// RequireResourcePermission checks the exact EDO operation and scope. Native
// HTTP requests still enforce their real RBAC verbs; this check adds no SSAR.
func (s *Service) RequireResourcePermission(ctx context.Context, clusterID, resource, namespace, action string, all bool) error {
	_, _, err := s.authorizeResourceOperation(ctx, clusterID, resource, namespace, action, all)
	return err
}

func (s *Service) authorizeResourceOperation(ctx context.Context, clusterID, resource, namespace, action string, all bool) (context.Context, *clusterQueries, error) {
	def, ok := ResourceType(resource)
	if !ok || (action != "read" && action != "create" && action != "update" && action != "delete" && action != "execute") {
		return ctx, nil, ErrInvalidResource
	}
	if def.Resource == "namespaces" && action == "read" {
		if namespace != "" && namespace != "*" && len(validation.IsDNS1123Label(namespace)) != 0 {
			return ctx, nil, ErrInvalidResource
		}
	} else if def.Namespaced {
		if namespace == "" || namespace == "*" {
			if !all || action != "read" {
				return ctx, nil, ErrInvalidResource
			}
		} else if len(validation.IsDNS1123Label(namespace)) != 0 {
			return ctx, nil, ErrInvalidResource
		}
	} else if namespace != "" && namespace != "*" {
		return ctx, nil, ErrInvalidResource
	}
	query, err := s.clusterQueries(ctx, clusterID)
	if err != nil {
		return ctx, nil, err
	}
	return authorizeResourceQuery(ctx, query, def, namespace, action, all)
}

func authorizeResourceQuery(ctx context.Context, query *clusterQueries, def ResourceDefinition, namespace, action string, all bool) (context.Context, *clusterQueries, error) {
	if !query.authorization.permitsResourceOperation(def, namespace, action, all) {
		return ctx, nil, ErrRBACForbidden
	}
	intent := resourceOperationIntent{clusterID: query.clusterID, resource: def.Resource, namespace: namespace, action: action, all: all}
	return context.WithValue(ctx, resourceOperationIntentKey{}, intent), query, nil
}

func (q *clusterQueries) permitsRequestOperation(request *http.Request) bool {
	intent, present := request.Context().Value(resourceOperationIntentKey{}).(resourceOperationIntent)
	if !present || !q.authorization.enforced() {
		return true
	}
	def, ok := ResourceType(intent.resource)
	if !ok || intent.clusterID != q.clusterID || !q.authorization.permitsResourceOperation(def, intent.namespace, intent.action, intent.all) {
		return false
	}
	if !def.Namespaced {
		return true
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	for index, part := range parts {
		if part != "namespaces" || index+2 >= len(parts) {
			continue
		}
		actual := parts[index+1]
		if intent.all && intent.action == "read" && (intent.namespace == "" || intent.namespace == "*") {
			return q.authorization.has(actual, resourcePermissionCode(def, "read"))
		}
		return actual == intent.namespace
	}
	return false
}

func mutationResourceView(authorization *clusterAuthorization, def ResourceDefinition, namespace string, item *unstructured.Unstructured) map[string]any {
	if def.Resource == "namespaces" {
		namespace = item.GetName()
	}
	if authorization.permitsResourceOperation(def, namespace, "read", false) {
		return resourceView(def.Resource, item, false)
	}
	return map[string]any{"apiVersion": def.APIVersion, "kind": def.Kind, "metadata": map[string]any{"name": item.GetName(), "namespace": item.GetNamespace(), "uid": string(item.GetUID()), "resourceVersion": item.GetResourceVersion()}}
}
