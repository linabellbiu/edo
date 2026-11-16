package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// 官方二进制只连接内存中的 Docker API；所有删除请求都只修改测试夹具。
func TestK3dOfficialCLICleanupContract(t *testing.T) {
	binary := os.Getenv("EDO_TEST_K3D_BINARY")
	if binary == "" {
		t.Skip("需要通过 EDO_TEST_K3D_BINARY 指定已核验摘要的官方 k3d 工具")
	}
	for _, tc := range []struct {
		name, role                             string
		failContainer, failNetwork, failVolume bool
		remainingContainers                    int
		remainingNetworks, remainingVolumes    int
	}{
		{name: "created_server", role: "server"},
		{name: "container_delete_warning", role: "server", failContainer: true, remainingContainers: 1, remainingNetworks: 1, remainingVolumes: 1},
		{name: "network_delete_warning", role: "server", failNetwork: true, remainingNetworks: 1},
		{name: "volume_delete_warning", role: "server", failVolume: true, remainingVolumes: 1},
		{name: "network_and_volume_warnings", role: "server", failNetwork: true, failVolume: true, remainingNetworks: 1, remainingVolumes: 1},
		{name: "no_nodes", remainingNetworks: 1, remainingVolumes: 1},
		{name: "tools_only", role: "noRole", remainingNetworks: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newK3dOfficialCleanupFixture(tc.role)
			fixture.failContainer, fixture.failNetwork, fixture.failVolume = tc.failContainer, tc.failNetwork, tc.failVolume
			server := httptest.NewServer(fixture)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			isolated := t.TempDir()
			output, err := runK3dOfficialCleanup(ctx, binary, server.URL, fixture.cluster, isolated)
			if err != nil || ctx.Err() != nil {
				t.Fatalf("官方删除未按契约成功退出: err=%v output=%s requests=%v", err, output, fixture.requests)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.unexpected) != 0 {
				t.Fatalf("官方删除出现未登记请求: %v", fixture.unexpected)
			}
			remaining := fixture.inventory()
			if len(remaining.Containers) != tc.remainingContainers || len(remaining.Networks) != tc.remainingNetworks || len(remaining.Volumes) != tc.remainingVolumes || fixture.anonymous != tc.failContainer {
				t.Fatalf("实际剩余资源不符: containers=%d networks=%d volumes=%d anonymous=%v requests=%v", len(remaining.Containers), len(remaining.Networks), len(remaining.Volumes), fixture.anonymous, fixture.requests)
			}
			// 上游 CLI 对删除错误和部分创建现场仍返回 0，业务后验必须据实际资源拒绝成功。
			verifyErr := verifyK3dCleanupResult(fixture.daemon, remaining, err)
			wantFailure := tc.remainingContainers+tc.remainingNetworks+tc.remainingVolumes > 0
			if (verifyErr != nil) != wantFailure {
				t.Fatalf("官方退出码与业务后验混淆: remaining=%+v verifyErr=%v output=%s", remaining, verifyErr, output)
			}
			if tc.role != "" && !slices.Contains(fixture.requests, "DELETE /containers/"+fixture.nodeName) {
				t.Fatalf("未删除实际节点（包括 Created/tools）: %v", fixture.requests)
			}
			if tc.role == "" {
				for _, request := range fixture.requests {
					if strings.HasPrefix(request, "DELETE ") {
						t.Fatalf("无节点场景意外清理了网络或卷: %v", fixture.requests)
					}
				}
			}
			if tc.role == "noRole" && slices.Contains(fixture.requests, "DELETE /networks/"+fixture.networkName) {
				t.Fatalf("tools-only 场景意外恢复了不存在的网络标签: %v", fixture.requests)
			}
		})
	}
}

