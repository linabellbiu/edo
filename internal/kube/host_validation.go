package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"edo/internal/database"
	"edo/internal/hostcredential"
	"edo/internal/hostidentity"
	"edo/internal/model"
	"edo/internal/sshclient"
)

var (
	ErrClusterHostMismatch         = errors.New("所选主机与集群节点信息不一致，无法关联")
	ErrClusterHostCheckUnavailable = errors.New("无法核对主机与集群节点信息，请检查集群节点读取权限和主机连接后重试")
)

type nodeIdentity struct {
	addresses  []netip.Addr
	machineID  string
	systemUUID string
	bootID     string
}

type hostIdentityReader func(context.Context, model.Host) (hostidentity.Identity, error)

// ValidateHost 使用已保存的集群连接核对主机身份。临时主机的 SSH 密文需已绑定其 ID。
// 不写入关系，也不把 API Server 地址当成节点成员证据。
func (s *Service) ValidateHost(ctx context.Context, clusterID string, host model.Host) error {
	_, err := s.ValidateHostForSave(ctx, clusterID, host)
	return err
}

// ValidateHostForSave 返回可在主机保存事务中执行的连接快照复查，不持有网络请求中的数据库锁。
func (s *Service) ValidateHostForSave(ctx context.Context, clusterID string, host model.Host) (func(*gorm.DB) error, error) {
	cluster, err := s.Find(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if host.IsBuiltin || host.Mode != model.HostModeSSH || host.ProjectID != "" && host.ProjectID != cluster.ProjectID || host.DepartmentID != "" && host.DepartmentID != cluster.DepartmentID {
		return nil, ErrInvalidClusterHosts
	}
	if err := s.validateHosts(ctx, cluster, []model.Host{host}, nil); err != nil {
		return nil, err
	}
	snapshot := *cluster
	return func(tx *gorm.DB) error {
		var current model.KubernetesCluster
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "uuid = ?", snapshot.ID).Error; err != nil {
			return ErrClusterHostCheckUnavailable
		}
		if !sameClusterConnection(snapshot, current) {
			return ErrClusterHostCheckUnavailable
		}
		return nil
	}, nil
}

func clusterScope(ctx context.Context) (string, string) {
	departmentID := database.DefaultDepartmentID
	projectID := ""
	if scope, ok := database.DepartmentScopeFromContext(ctx); ok {
		if scope.DepartmentID != "" {
			departmentID = scope.DepartmentID
		}
		projectID = scope.ProjectID
	}
	if projectID == "" {
		projectID = database.DefaultProjectID(departmentID)
	}
	return departmentID, projectID
}

