package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/kube"
	"edo/internal/model"
)

func TestKubernetesConnectionStatusesHTTPPermissionsScopeAndRefresh(t *testing.T) {
	var requests atomic.Int32
	var reject atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/version" {
			t.Error("连接状态不应访问资源或写入集群")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if reject.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","reason":"Unauthorized","message":"private-credential-token","code":401}`)
			return
		}
		_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
	}))
	t.Cleanup(server.Close)
	var db *gorm.DB
	var kubernetesService *kube.Service
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, value *gorm.DB) { db, kubernetesService = value, deps.Kubernetes })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/kubernetes/clusters", map[string]any{"name": "connection-http", "mode": "kubeconfig", "default_namespace": "default", "kubeconfig": kubeconfigForHTTPTest(server)}, admin)
	var body struct {
		Cluster struct {
			ID string `json:"id"`
		} `json:"cluster"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &body) != nil || body.Cluster.ID == "" {
		t.Fatalf("创建测试集群失败: %d %s", created.Code, created.Body.String())
	}
	clusterID := body.Cluster.ID
	path := "/api/v1/kubernetes/clusters/connection-statuses?ids=" + clusterID
	role := createRoleForHTTPTest(t, router, admin, "connection-reader", []string{"kubernetes.workload.read"})
	userID := createUserForHTTPTest(t, router, admin, "connection-reader", "", []string{role})
	reader := loginForDepartmentTest(t, router, "connection-reader")
	deniedRole := createRoleForHTTPTest(t, router, admin, "connection-no-read", []string{"deployment.read"})
	createUserForHTTPTest(t, router, admin, "connection-no-read", "", []string{deniedRole})
	denied := loginForDepartmentTest(t, router, "connection-no-read")
	for _, check := range []struct {
		cookie *http.Cookie
		path   string
		code   int
	}{
		{nil, path, http.StatusUnauthorized},
		{denied, path, http.StatusForbidden},
		{admin, "/api/v1/kubernetes/clusters/connection-statuses?ids=invalid", http.StatusBadRequest},
		{admin, "/api/v1/kubernetes/clusters/connection-statuses?ids=" + strings.Repeat(clusterID+",", 500) + clusterID, http.StatusBadRequest},
	} {
		response := performJSONRequest(t, router, http.MethodGet, check.path, nil, check.cookie)
		if response.Code != check.code {
			t.Fatalf("状态接口权限/参数错误: want=%d got=%d body=%s", check.code, response.Code, response.Body.String())
		}
	}
	var foreign model.KubernetesCluster
	if err := db.First(&foreign, "uuid = ?", clusterID).Error; err != nil {
		t.Fatal(err)
	}
	foreign.RowID, foreign.ID, foreign.Name, foreign.ProjectID = 0, uuid.NewString(), "foreign-connection", uuid.NewString()
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	read := func(cookie *http.Cookie, query string) []kube.ConnectionStatus {
		t.Helper()
		response := performJSONRequest(t, router, http.MethodGet, query, nil, cookie)
		var value struct {
			Statuses []kube.ConnectionStatus `json:"statuses"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &value) != nil {
			t.Fatalf("状态接口读取失败: %d %s", response.Code, response.Body.String())
		}
		if len(value.Statuses) != 1 || value.Statuses[0].ClusterID != clusterID {
			t.Fatalf("集群范围错误: %+v", value.Statuses)
		}
		if strings.Contains(response.Body.String(), "private-credential") || strings.Contains(response.Body.String(), server.URL) {
			t.Fatal("响应泄漏内部诊断")
		}
		return value.Statuses
	}
	wait := func(cookie *http.Cookie, want string) kube.ConnectionStatus {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			value := read(cookie, path)[0]
			if value.Status == want && !value.Checking {
				return value
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("状态未变为 %s", want)
		return kube.ConnectionStatus{}
	}
	beforeRead := requests.Load()
	values := read(reader, path+","+foreign.ID)
	if values[0].Status != "unknown" || requests.Load() != beforeRead {
		t.Fatalf("普通读取不应启动检测: %+v", values)
	}
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		_ = kubernetesService.RunConnectionMonitoring(monitorCtx)
	}()
	defer func() {
		stopMonitor()
		<-monitorDone
	}()
	wait(reader, "connected")
	if err := db.Where("project_id = ? AND user_id = ?", database.DefaultProjectID(database.DefaultDepartmentID), userID).Delete(&model.ProjectMember{}).Error; err != nil {
		t.Fatal(err)
	}
	revoked := performJSONRequest(t, router, http.MethodGet, path, nil, reader)
	if revoked.Code != http.StatusForbidden {
		t.Fatalf("数据库撤权后仍读取缓存: %d %s", revoked.Code, revoked.Body.String())
	}
	connected := wait(admin, "connected")
	before := requests.Load()
	if read(admin, path+"&refresh=true")[0].Status != "connected" || requests.Load() != before {
		t.Fatal("重复手动刷新没有合并")
	}
	// 真实接口验证刷新绕过有效缓存，同时保留重复点击的最短间隔。
	time.Sleep(time.Until(connected.CheckedAt.Add(2100 * time.Millisecond)))
	reject.Store(true)
	if read(admin, path)[0].Status != "connected" {
		t.Fatal("普通轮询意外跳过缓存")
	}
	if !read(admin, path+"&refresh=true")[0].Checking {
		t.Fatal("手动刷新没有触发异步重检")
	}
	failed := wait(admin, "restricted")
	if failed.Reason != "credentials" || requests.Load() != before+1 {
		t.Fatalf("真实刷新未正确分类认证失败: %+v requests=%d before=%d", failed, requests.Load(), before)
	}
}
