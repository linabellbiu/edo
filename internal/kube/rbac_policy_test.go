package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"edo/internal/database"
	"edo/internal/model"
)

type rbacRESTPeer struct {
	mu          sync.Mutex
	objects     map[string]map[string]any
	sequence    int
	writes      int
	fail        atomic.Bool
	impersonate atomic.Bool
	requests    atomic.Int32
}

func (p *rbacRESTPeer) writeCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.writes }

func decodeRBACRequest(r *http.Request, target any) error {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if json.Valid(data) {
		return json.Unmarshal(data, target)
	}
	object, _, err := scheme.Codecs.UniversalDeserializer().Decode(data, nil, nil)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func (p *rbacRESTPeer) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	p.requests.Add(1)
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/version" {
		_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5+k3s1"}`)
		return
	}
	if r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
		var review struct {
			Spec struct {
				ResourceAttributes struct{ Verb, Resource, Name string }
			}
		}
		if err := decodeRBACRequest(r, &review); err != nil {
			t.Errorf("invalid native review encoding: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if review.Spec.ResourceAttributes.Verb != "impersonate" || review.Spec.ResourceAttributes.Resource != "users" || !strings.HasPrefix(review.Spec.ResourceAttributes.Name, "edo:") {
			t.Errorf("unsafe impersonation probe: %+v", review)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "status": map[string]any{"allowed": p.impersonate.Load()}})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/apis/rbac.authorization.k8s.io/v1/") {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Impersonate-User") != "" {
		t.Error("management request used a business identity")
	}
	if p.fail.Load() {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden","message":"private-token=fixture"}`)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/apis/rbac.authorization.k8s.io/v1/"), "/")
	namespace, resource, name := "", parts[0], ""
	if resource == "namespaces" {
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		namespace, resource = parts[1], parts[2]
		if len(parts) > 3 {
			name = parts[3]
		}
	} else if len(parts) > 1 {
		name = parts[1]
	}
	kinds := map[string]string{"roles": "Role", "rolebindings": "RoleBinding", "clusterroles": "ClusterRole", "clusterrolebindings": "ClusterRoleBinding"}
	kind, valid := kinds[resource]
	if !valid {
		http.NotFound(w, r)
		return
	}
	key := resource + "/" + namespace + "/" + name
	p.mu.Lock()
	defer p.mu.Unlock()
	status := func(code int, reason string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "reason": reason, "code": code})
	}
	switch r.Method {
	case http.MethodGet:
		if name == "" {
			items := []any{}
			for objectKey, object := range p.objects {
				if strings.HasPrefix(objectKey, resource+"/") {
					meta := object["metadata"].(map[string]any)
					if namespace == "" || meta["namespace"] == namespace {
						items = append(items, object)
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": kind + "List", "items": items})
			return
		}
		object, found := p.objects[key]
		if !found {
			status(http.StatusNotFound, "NotFound")
			return
		}
		_ = json.NewEncoder(w).Encode(object)
	case http.MethodPost, http.MethodPut:
		var object map[string]any
		if decodeRBACRequest(r, &object) != nil {
			status(http.StatusBadRequest, "BadRequest")
			return
		}
		meta := object["metadata"].(map[string]any)
		name, _ = meta["name"].(string)
		key = resource + "/" + namespace + "/" + name
		old, exists := p.objects[key]
		if r.Method == http.MethodPost && exists {
			status(http.StatusConflict, "AlreadyExists")
			return
		}
		if r.Method == http.MethodPut && !exists {
			status(http.StatusNotFound, "NotFound")
			return
		}
		if exists && meta["resourceVersion"] != old["metadata"].(map[string]any)["resourceVersion"] {
			status(http.StatusConflict, "Conflict")
			return
		}
		p.sequence++
		p.writes++
		meta["resourceVersion"] = fmt.Sprint(p.sequence)
		meta["namespace"] = namespace
		meta["uid"] = fmt.Sprintf("native-%d", p.sequence)
		if exists {
			meta["uid"] = old["metadata"].(map[string]any)["uid"]
		}
		object["apiVersion"] = "rbac.authorization.k8s.io/v1"
		object["kind"] = kind
		p.objects[key] = object
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(w).Encode(object)
	case http.MethodDelete:
		old, exists := p.objects[key]
		if !exists {
			status(http.StatusNotFound, "NotFound")
			return
		}
		var options metav1.DeleteOptions
		if err := decodeRBACRequest(r, &options); err != nil {
			t.Errorf("invalid delete encoding: %v", err)
			status(http.StatusBadRequest, "BadRequest")
			return
		}
		meta := old["metadata"].(map[string]any)
		if options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil || string(*options.Preconditions.UID) != meta["uid"] || *options.Preconditions.ResourceVersion != meta["resourceVersion"] {
			t.Error("cleanup lacked exact native UID/version preconditions")
			status(http.StatusConflict, "Conflict")
			return
		}
		delete(p.objects, key)
		p.writes++
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Success"})
	default:
		status(http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

type rbacFixture struct {
	service                 *Service
	cluster                 *model.KubernetesCluster
	adminID, userID, roleID string
	adminCtx, userCtx       context.Context
	peer                    *rbacRESTPeer
}

func newRBACFixture(t *testing.T) rbacFixture {
	t.Helper()
	service := newKubeTestService(t, time.Second)
	now := time.Now().UTC()
	admin := model.User{ID: uuid.NewString(), Username: "rbac-admin-" + uuid.NewString()[:8], DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: true, AuthVersion: 1, PasswordHash: "fixture", CreatedAt: now, UpdatedAt: now}
	user := model.User{ID: uuid.NewString(), Username: "rbac-user-" + uuid.NewString()[:8], DepartmentID: database.DefaultDepartmentID, IsActive: true, AuthVersion: 1, PasswordHash: "fixture", CreatedAt: now, UpdatedAt: now}
	project := model.Project{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, Name: "RBAC-" + uuid.NewString()[:8], IsActive: true, CreatedAt: now, UpdatedAt: now}
	role := model.Role{ID: uuid.NewString(), Name: "rbac-role-" + uuid.NewString()[:8], DisplayName: "测试项目角色", CreatedAt: now, UpdatedAt: now}
	for _, value := range []any{&admin, &user, &project, &role} {
		if err := service.db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, code := range []string{"kubernetes.rbac.read", "kubernetes.rbac.update", "kubernetes.workload.read", "kubernetes.workload.update", "kubernetes.config.read", "kubernetes.config.update", "kubernetes.secret.read", "kubernetes.node.read", "kubernetes.node.create"} {
		if err := service.db.Create(&model.RolePermission{RoleID: role.ID, Permission: code, CreatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := service.db.Create(&model.ProjectMember{ProjectID: project.ID, UserID: user.ID, RoleID: role.ID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	adminCtx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: admin.ID, ProjectID: project.ID, DepartmentID: project.DepartmentID, AllDepartments: true})
	userCtx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: user.ID, ProjectID: project.ID, DepartmentID: project.DepartmentID})
	peer := &rbacRESTPeer{objects: map[string]map[string]any{}}
	peer.impersonate.Store(true)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { peer.serve(t, w, r) }))
	t.Cleanup(server.Close)
	configuration := kubeconfigForServer(server)
	cluster, err := service.Create(adminCtx, admin.ID, Input{Name: "RBAC fixture", Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &configuration})
	if err != nil {
		t.Fatal(err)
	}
	return rbacFixture{service: service, cluster: cluster, adminID: admin.ID, userID: user.ID, roleID: role.ID, adminCtx: adminCtx, userCtx: userCtx, peer: peer}
}

func (f rbacFixture) grantInput(revision uint64) RBACGrantInput {
	return RBACGrantInput{RoleID: f.roleID, Namespace: "test", Permissions: []string{"kubernetes.workload.read", "kubernetes.workload.update"}, ExpectedRevision: revision}
}
func (f rbacFixture) enable(t *testing.T) *RBACState {
	t.Helper()
	state, err := f.service.SaveRoleGrant(f.adminCtx, f.adminID, f.cluster.ID, "", f.grantInput(0))
	if err != nil {
		t.Fatal(err)
	}
	state, err = f.service.SetClusterRBACEnabled(f.adminCtx, f.adminID, f.cluster.ID, true, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRBACDefaultsAndDraftsNeverWriteCluster(t *testing.T) {
	f := newRBACFixture(t)
	state, err := f.service.ListClusterRBAC(f.adminCtx, f.cluster.ID)
	if err != nil || state.Enabled || state.Revision != 0 || state.Status != "disabled" {
		t.Fatalf("unexpected default: %+v %v", state, err)
	}
	preview, err := f.service.PreviewRoleGrant(f.adminCtx, f.adminID, f.cluster.ID, "", f.grantInput(0))
	if err != nil || preview.AffectedUsers != 1 || len(preview.Subjects) != 1 || !strings.HasPrefix(preview.Subjects[0], "edo:") {
		t.Fatalf("preview %+v %v", preview, err)
	}
	state, err = f.service.SaveRoleGrant(f.adminCtx, f.adminID, f.cluster.ID, "", f.grantInput(0))
	if err != nil || state.Enabled || state.Revision != 1 || len(state.Grants) != 1 {
		t.Fatalf("draft %+v %v", state, err)
	}
	authorization, err := f.service.authorizationForCluster(f.userCtx, f.cluster)
	if err != nil || authorization.Enforced {
		t.Fatalf("draft changed legacy access: %+v %v", authorization, err)
	}
	if f.peer.writeCount() != 0 {
		t.Fatal("draft or preview mutated the cluster")
	}
	if _, err := f.service.SaveRoleGrant(f.adminCtx, f.adminID, f.cluster.ID, "", f.grantInput(0)); !errors.Is(err, ErrRBACConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
}

func TestRBACMappingRejectsWildcardAdminAndWrongScope(t *testing.T) {
	for _, input := range []RBACGrantInput{
		{RoleID: "role", Namespace: "*", Permissions: []string{"kubernetes.workload.read"}},
		{RoleID: "role", Namespace: "test", ClusterScope: true, Permissions: []string{"kubernetes.node.read"}},
		{RoleID: "role", Namespace: "test", Permissions: []string{"kubernetes.node.read"}},
		{RoleID: "role", ClusterScope: true, Permissions: []string{"kubernetes.workload.read"}},
		{RoleID: "role", Namespace: "test", Permissions: []string{"kubernetes.rbac.update"}},
		{RoleID: "role", Namespace: "test", Permissions: []string{"*"}},
	} {
		if _, err := normalizeRBACGrant(input); !errors.Is(err, ErrRBACInvalid) {
			t.Fatalf("invalid grant accepted: %+v %v", input, err)
		}
	}
	rules := rbacRules(map[string]bool{"kubernetes.config.read": true}, "test", false)
	for _, rule := range rules {
		for _, resource := range rule.Resources {
			if resource == "secrets" || resource == "namespaces" || resource == "nodes" || resource == "*" {
				t.Fatalf("unexpected resource in namespace config read: %+v", rule)
			}
		}
	}
	rules = rbacRules(map[string]bool{"kubernetes.node.create": true}, "", true)
	for _, rule := range rules {
		for _, resource := range rule.Resources {
			if resource == "secrets" {
				t.Fatal("node bootstrap capability granted raw kube-system Secret access")
			}
		}
		for _, verb := range rule.Verbs {
			if verb == "create" || verb == "bind" || verb == "escalate" || verb == "impersonate" {
				t.Fatalf("node bootstrap identity escalated: %+v", rule)
			}
		}
	}
}

func TestRBACPermissionScopesReuseNativeResourceCapabilities(t *testing.T) {
	for _, scenario := range []struct {
		code   string
		scopes string
	}{
		{"kubernetes.storage.read", "namespace,cluster"},
		{"kubernetes.storage.create", "namespace"},
		{"kubernetes.storage.update", "namespace"},
		{"kubernetes.workload.execute", "namespace"},
		{"kubernetes.node.create", "cluster"},
		{"kubernetes.rbac.update", ""},
	} {
		if actual := strings.Join(RBACPermissionScopes(scenario.code), ","); actual != scenario.scopes {
			t.Fatalf("scope drift for %s: got %q want %q", scenario.code, actual, scenario.scopes)
		}
	}
}

func TestRBACSyncUsesEffectiveDenyAndRevokesOldIdentity(t *testing.T) {
	f := newRBACFixture(t)
	if err := f.service.db.Create(&model.UserPermission{UserID: f.userID, Permission: "kubernetes.workload.update", Effect: model.PermissionDeny, CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	state := f.enable(t)
	if _, err := f.service.authorizationCurrent(f.userCtx, f.cluster); !errors.Is(err, ErrRBACSyncPending) {
		t.Fatalf("pending policy allowed access: %v", err)
	}
	state, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision)
	if err != nil || state.Status != "synced" || !state.Enabled {
		t.Fatalf("sync failed: %+v %v", state, err)
	}
	before, err := f.service.authorizationCurrent(f.userCtx, f.cluster)
	if err != nil || len(before.Namespaces) != 1 || before.Namespaces[0] != "test" || before.ClusterScope {
		t.Fatalf("bad effective scope: %+v %v", before, err)
	}
	if strings.Contains(strings.Join(before.Permissions["test"], ","), "update") {
		t.Fatal("EDO explicit deny reached native rules")
	}
	f.peer.mu.Lock()
	for _, object := range f.peer.objects {
		encoded, _ := json.Marshal(object)
		if strings.Contains(string(encoded), `"secrets"`) || strings.Contains(string(encoded), `"patch"`) || strings.Contains(string(encoded), `"update"`) {
			t.Fatalf("deny leaked into native policy: %s", encoded)
		}
	}
	f.peer.mu.Unlock()
	if err := f.service.db.Where("user_id = ?", f.userID).Delete(&model.UserPermission{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.authorizationCurrent(f.userCtx, f.cluster); !errors.Is(err, ErrRBACSyncPending) {
		t.Fatalf("old native identity survived effective policy change: %v", err)
	}
	after, err := f.service.authorizationForCluster(f.userCtx, f.cluster)
	if err != nil || after.Principal == before.Principal || !permissionSet(after.Permissions["test"])["kubernetes.workload.update"] {
		t.Fatalf("new policy was not synchronized: %+v %v", after, err)
	}
	if err := f.service.db.Where("project_id = ? AND user_id = ?", f.cluster.ProjectID, f.userID).Delete(&model.ProjectMember{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.authorizationCurrent(f.userCtx, f.cluster); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("removed member retained access: %v", err)
	}
	state, err = f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision)
	if err != nil || state.Status != "synced" {
		t.Fatalf("cleanup %+v %v", state, err)
	}
	f.peer.mu.Lock()
	defer f.peer.mu.Unlock()
	if len(f.peer.objects) != 0 {
		t.Fatalf("removed member retained managed RBAC objects: %d", len(f.peer.objects))
	}
}

func TestRBACSyncFailureAndExplicitDisableFailClosed(t *testing.T) {
	f := newRBACFixture(t)
	state := f.enable(t)
	f.peer.fail.Store(true)
	state, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision)
	if err != nil || !state.Enabled || state.Status != "error" || strings.Contains(state.LastError, "private-token") {
		t.Fatalf("unsafe failure: %+v %v", state, err)
	}
	if _, err := f.service.authorizationForCluster(f.userCtx, f.cluster); !errors.Is(err, ErrRBACSyncPending) {
		t.Fatalf("sync failure fell back to admin: %v", err)
	}
	if _, err := f.service.SetClusterRBACEnabled(f.userCtx, f.userID, f.cluster.ID, false, state.Revision); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("delegate disabled strict authorization: %v", err)
	}
	state, err = f.service.SetClusterRBACEnabled(f.adminCtx, f.adminID, f.cluster.ID, false, state.Revision)
	if err != nil || !state.Enabled || state.DesiredEnabled || state.Status != "pending" {
		t.Fatalf("disable weakened access before cleanup: %+v %v", state, err)
	}
	state, err = f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision)
	if err != nil || !state.Enabled || state.Status != "error" {
		t.Fatalf("failed cleanup downgraded access: %+v %v", state, err)
	}
	f.peer.fail.Store(false)
	state, err = f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision)
	if err != nil || state.Enabled || state.Status != "disabled" {
		t.Fatalf("explicit recovery failed: %+v %v", state, err)
	}
}

func TestRBACSyncLeaseFencesStaleRevisionAndUnownedObjects(t *testing.T) {
	f := newRBACFixture(t)
	state := f.enable(t)
	lease, err := f.service.claimRBACSync(f.adminCtx, f.cluster, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.claimRBACSync(f.adminCtx, f.cluster, state.Revision); !errors.Is(err, errRBACLeaseBusy) {
		t.Fatalf("concurrent lease granted: %v", err)
	}
	state, err = f.service.SetClusterRBACEnabled(f.adminCtx, f.adminID, f.cluster.ID, true, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.rbacSyncFence(f.adminCtx, f.cluster, lease, nil); !errors.Is(err, ErrRBACConflict) {
		t.Fatalf("old revision could still write RBAC: %v", err)
	}
	f.service.releaseRBACSync(f.adminCtx, f.cluster.ID, lease)
	plan, err := f.service.subjectPlan(f.userCtx, f.cluster, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	name := rbacObjectName(f.cluster.ID, f.userID, plan.authorization.Version, "test")
	f.peer.mu.Lock()
	f.peer.objects["roles/test/"+name] = map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": map[string]any{"name": name, "namespace": "test", "uid": "foreign", "resourceVersion": "1", "labels": map[string]any{"owner": "foreign"}}, "rules": []any{}}
	f.peer.mu.Unlock()
	state, err = f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision)
	if err != nil || state.Status != "error" {
		t.Fatalf("foreign native object overwritten: %+v %v", state, err)
	}
	f.peer.mu.Lock()
	defer f.peer.mu.Unlock()
	if f.peer.writes != 0 {
		t.Fatal("ownership conflict wrote native resources")
	}
}

func TestRBACDelegationAndProjectBoundaries(t *testing.T) {
	f := newRBACFixture(t)
	if _, err := f.service.SetClusterRBACEnabled(f.userCtx, f.userID, f.cluster.ID, true, 0); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("delegate bootstrapped an unassigned cluster: %v", err)
	}
	state, err := f.service.SaveRoleGrant(f.adminCtx, f.adminID, f.cluster.ID, "", f.grantInput(0))
	if err != nil {
		t.Fatal(err)
	}
	input := f.grantInput(state.Revision)
	input.Namespace = "production"
	if _, err := f.service.SaveRoleGrant(f.userCtx, f.userID, f.cluster.ID, "", input); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("delegate expanded namespace: %v", err)
	}
	other := database.WithDepartmentScope(f.adminCtx, database.DepartmentScope{UserID: f.adminID, ProjectID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, AllDepartments: true})
	if _, err := f.service.ListClusterRBAC(other, f.cluster.ID); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("cross-project RBAC state disclosed: %v", err)
	}
	if f.peer.writeCount() != 0 {
		t.Fatal("rejected management request wrote cluster RBAC")
	}
}

func TestRBACUnchangedSyncDoesNotRewriteNativeRules(t *testing.T) {
	f := newRBACFixture(t)
	state := f.enable(t)
	state, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision)
	if err != nil || state.Status != "synced" {
		t.Fatalf("initial sync %+v %v", state, err)
	}
	before := f.peer.writeCount()
	state, err = f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision)
	if err != nil || state.Status != "synced" || f.peer.writeCount() != before {
		t.Fatalf("unchanged sync rewrote native policies: before=%d after=%d state=%+v err=%v", before, f.peer.writeCount(), state, err)
	}
	plan, err := f.service.subjectPlan(f.userCtx, f.cluster, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.service.PreviewRoleGrant(f.adminCtx, f.adminID, f.cluster.ID, state.Grants[0].ID, f.grantInput(state.Revision))
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Subjects) != 1 || preview.Subjects[0] == plan.authorization.Principal {
		t.Fatal("next-revision preview did not version the identity")
	}
	nativeNamespaces := []string{}
	f.peer.mu.Lock()
	for key := range f.peer.objects {
		nativeNamespaces = append(nativeNamespaces, key)
	}
	f.peer.mu.Unlock()
	sort.Strings(nativeNamespaces)
	if len(nativeNamespaces) != 4 {
		t.Fatalf("unexpected native policy footprint: %v", nativeNamespaces)
	}
}