func normalizedHostIDs(requestedIDs []string) ([]string, error) {
	if len(requestedIDs) > 500 {
		return nil, ErrInvalidClusterHosts
	}
	ids := make([]string, 0, len(requestedIDs))
	for _, value := range requestedIDs {
		id := strings.TrimSpace(value)
		if id == "" || len(id) > 36 {
			return nil, ErrInvalidClusterHosts
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func (s *Service) validateHostSelection(ctx context.Context, cluster *model.KubernetesCluster, requestedIDs []string, connection *rest.Config) ([]model.Host, error) {
	ids, err := normalizedHostIDs(requestedIDs)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []model.Host{}, nil
	}
	var hosts []model.Host
	if err := s.db.WithContext(ctx).Where("uuid IN ?", ids).Order("uuid ASC").Find(&hosts).Error; err != nil {
		return nil, ErrClusterHostCheckUnavailable
	}
	if len(hosts) != len(ids) {
		return nil, ErrInvalidClusterHosts
	}
	for _, host := range hosts {
		if host.IsBuiltin || host.Mode != model.HostModeSSH || host.ProjectID != cluster.ProjectID || host.DepartmentID != cluster.DepartmentID {
			return nil, ErrInvalidClusterHosts
		}
	}
	if err := s.validateHosts(ctx, cluster, hosts, connection); err != nil {
		return nil, err
	}
	return hosts, nil
}

func (s *Service) validateHosts(ctx context.Context, cluster *model.KubernetesCluster, hosts []model.Host, connection *rest.Config) error {
	if len(hosts) == 0 {
		return nil
	}
	if err := s.requireHostValidationAuthorization(ctx, cluster); err != nil {
		return err
	}
	ctx, cancel := s.hostValidationContext(ctx)
	defer cancel()
	if connection == nil {
		var err error
		connection, err = s.hostValidationRESTConfig(cluster)
		if err != nil {
			return ErrClusterHostCheckUnavailable
		}
	}
	client, err := kubernetes.NewForConfig(connection)
	if err != nil {
		return ErrClusterHostCheckUnavailable
	}
	nodes := make([]nodeIdentity, 0)
	continuation := ""
	seen := map[string]bool{}
	for {
		page, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil {
			return ErrClusterHostCheckUnavailable
		}
		for _, node := range page.Items {
			identity := nodeIdentity{machineID: normalizedIdentityID(node.Status.NodeInfo.MachineID), systemUUID: normalizedIdentityID(node.Status.NodeInfo.SystemUUID), bootID: normalizedIdentityID(node.Status.NodeInfo.BootID)}
			for _, address := range node.Status.Addresses {
				if address.Type == corev1.NodeInternalIP || address.Type == corev1.NodeExternalIP {
					if ip, ok := identityIP(address.Address); ok {
						identity.addresses = append(identity.addresses, ip)
					}
				}
			}
			nodes = append(nodes, identity)
		}
		continuation = page.Continue
		if continuation == "" {
			break
		}
		if seen[continuation] {
			return ErrClusterHostCheckUnavailable
		}
		seen[continuation] = true
	}
	if len(nodes) == 0 {
		return ErrClusterHostMismatch
	}
	for _, host := range hosts {
		identity, err := s.readHostIdentity(ctx, host)
		if err != nil || ctx.Err() != nil {
			return ErrClusterHostCheckUnavailable
		}
		if !slices.ContainsFunc(nodes, func(node nodeIdentity) bool { return nodeMatchesHost(node, identity) }) {
			return ErrClusterHostMismatch
		}
	}
	if ctx.Err() != nil {
		return ErrClusterHostCheckUnavailable
	}
	return nil
}

func (s *Service) hostValidationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.config.RequestTimeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

func (s *Service) hostValidationRESTConfig(cluster *model.KubernetesCluster) (*rest.Config, error) {
	if cluster.Mode != model.KubernetesKubeconfig || s.secrets == nil {
		return nil, ErrClusterHostCheckUnavailable
	}
	raw, err := s.secrets.Decrypt(cluster.KubeconfigCiphertext, kubeconfigAAD(cluster.ID))
	if err != nil {
		return nil, err
	}
	connection, err := safeRESTConfig([]byte(raw))
	if err != nil {
		return nil, err
	}
	s.configureRESTConfig(connection)
	return connection, nil
}

func (s *Service) readHostIdentity(ctx context.Context, host model.Host) (hostidentity.Identity, error) {
	if s.hostIdentityProbe != nil {
		return s.hostIdentityProbe(ctx, host)
	}
	if s.secrets == nil {
		return hostidentity.Identity{}, ErrClusterHostCheckUnavailable
	}
	raw, err := s.secrets.Decrypt(host.SSHCredentialCiphertext, hostcredential.AAD(host.ID))
	if err != nil {
		return hostidentity.Identity{}, ErrClusterHostCheckUnavailable
	}
	var bundle sshclient.Bundle
	if json.Unmarshal([]byte(raw), &bundle) != nil {
		return hostidentity.Identity{}, ErrClusterHostCheckUnavailable
	}
	hostURL := (&url.URL{Scheme: "ssh", User: url.User(host.SSHUsername), Host: net.JoinHostPort(host.Address, strconv.Itoa(host.SSHPort))}).String()
	return hostidentity.Probe(ctx, hostURL, bundle, host.SSHHostKeyFingerprint, s.config.ConnectTimeout)
}

func identityIP(value string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	return ip, !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast()
}

func normalizedIdentityID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", "", "{", "", "}", "").Replace(value)
	if len(value) != 32 {
		return ""
	}
	valid, allF := false, true
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return ""
		}
		valid = valid || character != '0'
		allF = allF && character == 'f'
	}
	if !valid || allF {
		return ""
	}
	return value
}

func nodeMatchesHost(node nodeIdentity, host hostidentity.Identity) bool {
	addresses := make([]string, 0, len(node.addresses))
	for _, address := range node.addresses {
		addresses = append(addresses, address.String())
	}
	return hostidentity.Matches(hostidentity.Identity{
		Addresses: addresses, MachineID: node.machineID, SystemUUID: node.systemUUID, BootID: node.bootID,
	}, host)
}

func sameHostConnection(before, after model.Host) bool {
	return before.ID == after.ID && before.Mode == after.Mode && before.Address == after.Address && before.SSHPort == after.SSHPort && before.SSHUsername == after.SSHUsername && before.SSHAuthType == after.SSHAuthType && before.SSHCredentialCiphertext == after.SSHCredentialCiphertext && before.SSHHostKeyFingerprint == after.SSHHostKeyFingerprint && before.IsBuiltin == after.IsBuiltin && before.IsActive == after.IsActive && before.DepartmentID == after.DepartmentID && before.ProjectID == after.ProjectID && before.UpdatedAt.Equal(after.UpdatedAt)
}

func sameClusterConnection(before, after model.KubernetesCluster) bool {
	return before.ID == after.ID && before.Mode == after.Mode && before.APIServer == after.APIServer && before.KubeconfigCiphertext == after.KubeconfigCiphertext && before.IsActive == after.IsActive && before.DepartmentID == after.DepartmentID && before.ProjectID == after.ProjectID && before.UpdatedAt.Equal(after.UpdatedAt)
}

func verifyHostSnapshots(tx *gorm.DB, verified []model.Host) error {
	for _, before := range verified {
		var current model.Host
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "uuid = ?", before.ID).Error; err != nil {
			return ErrClusterHostCheckUnavailable
		}
		if !sameHostConnection(before, current) {
			return ErrClusterHostCheckUnavailable
		}
	}
	return nil
}

func clusterLinkedHostIDs(tx *gorm.DB, id string) ([]string, error) {
	var ids []string
	if err := tx.Model(&model.HostCapability{}).Where("kind = ? AND runtime_id = ?", model.HostCapabilityKubernetes, id).Order("host_id ASC").Pluck("host_id", &ids).Error; err != nil {
		return nil, ErrClusterHostCheckUnavailable
	}
	return ids, nil
}
