package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"edo/internal/kube"
	"edo/internal/model"
)

func isServerJoin(input Input) bool { return input.Action == "join" && input.JoinRole == "server" }

func normalizeJoinSelection(input *Input) error {
	input.JoinRole = strings.TrimSpace(input.JoinRole)
	if input.Action != "join" {
		if input.JoinRole != "" || len(input.JoinHostIDs) > 0 || input.ServerToken != "" {
			return &ValidationError{Message: "创建集群不接受加入节点的角色、主机或 Server Token"}
		}
		return nil
	}
	if input.JoinRole == "" {
		input.JoinRole = "agent"
	}
	if input.JoinRole != "agent" && input.JoinRole != "server" {
		return &ValidationError{Message: "请选择 Master 或 Worker 节点角色"}
	}
	if len(input.JoinHostIDs) > 0 && len(input.AgentHostIDs) > 0 {
		return &ValidationError{Message: "加入主机列表不能重复提供"}
	}
	if len(input.JoinHostIDs) == 0 && input.JoinRole == "agent" {
		input.JoinHostIDs = append([]string(nil), input.AgentHostIDs...)
	}
	if input.JoinRole == "server" {
		if len(input.JoinHostIDs) != 1 || len(input.AgentHostIDs) > 0 {
			return &ValidationError{Message: "为保护 etcd 仲裁，请每次只添加一台 Master"}
		}
		input.ServerToken = strings.TrimSpace(input.ServerToken)
		if input.ServerToken == "" || len(input.ServerToken) > 4096 || strings.ContainsAny(input.ServerToken, "\r\n\x00") {
			return &ValidationError{Message: "添加 Master 需要管理员提供完整的安全 Server Token"}
		}
	} else if input.ServerToken != "" {
		return &ValidationError{Message: "Worker 不接受长期 Server Token，请使用后台生成的短期加入凭据"}
	}
	return nil
}

func (s *Service) prepareServerJoin(ctx context.Context, input *Input) error {
	if !isServerJoin(*input) {
		return nil
	}
	if !preloadedBinary(input.Installer) {
		if _, err := serverArtifactBase(input.Installer); err != nil {
			return err
		}
	}
	if len(input.AgentOptions) > 0 {
		return &ValidationError{Message: "Master 加入请使用 Server 选项，不能同时提供 Agent 选项"}
	}
	// Component arguments override K3s-generated membership, datastore, TLS,
	// networking and signing configuration after the critical options check.
	for _, key := range []string{"etcd-arg", "kube-apiserver-arg", "kube-controller-manager-arg", "kube-scheduler-arg", "kube-cloud-controller-manager-arg", "kube-controller-arg", "kube-cloud-controller-arg", "cloud-controller-manager-arg"} {
		if value, exists := input.ServerOptions[key]; exists && len(stringList(value)) > 0 {
			return &ValidationError{Message: "添加 Master 不允许覆盖 " + key + "，以免改变已核对的集群成员、关键配置或认证设置"}
		}
	}
	for _, key := range []string{"cluster-init", "cluster-reset", "disable-etcd", "disable-apiserver", "disable-controller-manager", "disable-scheduler", "disable-agent", "rootless"} {
		if value, ok := input.ServerOptions[key]; ok && value != nil {
			if off, valid := value.(bool); !valid || off {
				return &ValidationError{Message: "添加完整 Master 不允许修改 " + key}
			}
		}
	}
	for _, key := range []string{"datastore-endpoint", "datastore-cafile", "datastore-certfile", "datastore-keyfile", "cluster-reset-restore-path"} {
		if value, ok := input.ServerOptions[key]; ok && value != nil {
			if text, valid := value.(string); !valid || text != "" {
				return &ValidationError{Message: "当前自动 Master 加入仅支持已验证的 embedded-etcd，不能覆盖数据存储或恢复参数"}
			}
		}
	}
	info, err := s.kubernetes.InspectServerJoin(ctx, input.ClusterID, input.Endpoint, input.ServerToken)
	if err != nil {
		return err
	}
	if info == nil || info.Datastore != "embedded-etcd" || len(info.Members) == 0 || info.CriticalDigest == "" || info.MembershipDigest == "" || len(info.CriticalOptions) == 0 {
		return kube.ErrServerJoinUnsupported
	}
	if info.Version != input.targetVersion {
		return ErrTargetVersionChanged
	}
	options := maps.Clone(input.ServerOptions)
	if options == nil {
		options = map[string]any{}
	}
	for key, expected := range info.CriticalOptions {
		if value, exists := options[key]; exists {
			if key == "disable" {
				values := stringList(value)
				slices.Sort(values)
				value = slices.Compact(values)
			}
			if !sameOption(value, expected) {
				return &ValidationError{Message: "Master 关键参数必须与目标集群一致：" + key}
			}
		}
		options[key] = expected
	}
	for _, key := range []string{"disable-etcd", "disable-apiserver", "disable-controller-manager", "disable-scheduler", "disable-agent"} {
		options[key] = false
	}
	options["cluster-init"] = false
	input.ServerOptions = options
	input.serverJoin = info
	input.targetCAData = append([]byte(nil), info.CAData...)
	return nil
}

