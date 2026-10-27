package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"edo/internal/model"
	"edo/internal/sshclient"
)

type nativeNodeAuthorizer struct{ denied bool }

func (a *nativeNodeAuthorizer) HasPermission(_ context.Context, _ *model.User, permission string) (bool, error) {
	return permission != "kubernetes.node.create" || !a.denied, nil
}

type liveNodeAuthorizer struct{ denied atomic.Bool }

func (a *liveNodeAuthorizer) HasPermission(_ context.Context, _ *model.User, permission string) (bool, error) {
	return permission != "kubernetes.node.create" || !a.denied.Load(), nil
}

type waitingInstallTransport struct {
	*fakeTransport
	started chan struct{}
	release <-chan struct{}
}

func (t *waitingInstallTransport) Run(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string, output io.Writer) error {
	if err := t.fakeTransport.Run(ctx, host, bundle, script, output); err != nil {
		return err
	}
	close(t.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.release:
		return nil
	}
}

type waitingHeartbeatAuthorizer struct {
	armed   atomic.Bool
	entered chan struct{}
}

func (a *waitingHeartbeatAuthorizer) HasPermission(ctx context.Context, _ *model.User, permission string) (bool, error) {
	if permission == "cluster.execute" && a.armed.CompareAndSwap(true, false) {
		close(a.entered)
		<-ctx.Done()
		return false, ctx.Err()
	}
	return true, nil
}

func TestNativeWorkerCompletionDoesNotTreatHeartbeatShutdownAsRevocation(t *testing.T) {
	f := newFixture(t)
	auth := &waitingHeartbeatAuthorizer{entered: make(chan struct{})}
	f.service.authorizer = auth
	f.service.kubernetes = fakeClusterClient(t, f)
	release := make(chan struct{})
	transport := &waitingInstallTransport{fakeTransport: f.transport, started: make(chan struct{}), release: release}
	f.service.WithTransport(transport)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		f.service.runInstallation(ctx, *record)
	}()
	select {
	case <-transport.started:
	case <-ctx.Done():
		t.Fatal("安装未开始")
	}
	auth.armed.Store(true)
	select {
	case <-auth.entered:
	case <-ctx.Done():
		<-finished
		t.Fatal("心跳未进入授权检查")
	}
	close(release)
	<-finished
	var result model.K3sInstallation
	if err := f.db.First(&result, "uuid = ?", plan.ID).Error; err != nil || result.Status != "succeeded" {
		t.Fatalf("正常结束取消心跳被误判为撤权: status=%s error=%s err=%v", result.Status, result.ErrorMessage, err)
	}
}

func TestNativeWorkerCancelsLongInstallWhenPermissionRevoked(t *testing.T) {
	f := newFixture(t)
	auth := &liveNodeAuthorizer{}
	f.service.authorizer = auth
	clusters := fakeClusterClient(t, f)
	f.service.kubernetes = clusters
	transport := &waitingInstallTransport{fakeTransport: f.transport, started: make(chan struct{})}
	f.service.WithTransport(transport)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		f.service.runInstallation(ctx, *record)
	}()
	select {
	case <-transport.started:
	case <-ctx.Done():
		t.Fatal("安装未开始")
	}
	// 模拟官方安装器的长下载；撤权必须主动取消连接，不能等下载返回后才拒绝。
	auth.denied.Store(true)
	select {
	case <-finished:
	case <-ctx.Done():
		<-finished
		t.Fatal("安装期间撤权没有由心跳主动取消")
	}
	var result model.K3sInstallation
	if err := f.db.First(&result, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !strings.Contains(result.ErrorMessage, "权限") || len(f.transport.writes) != 1 || clusters.created != 0 {
		t.Fatalf("撤权后继续写入或错误终态不准确: status=%s error=%s writes=%d clusters=%d", result.Status, result.ErrorMessage, len(f.transport.writes), clusters.created)
	}
}

