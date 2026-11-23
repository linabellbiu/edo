package kube

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestNamespaceResourcesUseScopedNativeOperations(t *testing.T) {
	for _, target := range []struct {
		resource, kind, permission string
		config                     map[string]any
	}{
		{"limitranges", "LimitRange", "kubernetes.namespace_policy", map[string]any{"spec": map[string]any{"limits": []any{map[string]any{"type": "Container", "defaultRequest": map[string]any{"cpu": "200m"}}}}}},
		{"resourcequotas", "ResourceQuota", "kubernetes.namespace_policy", map[string]any{"spec": map[string]any{"hard": map[string]any{"requests.cpu": "2", "pods": "8"}}}},
		{"serviceaccounts", "ServiceAccount", "kubernetes.service_account", map[string]any{"automountServiceAccountToken": false, "imagePullSecrets": []any{map[string]any{"name": "registry"}}}},
	} {
		t.Run(target.resource, func(t *testing.T) {
			var reads, validations, creates, updates, deletes atomic.Int32
			permissions := []string{target.permission + ".read", target.permission + ".create", target.permission + ".update", target.permission + ".delete"}
			newObject := func() map[string]any {
				object := map[string]any{"apiVersion": "v1", "kind": target.kind, "metadata": map[string]any{"name": "fixture", "namespace": "default"}}
				for key, value := range target.config {
					object[key] = value
				}
				return object
			}
			service, id, ctx := mappedOperationService(t, permissions, func(w http.ResponseWriter, r *http.Request) {
				base := "/api/v1/namespaces/default/" + target.resource
				wantPath := base + "/fixture"
				if r.Method == http.MethodPost {
					wantPath = base
				}
				if r.URL.Path != wantPath || r.Header.Get("Impersonate-User") != "edo:project:operation-user:operation-policy" {
					t.Errorf("错误的原生资源路由或身份: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusForbidden)
					return
				}
				object := newObject()
				metadata := object["metadata"].(map[string]any)
				metadata["uid"], metadata["resourceVersion"] = "namespace-resource-uid", "7"
				if target.kind == "ServiceAccount" {
					object["secrets"] = []any{map[string]any{"name": "legacy-account-token-ref"}}
				}
				switch r.Method {
				case http.MethodGet:
					reads.Add(1)
				case http.MethodPost, http.MethodPatch:
					if r.URL.Query().Get("fieldValidation") != "Strict" {
						t.Error("未使用 Kubernetes 严格校验")
					}
					if r.URL.Query().Get("dryRun") == "All" {
						validations.Add(1)
					} else if r.Method == http.MethodPost {
						creates.Add(1)
					} else {
						updates.Add(1)
					}
				case http.MethodDelete:
					var options struct {
						Preconditions struct{ UID, ResourceVersion string }
					}
					if err := json.NewDecoder(r.Body).Decode(&options); err != nil || options.Preconditions.UID != "namespace-resource-uid" || options.Preconditions.ResourceVersion != "7" {
						t.Errorf("删除未使用固定的 UID 和版本: %v", err)
					}
					deletes.Add(1)
				}
				_ = json.NewEncoder(w).Encode(object)
			})
			identity := ResourceIdentity{UID: "namespace-resource-uid", ResourceVersion: "7"}
			object := newObject()
			if err := service.ValidateResourceCreate(ctx, id, target.resource, "default", "fixture", object); err != nil {
				t.Fatal(err)
			}
			if _, err := service.SaveResource(ctx, id, target.resource, "default", "fixture", object, true); err != nil {
				t.Fatal(err)
			}
			current, err := service.GetResource(ctx, id, target.resource, "default", "fixture", false)
			if err != nil || current["kind"] != target.kind || current["secrets"] != nil {
				t.Fatalf("普通读取返回错误对象或令牌引用: %v", err)
			}
			object["metadata"].(map[string]any)["uid"] = identity.UID
			object["metadata"].(map[string]any)["resourceVersion"] = identity.ResourceVersion
			if err := service.ValidateResource(ctx, id, target.resource, "default", "fixture", object); err != nil {
				t.Fatal(err)
			}
			if _, err := service.SaveResource(ctx, id, target.resource, "default", "fixture", object, false); err != nil {
				t.Fatal(err)
			}
			if err := service.DeleteResource(ctx, id, target.resource, "other", "fixture", identity); !errors.Is(err, ErrRBACForbidden) {
				t.Fatalf("未拒绝越出授权的命名空间: %v", err)
			}
			if target.kind == "ServiceAccount" {
				object["secrets"] = []any{map[string]any{"name": "forbidden-reference"}}
				if _, err := service.SaveResource(ctx, id, target.resource, "default", "fixture", object, false); !errors.Is(err, ErrInvalidResource) {
					t.Fatalf("允许了修改令牌引用: %v", err)
				}
			}
			if err := service.DeleteResource(ctx, id, target.resource, "default", "fixture", identity); err != nil {
				t.Fatal(err)
			}
			if reads.Load() != 1 || validations.Load() != 2 || creates.Load() != 1 || updates.Load() != 1 || deletes.Load() != 1 {
				t.Fatal("原生 API 的读取、校验或写入次数不正确")
			}
		})
	}
}

func TestNamespaceResourceGrantsDoNotInheritConfigurationOrTokenAccess(t *testing.T) {
	resources := []string{"limitranges", "resourcequotas", "serviceaccounts"}
	for _, resource := range resources {
		def, _ := ResourceType(resource)
		config := rbacRules(map[string]bool{"kubernetes.config.read": true, "kubernetes.config.update": true}, "default", false)
		if mappedRuleAllows(config, "", resource, "get") || mappedRuleAllows(config, "", resource, "patch") {
			t.Fatalf("配置权限扩大到了 %s", resource)
		}
		actions := map[string]bool{def.Permission + ".read": true, def.Permission + ".create": true, def.Permission + ".update": true, def.Permission + ".delete": true}
		rules := rbacRules(actions, "default", false)
		if !mappedRuleAllows(rules, "", resource, "list") || !mappedRuleAllows(rules, "", resource, "patch") || len(rbacRules(actions, "", true)) != 0 {
			t.Fatalf("%s 缺少命名空间授权或扩大到了集群", resource)
		}
		if mappedRuleAllows(rules, "", "secrets", "get") || mappedRuleAllows(rules, "", "serviceaccounts/token", "create") {
			t.Fatalf("%s 扩大到了凭据访问", resource)
		}
	}
	def, _ := ResourceType("namespaces")
	if canRead := (&clusterAuthorization{Enforced: true, Permissions: map[string][]string{"": {"kubernetes.config.read"}}}).permitsResourceOperation(def, "", "read", true); !canRead {
		t.Fatal("命名空间迁移丢失了既有配置授权")
	}
}
