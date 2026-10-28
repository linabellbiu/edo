package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshclient"
)

func (k *fakeClusters) Test(context.Context, kube.Input) (*kube.ConnectionTest, error) {
	return &kube.ConnectionTest{Version: k3dK3sVersion}, nil
}

type k3dTestTransport struct {
	*fakeTransport
	blocked, failCreate bool
	configMisses        int
	onRun               func(string)
}

func k3dPreflightFixture() string {
	var output strings.Builder
	for _, definition := range k3dPreflightDefinitions {
		status := "passed"
		if definition.id == "component_images" {
			status = "warning"
		}
		fmt.Fprintf(&output, "EDO_K3S_CHECK\t%s\t%s\n", definition.id, status)
	}
	output.WriteString("EDO_K3S_HOSTNAME\tk3d-host\nEDO_K3S_IDENTITY_BEGIN\naddresses\t192.0.2.10\nmachine_id\t0123456789abcdef0123456789abcdef\nsystem_uuid\t\nboot_id\t\nEDO_K3S_IDENTITY_END\nEDO_K3S_REPORT_END\n")
	return output.String()
}

func (t *k3dTestTransport) Read(_ context.Context, _ model.Host, _ sshclient.Bundle, script string) (string, error) {
	if strings.Contains(script, "kubeconfig get") {
		if t.configMisses > 0 {
			t.configMisses--
			return "", ErrRemote
		}
		return t.kubeconfig, nil
	}
	if !strings.Contains(script, "edo_docker_binary=") {
		return "", errors.New("unexpected native preflight")
	}
	output := k3dPreflightFixture()
	if t.blocked {
		output = strings.Replace(output, "EDO_K3S_CHECK\tdocker\tpassed", "EDO_K3S_CHECK\tdocker\tfailed", 1)
	}
	return output, nil
}

func (t *k3dTestTransport) Run(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string, output io.Writer) error {
	if err := t.fakeTransport.Run(ctx, host, bundle, script, output); err != nil {
		return err
	}
	if t.onRun != nil {
		t.onRun(script)
	}
	if t.failCreate && strings.Contains(script, "edo_cli cluster create") {
		return ErrRemote
	}
	return nil
}

func k3dFixture(t *testing.T) (fixture, *k3dTestTransport, *fakeClusters, Input) {
	t.Helper()
	f := newFixture(t)
	transport := &k3dTestTransport{fakeTransport: f.transport}
	f.service.WithTransport(transport)
	f.service.k3dWaitInterval = 5 * time.Millisecond
	clusters := fakeClusterClient(t, f)
	f.service.kubernetes = clusters
	return f, transport, clusters, Input{Action: "create", Mode: "k3d", Profile: "default", Name: "容器测试集群", ServerHostID: f.host.ID}
}