func TestNativeHeartbeatAuthorityRechecksOnlyImmutableScopeAndHosts(t *testing.T) {
	for _, mode := range []string{"unchanged", "auth-version", "inactive-actor", "inactive-project", "host-identity"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
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
			switch mode {
			case "unchanged":
				// 已接入同名集群是安装过程的正常进展，心跳不能按新建名称冲突取消它。
				err = f.db.Create(&model.KubernetesCluster{ID: record.ID, Name: record.Name, ProjectID: record.ProjectID, DepartmentID: record.DepartmentID, Mode: model.KubernetesKubeconfig, IsActive: true}).Error
			case "auth-version":
				err = f.db.Model(&model.User{}).Where("uuid = ?", f.actor.ID).Update("auth_version", f.actor.AuthVersion+1).Error
			case "inactive-actor":
				err = f.db.Model(&model.User{}).Where("uuid = ?", f.actor.ID).Update("is_active", false).Error
			case "inactive-project":
				err = f.db.Model(&model.Project{}).Where("uuid = ?", f.host.ProjectID).Update("is_active", false).Error
			case "host-identity":
				err = f.db.Model(&model.Host{}).Where("uuid = ?", f.host.ID).Update("address", "192.0.2.11").Error
			}
			if err != nil {
				t.Fatal(err)
			}
			err = f.service.recheckInstallationAuthority(f.ctx, record.CreatedBy, record.Action, snapshot)
			if mode == "unchanged" && err != nil || mode != "unchanged" && err == nil {
				t.Fatalf("心跳没有准确复核授权或主机: mode=%s err=%v", mode, err)
			}
		})
	}
}

func TestNativeNodePermissionIsCheckedBeforePlanAndBeforeWrites(t *testing.T) {
	t.Run("plan-denied-before-preflight", func(t *testing.T) {
		f := newFixture(t)
		f.service.authorizer = &nativeNodeAuthorizer{denied: true}
		if _, err := f.service.Preview(f.ctx, f.actor.ID, f.input()); !errors.Is(err, ErrPermission) {
			t.Fatal("原生节点创建权限缺失仍生成安装计划", err)
		}
		var count int64
		if err := f.db.Model(&model.K3sInstallation{}).Count(&count).Error; err != nil || count != 0 || f.transport.reads != 0 || len(f.transport.writes) != 0 {
			t.Fatalf("缺权限仍开始检查或保存计划: count=%d reads=%d writes=%d err=%v", count, f.transport.reads, len(f.transport.writes), err)
		}
	})
	t.Run("execute-rechecks-permission", func(t *testing.T) {
		f := newFixture(t)
		auth := &nativeNodeAuthorizer{}
		f.service.authorizer = auth
		plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
		if err != nil {
			t.Fatal(err)
		}
		auth.denied = true
		if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrPermission) {
			t.Fatal("计划确认后撤销权限仍能提交", err)
		}
		var record model.K3sInstallation
		if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil || record.Status != "planned" || len(f.transport.writes) != 0 {
			t.Fatalf("权限失败仍消耗计划或安装: status=%s err=%v", record.Status, err)
		}
	})
	t.Run("worker-rechecks-before-remote-write", func(t *testing.T) {
		f := newFixture(t)
		auth := &nativeNodeAuthorizer{}
		f.service.authorizer = auth
		plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
			t.Fatal(err)
		}
		auth.denied = true
		record, err := f.service.claim(f.ctx)
		if err != nil || record == nil {
			t.Fatal(err)
		}
		f.service.runInstallation(f.ctx, *record)
		var current model.K3sInstallation
		if err := f.db.First(&current, "uuid = ?", plan.ID).Error; err != nil || current.Status != "failed" || len(f.transport.writes) != 0 {
			t.Fatalf("权限撤销后仍发生安装或成功: status=%s writes=%d err=%v", current.Status, len(f.transport.writes), err)
		}
	})
	t.Run("k3d-does-not-create-physical-nodes", func(t *testing.T) {
		f, _, _, input := k3dFixture(t)
		f.service.authorizer = &nativeNodeAuthorizer{denied: true}
		if _, err := f.service.Preview(f.ctx, f.actor.ID, input); err != nil {
			t.Fatal("k3d 被误加物理节点权限", err)
		}
	})
	t.Run("pure-control-plane-does-not-register-nodes", func(t *testing.T) {
		f := newFixture(t)
		f.service.authorizer = &nativeNodeAuthorizer{denied: true}
		input := f.input()
		input.Profile = "custom"
		input.ServerOptions = map[string]any{"disable-agent": true}
		if _, err := f.service.Preview(f.ctx, f.actor.ID, input); err != nil {
			t.Fatal("纯控制平面被误加节点权限", err)
		}
		input.AgentHostIDs = []string{"physical-agent"}
		if !needsNativeNodePermission(input) {
			t.Fatal("显式加入物理工作节点未要求节点权限")
		}
	})
}

func readySystemDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system", UID: "coredns-a", Generation: 2}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))}, Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue}}}}
}

func TestSystemDeploymentReadinessRequiresActualCompletedRollout(t *testing.T) {
	if !systemDeploymentReady(readySystemDeployment()) || systemDeploymentReady(nil) {
		t.Fatal("就绪或缺失对象判断错误")
	}
	for name, change := range map[string]func(*appsv1.Deployment){
		"missing-replicas":  func(d *appsv1.Deployment) { d.Spec.Replicas = nil },
		"scaled-zero":       func(d *appsv1.Deployment) { d.Spec.Replicas = ptr.To(int32(0)) },
		"old-generation":    func(d *appsv1.Deployment) { d.Status.ObservedGeneration = 1 },
		"old-replicas":      func(d *appsv1.Deployment) { d.Status.UpdatedReplicas = 0 },
		"surging":           func(d *appsv1.Deployment) { d.Status.Replicas = 2 },
		"not-ready":         func(d *appsv1.Deployment) { d.Status.ReadyReplicas = 0 },
		"unavailable":       func(d *appsv1.Deployment) { d.Status.AvailableReplicas = 0 },
		"missing-condition": func(d *appsv1.Deployment) { d.Status.Conditions = nil },
		"false-condition":   func(d *appsv1.Deployment) { d.Status.Conditions[0].Status = corev1.ConditionFalse },
		"deleting":          func(d *appsv1.Deployment) { now := metav1.Now(); d.DeletionTimestamp = &now },
	} {
		t.Run(name, func(t *testing.T) {
			value := readySystemDeployment()
			change(value)
			if systemDeploymentReady(value) {
				t.Fatal("未完成的系统组件被当成成功")
			}
		})
	}
}

func TestExpectedSystemDeploymentsRespectsOnlyExplicitDisable(t *testing.T) {
	if got := expectedSystemDeployments(Input{}); strings.Join(got, ",") != "coredns,metrics-server,local-path-provisioner" {
		t.Fatal(got)
	}
	input := Input{ServerOptions: map[string]any{"disable": []any{"traefik", "servicelb", "local-storage", "metrics-server"}}}
	if got := expectedSystemDeployments(input); len(got) != 1 || got[0] != "coredns" {
		t.Fatal(got)
	}
	input.ServerOptions["disable"] = []string{"coredns", "metrics-server", "local-storage"}
	if len(expectedSystemDeployments(input)) != 0 {
		t.Fatal("明确禁用的组件仍被要求就绪")
	}
	if err := (&Service{}).waitForCreatedComponents(context.Background(), nil, savedInput{Input: Input{Action: "join"}}); err != nil {
		t.Fatal("加入节点不能要求既有集群组件重新就绪", err)
	}
}

