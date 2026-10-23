package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"edo/internal/database"
	"edo/internal/model"
)

func architectureNode(name, architecture, operatingSystem string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"kubernetes.io/arch": "incorrect-label"}},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{
			Architecture: architecture, OperatingSystem: operatingSystem,
		}},
	}
}

func TestNodeArchitecturesUseNodeStatusAndCompletePages(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/nodes" || r.URL.Query().Get("limit") != "500" {
			t.Errorf("节点范围不正确: %s", r.URL)
		}
		list := corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}}
		if r.URL.Query().Get("continue") == "" {
			list.Continue = "second-page"
			list.Items = []corev1.Node{architectureNode("first", "aarch64", "linux"), architectureNode("duplicate", "arm64", "linux")}
		} else {
			if r.URL.Query().Get("continue") != "second-page" {
				t.Errorf("分页游标丢失: %s", r.URL)
			}
			list.Items = []corev1.Node{architectureNode("second", "amd64", "linux")}
		}
		_ = json.NewEncoder(w).Encode(list)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	cluster, err := service.Find(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	architectures, err := service.NodeArchitectures(context.Background(), id, cluster.ProjectID)
	if err != nil || !reflect.DeepEqual(architectures, []model.HostArchitecture{model.HostArchitectureAMD64, model.HostArchitectureARM64}) || calls.Load() != 2 {
		t.Fatalf("没有完整去重冻结真实节点架构: architectures=%v calls=%d err=%v", architectures, calls.Load(), err)
	}
}

func TestNodeArchitecturesFailClosedWithoutFallback(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		code  int
		nodes []corev1.Node
		want  error
	}{
		{name: "empty", want: ErrNodeArchitecturesMissing},
		{name: "missing", nodes: []corev1.Node{architectureNode("incomplete", "", "linux")}, want: ErrNodeArchitecturesMissing},
		{name: "unsupported", nodes: []corev1.Node{architectureNode("valid", "amd64", "linux"), architectureNode("unsupported", "ppc64le", "linux")}, want: ErrNodeArchitectureUnsupported},
		{name: "unsupported-system", nodes: []corev1.Node{architectureNode("other-system", "amd64", "other")}, want: ErrNodeArchitectureUnsupported},
		{name: "forbidden", code: http.StatusForbidden, want: ErrNodeArchitecturesUnavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if scenario.code != 0 {
					w.WriteHeader(scenario.code)
					_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"private-api.example.com diagnostic"}`)
					return
				}
				_ = json.NewEncoder(w).Encode(corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, Items: scenario.nodes})
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			cluster, err := service.Find(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			architectures, err := service.NodeArchitectures(context.Background(), id, cluster.ProjectID)
			if !errors.Is(err, scenario.want) || architectures != nil {
				t.Fatalf("无效架构不应回退或返回部分结果: architectures=%v err=%v", architectures, err)
			}
			if strings.Contains(err.Error(), "private-api") {
				t.Fatalf("向调用方泄露上游诊断信息: %v", err)
			}
		})
	}
}

func TestNodeArchitecturesScopeAndInactiveClusterPreventRequests(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("范围外或停用集群不应访问节点")
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	cluster, err := service.Find(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, projectID := range []string{"", "another-project"} {
		if result, err := service.NodeArchitectures(context.Background(), id, projectID); !errors.Is(err, ErrClusterNotFound) || result != nil {
			t.Fatalf("集群项目边界失效: result=%v err=%v", result, err)
		}
	}
	scoped := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: cluster.DepartmentID, ProjectID: "another-project"})
	if _, err := service.NodeArchitectures(scoped, id, cluster.ProjectID); !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("请求项目边界失效: %v", err)
	}
	if err := service.db.Model(&model.KubernetesCluster{}).Where("id = ?", id).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.NodeArchitectures(context.Background(), id, cluster.ProjectID); !errors.Is(err, ErrClusterNotFound) || calls.Load() != 0 {
		t.Fatalf("停用集群仍访问了节点: calls=%d err=%v", calls.Load(), err)
	}
}
