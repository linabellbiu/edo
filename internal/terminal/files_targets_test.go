package terminal

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/dockerengine"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/terminalfiles"
	"edo/internal/testutil"
)

func TestContainerFilesDockerPinsFullIDAndEndpointRevision(t *testing.T) {
	id := strings.Repeat("a", 64)
	var requested atomic.Value
	commands := make(chan []string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.54")
			fmt.Fprint(w, "OK")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/exec/file-exec/json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ID": "file-exec", "ContainerID": id, "Running": false, "ExitCode": 0})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/exec/file-exec/start") {
			var input struct {
				TTY bool `json:"Tty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.TTY {
				t.Error("文件协议使用了交互式 TTY")
			}
			connection, buffer, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer connection.Close()
			buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			payload := []byte("./a\x00file\x003\x001700000000\x00640\x00")
			header := make([]byte, 8)
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
			buffer.Write(header)
			buffer.Write(payload)
			buffer.Flush()
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/"+id+"/exec") {
			var input struct {
				Cmd        []string
				TTY        bool `json:"Tty"`
				Privileged bool
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.TTY || input.Privileged {
				t.Error("文件操作启用了 TTY 或特权")
			}
			commands <- input.Cmd
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Id":"file-exec"}`)
			return
		}
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			requested.Store(r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"Id": id, "State": map[string]any{"Running": true}})
			return
		}
		t.Errorf("未核验目标就创建 Exec: %s", r.URL.Path)
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()
	db := testutil.NewDatabase(t)
	manager, _ := secret.New("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	endpoint := model.DockerEndpoint{ID: "files-docker", Name: "files-docker", Host: strings.Replace(server.URL, "http://", "tcp://", 1), IsActive: true, CreatedBy: "test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(dockerengine.NewService(db, manager, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second}), nil, time.Hour)
	backend, err := service.OpenDockerFiles(context.Background(), endpoint.ID, "friendly-name")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if !strings.Contains(requested.Load().(string), id) {
		t.Fatal("未将显示名称解析为固定完整容器ID")
	}
	directory, err := backend.List(context.Background(), "/tmp/arg ' $(literal)")
	if err != nil || len(directory.Entries) != 1 || directory.Entries[0].Name != "a" {
		t.Fatalf("Docker 复用流协议失败: %+v %v", directory, err)
	}
	if command := <-commands; len(command) != 5 || command[2] != fileListScript || command[4] != "/tmp/arg ' $(literal)" {
		t.Fatal("路径未按固定命令独立参数传递")
	}
	// 此假目标不支持 ready 握手而直接结束，上传协程不能一直等许可。
	failed := make(chan error, 1)
	go func() {
		_, err := backend.Put(context.Background(), "/tmp/a", strings.NewReader("abc"), 3, "")
		failed <- err
	}()
	select {
	case err := <-failed:
		if !errors.Is(err, terminalfiles.ErrUnavailable) {
			t.Fatalf("目标早退后上传未安全失败: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("目标早退后上传协程仍等待提交许可")
	}
	<-commands
	if err := db.Model(&endpoint).Update("updated_at", time.Now().Add(time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := backend.List(context.Background(), "/tmp"); !errors.Is(err, terminalfiles.ErrConflict) {
		t.Fatalf("配置变化仍使用旧目标: %v", err)
	}
	if err := db.Model(&endpoint).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenDockerFiles(context.Background(), endpoint.ID, id); !errors.Is(err, dockerengine.ErrEndpointNotFound) {
		t.Fatalf("停用连接未拒绝: %v", err)
	}
}

func TestContainerFilesKubernetesUIDAndPermissionFence(t *testing.T) {
	var podUID atomic.Value
	podUID.Store("pod-fixed")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v1/namespaces/default/pods/app" {
			t.Errorf("错误资源路径: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"app","namespace":"default","uid":%q},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running","containerStatuses":[{"name":"app","containerID":"containerd://fixed","state":{"running":{}}}]}}`, podUID.Load().(string))
	}))
	defer server.Close()
	db := testutil.NewDatabase(t)
	manager, _ := secret.New("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	kubeconfig := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\n    certificate-authority-data: %s\nusers:\n- name: test\n  user:\n    token: test-token\ncontexts:\n- name: test\n  context:\n    cluster: test\n    user: test\ncurrent-context: test\n", server.URL, base64.StdEncoding.EncodeToString(ca))
	cipher, err := manager.Encrypt(kubeconfig, []byte("kubernetes_cluster:files-kube:kubeconfig"))
	if err != nil {
		t.Fatal(err)
	}
	cluster := model.KubernetesCluster{ID: "files-kube", Name: "files-kube", Mode: model.KubernetesKubeconfig, KubeconfigCiphertext: cipher, APIServer: server.URL, DefaultNamespace: "default", IsActive: true, CreatedBy: "test", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, kube.NewService(db, manager, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second}), time.Hour)
	if _, err := service.OpenKubernetesFiles(context.Background(), cluster.ID, "default", "app", "app", "wrong-uid"); !errors.Is(err, terminalfiles.ErrConflict) {
		t.Fatalf("旧UID未拒绝: %v", err)
	}
	backend, err := service.OpenKubernetesFiles(context.Background(), cluster.ID, "default", "app", "app", "pod-fixed")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	podUID.Store("pod-replaced")
	if _, err := backend.List(context.Background(), "/tmp"); !errors.Is(err, terminalfiles.ErrConflict) {
		t.Fatalf("同名Pod重建未拒绝: %v", err)
	}
	policy := model.KubernetesRBACPolicy{ClusterID: cluster.ID, Enabled: true, DesiredEnabled: true, Status: "synced"}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	before := requests.Load()
	if _, err := service.OpenKubernetesFiles(context.Background(), cluster.ID, "default", "app", "app", "pod-replaced"); !errors.Is(err, kube.ErrRBACForbidden) {
		t.Fatalf("无资源执行权限未拒绝: %v", err)
	}
	if after := requests.Load(); after != before {
		t.Fatal("权限拒绝后仍访问Pod")
	}
}
