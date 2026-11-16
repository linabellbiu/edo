package k3s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm/clause"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

type localCLIFixture struct {
	mu                                   sync.Mutex
	revision, daemon, foreign, ports, id string
	architecture                         string
	created, wrongOwner, wrongResources  bool
	separateFilesystems, failUpdate      bool
	calls                                [][]string
	config                               string
	server, worker                       K3dNodeResources
	limits                               map[string]K3dNodeResources
}

func (f *localCLIFixture) LocalCLIEnvironment(context.Context) ([]string, string, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []string{"PATH=/fixture", "DOCKER_HOST=tcp://docker.test:2376", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/fixture/certs"}, f.revision, func() {}, nil
}
func (f *localCLIFixture) run(ctx context.Context, binary string, args, env []string, out, diagnostic io.Writer) error {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{binary}, args...))
	joined := strings.Join(args, " ")
	write := func(value string) error { _, err := io.WriteString(out, value); return err }
	switch {
	case joined == "info --format {{json .}}":
		daemon := f.daemon
		f.mu.Unlock()
		return write(fmt.Sprintf(`{"ID":%q,"OSType":"linux","Architecture":"aarch64","ServerVersion":"29.8.2","NCPU":16,"MemTotal":34359738368}`, daemon))
	case strings.HasPrefix(joined, "container ls"):
		foreign, ports := f.foreign, f.ports
		f.mu.Unlock()
		if foreign != "" {
			return write(fmt.Sprintf(`{"Names":"k3d-other-server-0","Labels":"k3d.cluster=other,edo.dev/installation=%s","Ports":""}`, foreign))
		}
		if ports != "" {
			return write(fmt.Sprintf(`{"Names":"existing","Labels":"","Ports":%q}`, ports))
		}
		return nil
	case joined == "version --format {{json .Server}}":
		architecture := f.architecture
		f.mu.Unlock()
		if architecture == "" {
			architecture = "arm64"
		}
		return write(fmt.Sprintf(`{"Arch":%q,"Components":[{"Name":"runc","Version":"1.3.5"}]}`, architecture))
	case joined == "volume ls --format {{.Name}}":
		f.mu.Unlock()
		return nil
	case len(args) > 2 && args[0] == "cluster" && args[1] == "create":
		if f.separateFilesystems {
			for _, arg := range args {
				if arg == "--servers-memory" || arg == "--agents-memory" {
					f.mu.Unlock()
					_, _ = io.WriteString(diagnostic, "error mounting CLI meminfo to /proc/meminfo: not a directory\n")
					return errors.New("Docker 服务端无法读取 CLI 本地文件")
				}
			}
		}
		f.created = true
		name := args[2]
		for i, arg := range args {
			if arg == "--runtime-label" {
				f.id = strings.TrimSuffix(strings.TrimPrefix(args[i+1], "edo.dev/installation="), "@all")
			}
		}
		f.mu.Unlock()
		return write("INFO Pulling image 'registry.test/rancher/k3s:fixture'\n{\"status\":\"Downloading\",\"id\":\"layer-a\"}\rINFO Created cluster " + name + "\nClusterToken: generated-fixture-secret\n")
	case strings.HasPrefix(joined, "volume inspect"):
		id := f.id
		f.mu.Unlock()
		return write(k3dImageVolumeName(id) + " " + k3dResourceName(id) + "\n")
	case len(args) > 2 && args[0] == "inspect":
		id := f.id
		owner := id
		if f.wrongOwner {
			owner = "other-installation"
		}
		limits := make(map[string]K3dNodeResources, len(f.limits))
		for id, resource := range f.limits {
			limits[id] = resource
		}
		wrong := f.wrongResources
		f.mu.Unlock()
		for i, name := range args[3:] {
			containerID := strings.Repeat(fmt.Sprint(i+1), 64)
			resource := limits[containerID]
			if wrong {
				resource.CPUMillis++
			}
			node := localK3dContainer{ID: containerID, Name: "/" + name, Labels: map[string]string{"edo.dev/installation": owner, "k3d.cluster": k3dResourceName(id)}, CPU: resource.CPUMillis * 1000000, Memory: resource.MemoryMiB * 1024 * 1024, Swap: resource.MemoryMiB * 1024 * 1024}
			data, _ := json.Marshal(node)
			if err := write(string(data) + "\n"); err != nil {
				return err
			}
		}
		return nil
	case len(args) > 0 && args[0] == "update":
		if f.failUpdate {
			f.mu.Unlock()
			return errors.New("模拟 Docker 资源设置失败")
		}
		id := args[len(args)-1]
		resource := f.worker
		if id == strings.Repeat("1", 64) {
			resource = f.server
		}
		expected := []string{"update", "--cpus", k3dCPUArgument(resource.CPUMillis), "--memory", k3dMemoryArgument(resource.MemoryMiB), "--memory-swap", k3dMemoryArgument(resource.MemoryMiB), id}
		if !f.created || strings.Join(args, " ") != strings.Join(expected, " ") {
			f.mu.Unlock()
			return fmt.Errorf("资源设置不符合配置快照: %s", joined)
		}
		if f.limits == nil {
			f.limits = make(map[string]K3dNodeResources)
		}
		f.limits[id] = resource
		f.mu.Unlock()
		return write(args[len(args)-1] + "\n")
	case len(args) > 0 && args[0] == "logs":
		f.mu.Unlock()
		_ = write("2026-11-16T10:00:00.000Z Starting k3s\n")
		<-ctx.Done()
		return ctx.Err()
	case strings.HasPrefix(joined, "kubeconfig get"):
		config := f.config
		f.mu.Unlock()
		return write(config)
	default:
		f.mu.Unlock()
		return fmt.Errorf("未登记的隔离 CLI 调用: %s", joined)
	}
}
func localK3dFixture(t *testing.T) (fixture, *localCLIFixture, Input) {
	t.Helper()
	f := newFixture(t)
	scope, _ := database.DepartmentScopeFromContext(f.ctx)
	host := model.Host{ID: model.BuiltinLocalHostID, Name: "本地主机", Mode: model.HostModeLocal, Address: "localhost", IsBuiltin: true, IsActive: true, DepartmentID: scope.DepartmentID, ProjectID: scope.ProjectID, CreatedBy: f.actor.ID}
	if err := f.db.WithContext(f.ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "uuid"}}, DoUpdates: clause.AssignmentColumns([]string{"name", "mode", "address", "is_builtin", "is_active", "department_id", "project_id"})}).Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(f.ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "host_id"}, {Name: "kind"}}, DoUpdates: clause.AssignmentColumns([]string{"status"})}).Create(&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityDocker, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	cli := &localCLIFixture{revision: "runtime-revision", daemon: "daemon-id", config: f.transport.kubeconfig, server: K3dNodeResources{1750, 2048}, worker: K3dNodeResources{500, 1024}, separateFilesystems: true}
	runtime := newLocalK3dRuntime(cli)
	runtime.runCommand = cli.run
	path := filepath.Join(t.TempDir(), "k3d")
	data := []byte("fixture official k3d tool")
	if err := os.WriteFile(path, data, 0755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	runtime.tool = &localK3dTool{path: path, url: "https://fixture.invalid/official-k3d", hash: hex.EncodeToString(digest[:])}
	f.service.localK3d = runtime
	f.service.k3dWaitInterval = 5 * time.Millisecond
	input := Input{Action: "create", Mode: "k3d", Profile: "default", Name: "本地容器测试", ServerHostID: host.ID, K3d: &K3dOptions{ImageSource: "dockerhub", Image: "docker.io/rancher/k3s:v1.37.1-k3s1", APIPort: 16443, BindAddress: "0.0.0.0", ServerResources: &cli.server, WorkerResources: &cli.worker}}
	return f, cli, input
}
func TestLocalK3dPreviewCheckAndSnapshotDoNotNeedSSHOrSudo(t *testing.T) {
	f, cli, input := localK3dFixture(t)
	info, err := f.service.LocalK3dInfo(f.ctx, f.actor.ID)
	if err != nil || info.APIHost != "docker.test" || info.BindAddress != "0.0.0.0" || info.Architecture != "arm64" {
		t.Fatal(info, err)
	}
	report, err := f.service.Check(f.ctx, f.actor.ID, input)
	if err != nil || !report.Passed || report.Hosts[0].Privilege != nil {
		t.Fatal("本地检测错误地依赖 SSH 或 sudo", report, err)
	}
	var count int64
	f.db.Model(&model.K3sInstallation{}).Count(&count)
	if count != 0 {
		t.Fatal("只读检测创建了任务")
	}
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil || plan.Endpoint != "https://docker.test:16443" || len(plan.Hosts) != 1 || plan.Hosts[0].ID != model.BuiltinLocalHostID {
		t.Fatal(plan, err)
	}
	var record model.K3sInstallation
	f.db.First(&record, "uuid = ?", plan.ID)
	snapshot, err := f.service.decode(record)
	if err != nil || snapshot.LocalK3d == nil || snapshot.LocalK3d.DaemonID != cli.daemon || snapshot.LocalK3d.Revision != cli.revision {
		t.Fatal("没有冻结本地 Docker 身份", err)
	}
	if f.transport.privileges != 0 || len(f.transport.writes) != 0 || f.transport.reads != 0 {
		t.Fatal("本地请求误走 SSH")
	}
	for _, args := range cli.calls {
		if strings.Contains(strings.Join(args, " "), "cluster create") {
			t.Fatal("检测或预览创建了集群")
		}
	}
}
func TestLocalK3dRejectsOtherProjectDisabledCapabilityAndNativeInstall(t *testing.T) {
	for _, scenario := range []string{"project", "inactive", "capability", "native"} {
		t.Run(scenario, func(t *testing.T) {
			f, cli, input := localK3dFixture(t)
			switch scenario {
			case "project":
				scope, _ := database.DepartmentScopeFromContext(f.ctx)
				scope.ProjectID = "foreign"
				f.ctx = database.WithDepartmentScope(context.Background(), scope)
			case "inactive":
				f.db.WithContext(f.ctx).Model(&model.Host{}).Where("uuid = ?", model.BuiltinLocalHostID).Update("is_active", false)
			case "capability":
				f.db.WithContext(f.ctx).Model(&model.HostCapability{}).Where("host_id = ? AND kind = ?", model.BuiltinLocalHostID, model.HostCapabilityDocker).Update("status", model.HostCapabilityUnreachable)
			case "native":
				input = f.input()
				input.ServerHostID = model.BuiltinLocalHostID
			}
			if _, err := f.service.Preview(f.ctx, f.actor.ID, input); err == nil {
				t.Fatal("错误地允许了本地安装")
			}
			if len(cli.calls) != 0 || len(f.transport.writes) != 0 {
				t.Fatal("边界拒绝前访问了运行时")
			}
		})
	}
}
func TestLocalK3dOfficialCreationLogsResourcesAndIndependentImport(t *testing.T) {
	f, cli, input := localK3dFixture(t)
	clusters := fakeClusterClient(t, f)
	f.service.kubernetes = clusters
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	nodes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("labelSelector") != "edo.dev/installation="+plan.ID {
			t.Error("没有按准确安装选择节点")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"server"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"worker"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`)
	}))
	defer nodes.Close()
	clusters.client, err = kubernetes.NewForConfig(&rest.Config{Host: nodes.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	f.service.runInstallation(ctx, *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || result.Status != "succeeded" || clusters.created != 1 || len(clusters.linked) != 0 {
		t.Fatalf("本地安装没有正确接入: status=%s error=%s err=%v", result.Status, result.Error, err)
	}
	if !strings.Contains(result.Logs, "Downloading") || strings.Contains(result.Logs, "generated-fixture-secret") {
		t.Fatal("本地真实输出没有安全保存")
	}
	if len(f.transport.writes) != 0 || f.transport.privileges != 0 {
		t.Fatal("本地安装调用了 SSH")
	}
	updates := 0
	for _, args := range cli.calls {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " update ") {
			updates++
		}
		if strings.Contains(joined, "delete") || strings.Contains(joined, "--kubeconfig-update-default=true") || strings.Contains(joined, "docker pull") {
			t.Fatal("增加了非官方或破坏性操作", joined)
		}
	}
	if updates != 2 {
		t.Fatal("没有对 Server 与 Worker 分别核验资源限制", updates)
	}
}
func TestLocalK3dConnectionChangeInvalidatesPlanAndExistingClusterBlocks(t *testing.T) {
	f, cli, input := localK3dFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	cli.revision = "changed-runtime"
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrHostChanged) {
		t.Fatal("连接变化后仍执行旧计划", err)
	}
	cli.foreign = "existing-installation"
	report, err := f.service.Check(f.ctx, f.actor.ID, input)
	if err != nil || report.Passed {
		t.Fatal("已有本地集群仍允许重复安装", err)
	}
}

func TestLocalK3dSeparateFilesystemsKeepActualNodeLimits(t *testing.T) {
	for _, agents := range []int{0, 2} {
		t.Run(fmt.Sprint(agents), func(t *testing.T) {
			f, cli, input := localK3dFixture(t)
			input.K3d.Agents = &agents
			_, _, _, _, _, err := f.service.normalizeK3d(f.ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			id := "1ec72535-b388-4165-82d8-10cafb3f8793"
			if err := f.service.localK3d.create(f.ctx, input, id, io.Discard); err != nil {
				t.Fatal("独立 Docker 文件系统导致创建失败", err)
			}
			nodes, err := f.service.localK3d.nodes(f.ctx, input, id)
			if err != nil || len(nodes) != 1+agents || len(cli.limits) != 1+agents {
				t.Fatal("没有逐一设置并核验实际资源", nodes, err)
			}
			for i, node := range nodes {
				resource := cli.worker
				if i == 0 {
					resource = cli.server
				}
				if node.CPU != resource.CPUMillis*1000000 || node.Memory != resource.MemoryMiB*1024*1024 || node.Swap != node.Memory {
					t.Fatal("本地节点实际限额未保留", node)
				}
			}
			remote := k3dCreateCommand(input, id)
			if !strings.Contains(remote, "--servers-memory "+k3dMemoryArgument(cli.server.MemoryMiB)) || !strings.Contains(remote, "--agents-memory "+k3dMemoryArgument(cli.worker.MemoryMiB)) {
				t.Fatal("修改本地连接时删除了 SSH 模式的创建时内存限制", remote)
			}
		})
	}
}

func TestLocalK3dResourceUpdateFailureNeverImportsOrCleansUp(t *testing.T) {
	f, cli, input := localK3dFixture(t)
	clusters := fakeClusterClient(t, f)
	f.service.kubernetes = clusters
	cli.failUpdate = true
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(f.ctx, *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || result.Status != "failed" || !cli.created || clusters.created != 0 {
		t.Fatal("资源设置失败仍接入或报告成功", result, err)
	}
	for _, args := range cli.calls {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " delete") || strings.Contains(joined, " rm") || strings.Contains(joined, "kubeconfig get") {
			t.Fatal("资源设置失败后继续接入或自动清理", joined)
		}
	}
}

func TestLocalK3dWrongOwnerNeverUpdatesContainerAndResourceMismatchFails(t *testing.T) {
	for _, wrongOwner := range []bool{true, false} {
		t.Run(fmt.Sprint(wrongOwner), func(t *testing.T) {
			f, cli, input := localK3dFixture(t)
			cli.id = "1ec72535-b388-4165-82d8-10cafb3f8793"
			cli.wrongOwner = wrongOwner
			cli.wrongResources = !wrongOwner
			_, _, _, _, _, err := f.service.normalizeK3d(f.ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			err = f.service.localK3d.create(f.ctx, input, cli.id, io.Discard)
			if !errors.Is(err, ErrHostChanged) {
				t.Fatal("归属或资源限制不匹配仍成功", err)
			}
			for _, args := range cli.calls {
				if wrongOwner && len(args) > 1 && args[1] == "update" {
					t.Fatal("更新了其他安装的容器")
				}
				if strings.Contains(strings.Join(args, " "), " delete") {
					t.Fatal("失败后删除了现场")
				}
			}
		})
	}
}
func TestLocalK3dToolPreflightIsReadOnlyAndDownloadVerifiesHash(t *testing.T) {
	var methods []string
	content := []byte("fixture official asset")
	hash := sha256.Sum256(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method != "HEAD" {
			_, _ = w.Write(content)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "new-cache", "edo", "k3d", k3dVersion, "k3d")
	tool := &localK3dTool{path: path, url: server.URL, hash: hex.EncodeToString(hash[:]), client: server.Client()}
	if _, err := tool.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("只读工具检测创建了缓存目录")
	}
	if err := tool.prepare(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if valid, err := tool.valid(); !valid || err != nil {
		t.Fatal("工具未验证", err)
	}
	if strings.Join(methods, ",") != "HEAD,GET" {
		t.Fatal("工具检测下载请求不正确", methods)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := tool.prepare(context.Background(), io.Discard); err == nil {
		t.Fatal("覆盖了被替换的工具")
	}
}

func TestLocalK3dDetectsToolPlatformsFromOfficialReleaseMetadata(t *testing.T) {
	for _, scenario := range []struct {
		name, platform, digest string
		available              bool
	}{
		{"当前平台有官方工具", "linux/arm64", "sha256:" + strings.Repeat("a", 64), true},
		{"官方未发布当前平台", "new-os/new-cpu", "sha256:" + strings.Repeat("a", 64), false},
		{"官方资产缺少摘要", "linux/arm64", "", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet {
					t.Fatal("读取平台支持清单时尝试下载工具")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"assets": []map[string]string{{"name": "k3d-linux-arm64", "browser_download_url": "https://github.com/k3d-io/k3d/releases/download/" + k3dVersion + "/k3d-linux-arm64", "digest": scenario.digest}}})
			}))
			defer server.Close()
			cache := t.TempDir()
			tool := &localK3dTool{cache: cache, platform: scenario.platform, releaseURL: server.URL, client: server.Client()}
			err := tool.resolve(context.Background())
			if (err == nil) != scenario.available {
				t.Fatal("没有按真实发布清单判断平台支持", err)
			}
			if scenario.available {
				if err := tool.resolve(context.Background()); err != nil || requests != 1 {
					t.Fatal("重复读取了固定版本的官方发布清单", err)
				}
			}
			entries, _ := os.ReadDir(cache)
			if len(entries) != 0 {
				t.Fatal("只读平台检测创建了文件")
			}
		})
	}
}
func TestLocalK3dDaemonReplacementFailsBeforeCreation(t *testing.T) {
	f, cli, input := localK3dFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	cli.daemon = "replacement-daemon"
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(f.ctx, *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || result.Status != "failed" || cli.created {
		t.Fatal("Docker daemon 已变化仍创建了资源", err)
	}
}

func TestLocalK3dRuncRequirementMatchesOfficialMinimum(t *testing.T) {
	for _, value := range []string{"1.0.0-rc93", "1.0.0-rc94+dev", "1.0.0", "1.3.5", "2.0.0-rc1"} {
		if supported, known := localRuncSupported(value); !known || !supported {
			t.Fatal("错误拒绝已满足官方要求的 runc", value)
		}
	}
	for _, value := range []string{"0.9.0", "1.0.0-rc92"} {
		if supported, known := localRuncSupported(value); !known || supported {
			t.Fatal("接受了不满足官方要求的 runc", value)
		}
	}
	if _, known := localRuncSupported(""); known {
		t.Fatal("缺失运行时版本被视为通过")
	}
}

func TestLocalK3dArchitectureUsesDockerServerInsteadOfPresetList(t *testing.T) {
	f, cli, _ := localK3dFixture(t)
	cli.architecture = "riscv64"
	info, err := f.service.LocalK3dInfo(f.ctx, f.actor.ID)
	if err != nil || info.Architecture != "riscv64" {
		t.Fatal("Docker 实际架构被预设名单覆盖", info.Architecture, err)
	}
}

func TestLocalK3dSubmitReusesTransactionWithSingleDatabaseConnection(t *testing.T) {
	f, _, input := localK3dFixture(t)
	f.service.localK3d.docker = dockerengine.NewService(f.db, f.service.secrets, config.Runtime{DockerBuilderHost: "unix:///fixture-must-not-connect.sock"})
	sql, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sql.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	plan, err := f.service.Preview(ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.service.Execute(ctx, f.actor.ID, plan.ID)
	if err != nil || result.Status != "pending" {
		t.Fatal("本地 Docker 配置复核没有复用提交事务，可能等待连接池", err)
	}
}

func TestLocalK3dAggregateMemoryDoesNotOverflow(t *testing.T) {
	f, _, input := localK3dFixture(t)
	agents := 8
	input.K3d.Agents = &agents
	input.K3d.WorkerResources.MemoryMiB = math.MaxInt64 / (1024 * 1024)
	report, err := f.service.Check(f.ctx, f.actor.ID, input)
	if err != nil || report.Passed {
		t.Fatal("累计内存发生溢出而错误通过", err)
	}
	failed := false
	for _, check := range report.Hosts[0].Checks {
		if check.ID == "memory" && check.Status == "failed" {
			failed = true
		}
	}
	if !failed {
		t.Fatal("没有明确报告累计内存不足")
	}
}
