package kube

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
)

type deploymentScopeFixture struct {
	mu          sync.Mutex
	current     map[string]*appsv1.Deployment
	requests    map[string]int
	patches     int
	afterGet    func()
	beforePatch func()
}

func newDeploymentScopeFixture(t *testing.T) (*Service, string, *identityTestPolicies, *deploymentScopeFixture) {
	t.Helper()
	fixture := &deploymentScopeFixture{current: map[string]*appsv1.Deployment{}, requests: map[string]int{}}
	for _, namespace := range []string{"a", "b", "c"} {
		replicas := int32(3)
		fixture.current[namespace] = &appsv1.Deployment{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: namespace, UID: types.UID("uid-" + namespace), ResourceVersion: "1", Generation: 1},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "demo"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "demo"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "demo", Image: "registry.example.test/private:v1"}}},
				},
			},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3, AvailableReplicas: 3},
		}
	}
	server := identityResourceTestServer(t, func(w http.ResponseWriter, request *http.Request) {
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if len(parts) != 7 || parts[0] != "apis" || parts[1] != "apps" || parts[3] != "namespaces" || parts[5] != "deployments" || parts[6] != "demo" {
			t.Errorf("unexpected deployment API: %s", request.URL.Path)
			http.NotFound(w, request)
			return
		}
		if request.Header.Get("Impersonate-User") != "edo:project:alice:version-1" {
			t.Error("deployment API used a different principal or management credentials")
		}
		namespace := parts[4]
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.requests[namespace]++
		current := fixture.current[namespace]
		if current == nil {
			http.NotFound(w, request)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(current)
			if fixture.afterGet != nil {
				fixture.afterGet()
			}
		case http.MethodPatch:
			if fixture.beforePatch != nil {
				fixture.beforePatch()
			}
			patch, err := io.ReadAll(request.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var object map[string]json.RawMessage
			if json.Unmarshal(patch, &object) != nil || len(object) != 2 || object["metadata"] == nil || object["spec"] == nil {
				t.Errorf("control operation sent more than metadata/spec: %s", patch)
			}
			var metadata struct {
				UID             string                     `json:"uid"`
				ResourceVersion string                     `json:"resourceVersion"`
				Annotations     map[string]json.RawMessage `json:"annotations"`
			}
			_ = json.Unmarshal(object["metadata"], &metadata)
			if metadata.UID != string(current.UID) || metadata.ResourceVersion != current.ResourceVersion {
				t.Errorf("control patch lost object identity/concurrency: %s", patch)
			}
			for name := range metadata.Annotations {
				if name != stoppedReplicasAnnotation {
					t.Errorf("control patch changed an unrelated annotation: %s", name)
				}
			}
			var spec map[string]json.RawMessage
			_ = json.Unmarshal(object["spec"], &spec)
			for name := range spec {
				if name != "replicas" && name != "template" {
					t.Errorf("control patch changed an unrelated spec: %s", name)
				}
			}
			if template := spec["template"]; template != nil && strings.Contains(string(template), `"spec"`) {
				t.Errorf("control patch sent a container/image/pod spec: %s", patch)
			}
			encoded, _ := json.Marshal(current)
			merged, err := jsonpatch.MergePatch(encoded, patch)
			next := &appsv1.Deployment{}
			if err != nil || json.Unmarshal(merged, next) != nil {
				t.Errorf("invalid native merge patch: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fixture.patches++
			fixture.updateStatus(current, next)
			fixture.current[namespace] = next
			_ = json.NewEncoder(w).Encode(next)
		case http.MethodPut:
			body, _ := io.ReadAll(request.Body)
			decoded, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, nil)
			next, ok := decoded.(*appsv1.Deployment)
			if err != nil || !ok {
				t.Errorf("invalid official deployment update encoding: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fixture.updateStatus(current, next)
			fixture.current[namespace] = next
			_ = json.NewEncoder(w).Encode(next)
		default:
			t.Errorf("unexpected deployment method: %s", request.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"a": {"kubernetes.workload.execute"}, "b": {"kubernetes.workload.read", "kubernetes.workload.update"}, "c": {"kubernetes.workload.read"}})
	service.authorizationResolver = policies.resolve
	return service, id, policies, fixture
}

func (fixture *deploymentScopeFixture) updateStatus(previous, next *appsv1.Deployment) {
	next.Generation = previous.Generation + 1
	next.ResourceVersion = strconv.FormatInt(next.Generation, 10)
	desired := int32(1)
	if next.Spec.Replicas != nil {
		desired = *next.Spec.Replicas
	}
	next.Status = appsv1.DeploymentStatus{ObservedGeneration: next.Generation, Replicas: desired, UpdatedReplicas: desired, ReadyReplicas: desired, AvailableReplicas: desired}
}

func TestRBACDeploymentOperationsRequireTheirExactNamespaceCode(t *testing.T) {
	service, id, _, fixture := newDeploymentScopeFixture(t)
	ctx := identityTestContext(t, service, id, "alice")
	if _, err := service.DeploymentRuntimeState(ctx, id, "a", "demo"); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("execute's native get leaked runtime state: %v", err)
	}
	if identity, err := service.DeploymentImageIdentity(ctx, id, "a", "demo", "demo"); !errors.Is(err, ErrRBACForbidden) || identity.Image != "" {
		t.Fatalf("execute's native get leaked a deployment image: %+v %v", identity, err)
	}
	if previous, err := service.DeployImage(ctx, id, "a", "demo", "demo", "new:v2", "scope-test", time.Second); !errors.Is(err, ErrRBACForbidden) || previous != "" {
		t.Fatalf("an update grant from B authorized a release in A: %q %v", previous, err)
	}
	if previous, err := service.DeployImageIfCurrent(ctx, id, "a", "demo", "demo", "new:v2", "scope-test", time.Second, DeploymentImageIdentity{UID: "uid-a"}); !errors.Is(err, ErrRBACForbidden) || previous != "" {
		t.Fatalf("conditional release bypassed its namespace update scope: %q %v", previous, err)
	}
	for _, namespace := range []string{"b", "c"} {
		if _, err := service.ControlDeployment(ctx, id, namespace, "demo", "scale", 1, 30*time.Second); !errors.Is(err, ErrRBACForbidden) {
			t.Fatalf("an execute grant from A authorized control in %s: %v", namespace, err)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.requests) != 0 {
		t.Fatalf("semantic scope denials reached the native API: %+v", fixture.requests)
	}
}

func TestRBACExecuteOnlyDeploymentControlUsesNarrowNativePatches(t *testing.T) {
	service, id, _, fixture := newDeploymentScopeFixture(t)
	ctx := identityTestContext(t, service, id, "alice")
	for _, operation := range []struct {
		action   string
		replicas int32
		expected int32
	}{{"stop", 0, 0}, {"restart", 0, 3}, {"scale", 5, 5}} {
		result, err := service.ControlDeployment(ctx, id, "a", "demo", operation.action, operation.replicas, 30*time.Second)
		if err != nil || result.Replicas != operation.expected {
			t.Fatalf("execute-only %s was blocked by a missing generic update/read grant: %+v %v", operation.action, result, err)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.patches != 3 || fixture.current["a"].Spec.Template.Spec.Containers[0].Image != "registry.example.test/private:v1" || fixture.current["a"].Spec.Template.Annotations[restartedAtAnnotation] == "" {
		t.Fatalf("narrow control changed a container or missed an action: patches=%d deployment=%+v", fixture.patches, fixture.current["a"])
	}
}

func TestRBACDeploymentUpdateAndReadRemainAvailableInTheirOwnNamespace(t *testing.T) {
	service, id, _, _ := newDeploymentScopeFixture(t)
	ctx := identityTestContext(t, service, id, "alice")
	identity, err := service.DeploymentImageIdentity(ctx, id, "b", "demo", "demo")
	if err != nil || identity.UID != "uid-b" {
		t.Fatalf("the namespace read grant was not honored: %+v %v", identity, err)
	}
	previous, err := service.DeployImageIfCurrent(ctx, id, "b", "demo", "demo", "registry.example.test/private:v2", "scope-test", time.Second, identity)
	if err != nil || previous != identity.Image {
		t.Fatalf("the namespace update grant was not honored: previous=%q err=%v", previous, err)
	}
	result, err := service.DeploymentRuntimeState(ctx, id, "b", "demo")
	if err != nil || !result.Running {
		t.Fatalf("the namespace runtime read grant was not honored: %+v %v", result, err)
	}
}

func TestRBACDeploymentControlRejectsPolicyChangeBeforeItsPatch(t *testing.T) {
	service, id, policies, fixture := newDeploymentScopeFixture(t)
	fixture.afterGet = func() {
		policies.mu.Lock()
		defer policies.mu.Unlock()
		policies.revoked["alice"] = true
	}
	ctx := identityTestContext(t, service, id, "alice")
	_, err := service.ControlDeployment(ctx, id, "a", "demo", "scale", 5, 30*time.Second)
	if !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("revocation between the dependency read and action patch was lost: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.patches != 0 || fixture.current["a"].Spec.Replicas == nil || *fixture.current["a"].Spec.Replicas != 3 {
		t.Fatal(fmt.Sprintf("revoked actor still changed replicas: patches=%d", fixture.patches))
	}
}

func TestRBACDeploymentUpdateWithoutReadDoesNotReturnItsPreviousImage(t *testing.T) {
	service, id, policies, fixture := newDeploymentScopeFixture(t)
	policies.grant("alice", map[string][]string{"b": {"kubernetes.workload.update"}})
	ctx := identityTestContext(t, service, id, "alice")
	previous, err := service.DeployImage(ctx, id, "b", "demo", "demo", "registry.example.test/private:v2", "scope-test", time.Second)
	if err != nil || previous != "" {
		t.Fatalf("write-only image update exposed its dependency read or was blocked: previous=%q err=%v", previous, err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.current["b"].Spec.Template.Spec.Containers[0].Image != "registry.example.test/private:v2" {
		t.Fatal("redacting the public result changed the actual release")
	}
}
