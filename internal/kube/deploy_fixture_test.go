package kube

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/client-go/kubernetes/scheme"
)

const (
	readinessPreviousImage = "registry.example.test/team/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	readinessTargetImage   = "registry.example.test/team/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	readinessDeploymentID  = "edo-pipeline-deployment"
)

// 通过真实 client-go HTTP 编解码模拟 Update 后观察到的集群对象，不替换发布逻辑。
func deployReadinessFixture(t *testing.T, observe func(*appsv1.Deployment)) (*Service, string) {
	t.Helper()
	replicas := int32(2)
	current := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "api", Namespace: "acceptance", UID: "original-deployment", ResourceVersion: "11", Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "api", Image: readinessPreviousImage},
					{Name: "sidecar", Image: "registry.example.test/team/sidecar@sha256:" + strings.Repeat("c", 64)},
				}},
			},
		},
	}
	var mu sync.Mutex
	updated := false
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/apps/v1/namespaces/acceptance/deployments/api" {
			t.Errorf("访问了非目标工作负载: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			observed := current.DeepCopy()
			if updated && observe != nil {
				observe(observed)
			}
			_ = json.NewEncoder(w).Encode(observed)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("读取更新请求失败: %v", err)
				http.Error(w, "更新请求无效", http.StatusBadRequest)
				return
			}
			object, _, err := protobuf.NewSerializer(scheme.Scheme, scheme.Scheme).Decode(body, nil, nil)
			next, ok := object.(*appsv1.Deployment)
			if err != nil || !ok {
				t.Errorf("client-go 更新请求无法解码: %v", err)
				http.Error(w, "更新请求无效", http.StatusBadRequest)
				return
			}
			if next.UID != current.UID || next.ResourceVersion != current.ResourceVersion || next.Spec.Template.Spec.Containers[1].Image != current.Spec.Template.Spec.Containers[1].Image {
				t.Error("更新丢失对象并发身份或改动了其他容器")
			}
			if next.Spec.Template.Spec.Containers[0].Image != readinessTargetImage || next.Spec.Template.Annotations["edo.io/deployment-id"] != readinessDeploymentID {
				t.Error("更新没有使用本次固定镜像和发布标识")
			}
			next.Generation = 2
			next.ResourceVersion = "12"
			next.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2}
			current = next
			updated = true
			_ = json.NewEncoder(w).Encode(next)
		default:
			t.Errorf("意外请求方法: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	service := newKubeTestService(t, time.Second)
	return service, createKubeTestCluster(t, service, server)
}
