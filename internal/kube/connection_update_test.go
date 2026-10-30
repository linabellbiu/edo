package kube

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/model"
)

func connectionUpdateSnapshot(t *testing.T, service *Service, id string) model.KubernetesCluster {
	t.Helper()
	cluster, err := service.Find(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return *cluster
}

func assertConnectionUpdateUnchanged(t *testing.T, before, after model.KubernetesCluster) {
	t.Helper()
	if before.ID != after.ID || before.Name != after.Name || before.Mode != after.Mode || before.APIServer != after.APIServer || before.DefaultNamespace != after.DefaultNamespace || before.KubeconfigCiphertext != after.KubeconfigCiphertext || before.ProjectID != after.ProjectID || before.DepartmentID != after.DepartmentID || !before.UpdatedAt.Equal(after.UpdatedAt) {
		t.Fatal("连接校验失败后修改了集群或原配置")
	}
}

func TestClusterUpdateRejectsUnverifiedReplacementWithoutWriting(t *testing.T) {
	for _, scenario := range []string{"unreachable", "forbidden", "invalid-version", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			service := newKubeTestService(t, time.Second)
			old := newKubeVersionServer(t)
			id := createKubeTestCluster(t, service, old)
			before := connectionUpdateSnapshot(t, service, id)
			var calls atomic.Int32
			candidate := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch scenario {
				case "forbidden":
					http.Error(w, "synthetic-private-diagnostic", http.StatusForbidden)
				case "invalid-version":
					_, _ = io.WriteString(w, `{"kind":"not-a-version"}`)
				case "deadline":
					<-r.Context().Done()
				default:
					_, _ = io.WriteString(w, `{"gitVersion":"v1.37.1"}`)
				}
			}))
			t.Cleanup(candidate.Close)
			value := kubeconfigForServer(candidate)
			if scenario == "unreachable" {
				candidate.Close()
			}
			ctx := context.Background()
			if scenario == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			_, err := service.Update(ctx, id, Input{Name: "must-not-save", Mode: model.KubernetesKubeconfig, DefaultNamespace: "changed", Kubeconfig: &value})
			if !errors.Is(err, ErrClusterUnreachable) {
				t.Fatalf("替换连接未通过真实测试却被接受，场景=%s，错误=%v", scenario, err)
			}
			if scenario != "unreachable" && calls.Load() != 1 {
				t.Fatalf("替换必须真实访问一次版本接口，次数=%d", calls.Load())
			}
			assertConnectionUpdateUnchanged(t, before, connectionUpdateSnapshot(t, service, id))
		})
	}
}

func TestClusterUpdateRetestsReplacementAfterSuccessfulPreview(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, newKubeVersionServer(t))
	before := connectionUpdateSnapshot(t, service, id)
	var reject atomic.Bool
	var calls atomic.Int32
	candidate := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if reject.Load() {
			http.Error(w, "synthetic-rejected", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"gitVersion":"v1.37.1"}`)
	}))
	defer candidate.Close()
	value := kubeconfigForServer(candidate)
	input := Input{Name: "previewed-update", Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &value}
	if _, err := service.Test(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	reject.Store(true)
	if _, err := service.Update(context.Background(), id, input); !errors.Is(err, ErrClusterUnreachable) {
		t.Fatalf("不能用先前预览代替保存时的真实测试: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("预览与保存必须各测试一次，次数=%d", calls.Load())
	}
	assertConnectionUpdateUnchanged(t, before, connectionUpdateSnapshot(t, service, id))
}

func TestClusterUpdateMetadataKeepsStoredConnectionOffline(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	old := newKubeVersionServer(t)
	id := createKubeTestCluster(t, service, old)
	before := connectionUpdateSnapshot(t, service, id)
	old.Close()
	updated, err := service.Update(context.Background(), id, Input{Name: "offline-metadata", Mode: model.KubernetesKubeconfig, DefaultNamespace: "existing-ns"})
	if err != nil {
		t.Fatalf("保留原配置的元数据修改不应访问网络: %v", err)
	}
	if updated.ID != before.ID || updated.APIServer != before.APIServer || updated.KubeconfigCiphertext != before.KubeconfigCiphertext || updated.Name != "offline-metadata" || updated.DefaultNamespace != "existing-ns" {
		t.Fatal("元数据修改未保留原集群身份和密文")
	}
}

func TestClusterUpdateReplacementUsesFreshVersionAndSameIdentity(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, newKubeVersionServer(t))
	before := connectionUpdateSnapshot(t, service, id)
	var calls atomic.Int32
	candidate := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			t.Errorf("连接测试读取了非版本接口: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		_, _ = io.WriteString(w, `{"gitVersion":"v1.37.1"}`)
	}))
	defer candidate.Close()
	value := kubeconfigForServer(candidate)
	updated, err := service.Update(context.Background(), id, Input{Name: "verified-update", Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &value})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || updated.ID != before.ID || updated.APIServer != candidate.URL || updated.KubeconfigCiphertext == before.KubeconfigCiphertext {
		t.Fatal("替换未真实测试新连接或改变了集群身份")
	}
}

func TestClusterUpdateReplacementRejectsConcurrentChangeDuringProbe(t *testing.T) {
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, newKubeVersionServer(t))
	before := connectionUpdateSnapshot(t, service, id)
	probeErrors := make(chan error, 1)
	candidate := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeErrors <- service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", id).Updates(map[string]any{"api_server": "https://concurrent.example.test", "updated_at": before.UpdatedAt.Add(time.Second)}).Error
		_, _ = io.WriteString(w, `{"gitVersion":"v1.37.1"}`)
	}))
	defer candidate.Close()
	value := kubeconfigForServer(candidate)
	if _, err := service.Update(context.Background(), id, Input{Name: "must-not-overwrite", Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &value}); !errors.Is(err, ErrClusterHostCheckUnavailable) {
		t.Fatalf("真实测试期间的并发连接变化未被拒绝: %v", err)
	}
	if probeWriteErr := <-probeErrors; probeWriteErr != nil {
		t.Fatal(probeWriteErr)
	}
	after := connectionUpdateSnapshot(t, service, id)
	if after.APIServer != "https://concurrent.example.test" || after.Name != before.Name || after.KubeconfigCiphertext != before.KubeconfigCiphertext {
		t.Fatal("连接测试后覆盖了其他请求的并发修改")
	}
}