func TestK3dNormalizationDefaultsAndRejectsUnsafeTargets(t *testing.T) {
	for _, scenario := range []struct {
		name string
		edit func(*Input)
	}{
		{"join", func(i *Input) { i.Action = "join" }},
		{"other mode", func(i *Input) { i.Mode = "docker" }},
		{"second physical host", func(i *Input) { i.AgentHostIDs = []string{"other"} }},
		{"native settings", func(i *Input) { i.ServerOptions = map[string]any{"docker": true} }},
		{"installer settings", func(i *Input) { i.Installer = map[string]any{"INSTALL_K3S_EXEC": "server"} }},
		{"raw YAML", func(i *Input) { i.Overrides = "server: {}" }},
		{"wildcard", func(i *Input) { i.K3d = &K3dOptions{BindAddress: "0.0.0.0"} }},
		{"zero network", func(i *Input) { i.K3d = &K3dOptions{BindAddress: "0.2.3.4"} }},
		{"reserved network", func(i *Input) { i.K3d = &K3dOptions{BindAddress: "240.2.3.4"} }},
		{"loopback", func(i *Input) { i.K3d = &K3dOptions{BindAddress: "127.0.0.1"} }},
		{"link local", func(i *Input) { i.K3d = &K3dOptions{BindAddress: "169.254.1.1"} }},
		{"IPv6", func(i *Input) { i.K3d = &K3dOptions{BindAddress: "2001:db8::1"} }},
		{"multicast", func(i *Input) { i.K3d = &K3dOptions{BindAddress: "224.0.0.1"} }},
		{"too many agents", func(i *Input) { n := 9; i.K3d = &K3dOptions{Agents: &n} }},
		{"negative agents", func(i *Input) { n := -1; i.K3d = &K3dOptions{Agents: &n} }},
		{"privileged port", func(i *Input) { i.K3d = &K3dOptions{APIPort: 443} }},
		{"different advertised IP", func(i *Input) { i.Endpoint = "https://192.0.2.11:16443" }},
		{"different advertised port", func(i *Input) { i.Endpoint = "https://192.0.2.10:6443" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, transport, _, input := k3dFixture(t)
			scenario.edit(&input)
			if _, err := f.service.Preview(f.ctx, f.actor.ID, input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("接受了无效安装配置: %v", err)
			}
			if len(transport.writes) != 0 {
				t.Fatal("无效配置产生远程写入")
			}
		})
	}
	f, _, _, input := k3dFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil || plan.Mode != "k3d" || plan.K3d == nil || k3dAgents(plan.K3d) != 1 || plan.K3d.APIPort != 16443 || plan.Endpoint != "https://192.0.2.10:16443" || len(plan.Hosts) != 1 || plan.Hosts[0].Role != "host" || plan.ServerConfig != "" {
		t.Fatalf("k3d 默认配置不正确: %+v err=%v", plan, err)
	}
	for _, flag := range []string{"--no-rollback", "--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "edo.dev/installation=" + plan.ID + "@all"} {
		if !strings.Contains(plan.Commands[0], flag) {
			t.Fatalf("计划遗漏官方 CLI 安全参数: %s", flag)
		}
	}
}

type k3dDelayedAPI struct {
	*fakeClusters
	tests, failures int
	onTest          func()
	onPing          func()
	testErr         error
	pingErr         error
}

func (k *k3dDelayedAPI) Test(context.Context, kube.Input) (*kube.ConnectionTest, error) {
	k.tests++
	if k.onTest != nil {
		k.onTest()
	}
	if k.testErr != nil {
		return nil, k.testErr
	}
	if k.tests <= k.failures {
		return nil, kube.ErrClusterUnreachable
	}
	return &kube.ConnectionTest{Version: k3dK3sVersion}, nil
}

func (k *k3dDelayedAPI) Ping(ctx context.Context, id string) (string, error) {
	if k.onPing != nil {
		k.onPing()
	}
	if k.pingErr != nil {
		return "", k.pingErr
	}
	return k.fakeClusters.Ping(ctx, id)
}

