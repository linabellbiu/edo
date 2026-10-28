package k3s

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"edo/internal/hostidentity"
	"edo/internal/kube"
	"edo/internal/model"
)

const preflightVersion = 1

// PreflightTimeout bounds the complete multi-host read-only check. HTTP callers
// use the same budget instead of the server's shorter ordinary request limit.
const PreflightTimeout = 150 * time.Second

type PreflightReport struct {
	CheckedAt time.Time        `json:"checked_at"`
	Passed    bool             `json:"passed"`
	Checks    []PreflightCheck `json:"checks"`
	Hosts     []HostPreflight  `json:"hosts"`
}

type PreflightError struct{ Report *PreflightReport }

func (e *PreflightError) Error() string { return ErrPreflightFailed.Error() }
func (e *PreflightError) Unwrap() error { return ErrPreflightFailed }

func check(id, name, status, message string) PreflightCheck {
	return PreflightCheck{ID: id, Name: name, Status: status, Message: message}
}

func (s *Service) preparePreflightInput(ctx context.Context, input Input) (Input, error) {
	if input.ClusterID == "" {
		return input, nil
	}
	if s.kubernetes == nil {
		return input, ErrRemote
	}
	if err := s.kubernetes.RequireClusterPermission(ctx, input.ClusterID, "", "kubernetes.node.create"); err != nil {
		return input, err
	}
	connection, err := s.kubernetes.RESTConfig(kube.WithInternalClusterAccess(ctx, "node-bootstrap"), input.ClusterID)
	if err != nil || connection == nil || connection.Insecure {
		return input, ErrRemote
	}
	input.targetCAData = append([]byte(nil), connection.CAData...)
	if isServerJoin(input) {
		if err := s.prepareServerJoin(ctx, &input); err != nil {
			return input, err
		}
	}
	return input, nil
}

// Every invocation reads the machines and the target API anew. A displayed
// report is evidence for review, never a reusable authorization to install.
func (s *Service) runPreflight(ctx context.Context, input Input, hosts []Host) (*PreflightReport, error) {
	if input.Mode == "k3d" {
		return s.runK3dPreflight(ctx, input, hosts)
	}
	ctx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()
	report := &PreflightReport{CheckedAt: time.Now().UTC(), Checks: []PreflightCheck{}, Hosts: make([]HostPreflight, len(hosts))}
	endpointReady := input.Action == "join"
	clusterPrepared := false
	var nodes []corev1.Node
	if endpointReady {
		clusterTimeout := 15 * time.Second
		if isServerJoin(input) {
			clusterTimeout = 45 * time.Second
		}
		clusterCtx, stop := context.WithTimeout(ctx, clusterTimeout)
		prepared, err := s.preparePreflightInput(clusterCtx, input)
		if err == nil {
			input = prepared
			clusterPrepared = true
		}
		stop()
		if err != nil {
			if isServerJoin(input) {
				var blocked *PreflightError
				failure := serverJoinPreflightError(hosts, err)
				if errors.As(failure, &blocked) {
					return blocked.Report, nil
				}
				return nil, failure
			}
			report.Checks = append(report.Checks, check("cluster_access", "目标集群健康与节点读取", "failed", "无法实时核对目标集群健康、证书或完整节点列表，请检查集群连接及节点读取权限"))
		}
	}
	if isServerJoin(input) && clusterPrepared {
		report.Checks = append(report.Checks, check("server_ha", "Master 高可用、凭据与关键配置", "passed", "已确认目标为 embedded-etcd，Server Token 有效，现有控制平面健康且关键配置可核对"))
		if (len(input.serverJoin.Members)+1)%2 == 0 {
			report.Checks = append(report.Checks, check("etcd_quorum", "etcd 成员数量", "warning", "加入后 etcd 成员为偶数，建议最终配置 3 或 5 个成员；请逐台添加并等待健康"))
		}
	}
	// Bounded concurrency keeps multi-host previews within the request budget.
	var workers sync.WaitGroup
	slots := make(chan struct{}, 8)
	for index, selected := range hosts {
		workers.Add(1)
		go func(index int, selected Host) {
			defer workers.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				report.Hosts[index] = failedHostPreflight(selected)
				return
			}
			probeCtx, stop := context.WithTimeout(ctx, 35*time.Second)
			defer stop()
			current, bundle, err := s.loadHost(probeCtx, selected.ID)
			if err != nil {
				slog.ErrorContext(probeCtx, "读取 K3s 预检主机失败", "operation", "k3s_preflight_load_host", "host_id", selected.ID, "err", err)
				report.Hosts[index] = failedHostPreflight(selected)
				return
			}
			output, err := s.transport.Read(probeCtx, current, bundle, preflightProbeScript(selected, input, endpointReady))
			if err != nil {
				slog.ErrorContext(probeCtx, "读取 K3s 主机预检失败", "operation", "k3s_preflight_read", "host_id", selected.ID, "err", err)
			}
			report.Hosts[index] = parsePreflightProbe(selected, output, err)
			if isServerJoin(input) && report.Hosts[index].Passed {
				peerScript, peerErr := serverPeerProbeScript(input.serverJoin)
				peerOutput := ""
				if peerErr == nil {
					peerOutput, peerErr = s.transport.Read(probeCtx, current, bundle, peerScript)
				}
				status, message := "passed", "从新 Master 到现有 etcd 成员的 2379/2380 TCP 连接可达"
				if peerErr != nil || strings.TrimSpace(peerOutput) != "EDO_K3S_ETCD_PEERS_OK" {
					status, message = "failed", "无法从新 Master 连接现有 etcd 成员的 2379/2380 端口；此检查需要 bash 与 timeout，请核对网络及工具"
					report.Hosts[index].Passed = false
				}
				report.Hosts[index].Checks = append(report.Hosts[index].Checks, check("etcd_peers", "etcd 成员端口连通性", status, message))
			}
		}(index, selected)
	}
	workers.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if clusterPrepared {
		// Read membership after the potentially slow SSH probes, so a node
		// installed while those probes ran cannot bypass duplicate detection.
		clusterCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		var err error
		nodes, err = s.preflightNodes(clusterCtx, input.ClusterID)
		stop()
		if err != nil {
			report.Checks = append(report.Checks, check("cluster_access", "目标集群健康与节点读取", "failed", "无法实时核对目标集群健康或完整节点列表，请检查集群连接及节点读取权限"))
		} else {
			report.Checks = append(report.Checks, check("cluster_access", "目标集群健康与节点读取", "passed", "已实时确认 API 就绪并读取完整节点列表"))
		}
	}
	checkPreflightIdentities(report, hosts, input, nodes)
	report.Passed = len(hosts) > 0
	for _, item := range report.Checks {
		report.Passed = report.Passed && item.Status != "failed"
	}
	for _, host := range report.Hosts {
		report.Passed = report.Passed && host.Passed
	}
	return report, nil
}

