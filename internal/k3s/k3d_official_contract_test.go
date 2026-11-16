package k3s

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// 使用官方二进制和隔离 Docker API 验证真实请求，不访问任何实际 daemon 或集群。
func TestK3dOfficialCLILocalCreationDoesNotBindClientFiles(t *testing.T) {
	binary := os.Getenv("EDO_TEST_K3D_BINARY")
	if binary == "" {
		t.Skip("需要通过 EDO_TEST_K3D_BINARY 指定已核验摘要的官方 k3d 工具")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var mu sync.Mutex
	var binds []string
	var captured bool
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := r.URL.Path
		if strings.HasPrefix(path, "/v1.") {
			_, path, _ = strings.Cut(path[1:], "/")
			path = "/" + path
		}
		requests = append(requests, r.Method+" "+path)
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch {
		case path == "/_ping":
			w.Header().Set("API-Version", "1.45")
			_, _ = w.Write([]byte("OK"))
			return
		case path == "/version":
			result = map[string]any{"ApiVersion": "1.45", "Os": "linux", "Arch": "arm64", "Version": "29.8.2"}
		case path == "/info":
			result = map[string]any{"OSType": "linux", "CgroupVersion": "2"}
		case path == "/containers/json" || path == "/networks":
			result = []any{}
		case path == "/networks/create":
			result = map[string]any{"Id": "fixture-network"}
		case path == "/networks/fixture-network":
			result = map[string]any{"Id": "fixture-network", "Name": "fixture-network", "IPAM": map[string]any{"Config": []any{map[string]string{"Subnet": "172.25.0.0/16", "Gateway": "172.25.0.1"}}}}
		case path == "/volumes/create":
			result = map[string]any{"Name": "fixture-images"}
		case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			result = map[string]any{"Id": "sha256:fixture", "Os": "linux", "Architecture": "arm64", "Config": map[string]any{}}
		case path == "/containers/create":
			if strings.HasSuffix(r.URL.Query().Get("name"), "-server-0") {
				var body struct {
					HostConfig struct{ Binds []string }
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				captured, binds = true, body.HostConfig.Binds
			}
			// 在记录创建请求后主动中止；模拟 API 不创建或启动任何资源。
			w.WriteHeader(http.StatusInternalServerError)
			result = map[string]string{"message": "隔离契约测试已记录请求"}
		default:
			w.WriteHeader(http.StatusNotFound)
			result = map[string]string{"message": "未登记的隔离 Docker 请求"}
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	input := Input{Endpoint: "https://docker.test:16443", K3d: &K3dOptions{ImageSource: "custom", Image: "registry.test/k3s:fixture", APIPort: 16443, BindAddress: "0.0.0.0", ServerResources: &K3dNodeResources{2000, 4000}, WorkerResources: &K3dNodeResources{1000, 1024}}}
	args := k3dCreateArguments(input, "1ec72535-b388-4165-82d8-10cafb3f8793", false)
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = []string{"HOME=" + t.TempDir(), "DOCKER_HOST=" + strings.Replace(server.URL, "http://", "tcp://", 1), "DOCKER_API_VERSION=1.45"}
	output, err := command.CombinedOutput()
	mu.Lock()
	defer mu.Unlock()
	if ctx.Err() != nil || err == nil || !captured {
		t.Fatalf("官方 CLI 未到达受控创建请求: err=%v requests=%v output=%s", err, requests, output)
	}
	if len(binds) != 1 || !strings.HasSuffix(binds[0], "-images:/k3d/images") {
		t.Fatalf("官方 CLI 增加了镜像卷以外的挂载: %v", binds)
	}
}