func TestK3dWorkerWaitsForRealConnectionAndDoesNotAssociatePhysicalHost(t *testing.T) {
	f, transport, clusters, input := k3dFixture(t)
	transport.configMisses = 1
	api := &k3dDelayedAPI{fakeClusters: clusters, failures: 1}
	f.service.kubernetes = api
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	nodes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("labelSelector") != "edo.dev/installation="+plan.ID {
			t.Error("节点就绪未按本次安装标签核对")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(corev1.NodeList{Items: []corev1.Node{
			{ObjectMeta: metav1.ObjectMeta{Name: "server"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "agent"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		}})
	}))
	defer nodes.Close()
	clusters.client, err = kubernetes.NewForConfig(&rest.Config{Host: nodes.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrPlanConsumed) {
		t.Fatal("同一计划可以重复执行")
	}
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(context.Background(), *record)
	result, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || result.Mode != "k3d" || result.K3d == nil || result.Status != "succeeded" || api.tests != 2 || clusters.created != 1 || len(clusters.linked) != 0 || clusters.joins != 0 || len(transport.writes) != 2 {
		t.Fatalf("k3d 未按独立连接接入: %+v tests=%d created=%d err=%v", result, api.tests, clusters.created, err)
	}
	for _, script := range transport.writes {
		if strings.Contains(script, "base64 -d > /etc/rancher/k3s/config.yaml") || strings.Contains(script, "cluster delete") {
			t.Fatal("k3d 误写原生配置或自动删除集群")
		}
	}
}

func TestK3dFailureAndRevocationNeverImportOrDeleteResources(t *testing.T) {
	for _, scenario := range []string{"preflight", "revoked", "download-revocation", "update-failure", "waiting-revocation"} {
		t.Run(scenario, func(t *testing.T) {
			f, transport, clusters, input := k3dFixture(t)
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "preflight":
				transport.blocked = true
			case "revoked":
				f.auth.denied = true
			case "download-revocation":
				transport.onRun = func(script string) { f.auth.denied = true }
			case "update-failure":
				transport.failCreate = true
			case "waiting-revocation":
				f.service.kubernetes = &k3dDelayedAPI{fakeClusters: clusters, failures: 100, onTest: func() { f.auth.denied = true }}
			}
			record, err := f.service.claim(context.Background())
			if err != nil || record == nil {
				t.Fatal(err)
			}
			f.service.runInstallation(context.Background(), *record)
			var result model.K3sInstallation
			if err := f.db.First(&result, "uuid = ?", plan.ID).Error; err != nil || result.Status != "failed" || clusters.created != 0 {
				t.Fatalf("失败安装被当作已接入: %+v err=%v", result, err)
			}
			for _, script := range transport.writes {
				if strings.Contains(script, "cluster delete") || strings.Contains(script, "docker rm") || strings.Contains(script, "volume rm") {
					t.Fatal("失败后自动删除了远端资源")
				}
			}
		})
	}
}

func TestK3dRejectsPlansWithChangedFrozenToolOrKubernetesVersion(t *testing.T) {
	for _, change := range []string{"tool", "image", "missing"} {
		t.Run(change, func(t *testing.T) {
			f, transport, _, input := k3dFixture(t)
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			var record model.K3sInstallation
			if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.service.decode(record)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "tool":
				snapshot.K3dVersion = "v5.8.3"
			case "image":
				snapshot.K3dK3sVersion = "v1.32.5+k3s1"
			case "missing":
				snapshot.K3dVersion, snapshot.K3dK3sVersion = "", ""
			}
			encoded, _ := json.Marshal(snapshot)
			ciphertext, err := f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+record.ID))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&record).Update("input_ciphertext", ciphertext).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrPreflightRequired) {
				t.Fatalf("版本变化后仍可执行: %v", err)
			}
			if len(transport.writes) != 0 {
				t.Fatal("旧计划产生了安装副作用")
			}
		})
	}
}

func TestKubeconfigEndpointRewriteKeepsUnselectedContexts(t *testing.T) {
	raw := "apiVersion: v1\nkind: Config\ncurrent-context: chosen\ncontexts:\n- name: chosen\n  context:\n    cluster: chosen-cluster\n- name: other\n  context:\n    cluster: other-cluster\nclusters:\n- name: chosen-cluster\n  cluster:\n    server: https://127.0.0.1:6443\n- name: other-cluster\n  cluster:\n    server: https://other.example:7443\n"
	encoded, err := rewriteKubeconfigEndpoint([]byte(raw), "https://192.0.2.10:16443")
	if err != nil {
		t.Fatal(err)
	}
	config, err := clientcmd.Load([]byte(encoded))
	if err != nil || config.CurrentContext != "chosen" || config.Clusters["chosen-cluster"].Server != "https://192.0.2.10:16443" || config.Clusters["other-cluster"].Server != "https://other.example:7443" {
		t.Fatal("改写了非当前上下文的集群", err)
	}
	for _, malformed := range []string{"apiVersion: v1\nkind: Config\n", strings.Replace(raw, "current-context: chosen", "current-context: missing", 1), strings.Replace(raw, "cluster: chosen-cluster", "cluster: absent", 1)} {
		if _, err := rewriteKubeconfigEndpoint([]byte(malformed), "https://192.0.2.10:16443"); !errors.Is(err, ErrRemote) {
			t.Fatalf("缺少当前集群仍被接受: %v", err)
		}
	}
}

