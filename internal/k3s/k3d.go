package k3s

import (
	"context"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshclient"
)

const k3dVersion = "v5.9.0"

// 仅用于缺少镜像字段的历史快照，新计划必须显式选择镜像。
const legacyK3dK3sVersion = "v1.37.1+k3s1"

func k3dAgents(options *K3dOptions) int {
	if options == nil || options.Agents == nil {
		return 1
	}
	return *options.Agents
}

func k3dResources(options *K3dOptions) (server, worker K3dNodeResources) {
	// 旧任务未记录资源字段时，继续使用其创建时的既有默认值。
	server = K3dNodeResources{CPUMillis: 2000, MemoryMiB: 2048}
	worker = K3dNodeResources{CPUMillis: 1000, MemoryMiB: 1024}
	if options != nil {
		if options.ServerResources != nil {
			server = *options.ServerResources
		}
		if options.WorkerResources != nil {
			worker = *options.WorkerResources
		}
	}
	return
}

func validateK3dResources(options *K3dOptions) error {
	server, worker := k3dResources(options)
	for _, node := range []struct {
		role string
		K3dNodeResources
	}{{"Server", server}, {"Worker", worker}} {
		if node.CPUMillis < 10 || node.CPUMillis > math.MaxInt64/1_000_000 {
			return &ValidationError{Message: "k3d " + node.role + " CPU 至少为 0.01 核，且不能超出容器资源范围"}
		}
		if node.MemoryMiB < 6 || node.MemoryMiB > math.MaxInt64/(1024*1024) {
			return &ValidationError{Message: "k3d " + node.role + " 内存至少为 6 MiB，且不能超出容器资源范围"}
		}
	}
	return nil
}

func k3dCPUArgument(millis int64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%03d", millis/1000, millis%1000), "0"), ".")
}

func k3dMemoryArgument(mib int64) string {
	if mib%1024 == 0 {
		return strconv.FormatInt(mib/1024, 10) + "g"
	}
	return strconv.FormatInt(mib, 10) + "m"
}

