package kube

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestKubernetesRuntimeStopRestartAndScale(t *testing.T) {
	replicas := int32(3)
	current := appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "demo"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "demo"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "demo", Image: "demo:v1"}}},
			},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 3, ReadyReplicas: 3, AvailableReplicas: 3},
	}
	current.Generation = 1
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/version" {
			_, _ = io.WriteString(response, `{"gitVersion":"v1.32.4"}`)
			return
		}
		if request.URL.Path != "/apis/apps/v1/namespaces/default/deployments/demo" {
			http.NotFound(response, request)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch request.Method {
		case http.MethodGet:
			_ = json.NewEncoder(response).Encode(current)
		case http.MethodPut:
			payload, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				http.Error(response, readErr.Error(), http.StatusBadRequest)
				return
			}
			object, _, decodeErr := protobuf.NewSerializer(scheme.Scheme, scheme.Scheme).Decode(payload, nil, nil)
			updated, ok := object.(*appsv1.Deployment)
			if decodeErr != nil || !ok {
				http.Error(response, "无法解析 Deployment Update", http.StatusBadRequest)
				return
			}
			updated.Generation = current.Generation + 1
			desired := int32(1)
			if updated.Spec.Replicas != nil {
				desired = *updated.Spec.Replicas
			}
			updated.Status.ObservedGeneration = updated.Generation
			updated.Status.UpdatedReplicas = desired
			updated.Status.ReadyReplicas = desired
			updated.Status.AvailableReplicas = desired
			updated.Status.UnavailableReplicas = 0
			current = *updated
			_ = json.NewEncoder(response).Encode(current)
		default:
			response.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	service := newKubeTestService(t, 5*time.Second)
	clusterID := createKubeTestCluster(t, service, server)
	state, err := service.ControlDeployment(context.Background(), clusterID, "default", "demo", "stop", 0, 30*time.Second)
	if err != nil || state.State != "stopped" || state.Replicas != 0 {
		t.Fatalf("停止 Kubernetes Deployment 失败: state=%+v err=%v", state, err)
	}
	mu.Lock()
	savedReplicas := current.Annotations[stoppedReplicasAnnotation]
	mu.Unlock()
	if savedReplicas != "3" {
		t.Fatalf("停止时没有保存原副本数: %q", savedReplicas)
	}
	state, err = service.ControlDeployment(context.Background(), clusterID, "default", "demo", "restart", 0, 30*time.Second)
	if err != nil || !state.Running || state.Replicas != 3 {
		t.Fatalf("重启已停止 Kubernetes Deployment 没有恢复原副本数: state=%+v err=%v", state, err)
	}
	mu.Lock()
	restartedAt := current.Spec.Template.Annotations[restartedAtAnnotation]
	mu.Unlock()
	if restartedAt == "" {
		t.Fatal("Kubernetes 重启没有修改 Pod 模板，无法触发滚动重启")
	}
	state, err = service.ControlDeployment(context.Background(), clusterID, "default", "demo", "scale", 5, 30*time.Second)
	if err != nil || state.Replicas != 5 || state.ReadyReplicas != 5 {
		t.Fatalf("Kubernetes 扩缩容失败: state=%+v err=%v", state, err)
	}
}