func failedHostPreflight(host Host) HostPreflight {
	return HostPreflight{HostID: host.ID, HostName: host.Name, Role: host.Role, Checks: []PreflightCheck{
		check("ssh", "SSH 与主机范围", "failed", "无法使用已保存且指纹固定的 SSH 连接完成检查，请检查主机状态、连接凭据和 sudo 权限"),
	}}
}

func (s *Service) preflightNodes(ctx context.Context, clusterID string) ([]corev1.Node, error) {
	if err := s.kubernetes.RequireClusterPermission(ctx, clusterID, "", "kubernetes.node.create"); err != nil {
		return nil, err
	}
	client, err := s.kubernetes.Clientset(kube.WithInternalClusterAccess(ctx, "node-bootstrap"), clusterID)
	if err != nil || client == nil {
		return nil, ErrRemote
	}
	if _, err := client.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx); err != nil {
		return nil, ErrRemote
	}
	nodes := make([]corev1.Node, 0)
	continuation, seen := "", map[string]bool{}
	for {
		page, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil || len(nodes)+len(page.Items) > 10000 {
			return nil, ErrRemote
		}
		nodes = append(nodes, page.Items...)
		continuation = page.Continue
		if continuation == "" {
			break
		}
		if seen[continuation] || len(seen) >= 100 {
			return nil, ErrRemote
		}
		seen[continuation] = true
	}
	if len(nodes) == 0 {
		return nil, ErrRemote
	}
	return nodes, nil
}

func nodeHostIdentity(node corev1.Node) hostidentity.Identity {
	identity := hostidentity.Identity{MachineID: node.Status.NodeInfo.MachineID, SystemUUID: node.Status.NodeInfo.SystemUUID, BootID: node.Status.NodeInfo.BootID}
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP || address.Type == corev1.NodeExternalIP {
			identity.Addresses = append(identity.Addresses, address.Address)
		}
	}
	return identity
}