func (s *Service) normalizeK3d(ctx context.Context, input Input) (Input, []Host, map[string]string, *model.KubernetesCluster, []string, error) {
	if input.Action != "create" || (input.Profile != "default" && input.Profile != "custom") || input.ServerHostID == "" || input.ClusterID != "" || len(input.AgentHostIDs) != 0 || len(input.JoinHostIDs) != 0 || input.JoinRole != "" || input.ServerToken != "" || len(input.ServerOptions) != 0 || len(input.AgentOptions) != 0 || len(input.Installer) != 0 || strings.TrimSpace(input.Overrides) != "" || input.DownloadSource != "" || input.SystemImageRegistry != nil {
		return input, nil, nil, nil, nil, &ValidationError{Message: "k3d 只支持在一台本地或 SSH 主机创建集群，不接受原生安装参数或加入节点配置"}
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validName.MatchString(input.Name) {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	host, _, err := s.loadInstallationHost(ctx, "k3d", input.ServerHostID)
	if err != nil {
		return input, nil, nil, nil, nil, err
	}
	if err := kube.RequireHostClusterAvailable(ctx, s.db, []string{host.ID}, ""); err != nil {
		return input, nil, nil, nil, nil, err
	}
	options := K3dOptions{APIPort: 16443, BindAddress: host.Address}
	if host.Mode == model.HostModeLocal {
		local, err := s.localK3d.describe(ctx)
		if err != nil {
			return input, nil, nil, nil, nil, err
		}
		input.localK3d = &localK3dSnapshot{Revision: local.revision, DaemonID: local.daemonID}
		options.BindAddress = local.BindAddress
		if input.K3d != nil {
			options = *input.K3d
			if options.BindAddress == "" {
				options.BindAddress = local.BindAddress
			}
			if options.APIPort == 0 {
				options.APIPort = 16443
			}
		}
		if input.Endpoint == "" {
			input.Endpoint = "https://" + net.JoinHostPort(local.APIHost, strconv.Itoa(options.APIPort))
		}
	}
	if input.K3d != nil && host.Mode != model.HostModeLocal {
		options = *input.K3d
		if options.APIPort == 0 {
			options.APIPort = 16443
		}
		if strings.TrimSpace(options.BindAddress) == "" {
			options.BindAddress = host.Address
		}
	}
	agents := k3dAgents(input.K3d)
	options.Agents = &agents
	if err := validateK3dResources(&options); err != nil {
		slog.WarnContext(ctx, "k3d 节点资源配置无效", "operation", "k3d_validate_resources", "host_id", host.ID, "err", err)
		return input, nil, nil, nil, nil, err
	}
	server, worker := k3dResources(&options)
	options.ServerResources, options.WorkerResources = &server, &worker
	if err := normalizeK3dImage(&options); err != nil {
		slog.WarnContext(ctx, "k3d 镜像配置无效", "operation", "k3d_validate_image", "host_id", host.ID, "err", err)
		return input, nil, nil, nil, nil, err
	}
	address, err := netip.ParseAddr(strings.TrimSpace(options.BindAddress))
	address = address.Unmap()
	if err != nil || !address.Is4() || (!address.IsGlobalUnicast() && !(host.Mode == model.HostModeLocal && (address.IsLoopback() || address.IsUnspecified()))) || (address.IsLoopback() && host.Mode != model.HostModeLocal) || address.IsLinkLocalUnicast() || address.Zone() != "" || agents < 0 || agents > 8 || options.APIPort < 1024 || options.APIPort > 65535 {
		return input, nil, nil, nil, nil, &ValidationError{Message: "k3d 需要主机实际的非回环单播 IPv4、1024 至 65535 的 API 端口，以及 0 至 8 个 Worker"}
	}
	first := address.As4()[0]
	if first == 0 && !(host.Mode == model.HostModeLocal && address.IsUnspecified()) || first >= 224 {
		return input, nil, nil, nil, nil, &ValidationError{Message: "k3d API 绑定地址不能使用保留或组播 IPv4"}
	}
	options.BindAddress = address.Unmap().String()
	input.K3d = &options
	input.Endpoint = strings.TrimRight(strings.TrimSpace(input.Endpoint), "/")
	if input.Endpoint == "" {
		input.Endpoint = "https://" + net.JoinHostPort(options.BindAddress, strconv.Itoa(options.APIPort))
	}
	endpoint, err := url.Parse(input.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" || endpoint.Port() != strconv.Itoa(options.APIPort) || strings.ContainsAny(input.Endpoint, "\r\n\x00") {
		return input, nil, nil, nil, nil, &ValidationError{Message: "k3d API 地址必须是 HTTPS 地址，端口须与绑定端口一致"}
	}
	if advertised, err := netip.ParseAddr(endpoint.Hostname()); err == nil && advertised.Unmap() != address.Unmap() && host.Mode != model.HostModeLocal {
		return input, nil, nil, nil, nil, &ValidationError{Message: "k3d API 地址的 IP 必须与所选绑定地址一致"}
	}
	if host.Mode == model.HostModeLocal && (endpoint.Hostname() == "0.0.0.0" || endpoint.Hostname() == "::") {
		return input, nil, nil, nil, nil, &ValidationError{Message: "本地 k3d API 地址必须填写 EDO 可以访问的主机名或 IP"}
	}
	var existing int64
	if err := s.db.WithContext(ctx).Model(&model.KubernetesCluster{}).Where("name = ?", input.Name).Count(&existing).Error; err != nil {
		return input, nil, nil, nil, nil, err
	}
	if existing > 0 {
		return input, nil, nil, nil, nil, kube.ErrClusterExists
	}
	hosts := []Host{{ID: host.ID, Name: host.Name, Address: host.Address, Role: "host"}}
	return input, hosts, map[string]string{host.ID: hostDigest(host)}, nil, nil, nil
}

func k3dResourceName(id string) string {
	parsed, err := uuid.Parse(id)
	if err != nil || len(id) != 36 {
		return ""
	}
	// 保留完整 128 位 UUID，同时满足固定 k3d 版本的 32 字符名称上限。
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(parsed[:])
	return "edo-" + strings.ToLower(encoded)
}

func k3dImageVolumeName(id string) string {
	name := k3dResourceName(id)
	if name == "" {
		return ""
	}
	return "k3d-" + name + "-images"
}

func (s *Service) runK3dPreflight(ctx context.Context, input Input, hosts []Host) (*PreflightReport, error) {
	if input.ServerHostID == model.BuiltinLocalHostID {
		return s.localK3d.preflight(ctx, input, hosts)
	}
	if len(hosts) != 1 || input.K3d == nil {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	report := &PreflightReport{CheckedAt: time.Now().UTC(), Checks: []PreflightCheck{}, Hosts: []HostPreflight{}}
	host, bundle, err := s.loadHost(ctx, hosts[0].ID)
	if err != nil {
		slog.ErrorContext(ctx, "读取 k3d 预检主机失败", "operation", "k3d_preflight_load_host", "host_id", hosts[0].ID, "err", err)
		return nil, err
	}
	privilege, privilegeErr := s.transport.ProbePrivileges(ctx, host, bundle)
	if blocked := privilegePreflight(hosts[0], privilege, privilegeErr); blocked != nil {
		report.Hosts = append(report.Hosts, *blocked)
		return report, nil
	}
	output, err := s.transport.Read(ctx, host, bundle, k3dPreflightScript(input))
	if err != nil {
		slog.ErrorContext(ctx, "读取 k3d 主机预检失败", "operation", "k3d_preflight_read", "host_id", hosts[0].ID, "err", err)
	}
	checked := parseK3dPreflight(hosts[0], output, err)
	redactor := newInstallationLogRedactor(installationLogSecrets(input, bundle))
	for _, line := range strings.Split(output, "\n") {
		if encoded, ok := strings.CutPrefix(line, "EDO_K3S_TERMINAL\t"); ok && len(encoded) <= 96*1024 {
			if decoded, decodeErr := base64.StdEncoding.DecodeString(encoded); decodeErr == nil {
				for _, text := range strings.Split(string(decoded), "\n") {
					if safe := redactor.line(text); safe != "" {
						checked.Logs = appendBoundedInstallationLog(checked.Logs, safe+"\n", 64*1024)
					}
				}
			}
		}
	}
	privilege = completedPrivilege(privilege, checked.Checks, err)
	checked.Privilege = &privilege
	report.Hosts = append(report.Hosts, checked)
	report.Passed = checked.Passed
	return report, nil
}

func (s *Service) performK3d(ctx context.Context, record *model.K3sInstallation, snapshot savedInput) error {
	if snapshot.Input.ServerHostID == model.BuiltinLocalHostID {
		return s.performLocalK3d(ctx, record, snapshot)
	}
	resourceName := k3dResourceName(record.ID)
	if resourceName == "" {
		slog.ErrorContext(ctx, "k3d 安装任务标识无效", "operation", "k3d_resource_name", "err", ErrInvalidInput)
		return ErrInvalidInput
	}
	input := snapshot.Input
	host := Host{ID: input.ServerHostID, Role: "host"}
	var hosts []Host
	if err := json.Unmarshal(record.Hosts, &hosts); err != nil || len(hosts) != 1 || hosts[0].ID != host.ID || input.K3d == nil {
		return ErrInvalidInput
	}
	host = hosts[0]
	if err := s.hostProgress(record, hosts, 0, "checking", ""); err != nil {
		return err
	}
	report, err := s.runK3dPreflight(ctx, input, hosts)
	if err != nil {
		return err
	}
	if err := requirePreflight(report); err != nil {
		return err
	}
	current, bundle, err := s.loadReviewedHost(ctx, snapshot, host.ID)
	if err != nil {
		return err
	}
	prepare, err := k3dPrepareToolScript(input, record.ID)
	if err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	if err := s.log(record, "创建 k3d 集群："+resourceName); err != nil {
		return err
	}
	if err := s.hostProgress(record, hosts, 0, "installing", ""); err != nil {
		return err
	}
	if err := s.runHostOperation(ctx, record, hosts, 0, "k3d_tool", func(ctx context.Context, output io.Writer) error {
		return s.transport.Run(ctx, current, bundle, prepare, output)
	}, installationLogSecrets(input, bundle)...); err != nil {
		return err
	}
	// 工具下载可能较慢；在真正创建容器前重新检查授权、主机和资源。
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	report, err = s.runK3dPreflight(ctx, input, hosts)
	if err != nil {
		return err
	}
	if err := requirePreflight(report); err != nil {
		return err
	}
	current, bundle, err = s.loadReviewedHost(ctx, snapshot, host.ID)
	if err != nil {
		return err
	}
	script, err := k3dInstallScript(input, record.ID)
	if err != nil {
		return err
	}
	if err := s.runHostOperation(ctx, record, hosts, 0, "k3d_create", func(ctx context.Context, output io.Writer) error {
		err := s.transport.Run(ctx, current, bundle, script, output)
		if err != nil && ctx.Err() == nil {
			s.readTerminalSnapshots(ctx, current, bundle, k3dTerminalObservations(input, record.ID, false), output)
		}
		return err
	}, installationLogSecrets(input, bundle)...); err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	connectionRecord := *record
	terminalLogs := &terminalDeduplicator{}
	var value string
	if err := s.runHostOperation(ctx, record, hosts, 0, "api", func(ctx context.Context, output io.Writer) error {
		terminalLogs.output = output
		return s.withTerminalObservations(ctx, current, bundle, k3dTerminalObservations(input, record.ID, true), terminalLogs, func(ctx context.Context) error {
			var err error
			value, err = s.waitForK3dConnection(ctx, &connectionRecord, snapshot, current, bundle)
			return err
		})
	}, installationLogSecrets(input, bundle)...); err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	cluster, err := s.kubernetes.Create(ctx, record.CreatedBy, kube.Input{Name: input.Name, Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &value})
	if err != nil {
		return err
	}
	record.ClusterID = cluster.ID
	if err := s.db.WithContext(ctx).Model(&model.K3sInstallation{}).Where("uuid = ? AND lease_owner = ?", record.ID, s.owner).Update("cluster_id", cluster.ID).Error; err != nil {
		return err
	}
	readyRecord := *record
	if err := s.runHostOperation(ctx, record, hosts, 0, "node", func(ctx context.Context, output io.Writer) error {
		terminalLogs.output = output
		return s.withTerminalObservations(ctx, current, bundle, k3dTerminalObservations(input, record.ID, true), terminalLogs, func(ctx context.Context) error {
			return s.waitForK3d(ctx, &readyRecord, snapshot, 1+k3dAgents(input.K3d))
		})
	}, installationLogSecrets(input, bundle)...); err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	if err := s.hostProgress(record, hosts, 0, "succeeded", "k3d 容器集群已就绪，未关联物理节点"); err != nil {
		return err
	}
	return s.log(record, "k3d 集群已独立接入 EDO："+resourceName)
}

func (s *Service) waitForK3dConnection(ctx context.Context, record *model.K3sInstallation, snapshot savedInput, host model.Host, bundle sshclient.Bundle) (string, error) {
	return s.waitForK3dConnectionRead(ctx, record, snapshot, func(ctx context.Context) (string, error) {
		return s.transport.Read(ctx, host, bundle, k3dReadConfigScript(record.ID))
	})
}

func (s *Service) waitForK3dConnectionRead(ctx context.Context, record *model.K3sInstallation, snapshot savedInput, read func(context.Context) (string, error)) (string, error) {
	if err := k3dWaitContextError(ctx); err != nil {
		return "", err
	}
	if k3dResourceName(record.ID) == "" {
		slog.ErrorContext(ctx, "k3d 连接任务标识无效", "operation", "k3d_connection_resource_name", "err", ErrInvalidInput)
		return "", ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	interval := s.k3dWaitInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := k3dWaitContextError(ctx); err != nil {
			return "", err
		}
		if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
			if canceled := k3dWaitContextError(ctx); canceled != nil {
				return "", canceled
			}
			return "", err
		}
		raw, readErr := read(ctx)
		if err := k3dWaitContextError(ctx); err != nil {
			return "", err
		}
		if readErr == nil {
			value, err := rewriteKubeconfigEndpoint([]byte(raw), snapshot.Input.Endpoint)
			if err == nil {
				_, err = s.kubernetes.Test(ctx, kube.Input{Name: snapshot.Input.Name, Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &value})
				if canceled := k3dWaitContextError(ctx); canceled != nil {
					return "", canceled
				}
				if err == nil {
					return value, nil
				}
				if apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
					return "", ErrPermission
				}
				if errors.Is(err, kube.ErrUnsafeKubeconfig) || errors.Is(err, kube.ErrInvalidCluster) {
					return "", ErrRemote
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", k3dWaitContextError(ctx)
		case <-ticker.C:
		}
	}
}

func k3dWaitContextError(ctx context.Context) error {
	err := ctx.Err()
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrReadiness
	}
	return err
}

func (s *Service) waitForK3d(ctx context.Context, record *model.K3sInstallation, snapshot savedInput, expected int) error {
	if err := k3dWaitContextError(ctx); err != nil {
		return err
	}
	client, err := s.kubernetes.Clientset(kube.WithInternalClusterAccess(ctx, "node-bootstrap"), record.ClusterID)
	if err != nil {
		if canceled := k3dWaitContextError(ctx); canceled != nil {
			return canceled
		}
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	interval := s.k3dWaitInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := k3dWaitContextError(ctx); err != nil {
			return err
		}
		if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
			if canceled := k3dWaitContextError(ctx); canceled != nil {
				return canceled
			}
			return err
		}
		_, pingErr := s.kubernetes.Ping(ctx, record.ClusterID)
		if err := k3dWaitContextError(ctx); err != nil {
			return err
		}
		if pingErr == nil {
			nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "edo.dev/installation=" + record.ID})
			if canceled := k3dWaitContextError(ctx); canceled != nil {
				return canceled
			}
			if apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
				return ErrPermission
			}
			if err == nil && len(nodes.Items) == expected {
				ready := true
				for _, node := range nodes.Items {
					found := false
					for _, condition := range node.Status.Conditions {
						found = found || condition.Type == "Ready" && condition.Status == "True"
					}
					ready = ready && found
				}
				if ready {
					return nil
				}
			}
		} else if apierrors.IsUnauthorized(pingErr) || apierrors.IsForbidden(pingErr) {
			return ErrPermission
		}
		select {
		case <-ctx.Done():
			return k3dWaitContextError(ctx)
		case <-ticker.C:
		}
	}
}