func TestSystemDeploymentWaitIsBoundedAndStopsOnPermissionOrIdentityChange(t *testing.T) {
	for _, mode := range []string{"pending-then-ready", "not-found-then-ready", "forbidden", "unauthorized", "bad-request", "timeout", "identity-changed", "object-replaced", "wrong-namespace", "rollout-failed"} {
		t.Run(mode, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			calls, checks := 0, 0
			client.PrependReactor("get", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
				calls++
				resource := schema.GroupResource{Group: "apps", Resource: "deployments"}
				switch mode {
				case "forbidden":
					return true, nil, apierrors.NewForbidden(resource, "coredns", errors.New("private-api-error"))
				case "unauthorized":
					return true, nil, apierrors.NewUnauthorized("private-api-error")
				case "bad-request":
					return true, nil, apierrors.NewBadRequest("private-api-error")
				case "not-found-then-ready":
					if calls == 1 {
						return true, nil, apierrors.NewNotFound(resource, "coredns")
					}
				}
				value := readySystemDeployment()
				if mode == "object-replaced" && calls > 1 {
					value.UID = "replaced"
				}
				if mode == "wrong-namespace" {
					value.Namespace = "another"
				}
				if mode == "rollout-failed" {
					value.Status.Conditions = append(value.Status.Conditions, appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"})
				}
				if mode == "timeout" || (mode == "pending-then-ready" || mode == "object-replaced") && calls == 1 {
					value.Status.AvailableReplicas = 0
				}
				return true, value, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			err := waitForSystemDeployments(ctx, client, []string{"coredns"}, time.Millisecond, func(context.Context) error {
				checks++
				if mode == "identity-changed" {
					return ErrHostChanged
				}
				return nil
			})
			switch mode {
			case "pending-then-ready", "not-found-then-ready":
				if err != nil || calls != 2 || checks != 2 {
					t.Fatalf("临时未就绪没有正确等待: %v %d %d", err, calls, checks)
				}
			case "identity-changed":
				if !errors.Is(err, ErrHostChanged) || calls != 0 {
					t.Fatal("身份变化后仍查询集群", err, calls)
				}
			case "object-replaced":
				if !errors.Is(err, ErrHostChanged) || calls != 2 {
					t.Fatal("同名组件替换仍继续等待", err, calls)
				}
			case "timeout":
				if !errors.Is(err, ErrReadiness) || calls < 1 {
					t.Fatal("未就绪没有有界失败", err)
				}
			default:
				if err == nil || calls != 1 || strings.Contains(err.Error(), "private-api-error") {
					t.Fatal("永久错误被重试或泄露", err, calls)
				}
			}
		})
	}
}

func TestPostInstallChecksExactVersionAndSystemServiceWithPortableShell(t *testing.T) {
	for _, mode := range []string{"systemd", "activating", "version-mismatch", "version-command-failed", "inactive", "not-enabled", "systemctl-failed", "openrc", "openrc-not-enabled", "openrc-not-running"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			writePreflightCommand(t, directory, "timeout", "shift\nexec \"$@\"\n")
			writePreflightCommand(t, directory, "sleep", "exit 0\n")
			writePreflightCommand(t, directory, "k3s", "[ \"$1\" = --version ] || exit 9\n[ \"$EDO_TEST_CASE\" != version-command-failed ] || exit 19\nversion="+shellQuote(CatalogK3sVersion)+"\n[ \"$EDO_TEST_CASE\" != version-mismatch ] || version=v1.36.0+k3s1\nprintf 'k3s version %s (fixture)\\n' \"$version\"\n")
			writePreflightCommand(t, directory, "systemctl", `
[ "$EDO_TEST_CASE" != systemctl-failed ] || exit 1
case "$*" in
  'show --property=ActiveState --value k3s.service')
    if [ "$EDO_TEST_CASE" = inactive ]; then echo inactive
    elif [ "$EDO_TEST_CASE" = activating ] && [ ! -f "$EDO_TEST_COUNTER" ]; then echo activating; : > "$EDO_TEST_COUNTER"
    else echo active; fi;;
  'show --property=SubState --value k3s.service') echo running;;
  'is-enabled k3s.service') if [ "$EDO_TEST_CASE" = not-enabled ]; then echo disabled; exit 1; else echo enabled; fi;;
  *) exit 9;;
esac
`)
			writePreflightCommand(t, directory, "rc-update", `[ "$*" = 'show default' ] || exit 9
if [ "$EDO_TEST_CASE" = openrc-not-enabled ]; then printf 'other | default\n'; else printf 'k3s | default\n'; fi
`)
			writePreflightCommand(t, directory, "rc-service", `[ "$*" = 'k3s status' ] || exit 9
[ "$EDO_TEST_CASE" != openrc-not-running ]
`)
			input := Input{Installer: map[string]any{"INSTALL_K3S_VERSION": CatalogK3sVersion, "INSTALL_K3S_BIN_DIR": directory}}
			script := postInstallVerificationScript(Host{Role: "server"}, input)
			// 只替换运行环境探测，不改被测版本、服务命令和失败分支。
			script = strings.ReplaceAll(script, "[ -d /run/systemd/system ]", `[ "$EDO_TEST_MANAGER" = systemd ]`)
			script = strings.ReplaceAll(script, "[ -x /sbin/openrc-run ]", `[ "$EDO_TEST_MANAGER" = openrc ]`)
			manager := "systemd"
			if strings.HasPrefix(mode, "openrc") {
				manager = "openrc"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-s")
			command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "EDO_TEST_CASE="+mode, "EDO_TEST_MANAGER="+manager, "EDO_TEST_COUNTER="+directory+"/counter")
			command.Stdin = strings.NewReader("set -eu\n" + script)
			output, err := command.CombinedOutput()
			if mode == "systemd" || mode == "activating" || mode == "openrc" {
				if err != nil {
					t.Fatalf("服务就绪检查误拒绝: %v %s", err, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal("错误版本或服务状态被当成成功", err)
				}
				want := 46
				if mode == "version-mismatch" {
					want = 45
				}
				if mode == "version-command-failed" {
					want = 19
				}
				if exit.ExitCode() != want {
					t.Fatalf("错误退出码: %d != %d, %s", exit.ExitCode(), want, output)
				}
			}
		})
	}
	if installedServiceName("agent", nil) != "k3s-agent" || installedServiceName("server", map[string]any{"INSTALL_K3S_NAME": "team-a"}) != "k3s-team-a" {
		t.Fatal("自定义服务名不匹配官方安装器")
	}
}

