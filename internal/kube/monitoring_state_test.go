package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func monitoringFixture() (*appsv1.Deployment, appsv1.ReplicaSet, corev1.Pod) {
	controller := true
	replicas := int32(1)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "metrics-server", Namespace: "kube-system", UID: "monitor", Generation: 1}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "metrics-server"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "metrics-server"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "metrics-server", Image: "metrics:current"}}}}}}
	rs := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{UID: "current", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: deployment.UID, Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: *deployment.Spec.Template.DeepCopy()}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "current-pod", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", UID: rs.UID, Controller: &controller}}}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	return deployment, rs, pod
}

func TestOverviewMonitoringFeedback(t *testing.T) {
	for _, tc := range []struct {
		name, reason, message, want string
		metricsCode                 int
		workloads, samples, ready   bool
	}{
		{name: "启动中", workloads: true, want: "installing"},
		{name: "真实拉取超时", workloads: true, reason: "ErrImagePull", message: "rpc error: context deadline exceeded https://private.invalid credential=hidden", want: "image_pull_timeout"},
		{name: "普通拉取失败", workloads: true, reason: "ImagePullBackOff", message: "Back-off pulling image", want: "image_pull_failed"},
		{name: "启动失败", workloads: true, reason: "CrashLoopBackOff", want: "startup_failed"},
		{name: "非拉取超时不误报", workloads: true, reason: "ContainerCreating", message: "mount timeout", want: "installing"},
		{name: "真实指标权限不足", workloads: true, metricsCode: 403, want: "forbidden"},
		{name: "缺少工作负载权限", want: "unavailable"},
		{name: "等待采样", workloads: true, ready: true, want: "waiting_metrics"},
		{name: "指标恢复不再诊断", workloads: true, samples: true, reason: "ErrImagePull", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment, rs, pod := monitoringFixture()
			if tc.ready {
				deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, AvailableReplicas: 1}
				pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			}
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: tc.reason, Message: tc.message}}}}
			diagnosticReads := 0
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v1/nodes":
					_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker"}}]}`)
				case strings.HasPrefix(r.URL.Path, "/apis/metrics.k8s.io/"):
					if tc.metricsCode != 0 {
						w.WriteHeader(tc.metricsCode)
						_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":%d}`, tc.metricsCode)
					} else if tc.samples && strings.HasSuffix(r.URL.Path, "/nodes") {
						_, _ = fmt.Fprintf(w, `{"apiVersion":"metrics.k8s.io/v1","kind":"NodeMetricsList","items":[{"metadata":{"name":"worker"},"timestamp":%q,"usage":{"cpu":"10m","memory":"100Mi"}}]}`, time.Now().UTC().Format(time.RFC3339))
					} else {
						_, _ = io.WriteString(w, `{"apiVersion":"metrics.k8s.io/v1","kind":"List","items":[]}`)
					}
				case r.URL.Path == "/apis/apps/v1/namespaces/kube-system/deployments/metrics-server":
					diagnosticReads++
					_ = json.NewEncoder(w).Encode(deployment)
				case r.URL.Path == "/apis/apps/v1/namespaces/kube-system/replicasets":
					if r.URL.Query().Get("labelSelector") != "app=metrics-server" || r.URL.Query().Get("limit") != "100" {
						t.Error("诊断必须限定组件及数量")
					}
					_ = json.NewEncoder(w).Encode(appsv1.ReplicaSetList{Items: []appsv1.ReplicaSet{rs}})
				case r.URL.Path == "/api/v1/namespaces/kube-system/pods":
					_ = json.NewEncoder(w).Encode(corev1.PodList{Items: []corev1.Pod{pod}})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{}})
				}
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			result, err := service.Overview(context.Background(), id, "team", map[string]bool{"kubernetes.node.read": true, "kubernetes.workload.read": tc.workloads})
			if err != nil || result.MonitoringState != tc.want {
				t.Fatalf("提示状态错误: %+v %v", result, err)
			}
			if (tc.samples || !tc.workloads || tc.metricsCode == 403) && diagnosticReads != 0 {
				t.Fatal("无需或无权诊断时不能额外读取组件")
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "private.invalid") || strings.Contains(string(encoded), "hidden") {
				t.Fatal("响应泄露原始诊断")
			}
		})
	}
}

func TestMonitoringIgnoresOldOrUnrelatedPods(t *testing.T) {
	deployment, rs, pod := monitoringFixture()
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, AvailableReplicas: 1}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	old := rs.DeepCopy()
	old.UID = "old"
	old.Spec.Template.Spec.Containers[0].Image = "metrics:old"
	for _, owner := range []types.UID{"old", "unrelated"} {
		failed := pod.DeepCopy()
		failed.OwnerReferences[0].UID = owner
		failed.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "i/o timeout"}}}}
		if got := currentMonitoringState(deployment, []appsv1.ReplicaSet{rs, *old}, []corev1.Pod{pod, *failed}); got != "waiting_metrics" {
			t.Fatal("旧版或无关 Pod 污染当前状态", got)
		}
	}
	deployment.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}
	if got := currentMonitoringState(deployment, []appsv1.ReplicaSet{rs}, nil); got != "startup_failed" {
		t.Fatal("组件实际超时应提示失败", got)
	}
}

func TestMonitoringRespectsNamespaceGrantsAndRevocation(t *testing.T) {
	for _, scenario := range []string{"namespace-denied", "revoked-during-read", "component-absent", "component-forbidden"} {
		t.Run(scenario, func(t *testing.T) {
			policies := newIdentityTestPolicies()
			grants := map[string][]string{"": {"kubernetes.node.read"}, "team": {"kubernetes.workload.read"}}
			if scenario != "namespace-denied" {
				grants["kube-system"] = []string{"kubernetes.workload.read"}
			}
			policies.grant("alice", grants)
			reads := 0
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Impersonate-User") != "edo:project:alice:version-1" {
					t.Error("诊断未使用当前用户身份")
				}
				switch r.URL.Path {
				case "/apis/apps/v1/namespaces/kube-system/deployments/metrics-server":
					reads++
					if scenario == "revoked-during-read" {
						policies.mu.Lock()
						policies.revoked["alice"] = true
						policies.mu.Unlock()
					}
					code, reason := 404, "NotFound"
					if scenario == "component-forbidden" {
						code, reason = 403, "Forbidden"
					}
					w.WriteHeader(code)
					_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Status","reason":%q,"code":%d}`, reason, code)
				case "/api/v1/nodes":
					_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker"}}]}`)
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{}})
				}
			})
			service := newKubeTestService(t, 3*time.Second)
			id := createKubeTestCluster(t, service, server)
			service.authorizationResolver = policies.resolve
			result, err := service.Overview(identityTestContext(t, service, id, "alice"), id, "team", map[string]bool{"kubernetes.node.read": true, "kubernetes.workload.read": true})
			if scenario == "revoked-during-read" {
				if result != nil || !errors.Is(err, ErrRBACForbidden) {
					t.Fatalf("撤权后返回了旧数据: %+v %v", result, err)
				}
			} else if err != nil || result.MonitoringState != "unavailable" {
				t.Fatalf("无证据不得推测安装或指标权限: %+v %v", result, err)
			}
			if scenario == "namespace-denied" && reads != 0 {
				t.Fatal("越权读取 kube-system")
			}
		})
	}
}
