package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
)

func administratorResourceFixture(t *testing.T, handler http.HandlerFunc) rbacFixture {
	t.Helper()
	f := newRBACFixture(t)
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/apis/rbac.authorization.k8s.io/") || strings.HasPrefix(r.URL.Path, "/apis/authorization.k8s.io/") {
			f.peer.serve(t, w, r)
			return
		}
		handler(w, r)
	})
	encrypted, err := f.service.secrets.Encrypt(kubeconfigForServer(server), kubeconfigAAD(f.cluster.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.cluster.APIServer, f.cluster.KubeconfigCiphertext = server.URL, encrypted
	if err := f.service.db.Save(f.cluster).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAdministratorAccessNeedsNoProjectRoleOrClusterGrant(t *testing.T) {
	f := administratorResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Impersonate-User") != "" {
			t.Error("管理员不应被模拟为受限的项目成员")
		}
		switch r.URL.Path {
		case "/api/v1/namespaces":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NamespaceList","items":[{"metadata":{"name":"default"}},{"metadata":{"name":"kube-system"}}]}`)
		case "/api/v1/pods", "/api/v1/namespaces/default/pods":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"metrics","namespace":"kube-system"}}]}`)
		case "/api/v1/namespaces/default/configmaps":
			if r.Method != http.MethodPost {
				t.Error("应执行真实的夹具新建请求")
			}
			var object map[string]any
			if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
				t.Error(err)
			}
			object["metadata"].(map[string]any)["uid"] = "created"
			object["metadata"].(map[string]any)["resourceVersion"] = "1"
			_ = json.NewEncoder(w).Encode(object)
		default:
			t.Errorf("非预期请求: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	state := f.enable(t)
	if state, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision); err != nil || state.Status != "synced" {
		t.Fatalf("普通成员前置授权未同步: %+v %v", state, err)
	}
	before := f.peer.writeCount()
	names, err := f.service.Namespaces(f.adminCtx, f.cluster.ID)
	if err != nil || !slices.Equal(names, []string{"default", "kube-system"}) {
		t.Fatalf("管理员应默认读取所有命名空间: %v %v", names, err)
	}
	for _, ns := range []string{"default", "*"} {
		list, err := f.service.ListResources(f.adminCtx, f.cluster.ID, "pods", ns, "")
		if err != nil || len(list.Items) != 1 {
			t.Fatalf("管理员读取被额外授权阻断: %+v %v", list, err)
		}
	}
	for _, def := range ResourceDefinitions() {
		ns := ""
		if def.Namespaced {
			ns = "default"
		}
		for _, action := range []string{"read", "create", "update", "delete", "execute"} {
			if err := f.service.RequireResourcePermission(f.adminCtx, f.cluster.ID, def.Resource, ns, action, false); err != nil {
				t.Fatalf("管理员缺少 %s/%s: %v", def.Resource, action, err)
			}
		}
	}
	object := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "admin-created", "namespace": "default"}, "data": map[string]any{"key": "value"}}
	if _, err := f.service.SaveResource(f.adminCtx, f.cluster.ID, "configmaps", "default", "admin-created", object, true); err != nil {
		t.Fatal("管理员新建资源失败", err)
	}
	if f.peer.writeCount() != before {
		t.Fatal("管理员访问不应额外写入原生 RBAC")
	}
	if _, err := f.service.ListResources(f.userCtx, f.cluster.ID, "pods", "default", ""); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("普通成员越过命名空间授权: %v", err)
	}
	wrong := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: f.adminID, ProjectID: "different-project", DepartmentID: f.cluster.DepartmentID, AllDepartments: true})
	if _, err := f.service.Namespaces(wrong, f.cluster.ID); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("管理员请求逃逸当前项目范围: %v", err)
	}
}

func TestAdministratorIdentityIsolatedAndRevoked(t *testing.T) {
	for _, change := range []string{"demoted", "disabled", "auth-version"} {
		t.Run(change, func(t *testing.T) {
			f := newRBACFixture(t)
			state := f.enable(t)
			if state, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision); err != nil || state.Status != "synced" {
				t.Fatalf("普通成员前置授权未同步: %+v %v", state, err)
			}
			identityStartResourceWatching(t, f.service)
			admin, err := f.service.clusterQueries(f.adminCtx, f.cluster.ID)
			if err != nil {
				t.Fatal(err)
			}
			member, err := f.service.clusterQueries(f.userCtx, f.cluster.ID)
			if err != nil {
				t.Fatal(err)
			}
			if admin == member || !admin.authorization.Administrator || member.authorization.Administrator || admin.version == member.version {
				t.Fatal("管理员缓存与普通成员混用")
			}
			lease, cancel, err := f.service.WithClusterAuthorizationLease(f.adminCtx, f.cluster.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			column, value := "is_superuser", any(false)
			if change == "disabled" {
				column = "is_active"
			}
			if change == "auth-version" {
				column, value = "auth_version", 2
			}
			if err := f.service.db.Model(&model.User{}).Where("uuid = ?", f.adminID).Update(column, value).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.service.validateQueryAuthorization(f.adminCtx, admin); err == nil {
				t.Fatal("旧管理员授权在身份变化后仍有效")
			}
			select {
			case <-admin.ctx.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("管理员长连接未随身份变化关闭")
			}
			select {
			case <-lease.Done():
			case <-time.After(time.Second):
				t.Fatal("管理员操作租约没有取消")
			}
			if member.ctx.Err() != nil {
				t.Fatal("管理员撤权误伤普通成员缓存")
			}
		})
	}
}

func TestAdministratorRecoveryDoesNotDependOnRBACSync(t *testing.T) {
	f := newRBACFixture(t)
	f.enable(t)
	if err := f.service.db.Model(&model.KubernetesRBACPolicy{}).Where("cluster_id = ?", f.cluster.ID).Updates(map[string]any{"status": "error", "last_error": "同步失败"}).Error; err != nil {
		t.Fatal(err)
	}
	before := f.peer.requests.Load()
	auth, err := f.service.authorizationForCluster(f.adminCtx, f.cluster)
	if err != nil || !auth.Administrator {
		t.Fatalf("管理员被同步状态阻断: %+v %v", auth, err)
	}
	if f.peer.requests.Load() != before {
		t.Fatal("管理员核验不应为了访问而修改集群")
	}
	if _, err := f.service.authorizationForCluster(f.userCtx, f.cluster); !errors.Is(err, ErrRBACSyncPending) {
		t.Fatalf("普通成员同步失败必须拒绝: %v", err)
	}
	// 即使是具有管理权限的项目成员，也不能通过伪造范围标记获得超级管理员访问。
	forged := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: f.userID, ProjectID: f.cluster.ProjectID, DepartmentID: f.cluster.DepartmentID, AllDepartments: true})
	if _, err := f.service.authorizationForCluster(forged, f.cluster); !errors.Is(err, ErrRBACSyncPending) {
		t.Fatalf("上下文标记变成超级管理员: %v", err)
	}
}

func TestAdministratorCacheDoesNotCrossActorsWhenRBACDisabled(t *testing.T) {
	f := newRBACFixture(t)
	identityStartResourceWatching(t, f.service)
	admin, err := f.service.clusterQueries(f.adminCtx, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	member, err := f.service.clusterQueries(f.userCtx, f.cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if admin == member || admin.version == member.version || member.scope.UserID != f.userID || member.authorization.Administrator {
		t.Fatal("关闭集群授权时普通成员复用了管理员连接及身份")
	}
}

func TestAdministratorMembershipDoesNotCreateNativeBindings(t *testing.T) {
	f := newRBACFixture(t)
	if err := f.service.db.Create(&model.ProjectMember{ProjectID: f.cluster.ProjectID, UserID: f.adminID, RoleID: f.roleID, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	state := f.enable(t)
	state, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision)
	if err != nil || state.Status != "synced" {
		t.Fatalf("管理员成员影响了普通授权同步: %+v %v", state, err)
	}
	var count int64
	if err := f.service.db.Model(&model.KubernetesRBACSubject{}).Where("cluster_id = ? AND user_id = ?", f.cluster.ID, f.adminID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("为管理员额外生成了原生授权身份")
	}
}

func TestAdministratorOverviewRejectsRevokedSnapshot(t *testing.T) {
	var f rbacFixture
	f = administratorResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"private-worker"}}]}`)
		case "/apis/metrics.k8s.io/v1/nodes":
			if err := f.service.db.Model(&model.User{}).Where("uuid = ?", f.adminID).Update("is_superuser", false).Error; err != nil {
				t.Error(err)
			}
			_, _ = io.WriteString(w, `{"apiVersion":"metrics.k8s.io/v1","kind":"NodeMetricsList","items":[]}`)
		default:
			t.Errorf("非预期请求: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	f.enable(t)
	result, err := f.service.Overview(f.adminCtx, f.cluster.ID, "*", map[string]bool{"kubernetes.node.read": true})
	if result != nil || !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("管理员撤权后返回了旧聚合数据: %+v %v", result, err)
	}
}
