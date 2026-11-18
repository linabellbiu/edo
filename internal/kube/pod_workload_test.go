package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestPodWorkloadDetailFollowsExactControllerUIDs(t *testing.T) {
	const podPath = "/api/v1/namespaces/team/pods/component-abc"
	const replicaPath = "/apis/apps/v1/namespaces/team/replicasets/revision-name"
	const deploymentPath = "/apis/apps/v1/namespaces/team/deployments/workload-name"
	for _, test := range []struct {
		name            string
		podOwners       string
		replicaUID      string
		deploymentUID   string
		wantDeployment  bool
		wantConflict    bool
		wantReplicaRead bool
	}{
		{
			name: "真实控制器归属", podOwners: `[{"kind":"ReplicaSet","apiVersion":"apps/v1","name":"revision-name","uid":"rs-a","controller":true}]`,
			replicaUID: "rs-a", deploymentUID: "deployment-a", wantDeployment: true, wantReplicaRead: true,
		},
		{
			name: "独立 Pod", podOwners: "[]",
		},
		{
			name: "关联引用不是控制器", podOwners: `[{"kind":"ReplicaSet","apiVersion":"apps/v1","name":"revision-name","uid":"rs-a"}]`,
		},
		{
			name: "同名副本集已重建", podOwners: `[{"kind":"ReplicaSet","apiVersion":"apps/v1","name":"revision-name","uid":"rs-a","controller":true}]`,
			replicaUID: "rs-new", wantConflict: true, wantReplicaRead: true,
		},
		{
			name: "同名 Deployment 已重建", podOwners: `[{"kind":"ReplicaSet","apiVersion":"apps/v1","name":"revision-name","uid":"rs-a","controller":true}]`,
			replicaUID: "rs-a", deploymentUID: "deployment-new", wantConflict: true, wantReplicaRead: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var replicaReads atomic.Int32
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET " + podPath:
					_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"component-abc","namespace":"team","uid":"pod-a","ownerReferences":%s}}`, test.podOwners)
				case "GET " + replicaPath:
					replicaReads.Add(1)
					_, _ = fmt.Fprintf(w, `{"apiVersion":"apps/v1","kind":"ReplicaSet","metadata":{"name":"revision-name","namespace":"team","uid":%q,"ownerReferences":[{"kind":"Deployment","apiVersion":"apps/v1","name":"workload-name","uid":"deployment-a","controller":true}]}}`, test.replicaUID)
				case "GET " + deploymentPath:
					_, _ = fmt.Fprintf(w, `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"workload-name","namespace":"team","uid":%q}}`, test.deploymentUID)
				default:
					t.Error("非预期的 Kubernetes 请求", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","reason":"NotFound","code":404}`)
				}
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			result, err := service.GetPodWorkloadDetail(context.Background(), id, "team", "component-abc")
			if (replicaReads.Load() == 1) != test.wantReplicaRead {
				t.Fatalf("副本集读取次数=%d", replicaReads.Load())
			}
			if test.wantConflict {
				if !errors.Is(err, ErrResourceConflict) {
					t.Fatalf("应拒绝同名重建的控制器: %v", err)
				}
				return
			}
			if err != nil || result == nil || (result.Deployment != nil) != test.wantDeployment {
				t.Fatalf("归属结果不匹配: %v, %v", result, err)
			}
			if result.Deployment != nil {
				metadata := result.Deployment.Object["metadata"].(map[string]any)
				if metadata["uid"] != "deployment-a" || result.Deployment.ReplicaSetUID != "rs-a" {
					t.Fatal("返回的控制器身份错误", metadata, result.Deployment.ReplicaSetUID)
				}
			}
		})
	}
}