func TestK3dConnectionWaitStopsOnCancellationWithoutImport(t *testing.T) {
	f, _, clusters, input := k3dFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	var record model.K3sInstallation
	if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.decode(record)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.service.kubernetes = &k3dDelayedAPI{fakeClusters: clusters, failures: 1, onTest: cancel}
	if _, err := f.service.waitForK3dConnection(ctx, &record, snapshot, f.host, sshclient.Bundle{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后没有停止连接等待: %v", err)
	}
	if clusters.created != 0 || len(clusters.linked) != 0 {
		t.Fatal("取消等待后仍接入或关联物理主机")
	}
}

type k3dWaitCancelAuthorizer struct{ cancel context.CancelFunc }

func (a k3dWaitCancelAuthorizer) HasPermission(context.Context, *model.User, string) (bool, error) {
	a.cancel()
	return false, context.Canceled
}

type k3dWaitCancelRead struct {
	Transport
	cancel context.CancelFunc
}

func (t k3dWaitCancelRead) Read(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
	value, err := t.Transport.Read(ctx, host, bundle, script)
	t.cancel()
	return value, err
}

func TestK3dWaitCancellationPriorityIsDeterministicAndNeverImports(t *testing.T) {
	for _, phase := range []string{"connection", "nodes"} {
		for _, mode := range []string{"already-canceled", "expired", "during-authorization", "permission-revoked", "api-canceled-success", "api-canceled-failure"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				f, _, clusters, input := k3dFixture(t)
				plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
				if err != nil {
					t.Fatal(err)
				}
				var record model.K3sInstallation
				if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
					t.Fatal(err)
				}
				snapshot, err := f.service.decode(record)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				// tick 始终可用，不依赖随机 select 分支或测试机负载触发竞态。
				f.service.k3dWaitInterval = time.Nanosecond
				api := &k3dDelayedAPI{fakeClusters: clusters}
				f.service.kubernetes = api
				want := error(context.Canceled)
				switch mode {
				case "already-canceled":
					cancel()
				case "expired":
					deadline, stop := context.WithDeadline(f.ctx, time.Now().Add(-time.Second))
					defer stop()
					ctx, want = deadline, ErrReadiness
				case "during-authorization":
					f.service.authorizer = k3dWaitCancelAuthorizer{cancel: cancel}
				case "permission-revoked":
					f.auth.denied = true
					want = ErrPermission
				case "api-canceled-success", "api-canceled-failure":
					if phase == "connection" {
						api.onTest = cancel
						if mode == "api-canceled-failure" {
							api.testErr = apierrors.NewUnauthorized("private-api-diagnostic")
						}
					} else {
						api.onPing = cancel
						if mode == "api-canceled-failure" {
							api.pingErr = apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "private-node", errors.New("private-api-diagnostic"))
						}
					}
				}
				var value string
				if phase == "connection" {
					value, err = f.service.waitForK3dConnection(ctx, &record, snapshot, f.host, sshclient.Bundle{})
				} else {
					err = f.service.waitForK3d(ctx, &record, snapshot, 1)
				}
				if err != want || value != "" || clusters.created != 0 || len(clusters.linked) != 0 {
					t.Fatalf("取消或真实撤权被错误归类/仍接入: got=%v want=%v valueLength=%d created=%d linked=%d", err, want, len(value), clusters.created, len(clusters.linked))
				}
				if !strings.HasPrefix(mode, "api-") && api.tests != 0 {
					t.Fatal("取消或撤权后仍测试集群连接", api.tests)
				}
			})
		}
	}
}