func TestK3dOfficialCLICleanupIsolatesKubeconfig(t *testing.T) {
	binary := os.Getenv("EDO_TEST_K3D_BINARY")
	if binary == "" {
		t.Skip("需要通过 EDO_TEST_K3D_BINARY 指定已核验摘要的官方 k3d 工具")
	}
	fixture := newK3dOfficialCleanupFixture("server")
	server := httptest.NewServer(fixture)
	defer server.Close()
	userHome, userConfig, isolated := t.TempDir(), t.TempDir(), t.TempDir()
	defaultConfig := filepath.Join(userHome, ".kube", "config")
	inheritedConfig := filepath.Join(userConfig, "active.yaml")
	standalone := filepath.Join(userConfig, "kubeconfig-"+fixture.cluster+".yaml")
	content := k3dOfficialCleanupConfig(fixture.cluster)
	for _, name := range []string{defaultConfig, inheritedConfig, standalone} {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟用户原有环境，受测 CLI 必须只改自己的临时配置。
	t.Setenv("HOME", userHome)
	t.Setenv("KUBECONFIG", inheritedConfig)
	t.Setenv("XDG_CONFIG_HOME", userConfig)
	isolatedConfig := filepath.Join(isolated, "config")
	isolatedStandalone := filepath.Join(isolated, "kubeconfig-"+fixture.cluster+".yaml")
	for _, name := range []string{isolatedConfig, isolatedStandalone} {
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := runK3dOfficialCleanup(ctx, binary, server.URL, fixture.cluster, isolated)
	if err != nil {
		t.Fatalf("官方删除失败: %v output=%s", err, output)
	}
	for _, name := range []string{defaultConfig, inheritedConfig, standalone} {
		actual, err := os.ReadFile(name)
		if err != nil || string(actual) != content {
			t.Fatalf("用户原有配置被改动: file=%s err=%v", name, err)
		}
	}
	actual, err := os.ReadFile(isolatedConfig)
	if err != nil || strings.Contains(string(actual), "k3d-"+fixture.cluster) || !strings.Contains(string(actual), "unrelated-context") {
		t.Fatalf("未验证官方确实修改了隔离配置或误删其他上下文: err=%v content=%s output=%s", err, actual, output)
	}
	if _, err := os.Stat(isolatedStandalone); !os.IsNotExist(err) {
		t.Fatalf("官方未删除隔离的独立 kubeconfig: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !fixture.inventory().empty() || fixture.anonymous || len(fixture.unexpected) != 0 {
		t.Fatalf("隔离删除未完成: requests=%v unexpected=%v", fixture.requests, fixture.unexpected)
	}
}

func runK3dOfficialCleanup(ctx context.Context, binary, address, cluster, directory string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, "cluster", "delete", cluster)
	command.Env = []string{
		"HOME=" + directory,
		"XDG_CONFIG_HOME=" + directory,
		"KUBECONFIG=" + filepath.Join(directory, "config"),
		"DOCKER_HOST=" + strings.Replace(address, "http://", "tcp://", 1),
		"DOCKER_API_VERSION=1.45",
	}
	return command.CombinedOutput()
}

func k3dOfficialCleanupConfig(cluster string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: k3d-%[1]s
  cluster:
    server: https://isolated.invalid
- name: unrelated-cluster
  cluster:
    server: https://unrelated.invalid
contexts:
- name: k3d-%[1]s
  context:
    cluster: k3d-%[1]s
    user: admin@k3d-%[1]s
- name: unrelated-context
  context:
    cluster: unrelated-cluster
    user: unrelated-user
users:
- name: admin@k3d-%[1]s
  user: {}
- name: unrelated-user
  user: {}
current-context: k3d-%[1]s
`, cluster)
}

type k3dOfficialCleanupFixture struct {
	mu                                                sync.Mutex
	installation, cluster, daemon, nodeName, nodeID   string
	networkName, networkID, volumeName, anonymousName string
	role                                              string
	node, network, volume, anonymous                  bool
	failContainer, failNetwork, failVolume            bool
	requests, unexpected                              []string
}

func newK3dOfficialCleanupFixture(role string) *k3dOfficialCleanupFixture {
	id := "1ec72535-b388-4165-82d8-10cafb3f8793"
	cluster := k3dResourceName(id)
	network := "k3d-" + cluster
	node := network + "-server-0"
	if role == "noRole" {
		node = network + "-tools"
	}
	return &k3dOfficialCleanupFixture{
		installation: id, cluster: cluster, daemon: "isolated-fixture-daemon", role: role,
		nodeName: node, nodeID: strings.Repeat("1", 64), networkName: network, networkID: strings.Repeat("2", 64),
		volumeName: k3dImageVolumeName(id), anonymousName: strings.Repeat("3", 64),
		node: role != "", network: true, volume: true, anonymous: role == "server",
	}
}

func (f *k3dOfficialCleanupFixture) labels() map[string]string {
	labels := map[string]string{"app": "k3d", "k3d.version": "v5.9.0", "k3d.cluster": f.cluster, "k3d.role": f.role}
	if f.role == "server" {
		labels["edo.dev/installation"] = f.installation
		labels["k3d.cluster.network"] = f.networkName
		labels["k3d.cluster.network.id"] = f.networkID
		labels["k3d.cluster.network.external"] = "false"
		labels["k3d.cluster.imageVolume"] = f.volumeName
	}
	return labels
}

func (f *k3dOfficialCleanupFixture) inventory() *k3dCleanupInventory {
	result := &k3dCleanupInventory{DaemonID: f.daemon}
	if f.node {
		labels := f.labels()
		result.Containers = append(result.Containers, k3dCleanupContainer{ID: f.nodeID, Name: f.nodeName, App: "k3d", Cluster: f.cluster, Installation: labels["edo.dev/installation"], Role: f.role, Network: labels["k3d.cluster.network"], NetworkID: labels["k3d.cluster.network.id"], NetworkExternal: labels["k3d.cluster.network.external"], ImageVolume: labels["k3d.cluster.imageVolume"]})
	}
	if f.network {
		result.Networks = append(result.Networks, k3dCleanupNetwork{ID: f.networkID, Name: f.networkName, App: "k3d"})
	}
	if f.volume {
		result.Volumes = append(result.Volumes, k3dCleanupVolume{Name: f.volumeName, App: "k3d", Cluster: f.cluster})
	}
	if f.anonymous {
		result.AnonymousVolumes = append(result.AnonymousVolumes, k3dCleanupVolume{Name: f.anonymousName, Driver: "local", CreatedAt: "2026-11-16T00:00:00Z"})
	}
	return result
}

func (f *k3dOfficialCleanupFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	if strings.HasPrefix(path, "/v1.") {
		_, path, _ = strings.Cut(path[1:], "/")
		path = "/" + path
	}
	request := r.Method + " " + path
	f.requests = append(f.requests, request)
	w.Header().Set("Content-Type", "application/json")
	var result any
	switch {
	case path == "/_ping":
		w.Header().Set("API-Version", "1.45")
		_, _ = w.Write([]byte("OK"))
		return
	case r.Method == http.MethodGet && path == "/version":
		result = map[string]string{"ApiVersion": "1.45", "Os": "linux", "Arch": "arm64", "Version": "29.8.2"}
	case r.Method == http.MethodGet && path == "/info":
		result = map[string]string{"ID": f.daemon, "OSType": "linux", "CgroupVersion": "2"}
	case r.Method == http.MethodGet && path == "/containers/json":
		var filters map[string]map[string]bool
		_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
		if r.URL.Query().Get("all") != "1" || !filters["label"]["app=k3d"] || !filters["label"]["k3d.cluster="+f.cluster] {
			f.unexpected = append(f.unexpected, request+"?"+r.URL.RawQuery)
		}
		result = []any{}
		if f.node {
			result = []any{map[string]any{"Id": f.nodeID, "Names": []string{"/" + f.nodeName}, "Labels": f.labels(), "State": "created"}}
		}
	case r.Method == http.MethodGet && path == "/containers/"+f.nodeID+"/json" && f.node:
		mounts := []any{map[string]any{"Type": "volume", "Name": f.volumeName, "Destination": "/k3d/images"}}
		if f.anonymous {
			mounts = append(mounts, map[string]any{"Type": "volume", "Name": f.anonymousName, "Destination": "/var/lib/rancher/k3s"})
		}
		result = map[string]any{
			"Id": f.nodeID, "Name": "/" + f.nodeName, "State": map[string]any{"Status": "created", "Running": false},
			"Config": map[string]any{"Labels": f.labels()}, "HostConfig": map[string]any{"RestartPolicy": map[string]string{"Name": "unless-stopped"}, "Binds": []string{f.volumeName + ":/k3d/images"}},
			"NetworkSettings": map[string]any{"Networks": map[string]any{f.networkName: map[string]string{"NetworkID": f.networkID}}}, "Mounts": mounts,
		}
	case r.Method == http.MethodDelete && path == "/containers/"+f.nodeName && f.node:
		if f.failContainer {
			w.WriteHeader(http.StatusInternalServerError)
			result = map[string]string{"message": "隔离夹具模拟容器删除失败"}
			break
		}
		if r.URL.Query().Get("force") != "1" || r.URL.Query().Get("v") != "1" {
			f.unexpected = append(f.unexpected, request+"?"+r.URL.RawQuery)
		} else {
			f.anonymous = false
		}
		f.node = false
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method == http.MethodGet && path == "/volumes":
		var filters map[string]map[string]bool
		_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
		if !filters["label"]["app=k3d"] || !filters["label"]["k3d.cluster="+f.cluster] {
			f.unexpected = append(f.unexpected, request+"?"+r.URL.RawQuery)
		}
		volumes := []any{}
		if f.volume {
			volumes = append(volumes, map[string]any{"Name": f.volumeName, "Labels": map[string]string{"app": "k3d", "k3d.cluster": f.cluster}})
		}
		result = map[string]any{"Volumes": volumes, "Warnings": []string{}}
	case r.Method == http.MethodGet && path == "/volumes/"+f.volumeName && f.volume:
		references := 0
		if f.node {
			references = 1
		}
		result = map[string]any{"Name": f.volumeName, "UsageData": map[string]int{"RefCount": references}}
	case r.Method == http.MethodDelete && path == "/volumes/"+f.volumeName && f.volume:
		if f.failVolume {
			w.WriteHeader(http.StatusInternalServerError)
			result = map[string]string{"message": "隔离夹具模拟卷删除失败"}
		} else {
			f.volume = false
			w.WriteHeader(http.StatusNoContent)
			return
		}
	case r.Method == http.MethodDelete && path == "/networks/"+f.networkName && f.network:
		if f.failNetwork || f.node {
			w.WriteHeader(http.StatusInternalServerError)
			result = map[string]string{"message": "隔离夹具模拟网络删除失败"}
		} else {
			f.network = false
			w.WriteHeader(http.StatusNoContent)
			return
		}
	default:
		f.unexpected = append(f.unexpected, request)
		w.WriteHeader(http.StatusNotFound)
		result = map[string]string{"message": "未登记的隔离 Docker 请求"}
	}
	_ = json.NewEncoder(w).Encode(result)
}
