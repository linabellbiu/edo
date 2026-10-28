package k3s

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"edo/internal/kube"
	"edo/internal/model"
)

// 只接受代码内登记的阶段，安装器输出和命令正文均不进入错误或任务日志。
var installationStages = map[string]string{
	"uninstall_guard": "卸载目标复核", "uninstall": "官方卸载脚本", "uninstall_verify": "卸载结果验证",
	"guard": "安装前检查", "preflight": "主机环境检查", "download": "下载安装器",
	"configuration": "写入安装配置", "installer": "官方安装器", "version": "安装版本校验",
	"service": "系统服务校验", "api": "API 就绪校验", "components": "系统组件就绪校验",
	"record": "保存安装进度", "ssh": "SSH 安装连接",
	"import": "接入集群", "node": "节点就绪校验", "association": "关联主机",
	"component_coredns": "CoreDNS 就绪校验", "component_metrics-server": "Metrics Server 就绪校验",
	"component_local-path-provisioner": "本地存储组件就绪校验",
	"installer_download":               "官方安装器下载文件", "installer_verify": "官方安装器校验文件",
	"installer_binary": "官方安装器安装程序", "installer_environment": "官方安装器写入服务环境",
	"installer_service": "官方安装器创建系统服务", "installer_start": "官方安装器启动系统服务",
	"k3d_tool": "准备 k3d 工具", "k3d_create": "创建 k3d 容器节点", "k3d_resources": "校验 k3d 资源限制",
}

type installationFailure struct {
	stage string
	code  int
	cause error
}

func (e *installationFailure) Error() string {
	name := installationStages[e.stage]
	if name == "" {
		name = "安装检查"
	}
	if e.code >= 0 {
		return fmt.Sprintf("%s失败（退出码 %d），请检查主机上的实际状态。", name, e.code)
	}
	if errors.Is(e.cause, ErrPermission) {
		return name + "失败，安装权限已失效或集群拒绝访问。"
	}
	if errors.Is(e.cause, ErrUninstallPermission) {
		return name + "失败，卸载权限已失效。"
	}
	if errors.Is(e.cause, ErrUninstallChanged) {
		return name + "失败，卸载目标或官方脚本已变化。"
	}
	if errors.Is(e.cause, ErrUninstallIncomplete) {
		return ErrUninstallIncomplete.Error()
	}
	if errors.Is(e.cause, ErrHostChanged) {
		return name + "失败，安装目标身份或连接已变化。"
	}
	if errors.Is(e.cause, ErrTargetVersionChanged) {
		return name + "失败，实际版本与安装计划不一致。"
	}
	if errors.Is(e.cause, context.DeadlineExceeded) {
		return name + "超时，请检查主机上的实际状态。"
	}
	return name + "失败，请检查主机上的实际状态。"
}

func (e *installationFailure) Unwrap() error { return e.cause }

type remoteExitError struct {
	code  int
	cause error
}

func (e *remoteExitError) Error() string {
	return fmt.Sprintf("远程安装命令退出（退出码 %d）", e.code)
}
func (e *remoteExitError) Unwrap() error { return e.cause }

func installationError(stage string, err error) error {
	if err == nil {
		return nil
	}
	var failure *installationFailure
	if errors.As(err, &failure) {
		return err
	}
	code := -1
	var exit *remoteExitError
	if errors.As(err, &exit) {
		code = exit.code
	}
	return &installationFailure{stage: stage, code: code, cause: err}
}

type installationDiagnostics struct {
	mu      sync.Mutex
	stage   string
	line    []byte
	discard bool
	seen    map[string]bool
	started time.Time
	updated time.Time
	onStage func(string, time.Time, time.Time, bool) error
	err     error
}

func (d *installationDiagnostics) Write(value []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return 0, d.err
	}
	for _, b := range value {
		if b == '\n' {
			line := string(d.line)
			stage := ""
			if !d.discard {
				if value, ok := strings.CutPrefix(line, "EDO_K3S_STAGE\t"); ok {
					stage = value
				}
			}
			if installationStages[stage] == "" {
				stage = ""
				if strings.HasPrefix(d.stage, "installer") {
					stage = officialInstallerStage(line)
				}
			}
			if stage != "" {
				d.setStageLocked(stage, time.Now())
			}
			d.line, d.discard = d.line[:0], false
		} else if len(d.line) < 80 && !d.discard {
			d.line = append(d.line, b)
		} else {
			d.discard = true
		}
	}
	return len(value), d.err
}

