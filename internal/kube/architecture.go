package kube

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"sort"
	"strings"
	"syscall"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"edo/internal/model"
)

var (
	ErrNodeArchitecturesUnavailable = errors.New("集群节点架构暂时无法读取，请检查集群状态后重试")
	ErrNodeArchitectureConnection   = errors.New("无法连接部署集群，请在集群设置中检查连接地址和网络可达性")
	ErrNodeArchitectureTimeout      = errors.New("读取集群节点架构超时，请检查集群连接后重试")
	ErrNodeArchitectureCredentials  = errors.New("部署集群拒绝了连接凭据，请在集群设置中更新凭据")
	ErrNodeArchitecturePermission   = errors.New("集群凭据没有读取节点的权限，请授予节点读取权限后重试")
	ErrNodeArchitectureTLS          = errors.New("集群证书校验失败，请在集群设置中检查证书和连接配置")
	ErrNodeArchitectureCanceled     = errors.New("读取集群节点架构已取消，请重新执行")
	ErrNodeArchitecturesMissing     = errors.New("集群没有完整的节点架构信息，请先检查节点状态")
	ErrNodeArchitectureUnsupported  = errors.New("集群包含不支持的节点平台，目前仅支持 Linux AMD64 和 ARM64")
)

// NodeArchitectures 使用真实节点状态推导本次发布需要支持的平台，不借用构建机或 SSH 主机架构。
func (s *Service) NodeArchitectures(ctx context.Context, clusterID, projectID string) ([]model.HostArchitecture, error) {
	logger := slog.Default()
	cluster, err := s.Find(ctx, clusterID)
	if err != nil {
		logger.WarnContext(ctx, "读取部署集群架构失败", "operation", "kubernetes_node_architectures_cluster", "cluster_id", clusterID, "project_id", projectID, "err", err)
		if errors.Is(err, ErrClusterNotFound) {
			return nil, ErrClusterNotFound
		}
		return nil, ErrNodeArchitecturesUnavailable
	}
	if projectID == "" || cluster.ProjectID != projectID || !cluster.IsActive {
		logger.WarnContext(ctx, "部署集群不在当前项目范围内或已停用", "operation", "kubernetes_node_architectures_scope", "cluster_id", clusterID, "project_id", projectID, "err", ErrClusterNotFound)
		return nil, ErrClusterNotFound
	}
	ctx, query, err := s.authorizeResourceOperation(ctx, clusterID, "nodes", "", "read", false)
	if err != nil {
		logger.ErrorContext(ctx, "初始化部署集群架构客户端失败", "operation", "kubernetes_node_architectures_client", "cluster_id", clusterID, "project_id", projectID, "err", err)
		return nil, nodeArchitectureReadError(err)
	}
	client := query.typed
	requestContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	architectures := make(map[model.HostArchitecture]struct{}, 2)
	seenPages := map[string]struct{}{}
	continuation := ""
	for {
		nodes, listErr := client.CoreV1().Nodes().List(requestContext, metav1.ListOptions{Limit: 500, Continue: continuation})
		if listErr != nil {
			logger.WarnContext(ctx, "查询部署集群节点架构失败", "operation", "kubernetes_node_architectures_list", "cluster_id", clusterID, "project_id", projectID, "err", listErr)
			return nil, nodeArchitectureReadError(listErr)
		}
		for i := range nodes.Items {
			node := &nodes.Items[i]
			info := node.Status.NodeInfo
			if strings.TrimSpace(info.Architecture) == "" || strings.TrimSpace(info.OperatingSystem) == "" {
				logger.WarnContext(ctx, "部署集群节点缺少平台信息", "operation", "kubernetes_node_architectures_missing", "cluster_id", clusterID, "node_name", node.Name, "err", ErrNodeArchitecturesMissing)
				return nil, ErrNodeArchitecturesMissing
			}
			architecture, valid := model.NormalizeHostArchitecture(info.Architecture)
			if !valid || strings.ToLower(strings.TrimSpace(info.OperatingSystem)) != "linux" {
				logger.WarnContext(ctx, "部署集群节点平台不受支持", "operation", "kubernetes_node_architectures_unsupported", "cluster_id", clusterID, "node_name", node.Name, "architecture", info.Architecture, "operating_system", info.OperatingSystem, "err", ErrNodeArchitectureUnsupported)
				return nil, ErrNodeArchitectureUnsupported
			}
			architectures[architecture] = struct{}{}
		}
		continuation = nodes.Continue
		if continuation == "" {
			break
		}
		if _, repeated := seenPages[continuation]; repeated {
			logger.WarnContext(ctx, "部署集群节点分页未能完成", "operation", "kubernetes_node_architectures_pagination", "cluster_id", clusterID, "err", ErrNodeArchitecturesUnavailable)
			return nil, ErrNodeArchitecturesUnavailable
		}
		seenPages[continuation] = struct{}{}
	}
	if len(architectures) == 0 {
		logger.WarnContext(ctx, "部署集群没有节点架构", "operation", "kubernetes_node_architectures_empty", "cluster_id", clusterID, "err", ErrNodeArchitecturesMissing)
		return nil, ErrNodeArchitecturesMissing
	}
	result := make([]model.HostArchitecture, 0, len(architectures))
	for architecture := range architectures {
		result = append(result, architecture)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

// 节点读取只依据错误类型区分现场故障，不把上游正文或连接信息交给调用方。
func nodeArchitectureReadError(err error) error {
	for _, stable := range []error{ErrClusterNotFound, ErrInvalidCluster, ErrRBACForbidden, ErrRBACSyncPending} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	switch {
	case errors.Is(err, ErrUnsafeKubeconfig), errors.Is(err, ErrKubeconfigRequired):
		return ErrInvalidCluster
	case apierrors.IsUnauthorized(err):
		return ErrNodeArchitectureCredentials
	case apierrors.IsForbidden(err), errors.Is(err, ErrResourceForbidden):
		return ErrNodeArchitecturePermission
	case errors.Is(err, context.Canceled):
		return ErrNodeArchitectureCanceled
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return ErrNodeArchitectureTimeout
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var systemRoots x509.SystemRootsError
	var verification *tls.CertificateVerificationError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalidCertificate) || errors.As(err, &systemRoots) || errors.As(err, &verification) {
		return ErrNodeArchitectureTLS
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return ErrNodeArchitectureTimeout
		}
		return ErrNodeArchitectureConnection
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return ErrNodeArchitectureConnection
	}
	return ErrNodeArchitecturesUnavailable
}