func TestInstallationDiagnosticsDiscardRawOutputAndKeepOnlySafeStageAndExit(t *testing.T) {
	diagnostics := &installationDiagnostics{}
	_, _ = diagnostics.Write([]byte("PRIVATE_TOKEN=fixture-secret\nEDO_K3S_STAGE\tver"))
	_, _ = diagnostics.Write([]byte("sion\n" + strings.Repeat("raw-secret", 4096) + "\nEDO_K3S_STAGE\tunknown-sensitive-stage\n"))
	err := diagnostics.failure(&remoteExitError{code: 45, cause: ErrRemote})
	if !errors.Is(err, ErrRemote) || !strings.Contains(safeFailure(err), "版本") || !strings.Contains(safeFailure(err), "45") || strings.Contains(fmt.Sprint(err), "secret") || strings.Contains(fmt.Sprint(err), "unknown") {
		t.Fatal("阶段诊断丢失或泄露安装器输出", err)
	}
}

func TestNativePlanRejectsUnfixedChannelBeforeInstalling(t *testing.T) {
	f := newFixture(t)
	input := f.input()
	input.Profile, input.Installer = "custom", map[string]any{"channel": "stable"}
	if _, err := f.service.Preview(f.ctx, f.actor.ID, input); !errors.Is(err, ErrInvalidInput) || len(f.transport.writes) != 0 {
		t.Fatal("未固定版本仍进入安装", err)
	}
}