func TestK3dWaitCancellationAfterReadOrNodeResponseNeverReturnsSuccess(t *testing.T) {
	for _, phase := range []string{"read", "nodes"} {
		t.Run(phase, func(t *testing.T) {
			f, transport, clusters, input := k3dFixture(t)
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			var record model.K3sInstallation
			if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.service.decode(record)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			f.service.k3dWaitInterval = time.Nanosecond
			var value string
			if phase == "read" {
				f.service.transport = k3dWaitCancelRead{Transport: transport, cancel: cancel}
				value, err = f.service.waitForK3dConnection(ctx, &record, snapshot, f.host, sshclient.Bundle{})
			} else {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					cancel()
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"fixture-node"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`)
				}))
				defer server.Close()
				clusters.client, err = kubernetes.NewForConfig(&rest.Config{Host: server.URL})
				if err != nil {
					t.Fatal(err)
				}
				err = f.service.waitForK3d(ctx, &record, snapshot, 1)
			}
			if err != context.Canceled || value != "" || clusters.created != 0 || len(clusters.linked) != 0 {
				t.Fatalf("异步响应取消后仍成功或接入: phase=%s err=%v valueLength=%d", phase, err, len(value))
			}
		})
	}
}

func TestNativeModeKeepsOfficialHostInstallerWithoutContainerFallback(t *testing.T) {
	f := newFixture(t)
	input := f.input()
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil || plan.Mode != "native" || plan.K3d != nil {
		t.Fatalf("省略模式未保持原生安装: %+v %v", plan, err)
	}
	var record model.K3sInstallation
	if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.decode(record)
	if err != nil {
		t.Fatal(err)
	}
	script, err := installScript(plan.Hosts[0], snapshot.Input, plan.ID, "fixture-only-token")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"https://get.k3s.io", "/etc/rancher/k3s/config.yaml", "/run/systemd/system", "/sbin/openrc-run"} {
		if !strings.Contains(script, required) {
			t.Fatalf("原生安装缺少宿主机约束: %s", required)
		}
	}
	for _, forbidden := range []string{"docker run", "podman run", "k3d cluster", "edo_cli cluster"} {
		if strings.Contains(script, forbidden) || strings.Contains(plan.Commands[0], forbidden) {
			t.Fatalf("原生安装意外依赖容器集群: %s", forbidden)
		}
	}
}

func TestK3dWaitRejectsPermanentAPIPermissionsWithoutRetry(t *testing.T) {
	for _, phase := range []string{"connection", "ping", "nodes"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(fmt.Sprintf("%s/%d", phase, status), func(t *testing.T) {
				f, _, clusters, input := k3dFixture(t)
				plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
				if err != nil {
					t.Fatal(err)
				}
				var record model.K3sInstallation
				if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
					t.Fatal(err)
				}
				snapshot, err := f.service.decode(record)
				if err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				var denied error = apierrors.NewUnauthorized("private-api-diagnostic")
				if status == http.StatusForbidden {
					denied = apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "private-node", errors.New("private-api-diagnostic"))
				}
				api := &k3dDelayedAPI{fakeClusters: clusters}
				f.service.kubernetes = api
				switch phase {
				case "connection":
					api.testErr = fmt.Errorf("%w: private-api-address: %w", kube.ErrClusterUnreachable, denied)
					_, err = f.service.waitForK3dConnection(f.ctx, &record, snapshot, f.host, sshclient.Bundle{})
					if api.tests != 1 {
						t.Fatalf("永久 API 拒绝被重试: %d", api.tests)
					}
				default:
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(status)
						_ = json.NewEncoder(w).Encode(denied.(apierrors.APIStatus).Status())
					}))
					defer server.Close()
					clusters.client, err = kubernetes.NewForConfig(&rest.Config{Host: server.URL})
					if err != nil {
						t.Fatal(err)
					}
					if phase == "ping" {
						api.pingErr = fmt.Errorf("private-api-address: %w", denied)
						api.onPing = func() { calls.Add(1) }
					}
					err = f.service.waitForK3d(f.ctx, &record, snapshot, 1)
					if calls.Load() != 1 {
						t.Fatalf("永久 API 拒绝被重试: %d", calls.Load())
					}
				}
				if !errors.Is(err, ErrPermission) || strings.Contains(err.Error(), "private-") || clusters.created != 0 {
					t.Fatalf("永久拒绝未安全停止接入: %v", err)
				}
			})
		}
	}
}