func officialInstallerStage(line string) string {
	if !strings.HasPrefix(line, "[INFO] ") {
		return ""
	}
	line = strings.TrimLeft(strings.TrimPrefix(line, "[INFO] "), " ")
	for _, item := range []struct{ prefix, stage string }{
		{"Downloading hash ", "installer_download"}, {"Downloading binary ", "installer_download"}, {"Downloading K3s artifact ", "installer_download"},
		{"Installing k3s to ", "installer_binary"}, {"env: Creating environment file ", "installer_environment"},
		{"systemd: Creating service file ", "installer_service"}, {"openrc: Creating service file ", "installer_service"},
		{"systemd: Starting ", "installer_start"}, {"openrc: Starting ", "installer_start"},
	} {
		if strings.HasPrefix(line, item.prefix) {
			return item.stage
		}
	}
	if line == "Verifying binary download" {
		return "installer_verify"
	}
	return ""
}

func (d *installationDiagnostics) setStageLocked(stage string, now time.Time) {
	if d.err != nil || installationStages[stage] == "" || d.seen[stage] {
		return
	}
	if now.Before(d.updated) {
		now = d.updated
	}
	if d.seen == nil {
		d.seen = make(map[string]bool)
	}
	d.seen[stage], d.stage, d.started, d.updated = true, stage, now, now
	if d.onStage != nil {
		d.err = d.onStage(stage, now, now, false)
	}
}

func (d *installationDiagnostics) start(stage string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setStageLocked(stage, time.Now())
	return d.err
}

func (d *installationDiagnostics) heartbeat(now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err == nil && d.stage != "" && now.Sub(d.updated) >= 30*time.Second && d.onStage != nil {
		d.updated = now
		d.err = d.onStage(d.stage, d.started, now, true)
	}
	return d.err
}

func (d *installationDiagnostics) failure(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}
	stage := d.stage
	if stage == "" {
		stage = "installer"
	}
	return installationError(stage, err)
}

func installedServiceName(role string, installer map[string]any) string {
	if name, _ := installer["INSTALL_K3S_NAME"].(string); name != "" {
		return "k3s-" + name
	}
	if role == "agent" {
		return "k3s-agent"
	}
	return "k3s"
}

func postInstallVerificationScript(host Host, input Input) string {
	version, _ := input.Installer["INSTALL_K3S_VERSION"].(string)
	service := installedServiceName(host.Role, input.Installer)
	return "printf 'EDO_K3S_STAGE\\tversion\\n'\nedo_version_output=$(timeout 5 " + shellQuote(binaryPath(input.Installer)) + " --version)\nedo_version=$(printf '%s\\n' \"$edo_version_output\" | awk 'NR==1{print $3}')\n[ \"$edo_version\" = " + shellQuote(version) + " ] || exit 45\nunset edo_version_output edo_version\nprintf 'EDO_K3S_STAGE\\tservice\\n'\nedo_service=" + shellQuote(service) + `
edo_service_ready=false
for edo_attempt in $(seq 1 30); do
  if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
    edo_active=$(systemctl show --property=ActiveState --value "$edo_service.service" 2>/dev/null) || exit 46
    edo_substate=$(systemctl show --property=SubState --value "$edo_service.service" 2>/dev/null) || exit 46
    edo_enabled=$(systemctl is-enabled "$edo_service.service" 2>/dev/null) || exit 46
    [ "$edo_enabled" = enabled ] || exit 46
    case "$edo_active:$edo_substate" in
      active:running) edo_service_ready=true; break;;
      failed:*|inactive:*|*:failed|*:dead) exit 46;;
    esac
  elif [ -x /sbin/openrc-run ]; then
    command -v rc-service >/dev/null && command -v rc-update >/dev/null || exit 46
    edo_runlevel=$(rc-update show default 2>/dev/null) || exit 46
    printf '%s\n' "$edo_runlevel" | awk -v want="$edo_service" '$1==want{found=1} END{exit !found}' || exit 46
    if rc-service "$edo_service" status >/dev/null 2>&1; then edo_service_ready=true; break; fi
  else exit 46; fi
  sleep 2
done
"$edo_service_ready" || exit 46
`
}

