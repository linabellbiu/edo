package kube

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func operationTestDeployment(namespace string) map[string]any {
	return map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": namespace, "uid": "deployment-uid", "resourceVersion": "1"}, "spec": map[string]any{"replicas": int64(2), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "private-image", "env": []any{map[string]any{"name": "TOKEN", "value": "do-not-leak"}}}}}}}}
}

func assertOperationIdentityOnly(t *testing.T, result map[string]any) {
	t.Helper()
	if len(result) != 3 || result["apiVersion"] == nil || result["kind"] == nil {
		t.Fatalf("write-only response returned fields beyond identity: %+v", result)
	}
	metadata, ok := result["metadata"].(map[string]any)
	if !ok || len(metadata) != 4 || metadata["name"] == nil || metadata["namespace"] == nil || metadata["uid"] == nil || metadata["resourceVersion"] == nil {
		t.Fatalf("write-only response returned labels, annotations, or other metadata: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "do-not-leak") || strings.Contains(string(encoded), "private-image") {
		t.Fatal("the API's mutation response leaked a derived read")
	}
}

func TestRBACResourceActionsDoNotAuthorizeGenericReadsOrUpdates(t *testing.T) {
	for _, readable := range []bool{false, true} {
		t.Run(fmt.Sprintf("executeRead=%t", readable), func(t *testing.T) {
			var calls, patches atomic.Int32
			server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" {
					t.Error("resource operation used a different native identity")
				}
				namespace := "a"
				if strings.Contains(request.URL.Path, "/namespaces/b/") {
					namespace = "b"
				}
				result := operationTestDeployment(namespace)
				if request.Method == http.MethodPatch {
					var patch map[string]any
					if err := json.NewDecoder(request.Body).Decode(&patch); err != nil {
						t.Error(err)
					}
					if namespace == "a" {
						spec, _ := patch["spec"].(map[string]any)
						for key := range spec {
							if key != "replicas" && key != "template" {
								t.Errorf("execute changed an arbitrary workload field: %s", key)
							}
						}
					}
					result["metadata"].(map[string]any)["resourceVersion"] = strconv.Itoa(int(patches.Add(1) + 1))
				}
				_ = json.NewEncoder(w).Encode(result)
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			policies := newIdentityTestPolicies()
			codes := []string{"kubernetes.workload.execute"}
			if readable {
				codes = append(codes, "kubernetes.workload.read")
			}
			policies.grant("alice", map[string][]string{"a": codes, "b": {"kubernetes.workload.update", "kubernetes.workload.read"}})
			service.authorizationResolver = policies.resolve
			ctx := identityTestContext(t, service, id, "alice")
			object := operationTestDeployment("a")
			for name, operation := range map[string]func() error{
				"save": func() error {
					_, err := service.SaveResource(ctx, id, "deployments", "a", "web", object, false)
					return err
				},
				"validate": func() error { return service.ValidateResource(ctx, id, "deployments", "a", "web", object) },
				"delete": func() error {
					return service.DeleteResource(ctx, id, "deployments", "a", "web", ResourceIdentity{UID: "deployment-uid", ResourceVersion: "1"})
				},
				"cross namespace execute": func() error { return service.RequireResourcePermission(ctx, id, "pods", "b", "execute", false) },
			} {
				before := calls.Load()
				if err := operation(); !errors.Is(err, ErrRBACForbidden) || calls.Load() != before {
					t.Fatalf("%s borrowed execute's native patch or another namespace's scope: err=%v native=%d", name, err, calls.Load()-before)
				}
			}
			if !readable {
				for name, operation := range map[string]func() error{
					"get":  func() error { _, err := service.GetResource(ctx, id, "deployments", "a", "web", false); return err },
					"list": func() error { _, err := service.ListResources(ctx, id, "deployments", "a", ""); return err },
					"events": func() error {
						_, err := service.ResourceEvents(ctx, id, "deployments", "a", "web", "deployment-uid")
						return err
					},
					"diagnostics": func() error {
						_, err := service.ResourceDiagnostics(ctx, id, "deployments", "a", "web", "deployment-uid")
						return err
					},
					"logs": func() error { _, err := service.PodLogs(ctx, id, "a", "web", "app", 10, false, false); return err },
				} {
					before := calls.Load()
					if err := operation(); !errors.Is(err, ErrRBACForbidden) || calls.Load() != before {
						t.Fatalf("%s borrowed execute's derived native get: err=%v native=%d", name, err, calls.Load()-before)
					}
				}
			}
			intentContext, _, err := service.authorizeResourceOperation(ctx, id, "deployments", "a", "execute", false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.SaveResource(intentContext, id, "deployments", "a", "web", object, false); !errors.Is(err, ErrRBACForbidden) {
				t.Fatalf("public Save inherited a private action intent: %v", err)
			}
			identity := ResourceIdentity{UID: "deployment-uid", ResourceVersion: "1"}
			for _, action := range []string{"restart", "scale"} {
				result, err := service.ResourceAction(ctx, id, "deployments", "a", "web", ResourceAction{ResourceIdentity: identity, Action: action, Replicas: 3})
				if err != nil {
					t.Fatalf("legitimate execute-only %s was rejected: %v", action, err)
				}
				if !readable {
					assertOperationIdentityOnly(t, result)
				} else if result["spec"] == nil {
					t.Fatal("an explicit same-namespace read grant lost the resource response")
				}
				identity.ResourceVersion = result["metadata"].(map[string]any)["resourceVersion"].(string)
			}
			if patches.Load() != 2 {
				t.Fatalf("generic writes reached native API or a legitimate action was lost: %d", patches.Load())
			}
			if _, err := service.SaveResource(ctx, id, "deployments", "b", "web", operationTestDeployment("b"), false); err != nil {
				t.Fatalf("the separate namespace's legitimate update was rejected: %v", err)
			}
		})
	}
}

func TestRBACReadUnionAndPrivateIntentKeepExactScope(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path != "/apis/apps/v1/namespaces/b/deployments" {
			t.Errorf("read union reached an execute-only or cluster-wide collection: %s", request.URL.Path)
		}
		_, _ = io.WriteString(w, `{"apiVersion":"apps/v1","kind":"DeploymentList","items":[{"metadata":{"name":"only-b","namespace":"b"}}]}`)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"a": {"kubernetes.workload.execute"}, "b": {"kubernetes.workload.update", "kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, service, id, "alice")
	result, err := service.ListResources(ctx, id, "deployments", "*", "")
	if err != nil || len(result.Items) != 1 || result.Items[0]["metadata"].(map[string]any)["namespace"] != "b" || calls.Load() != 1 {
		t.Fatalf("all-namespace read included a derived execute read: %+v %v", result, err)
	}
	intentContext, _, err := service.authorizeResourceOperation(ctx, id, "deployments", "b", "update", false)
	if err != nil {
		t.Fatal(err)
	}
	policies.mu.Lock()
	policies.policies["alice"].Permissions["b"] = []string{"kubernetes.workload.execute"}
	policies.policies["alice"].Version = "version-2"
	policies.policies["alice"].Principal = "edo:project:alice:version-2"
	policies.mu.Unlock()
	// A fresh connection now has native patch as an execute dependency. It must
	// still honor the original generic update intent before sending any data.
	client, _, err := service.resourceClient(intentContext, id, "deployments", "b", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Patch(intentContext, "web", types.MergePatchType, []byte(`{"spec":{"replicas":0}}`), metav1.PatchOptions{})
	if !errors.Is(err, ErrRBACForbidden) || calls.Load() != 1 {
		t.Fatalf("a newer execute-only principal borrowed the earlier update intent: %v calls=%d", err, calls.Load())
	}
}

func TestRBACWriteOnlyResponsesAndNamespaceSelectorCannotRevealResources(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		switch request.URL.Path {
		case "/apis/apps/v1/namespaces/b/deployments/web":
			_ = json.NewEncoder(w).Encode(operationTestDeployment("b"))
		case "/api/v1/namespaces":
			if request.Method != http.MethodPost {
				t.Error("a namespace selector sent a global native list")
			}
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"new-space","uid":"namespace-uid","resourceVersion":"1","annotations":{"private":"do-not-leak"}},"spec":{"finalizers":["kubernetes"]}}`)
		default:
			t.Errorf("a read without its precise code reached native API: %s", request.URL.Path)
			http.NotFound(w, request)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("writer", map[string][]string{"": {"kubernetes.config.create"}, "a": {"kubernetes.workload.execute", "kubernetes.config.read"}, "b": {"kubernetes.workload.update"}})
	policies.grant("node-reader", map[string][]string{"": {"kubernetes.node.read"}})
	service.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, service, id, "writer")
	result, err := service.SaveResource(ctx, id, "deployments", "b", "web", operationTestDeployment("b"), false)
	if err != nil {
		t.Fatal(err)
	}
	assertOperationIdentityOnly(t, result)
	result, err = service.SaveResource(ctx, id, "namespaces", "", "new-space", map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "new-space", "namespace": ""}}, true)
	if err != nil {
		t.Fatal(err)
	}
	assertOperationIdentityOnly(t, result)
	before := calls.Load()
	if _, err := service.GetResource(ctx, id, "namespaces", "", "new-space", false); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("namespace creation granted a read outside named grants: %v", err)
	}
	if _, err := service.GetResource(ctx, id, "secrets", "a", "private", true); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("config.read granted Secret contents: %v", err)
	}
	namespaces, err := service.Namespaces(identityTestContext(t, service, id, "node-reader"), id)
	if err != nil || len(namespaces) != 0 || calls.Load() != before {
		t.Fatalf("empty namespace selection broadened native access: %v %v calls=%d", namespaces, err, calls.Load()-before)
	}
}
