package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshclient"
)

const k3dVersion = "v5.9.0"
const k3dK3sVersion = "v1.37.1+k3s1"

func k3dAgents(options *K3dOptions) int {
	if options == nil || options.Agents == nil {
		return 1
	}
	return *options.Agents
}

func (s *Service) normalizeK3d(ctx context.Context, input Input) (Input, []Host, map[string]string, *model.KubernetesCluster, []string, error) {
	if input.Action != "create" || (input.Profile != "default" && input.Profile != "custom") || input.ServerHostID == "" || input.ClusterID != "" || len(input.AgentHostIDs) != 0 || len(input.JoinHostIDs) != 0 || input.JoinRole != "" || input.ServerToken != "" || len(input.ServerOptions) != 0 || len(input.AgentOptions) != 0 || len(input.Installer) != 0 || strings.TrimSpace(input.Overrides) != "" || input.DownloadSource != "" || input.SystemImageRegistry != nil {
		return input, nil, nil, nil, nil, &ValidationError{Message: "k3d 只支持在一台 SSH 主机创建集群，不接受原生安装参数或加入节点配置"}
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validName.MatchString(input.Name) {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	host, _, err := s.loadHost(ctx, input.ServerHostID)
	if err != nil {
		return input, nil, nil, nil, nil, err
	}
	options := K3dOptions{APIPort: 16443, BindAddress: host.Address}
	if input.K3d != nil {
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
	address, err := netip.ParseAddr(strings.TrimSpace(options.BindAddress))
	address = address.Unmap()
	if err != nil || !address.Is4() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.Zone() != "" || agents < 0 || agents > 8 || options.APIPort < 1024 || options.APIPort > 65535 {
		return input, nil, nil, nil, nil, &ValidationError{Message: "k3d 需要主机实际的非回环单播 IPv4、1024 至 65535 的 API 端口，以及 0 至 8 个 Worker"}
	}
	first := address.As4()[0]
	if first == 0 || first >= 224 {
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
	if advertised, err := netip.ParseAddr(endpoint.Hostname()); err == nil && advertised.Unmap() != address.Unmap() {
		return input, nil, nil, nil, nil, &ValidationError{Message: "k3d API 地址的 IP 必须与所选绑定地址一致"}
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

func k3dResourceName(id string) string { return "edo-" + id }

func k3dWarnings(id string) []string {
	return []string{
		"将在所选主机的本地 Docker 中通过官方 k3d 创建独立容器集群，不安装宿主机 K3s 服务，不修改默认 kubeconfig。",
		"Server 限制为 2 GiB，Worker 各 1 GiB；创建后立即设置 Server 2 核、Worker 1 核及交换限制，启动瞬间尚未限制 CPU。",
		"失败时保留已创建的容器、网络和卷供检查，不自动删除或重建；本次资源名：" + k3dResourceName(id),
		"容器节点不是物理主机，集群会独立接入，不自动建立物理节点关联。",
	}
}

func (s *Service) runK3dPreflight(ctx context.Context, input Input, hosts []Host) (*PreflightReport, error) {
	if len(hosts) != 1 || input.K3d == nil {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	report := &PreflightReport{CheckedAt: time.Now().UTC(), Checks: []PreflightCheck{}, Hosts: []HostPreflight{}}
	host, bundle, err := s.loadHost(ctx, hosts[0].ID)
	if err != nil {
		slog.ErrorContext(ctx, "读取 k3d 预检主机失败", "operation", "k3d_preflight_load_host", "host_id", hosts[0].ID, "err", err)
		return nil, err
	}
	output, err := s.transport.Read(ctx, host, bundle, k3dPreflightScript(input))
	if err != nil {
		slog.ErrorContext(ctx, "读取 k3d 主机预检失败", "operation", "k3d_preflight_read", "host_id", hosts[0].ID, "err", err)
	}
	checked := parseHostPreflight(hosts[0], output, err, k3dPreflightDefinitions)
	report.Hosts = append(report.Hosts, checked)
	report.Passed = checked.Passed
	return report, nil
}

func (s *Service) performK3d(ctx context.Context, record *model.K3sInstallation, snapshot savedInput) error {
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
	if err := s.log(record, "创建 k3d 集群："+k3dResourceName(record.ID)); err != nil {
		return err
	}
	if err := s.hostProgress(record, hosts, 0, "installing", ""); err != nil {
		return err
	}
	if err := s.runHostOperation(ctx, record, hosts, 0, "k3d_tool", func(ctx context.Context, output io.Writer) error {
		return s.transport.Run(ctx, current, bundle, prepare, output)
	}); err != nil {
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
		return s.transport.Run(ctx, current, bundle, script, output)
	}); err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	connectionRecord := *record
	var value string
	if err := s.runHostOperation(ctx, record, hosts, 0, "api", func(ctx context.Context, _ io.Writer) error {
		var err error
		value, err = s.waitForK3dConnection(ctx, &connectionRecord, snapshot, current, bundle)
		return err
	}); err != nil {
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
	if err := s.runHostOperation(ctx, record, hosts, 0, "node", func(ctx context.Context, _ io.Writer) error {
		return s.waitForK3d(ctx, &readyRecord, snapshot, 1+k3dAgents(input.K3d))
	}); err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	if err := s.hostProgress(record, hosts, 0, "succeeded", "k3d 容器集群已就绪，未关联物理节点"); err != nil {
		return err
	}
	return s.log(record, "k3d 集群已独立接入 EDO："+k3dResourceName(record.ID))
}

func (s *Service) waitForK3dConnection(ctx context.Context, record *model.K3sInstallation, snapshot savedInput, host model.Host, bundle sshclient.Bundle) (string, error) {
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
		raw, readErr := s.transport.Read(ctx, host, bundle, k3dReadConfigScript(record.ID))
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