func expectedSystemDeployments(input Input) []string {
	disabled := stringList(input.ServerOptions["disable"])
	result := []string{}
	for _, component := range []struct{ option, deployment string }{{"coredns", "coredns"}, {"metrics-server", "metrics-server"}, {"local-storage", "local-path-provisioner"}} {
		if !contains(disabled, component.option) {
			result = append(result, component.deployment)
		}
	}
	return result
}

func systemDeploymentReady(value *appsv1.Deployment) bool {
	if value == nil || value.DeletionTimestamp != nil || value.Spec.Replicas == nil || *value.Spec.Replicas < 1 || value.Status.ObservedGeneration < value.Generation {
		return false
	}
	desired := *value.Spec.Replicas
	if value.Status.Replicas != desired || value.Status.UpdatedReplicas != desired || value.Status.ReadyReplicas < desired || value.Status.AvailableReplicas < desired {
		return false
	}
	for _, condition := range value.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func terminalReadinessError(err error) bool {
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		code := status.Status().Code
		return code >= 400 && code < 500 && code != 404 && code != 408 && code != 429
	}
	return false
}

func waitForSystemDeployments(ctx context.Context, client kubernetes.Interface, names []string, interval time.Duration, recheck func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	identities := map[string]types.UID{}
	for {
		if err := recheck(ctx); err != nil {
			return err
		}
		ready := true
		pendingStage := "components"
		for _, name := range names {
			stage := "component_" + name
			value, err := client.AppsV1().Deployments("kube-system").Get(ctx, name, metav1.GetOptions{})
			if terminalReadinessError(err) {
				if apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
					return installationError(stage, ErrPermission)
				}
				return installationError(stage, ErrRemote)
			}
			if err == nil {
				if value == nil || value.Name != name || value.Namespace != "kube-system" || value.UID == "" || identities[name] != "" && identities[name] != value.UID {
					return installationError(stage, ErrHostChanged)
				}
				identities[name] = value.UID
				for _, condition := range value.Status.Conditions {
					if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded" {
						return installationError(stage, ErrReadiness)
					}
				}
			}
			if err != nil || !systemDeploymentReady(value) {
				ready = false
				pendingStage = stage
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return installationError(pendingStage, errors.Join(ErrReadiness, ctx.Err()))
		case <-ticker.C:
		}
	}
}

func sameReadinessCluster(before, after *model.KubernetesCluster) bool {
	return before != nil && after != nil && before.ID == after.ID && after.IsActive && before.ProjectID == after.ProjectID && before.DepartmentID == after.DepartmentID && before.Mode == after.Mode && before.APIServer == after.APIServer && before.KubeconfigCiphertext == after.KubeconfigCiphertext
}

func (s *Service) waitForCreatedComponents(ctx context.Context, record *model.K3sInstallation, snapshot savedInput) error {
	if snapshot.Input.Action != "create" || snapshot.Input.Mode == "k3d" {
		return nil
	}
	initial, err := s.kubernetes.Find(ctx, record.ClusterID)
	if err != nil {
		return installationError("components", err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client, err := s.kubernetes.Clientset(kube.WithInternalClusterAccess(readyCtx, "node-bootstrap"), record.ClusterID)
	if err != nil || client == nil {
		return installationError("components", ErrRemote)
	}
	recheck := func(ctx context.Context) error {
		if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
			return err
		}
		if needsNativeNodePermission(snapshot.Input) {
			if err := s.kubernetes.RequireClusterPermission(ctx, record.ClusterID, "", "kubernetes.node.create"); err != nil {
				return err
			}
		}
		current, err := s.kubernetes.Find(ctx, record.ClusterID)
		if err != nil || !sameReadinessCluster(initial, current) {
			return ErrHostChanged
		}
		return nil
	}
	if err := recheck(readyCtx); err != nil {
		return installationError("components", err)
	}
	version, err := s.kubernetes.Ping(readyCtx, record.ClusterID)
	if err != nil {
		return installationError("version", err)
	}
	expected, _ := snapshot.Input.Installer["INSTALL_K3S_VERSION"].(string)
	if version != expected {
		return installationError("version", ErrTargetVersionChanged)
	}
	return installationError("components", waitForSystemDeployments(readyCtx, client, expectedSystemDeployments(snapshot.Input), 3*time.Second, recheck))
}
