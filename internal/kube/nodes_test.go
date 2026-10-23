package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestNodeRemovalBlocksHealthyBusyControlPlaneAndChangedNodes(t *testing.T) {
	for _, test := range []string{"safe", "healthy", "control-plane", "not-cordoned", "active-pod", "recent-lease", "changed-uid", "last-ready", "not-confirmed"} {
		t.Run(test, func(t *testing.T) {
			deleted := false
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "uid-1", ResourceVersion: "12", Labels: map[string]string{}}, Spec: corev1.NodeSpec{Unschedulable: true}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}}
				if test == "healthy" {
					node.Status.Conditions[0].Status = corev1.ConditionTrue
				}
				if test == "control-plane" {
					node.Labels["node-role.kubernetes.io/control-plane"] = ""
				}
				if test == "not-cordoned" {
					node.Spec.Unschedulable = false
				}
				var result any
				switch {
				case r.Method == "DELETE":
					deleted = true
					var options metav1.DeleteOptions
					body, _ := io.ReadAll(r.Body)
					if _, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, &options); err != nil {
						t.Fatal(err)
					}
					if options.Preconditions == nil || string(*options.Preconditions.UID) != "uid-1" || *options.Preconditions.ResourceVersion != "12" {
						t.Error("删除缺少并发保护")
					}
					result = &metav1.Status{Status: "Success"}
				case r.URL.Path == "/api/v1/nodes/worker":
					result = &node
				case r.URL.Path == "/api/v1/nodes":
					list := &corev1.NodeList{}
					if test != "last-ready" {
						list.Items = []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "server"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}}
					}
					result = list
				case r.URL.Path == "/apis/coordination.k8s.io/v1/namespaces/kube-node-lease/leases/worker":
					at := metav1.NewMicroTime(time.Now().Add(-3 * time.Minute))
					if test == "recent-lease" {
						at = metav1.NewMicroTime(time.Now())
					}
					result = &coordv1.Lease{Spec: coordv1.LeaseSpec{RenewTime: &at}}
				case r.URL.Path == "/api/v1/pods":
					if r.URL.Query().Get("fieldSelector") != "spec.nodeName=worker" {
						t.Error("Pod 查询未限定节点")
					}
					pods := &corev1.PodList{}
					if test == "active-pod" {
						pods.Items = []corev1.Pod{{Status: corev1.PodStatus{Phase: corev1.PodRunning}}}
					}
					result = pods
				default:
					t.Error("意外接口", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(result)
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			input := NodeRemoval{ResourceIdentity: ResourceIdentity{UID: "uid-1", ResourceVersion: "12"}, Name: "worker", AgentStopped: true}
			if test == "changed-uid" {
				input.UID = "old"
			}
			if test == "not-confirmed" {
				input.AgentStopped = false
			}
			err := service.DeleteNode(context.Background(), id, "worker", input)
			if test == "safe" {
				if err != nil || !deleted {
					t.Fatal("安全删除失败", err)
				}
			} else if err == nil || deleted {
				t.Fatal("不安全删除未被拦截", err)
			}
		})
	}
}

func TestJoinValidationNeverCreatesStaticNodeOrReadsExistingTokens(t *testing.T) {
	called := false
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Error("无效参数不应请求集群", r.URL.Path)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	for _, input := range []NodeJoinInput{{Provider: "k3s", Minutes: 0}, {Provider: "k3s", Minutes: 61}, {Provider: "unknown", Minutes: 15}, {Provider: "k3s", Minutes: 15, Endpoint: "https://safe'; bad"}, {Provider: "k3s", Minutes: 15, Endpoint: "http://unsafe"}} {
		_, err := service.CreateNodeJoin(context.Background(), id, input)
		if !errors.Is(err, ErrInvalidResource) {
			t.Fatal("错误类型不正确", err)
		}
	}
	if called {
		t.Fatal("访问了集群")
	}
}