func TestNativeProgressPersistenceFailureStopsBeforeRemoteWrite(t *testing.T) {
	f := newFixture(t)
	f.service.kubernetes = fakeClusterClient(t, f)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(context.Background())
	if err != nil || record == nil {
		t.Fatal(err)
	}
	var injected atomic.Bool
	if err := f.db.Callback().Update().Before("gorm:update").Register("readiness_fail_progress", func(db *gorm.DB) {
		if values, ok := db.Statement.Dest.(map[string]any); ok && values["logs"] != nil && injected.CompareAndSwap(false, true) {
			_ = db.AddError(errors.New("fixture progress write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove("readiness_fail_progress") })
	f.service.runInstallation(context.Background(), *record)
	var result model.K3sInstallation
	if err := f.db.First(&result, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !strings.Contains(result.ErrorMessage, "保存安装进度") || len(f.transport.writes) != 0 {
		t.Fatalf("持久化失败仍继续安装: %+v writes=%d", result, len(f.transport.writes))
	}
}

type readinessFailureTransport struct{ Transport }

func (f readinessFailureTransport) Run(_ context.Context, _ model.Host, _ sshclient.Bundle, _ string, out io.Writer) error {
	_, _ = io.WriteString(out, "RAW_PRIVATE_INSTALL_TOKEN\nEDO_K3S_STAGE\tservice\n")
	return &remoteExitError{code: 46, cause: ErrRemote}
}

func TestNativeWorkerCannotSucceedWithBadVersionComponentsOrInstaller(t *testing.T) {
	for _, mode := range []string{"wrong-api-version", "component-forbidden", "all-components-disabled", "installer-failed"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			clusters := fakeClusterClient(t, f)
			var componentRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/nodes" {
					_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"fixture-node"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/apis/apps/v1/namespaces/kube-system/deployments/") {
					componentRequests.Add(1)
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","message":"RAW_PRIVATE_API_OBJECT","code":403}`)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			clusters.client = client
			if mode == "wrong-api-version" {
				clusters.version = "v1.36.0+k3s1"
			}
			f.service.kubernetes = clusters
			input := f.input()
			if mode == "all-components-disabled" {
				input.Profile = "custom"
				input.ServerOptions = map[string]any{"disable": []string{"coredns", "metrics-server", "local-storage"}}
			}
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
				t.Fatal(err)
			}
			record, err := f.service.claim(context.Background())
			if err != nil || record == nil {
				t.Fatal(err)
			}
			if mode == "installer-failed" {
				f.service.transport = readinessFailureTransport{f.transport}
			}
			f.service.runInstallation(context.Background(), *record)
			var result model.K3sInstallation
			if err := f.db.First(&result, "uuid = ?", plan.ID).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "all-components-disabled" {
				if result.Status != "succeeded" || componentRequests.Load() != 0 {
					t.Fatalf("明确禁用组件仍阻断: %s %d", result.Status, componentRequests.Load())
				}
				return
			}
			if result.Status != "failed" || strings.Contains(result.ErrorMessage+result.Logs, "RAW_PRIVATE") {
				t.Fatalf("安装问题仍成功或泄露正文: %+v", result)
			}
			if mode == "component-forbidden" && componentRequests.Load() != 1 {
				t.Fatal("403被重试", componentRequests.Load())
			}
			if mode == "component-forbidden" || mode == "wrong-api-version" {
				var hosts []Host
				if err := json.Unmarshal(result.Hosts, &hosts); err != nil || len(hosts) != 1 || hosts[0].Status != "failed" || !strings.Contains(hosts[0].Message, "控制平面已安装") || !strings.Contains(hosts[0].Message, result.ErrorMessage) {
					t.Fatalf("已安装的控制平面被错误显示为未开始: hosts=%s error=%s err=%v", result.Hosts, result.ErrorMessage, err)
				}
			}
			if mode == "installer-failed" && (clusters.created != 0 || !strings.Contains(result.ErrorMessage, "系统服务校验") || !strings.Contains(result.ErrorMessage, "46")) {
				t.Fatal("官方安装器失败仍接入，或安全诊断丢失", result.ErrorMessage)
			}
		})
	}
}

func TestReadinessClusterSnapshotRejectsIdentityAndConnectionChanges(t *testing.T) {
	initial := &model.KubernetesCluster{ID: "cluster-a", ProjectID: "project-a", DepartmentID: "department-a", Mode: model.KubernetesKubeconfig, IsActive: true, APIServer: "https://api.example.test", KubeconfigCiphertext: "encrypted-a"}
	for name, change := range map[string]func(*model.KubernetesCluster){
		"id":         func(c *model.KubernetesCluster) { c.ID = "cluster-b" },
		"project":    func(c *model.KubernetesCluster) { c.ProjectID = "project-b" },
		"department": func(c *model.KubernetesCluster) { c.DepartmentID = "department-b" },
		"inactive":   func(c *model.KubernetesCluster) { c.IsActive = false },
		"endpoint":   func(c *model.KubernetesCluster) { c.APIServer = "https://another.example.test" },
		"credential": func(c *model.KubernetesCluster) { c.KubeconfigCiphertext = "encrypted-b" },
	} {
		t.Run(name, func(t *testing.T) {
			value := *initial
			change(&value)
			if sameReadinessCluster(initial, &value) {
				t.Fatal("安装期间目标变化未失效")
			}
		})
	}
}