func sameOption(a, b any) bool {
	// Types have already been validated; JSON normalizes []string/[]any and numbers.
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

// Keep the downloaded installer in a root-owned /run directory between SSH
// sessions, so live cluster checks run after the potentially slow download.
func serverInstallerStage(installationID string) (string, error) {
	id, err := uuid.Parse(installationID)
	if err != nil || id.String() != installationID {
		return "", ErrInvalidInput
	}
	return "/run/edo-k3s-stage-" + installationID, nil
}

func serverInstallerDownloadScript(installationID string, input Input) (string, error) {
	sources, err := sourcesForInput(input)
	if err != nil {
		return "", err
	}
	stage, err := serverInstallerStage(installationID)
	if err != nil {
		return "", err
	}
	script := "set -eu\n[ \"$(id -u)\" = 0 ]\numask 077\nstage=" + shellQuote(stage) + "\nmkdir -m 700 \"$stage\"\nedo_stage_ready=false\ncleanup_stage() { if ! \"$edo_stage_ready\"; then rm -f \"$stage/install.sh\" \"$stage/k3s\" \"$stage/sha256sum.txt\" \"$stage/k3s.sha256\"; rmdir \"$stage\"; fi; }\ntrap cleanup_stage EXIT\ntrap 'exit 130' HUP INT TERM\ncurl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 180 --retry 3 " + shellQuote(sources.InstallerURL) + " -o \"$stage/install.sh\"\nchmod 600 \"$stage/install.sh\"\n"
	if !preloadedBinary(input.Installer) {
		version, _ := input.Installer["INSTALL_K3S_VERSION"].(string)
		if _, ok := parseK3sVersion(version); !ok {
			return "", ErrInvalidInput
		}
		script += "command -v sha256sum >/dev/null\ncase \"$(uname -m)\" in x86_64|amd64) edo_arch=amd64; edo_asset=k3s ;; aarch64|arm64) edo_arch=arm64; edo_asset=k3s-arm64 ;; armv7*) edo_arch=arm; edo_asset=k3s-armhf ;; *) exit 44 ;; esac\nedo_release=" + shellQuote(sourceReleaseURL(sources, version)) + "\n"
		curl := "curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 180 --retry 3 "
		script += curl + "--max-filesize 1048576 \"$edo_release/sha256sum-$edo_arch.txt\" -o \"$stage/sha256sum.txt\"\n"
		script += "edo_hash=$(awk -v asset=\"$edo_asset\" '$2==asset {hash=$1;count++} END {if(count!=1)exit 1;print hash}' \"$stage/sha256sum.txt\")\nprintf '%s\\n' \"$edo_hash\" | awk 'length($0)==64 && $0 !~ /[^a-fA-F0-9]/ {ok=1} END{exit !ok}'\n"
		script += curl + "--max-filesize 1073741824 \"$edo_release/$edo_asset\" -o \"$stage/k3s\"\nprintf '%s  k3s\\n' \"$edo_hash\" > \"$stage/k3s.sha256\"\n(cd \"$stage\" && sha256sum -c k3s.sha256 >/dev/null)\nchmod 700 \"$stage/k3s\"\n[ \"$(timeout 3 \"$stage/k3s\" --version | head -n 1 | awk '{print $3}')\" = " + shellQuote(version) + " ]\n"
	}
	return script + "edo_stage_ready=true\n", nil
}

func serverArtifactBase(installer map[string]any) (string, error) {
	base, _ := installer["INSTALL_K3S_ARTIFACT_URL"].(string)
	if base == "" {
		base = officialArtifactURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(base, "\r\n\x00 ") {
		return "", &ValidationError{Message: "Master 制品源必须是无凭据和查询参数的 HTTPS 地址"}
	}
	return strings.TrimRight(base, "/"), nil
}

func serverInstallerCleanupScript(installationID string) (string, error) {
	stage, err := serverInstallerStage(installationID)
	if err != nil {
		return "", err
	}
	return "set -eu\n[ \"$(id -u)\" = 0 ]\nstage=" + shellQuote(stage) + "\nif [ -d \"$stage\" ] && [ ! -L \"$stage\" ]; then rm -f \"$stage/install.sh\" \"$stage/k3s\" \"$stage/sha256sum.txt\" \"$stage/k3s.sha256\"; rmdir \"$stage\"; fi\n", nil
}

// Publish a fully copied executable with an atomic, no-clobber hard link.
// An installer outside EDO's lock may have created the destination meanwhile.
func serverInstallBinaryScript(installer map[string]any) string {
	target := binaryPath(installer)
	directory := path.Dir(target)
	return "install -d -m 755 " + shellQuote(directory) + "\nedo_binary_stage=$(mktemp " + shellQuote(directory+"/.edo-k3s-bin.XXXXXX") + ")\ninstall -m 755 \"$work/k3s\" \"$edo_binary_stage\"\nif ! ln -T \"$edo_binary_stage\" " + shellQuote(target) + "; then printf 'EDO_K3S_EXISTS\\n'; exit 42; fi\nrm -f \"$edo_binary_stage\"\nedo_binary_stage=''\n"
}

func serverJoinMessage(err error) string {
	switch {
	case errors.Is(err, kube.ErrServerJoinCredentials):
		return "无法验证 Server Token 或读取关键配置；请使用完整安全 Server Token，独立 Agent Token 配置需先由管理员核对"
	case errors.Is(err, kube.ErrServerJoinConfig):
		return "无法确认目标 Master 的完整关键配置，请核对版本与集群配置"
	case errors.Is(err, kube.ErrServerJoinUnhealthy):
		return "目标控制平面或 etcd 成员未全部就绪，或已有成员正在加入，请恢复健康后重试"
	case errors.Is(err, ErrTargetVersionChanged):
		return ErrTargetVersionChanged.Error()
	default:
		return "无法验证目标为可扩容的 embedded-etcd 集群；SQLite、外置或未知数据存储不会自动转换，请先由管理员核对"
	}
}

func serverJoinPreflightError(hosts []Host, err error) error {
	var detail *ValidationError
	if errors.As(err, &detail) {
		return err
	}
	for _, safe := range []error{kube.ErrRBACForbidden, kube.ErrRBACSyncPending, kube.ErrClusterNotFound} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	report := &PreflightReport{CheckedAt: time.Now().UTC(), Checks: []PreflightCheck{check("server_ha", "Master 高可用与关键配置", "failed", serverJoinMessage(err))}, Hosts: []HostPreflight{}}
	for _, host := range hosts {
		report.Hosts = append(report.Hosts, HostPreflight{HostID: host.ID, HostName: host.Name, Role: host.Role, Checks: []PreflightCheck{check("server_ha", "Master 加入资格", "failed", "目标集群检查未通过，尚未连接该主机执行安装")}})
	}
	return &PreflightError{Report: report}
}

func (s *Service) checkReviewedServer(ctx context.Context, snapshot savedInput) (*kube.ServerJoinInfo, error) {
	if !isServerJoin(snapshot.Input) {
		return nil, nil
	}
	if snapshot.ServerCriticalDigest == "" || snapshot.ServerMembershipDigest == "" || len(snapshot.ServerMembers) == 0 {
		return nil, ErrPreflightRequired
	}
	info, err := s.kubernetes.InspectServerJoin(ctx, snapshot.Input.ClusterID, snapshot.Input.Endpoint, snapshot.Input.ServerToken)
	if err != nil {
		return nil, serverJoinPreflightError(nil, err)
	}
	if info == nil || info.CriticalDigest != snapshot.ServerCriticalDigest || info.MembershipDigest != snapshot.ServerMembershipDigest || info.Version != snapshot.TargetVersion {
		return nil, ErrHostChanged
	}
	return info, nil
}

// Only TCP connectivity is tested here; service identity is independently checked
// through the strictly authenticated K3s supervisor. No peer certificate is read.
func serverPeerProbeScript(info *kube.ServerJoinInfo) (string, error) {
	if info == nil || info.Datastore != "embedded-etcd" || len(info.Members) == 0 {
		return "", ErrPreflightRequired
	}
	endpoints := []string{}
	for _, member := range info.Members {
		for _, raw := range append(append([]string(nil), member.PeerURLs...), member.ClientURLs...) {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || (u.Port() != "2379" && u.Port() != "2380") || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
				return "", ErrPreflightRequired
			}
			endpoints = append(endpoints, net.JoinHostPort(u.Hostname(), u.Port()))
		}
	}
	slices.Sort(endpoints)
	endpoints = slices.Compact(endpoints)
	if len(endpoints) == 0 || len(endpoints) > 40 {
		return "", ErrPreflightRequired
	}
	var script strings.Builder
	script.WriteString("set -eu\ncommand -v bash >/dev/null\ncommand -v timeout >/dev/null\n")
	for _, endpoint := range endpoints {
		host, port, err := net.SplitHostPort(endpoint)
		if err != nil {
			return "", ErrPreflightRequired
		}
		script.WriteString("timeout 3 bash --noprofile --norc -c 'exec 3<>/dev/tcp/$1/$2' _ " + shellQuote(host) + " " + shellQuote(port) + " >/dev/null 2>&1\n")
	}
	script.WriteString("printf 'EDO_K3S_ETCD_PEERS_OK\\n'\n")
	return script.String(), nil
}

func expectedServerMember(member kube.ServerJoinMember, nodeName string) bool {
	if member.Name == nodeName {
		return true
	}
	suffix, ok := strings.CutPrefix(member.Name, nodeName+"-")
	if !ok || len(suffix) != 8 {
		return false
	}
	_, err := strconv.ParseUint(suffix, 16, 32)
	return err == nil
}

func allowedJoinedMembership(before []kube.ServerJoinMember, after []kube.ServerJoinMember, nodeName string) bool {
	if len(after) != len(before)+1 {
		return false
	}
	seen := map[string]kube.ServerJoinMember{}
	for _, member := range after {
		if member.Learner || member.ID == "" {
			return false
		}
		if _, ok := seen[member.ID]; ok {
			return false
		}
		seen[member.ID] = member
	}
	for _, member := range before {
		current, ok := seen[member.ID]
		if !ok || !sameOption(member, current) {
			return false
		}
		delete(seen, member.ID)
	}
	for _, member := range seen {
		return expectedServerMember(member, nodeName)
	}
	return false
}

func (s *Service) waitForJoinedServer(ctx context.Context, record *model.K3sInstallation, snapshot savedInput, host Host) error {
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.recheck(waitCtx, *record, snapshot); err != nil {
			return err
		}
		if err := s.kubernetes.RequireClusterPermission(waitCtx, record.ClusterID, "", "kubernetes.node.create"); err != nil {
			return err
		}
		client, err := s.kubernetes.Clientset(kube.WithInternalClusterAccess(waitCtx, "node-bootstrap"), record.ClusterID)
		if err != nil {
			return err
		}
		list, err := client.CoreV1().Nodes().List(waitCtx, metav1.ListOptions{LabelSelector: "edo.dev/installation=" + record.ID + ",edo.dev/host=" + host.ID})
		if err == nil {
			for _, node := range list.Items {
				_, control := node.Labels["node-role.kubernetes.io/control-plane"]
				_, master := node.Labels["node-role.kubernetes.io/master"]
				_, etcd := node.Labels["node-role.kubernetes.io/etcd"]
				ready := false
				for _, condition := range node.Status.Conditions {
					ready = ready || condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
				}
				if !ready || (!control && !master) || !etcd {
					continue
				}
				info, inspectErr := s.kubernetes.InspectServerJoin(waitCtx, record.ClusterID, snapshot.Input.Endpoint, snapshot.Input.ServerToken)
				if inspectErr == nil && info != nil {
					if info.Version != snapshot.TargetVersion || info.CriticalDigest != snapshot.ServerCriticalDigest {
						return ErrHostChanged
					}
					if !allowedJoinedMembership(snapshot.ServerMembers, info.Members, node.Name) {
						return ErrHostChanged
					}
					return nil
				}
				if inspectErr != nil && !errors.Is(inspectErr, kube.ErrServerJoinUnhealthy) {
					return serverJoinPreflightError(nil, inspectErr)
				}
			}
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("%w: %v", ErrReadiness, waitCtx.Err())
		case <-ticker.C:
		}
	}
}
