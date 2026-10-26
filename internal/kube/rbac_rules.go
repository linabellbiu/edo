package kube

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	"edo/internal/access"
)

// RBACGrantPermissionCodes is a closed mapping; RBAC administration itself is
// deliberately never translated into downstream bind/escalate/impersonate rules.
func RBACGrantPermissionCodes() []string {
	result := []string{}
	for _, permission := range access.Catalog() {
		if strings.HasPrefix(permission.Code, "kubernetes.") && !strings.HasPrefix(permission.Code, "kubernetes.rbac.") {
			result = append(result, permission.Code)
		}
	}
	sort.Strings(result)
	return result
}

// RBACPermissionScopes lets the UI use the exact same resource mapping as the
// backend, including read/delete-only cluster storage resources.
func RBACPermissionScopes(code string) []string {
	result := []string{}
	if len(rbacRules(map[string]bool{code: true}, "namespace-preview", false)) > 0 {
		result = append(result, "namespace")
	}
	if len(rbacRules(map[string]bool{code: true}, "", true)) > 0 {
		result = append(result, "cluster")
	}
	return result
}

func normalizeRBACGrant(input RBACGrantInput) (RBACGrantInput, error) {
	input.RoleID, input.Namespace = strings.TrimSpace(input.RoleID), strings.TrimSpace(input.Namespace)
	if input.RoleID == "" || len(input.RoleID) > 36 || input.ClusterScope == (input.Namespace != "") || (!input.ClusterScope && len(validation.IsDNS1123Label(input.Namespace)) > 0) || len(input.Permissions) == 0 || len(input.Permissions) > 64 {
		return input, ErrRBACInvalid
	}
	allowed := map[string]bool{}
	for _, code := range RBACGrantPermissionCodes() {
		allowed[code] = true
	}
	seen := map[string]bool{}
	permissions := []string{}
	for _, code := range input.Permissions {
		if !allowed[code] {
			return input, ErrRBACInvalid
		}
		if len(rbacRules(map[string]bool{code: true}, input.Namespace, input.ClusterScope)) == 0 {
			return input, ErrRBACInvalid
		}
		if !seen[code] {
			permissions = append(permissions, code)
			seen[code] = true
		}
	}
	sort.Strings(permissions)
	input.Permissions = permissions
	if len(rbacRules(permissionSet(permissions), input.Namespace, input.ClusterScope)) == 0 {
		return input, ErrRBACInvalid
	}
	return input, nil
}

func permissionSet(codes []string) map[string]bool {
	result := make(map[string]bool, len(codes))
	for _, code := range codes {
		result[code] = true
	}
	return result
}

func rbacRules(permissions map[string]bool, namespace string, clusterScope bool) []rbacv1.PolicyRule {
	result := []rbacv1.PolicyRule{}
	add := func(group, resource string, verbs ...string) {
		if len(verbs) == 0 {
			return
		}
		result = append(result, rbacv1.PolicyRule{APIGroups: []string{group}, Resources: []string{resource}, Verbs: verbs})
	}
	for _, def := range ResourceDefinitions() {
		if def.Namespaced == clusterScope || def.Resource == "secrets" {
			continue
		}
		group, _ := schema.ParseGroupVersion(def.APIVersion)
		verbs := []string{}
		if permissions[def.Permission+".read"] {
			verbs = append(verbs, "get", "list", "watch")
		}
		if permissions[def.Permission+".create"] && def.Create {
			verbs = append(verbs, "create")
		}
		if def.Resource == "nodes" && permissions["kubernetes.node.create"] {
			verbs = append(verbs, "get", "list")
		}
		if permissions[def.Permission+".update"] && def.Update {
			verbs = append(verbs, "update", "patch")
		}
		if permissions[def.Permission+".delete"] && (def.Delete || def.Resource == "nodes") {
			verbs = append(verbs, "delete")
		}
		add(group.Group, def.Resource, verbs...)
	}
	if !clusterScope {
		if permissions["kubernetes.network.read"] {
			// Service diagnostics lists its EndpointSlices using a service-name
			// selector. No Pod read permission is needed for those addresses.
			add("discovery.k8s.io", "endpointslices", "list")
		}
		if permissions["kubernetes.workload.read"] {
			add("", "pods/log", "get")
			add("", "events", "get", "list", "watch")
			add("events.k8s.io", "events", "get", "list", "watch")
			add("apps", "replicasets", "get", "list", "watch")
			add("apps", "controllerrevisions", "get", "list", "watch")
			add("metrics.k8s.io", "pods", "get", "list")
		}
		if permissions["kubernetes.workload.execute"] {
			add("", "pods/exec", "create", "get")
			add("apps", "deployments/scale", "get", "update", "patch")
			add("apps", "statefulsets/scale", "get", "update", "patch")
			// Restart updates the reviewed workload template.
			add("apps", "deployments", "get", "patch")
			add("apps", "statefulsets", "get", "patch")
			add("apps", "daemonsets", "get", "patch")
			add("batch", "cronjobs", "get", "patch")
		}
		if permissions["kubernetes.secret.read"] {
			verbs := []string{"get", "list", "watch"}
			for _, action := range []string{"create", "update", "delete"} {
				if permissions["kubernetes.config."+action] {
					verbs = append(verbs, action)
					if action == "update" {
						verbs = append(verbs, "patch")
					}
				}
			}
			add("", "secrets", verbs...)
		}
	} else if permissions["kubernetes.node.read"] {
		add("metrics.k8s.io", "nodes", "get", "list")
	}
	if clusterScope && (permissions["kubernetes.node.read"] || permissions["kubernetes.node.create"]) {
		result = append(result, rbacv1.PolicyRule{NonResourceURLs: []string{"/version", "/readyz"}, Verbs: []string{"get"}})
	}
	for index := range result {
		sort.Strings(result[index].Verbs)
		result[index].Verbs = slices.Compact(result[index].Verbs)
	}
	sort.Slice(result, func(i, j int) bool {
		left, _ := json.Marshal(result[i])
		right, _ := json.Marshal(result[j])
		return string(left) < string(right)
	})
	return result
}
