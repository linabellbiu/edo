package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/model"
)

func TestKubernetesHostAssociationAPIAllowsIndependentClustersAndReturnsSafeSummary(t *testing.T) {
	var db *gorm.DB
	router, closeTest := newAuthTestRouter(t, func(_ *Dependencies, database *gorm.DB) {
		db = database
		cluster := model.KubernetesCluster{ID: "cluster-links", Name: "关联测试集群", Mode: model.KubernetesKubeconfig, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := db.Create(&cluster).Error; err != nil {
			t.Fatal(err)
		}
	})
	defer closeTest()
	admin := loginForDepartmentTest(t, router, "admin")
	path := "/api/v1/kubernetes/clusters/cluster-links/hosts"

	for _, payload := range []map[string]any{{}, {"host_ids": nil}} {
		response := performJSONRequest(t, router, http.MethodPut, path, payload, admin)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("缺失 host_ids 不能隐式清空关系: status=%d body=%s", response.Code, response.Body.String())
		}
	}
	linked := performJSONRequest(t, router, http.MethodPut, path, map[string]any{"host_ids": []string{"httpapi-deployment-host"}}, admin)
	var payload struct {
		Cluster kubernetesClusterResponse `json:"cluster"`
	}
	if linked.Code != http.StatusOK || json.Unmarshal(linked.Body.Bytes(), &payload) != nil || len(payload.Cluster.Hosts) != 1 {
		t.Fatalf("关联主机失败: status=%d body=%s", linked.Code, linked.Body.String())
	}
	if payload.Cluster.Hosts[0].ID != "httpapi-deployment-host" || payload.Cluster.Hosts[0].Address != "192.0.2.20" || strings.Contains(linked.Body.String(), "ssh_username") || strings.Contains(linked.Body.String(), "credential") {
		t.Fatalf("集群响应应仅含主机摘要: %s", linked.Body.String())
	}
	listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/clusters", nil, admin)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"hosts":[{"id":"httpapi-deployment-host"`) {
		t.Fatalf("列表未展示已关联主机: status=%d body=%s", listed.Code, listed.Body.String())
	}
	cleared := performJSONRequest(t, router, http.MethodPut, path, map[string]any{"host_ids": []string{}}, admin)
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"hosts":[]`) {
		t.Fatalf("空数组应解除主机关联: status=%d body=%s", cleared.Code, cleared.Body.String())
	}
	var cluster model.KubernetesCluster
	if err := db.First(&cluster, "uuid = ?", "cluster-links").Error; err != nil || !cluster.IsActive {
		t.Fatalf("解除主机关联不能删除或停用集群: %+v, %v", cluster, err)
	}
}

func TestKubernetesHostAssociationRequiresUpdateAndHostReadPermissionForSummary(t *testing.T) {
	router, closeTest := newAuthTestRouter(t, func(_ *Dependencies, db *gorm.DB) {
		cluster := model.KubernetesCluster{ID: "cluster-permissions", Name: "主机权限集群", Mode: model.KubernetesKubeconfig, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := db.Create(&cluster).Error; err != nil {
			t.Fatal(err)
		}
		capability := model.HostCapability{HostID: "httpapi-deployment-host", Kind: model.HostCapabilityKubernetes, RuntimeID: cluster.ID, Status: model.HostCapabilityUnchecked, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := db.Create(&capability).Error; err != nil {
			t.Fatal(err)
		}
	})
	defer closeTest()
	admin := loginForDepartmentTest(t, router, "admin")
	roleID := createRoleForHTTPTest(t, router, admin, "kube-workload-reader", []string{access.PermissionClusterCreate, "kubernetes.workload.read"})
	createUserForHTTPTest(t, router, admin, "kube-reader", "", []string{roleID})
	reader := loginForDepartmentTest(t, router, "kube-reader")
	path := "/api/v1/kubernetes/clusters/cluster-permissions/hosts"
	response := performJSONRequest(t, router, http.MethodPut, path, map[string]any{"host_ids": []string{}}, reader)
	if response.Code != http.StatusForbidden {
		t.Fatalf("资源只读用户不能更改主机关联: status=%d body=%s", response.Code, response.Body.String())
	}
	response = performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{
		"name": "reader-created-cluster", "mode": "kubeconfig", "kubeconfig": "unused", "host_ids": []string{"httpapi-deployment-host"},
	}, reader)
	if response.Code != http.StatusForbidden {
		t.Fatalf("仅有创建权限时不能绕过主机关联修改权限: status=%d body=%s", response.Code, response.Body.String())
	}
	listed := performJSONRequest(t, router, http.MethodGet, "/api/v1/kubernetes/clusters", nil, reader)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"hosts":null`) || strings.Contains(listed.Body.String(), "192.0.2.20") {
		t.Fatalf("无主机读取权限时不得暴露主机摘要: status=%d body=%s", listed.Code, listed.Body.String())
	}
}