func checkPreflightIdentities(report *PreflightReport, hosts []Host, input Input, nodes []corev1.Node) {
	for index := range report.Hosts {
		host := &report.Hosts[index]
		if !host.Passed {
			continue
		}
		options := optionsForHost(hosts[index], input)
		namesUnique, machinesUnique := true, true
		registersNode := hosts[index].Role != "server" || options["disable-agent"] != true
		withNodeID, _ := options["with-node-id"].(bool)
		for other := range report.Hosts {
			if other == index {
				continue
			}
			peer := report.Hosts[other]
			if hostidentity.Matches(host.Identity, peer.Identity) {
				machinesUnique = false
			}
			peerWithID, _ := optionsForHost(hosts[other], input)["with-node-id"].(bool)
			peerRegistersNode := hosts[other].Role != "server" || optionsForHost(hosts[other], input)["disable-agent"] != true
			if registersNode && peerRegistersNode && !withNodeID && !peerWithID && host.Hostname != "" && host.Hostname == peer.Hostname {
				namesUnique = false
			}
		}
		for _, node := range nodes {
			if hostidentity.Matches(nodeHostIdentity(node), host.Identity) {
				machinesUnique = false
			}
			if registersNode && !withNodeID && strings.EqualFold(host.Hostname, node.Name) {
				namesUnique = false
			}
		}
		status, message := "passed", "未发现与其他候选主机或目标集群节点重复的物理身份"
		if !machinesUnique {
			status, message = "failed", "该机器已是目标集群节点，或与本次另一条主机记录指向同一机器；请接入现有节点，勿重复安装"
		}
		host.Checks = append(host.Checks, check("machine_unique", "主机身份唯一性", status, message))
		status, message = "passed", "最终节点名称未被本批次或目标集群节点占用"
		if !registersNode {
			status, message = "warning", "该控制平面已禁用 Agent，不注册 Kubernetes 节点；仍检查主机物理身份及已有安装"
		} else if withNodeID {
			status, message = "warning", "已启用 with-node-id，K3s 将为节点名称追加唯一后缀；安装后会再次核对节点身份"
		} else if !namesUnique {
			status, message = "failed", "最终节点名称已被占用，请为该主机设置唯一的 node-name 或启用 with-node-id"
		}
		host.Checks = append(host.Checks, check("node_name_unique", "节点名称唯一性", status, message))
		host.Passed = machinesUnique && namesUnique
	}
}

func requirePreflight(report *PreflightReport) error {
	if report == nil || !report.Passed {
		return &PreflightError{Report: report}
	}
	return nil
}

func (s *Service) beforeHostInstall(ctx context.Context, record model.K3sInstallation, snapshot savedInput, host Host) (Input, error) {
	if err := s.recheckRuntime(ctx, record, snapshot); err != nil {
		return Input{}, err
	}
	input := snapshot.Input
	if host.Role == "agent" && record.ClusterID != "" {
		input.Action, input.ClusterID = "join", record.ClusterID
		if snapshot.Input.Action == "create" {
			// A custom creation channel may have resolved since preview. Workers
			// always follow the server actually created, using an exact version.
			version, err := s.liveTargetVersion(ctx, record.ClusterID)
			if err != nil {
				return Input{}, err
			}
			input.Installer = maps.Clone(input.Installer)
			input.Installer["INSTALL_K3S_VERSION"] = version
			delete(input.Installer, "INSTALL_K3S_CHANNEL")
			delete(input.Installer, "INSTALL_K3S_CHANNEL_URL")
			input.targetVersion = version
		}
	}
	prepared, err := s.preparePreflightInput(ctx, input)
	if err != nil {
		return Input{}, err
	}
	report, err := s.runPreflight(ctx, prepared, []Host{host})
	if err != nil {
		return Input{}, err
	}
	if err := requirePreflight(report); err != nil {
		return Input{}, err
	}
	if err := s.recheckRuntime(ctx, record, snapshot); err != nil {
		return Input{}, err
	}
	return prepared, nil
}

func preflightFailureMessage(err error) string {
	var failed *PreflightError
	if !errors.As(err, &failed) || failed.Report == nil {
		return ErrPreflightFailed.Error()
	}
	for _, item := range failed.Report.Checks {
		if item.Status == "failed" {
			return item.Message
		}
	}
	for _, host := range failed.Report.Hosts {
		for _, item := range host.Checks {
			if item.Status == "failed" {
				return fmt.Sprintf("%s：%s", host.HostName, item.Message)
			}
		}
	}
	return ErrPreflightFailed.Error()
}
