package kube

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	certutil "k8s.io/client-go/util/cert"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
)

var ErrNodeRemovalUnsafe = errors.New("节点不能移除：请先停调度、迁走普通 Pod 并停止节点 Agent，等待节点离线两分钟；控制平面和最后一个可用节点不能在这里移除")
var ErrJoinUnsupported = errors.New("无法生成加入命令，请检查集群类型、CA 证书和 Bootstrap Token 权限；托管集群请通过云平台扩容")

type NodeJoinInput struct {
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	Minutes  int    `json:"minutes"`
}
type NodeJoin struct {
	ID string `json:"id"`
	ResourceIdentity
	Command   string    `json:"command"`
	ExpiresAt time.Time `json:"expires_at"`
}

// 仅创建短期 Bootstrap Token，不读取集群原有的 server/agent 令牌。
func (s *Service) CreateNodeJoin(ctx context.Context, clusterID string, input NodeJoinInput) (*NodeJoin, error) {
	if (input.Provider != "k3s" && input.Provider != "kubeadm") || input.Minutes < 5 || input.Minutes > 60 {
		return nil, ErrInvalidResource
	}
	cfg, err := s.RESTConfig(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	endpoint := input.Endpoint
	if endpoint == "" {
		endpoint = cfg.Host
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(endpoint, "'\"\n\r\t ") {
		return nil, ErrInvalidResource
	}
	if net.ParseIP(u.Hostname()) == nil && len(validation.IsDNS1123Subdomain(u.Hostname())) != 0 {
		return nil, ErrInvalidResource
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, ErrInvalidResource
		}
	}
	certs, err := certutil.ParseCertsPEM(cfg.CAData)
	if err != nil || len(certs) == 0 {
		return nil, ErrJoinUnsupported
	}
	root := certs[len(certs)-1]
	if !root.IsCA || root.CheckSignatureFrom(root) != nil {
		return nil, ErrJoinUnsupported
	}
	var caHash string
	if input.Provider == "k3s" {
		data := cfg.CAData
		if len(certs) > 1 {
			data = root.Raw
		}
		sum := sha256.Sum256(data)
		caHash = hex.EncodeToString(sum[:])
	} else {
		key, keyErr := x509.MarshalPKIXPublicKey(root.PublicKey)
		if keyErr != nil {
			return nil, ErrJoinUnsupported
		}
		sum := sha256.Sum256(key)
		caHash = hex.EncodeToString(sum[:])
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	version, err := client.Discovery().ServerVersion()
	if err != nil {
		return nil, resourceError("nodes", err)
	}
	if (input.Provider == "k3s") != strings.Contains(version.GitVersion, "k3s") {
		return nil, ErrJoinUnsupported
	}
	if input.Provider == "kubeadm" {
		if _, err := client.CoreV1().ConfigMaps("kube-public").Get(ctx, "cluster-info", metav1.GetOptions{}); err != nil {
			return nil, ErrJoinUnsupported
		}
	}
	token, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return nil, fmt.Errorf("生成节点加入凭据失败: %w", err)
	}
	id, secretPart, _ := strings.Cut(token, ".")
	group := "system:bootstrappers:kubeadm:default-node-token"
	if input.Provider == "k3s" {
		group = "system:bootstrappers:k3s:default-node-token"
	}
	expires := time.Now().UTC().Add(time.Duration(input.Minutes) * time.Minute)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: bootstraputil.BootstrapTokenSecretName(id), Namespace: "kube-system", Labels: map[string]string{"edo.dev/node-join": clusterID}}, Type: corev1.SecretTypeBootstrapToken, Data: map[string][]byte{
		bootstrapapi.BootstrapTokenIDKey: []byte(id), bootstrapapi.BootstrapTokenSecretKey: []byte(secretPart),
		bootstrapapi.BootstrapTokenExpirationKey: []byte(expires.Format(time.RFC3339)), bootstrapapi.BootstrapTokenDescriptionKey: []byte("EDO worker node join"),
		bootstrapapi.BootstrapTokenUsageAuthentication: []byte("true"), bootstrapapi.BootstrapTokenUsageSigningKey: []byte("true"), bootstrapapi.BootstrapTokenExtraGroupsKey: []byte(group),
	}}
	created, err := client.CoreV1().Secrets("kube-system").Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return nil, resourceError("secrets", err)
	}
	command := fmt.Sprintf("sudo kubeadm join '%s' --token %s --discovery-token-ca-cert-hash sha256:%s", u.Host, token, caHash)
	if input.Provider == "k3s" {
		// 凭据从标准输入写入 root 专用文件，不放进进程命令行。
		command = fmt.Sprintf("sudo install -d -m 700 /etc/rancher/k3s\nprintf '%%s' '%s' | sudo sh -c 'umask 077; cat > /etc/rancher/k3s/edo-join-token'\nsudo k3s agent --server '%s' --token-file /etc/rancher/k3s/edo-join-token", "K10"+caHash+"::"+token, strings.TrimRight(endpoint, "/"))
	}
	return &NodeJoin{ID: id, ResourceIdentity: ResourceIdentity{UID: string(created.UID), ResourceVersion: created.ResourceVersion}, Command: command, ExpiresAt: expires}, nil
}

func (s *Service) RevokeNodeJoin(ctx context.Context, clusterID, id string, identity ResourceIdentity) error {
	if !bootstraputil.IsValidBootstrapTokenID(id) || !identity.valid() {
		return ErrInvalidResource
	}
	client, err := s.Clientset(ctx, clusterID)
	if err != nil {
		return err
	}
	name := bootstraputil.BootstrapTokenSecretName(id)
	secret, err := client.CoreV1().Secrets("kube-system").Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return resourceError("secrets", err)
	}
	if secret.Labels["edo.dev/node-join"] != clusterID || string(secret.UID) != identity.UID {
		return ErrResourceConflict
	}
	uid := types.UID(identity.UID)
	return resourceError("secrets", client.CoreV1().Secrets("kube-system").Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &identity.ResourceVersion}}))
}

type NodeRemoval struct {
	ResourceIdentity
	AgentStopped bool   `json:"agent_stopped"`
	Name         string `json:"name"`
}

func (s *Service) DeleteNode(ctx context.Context, clusterID, name string, input NodeRemoval) error {
	if !input.valid() || !input.AgentStopped || input.Name != name || len(validation.IsDNS1123Subdomain(name)) != 0 {
		return ErrInvalidResource
	}
	client, err := s.Clientset(ctx, clusterID)
	if err != nil {
		return err
	}
	node, err := client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return resourceError("nodes", err)
	}
	if string(node.UID) != input.UID || node.ResourceVersion != input.ResourceVersion {
		return ErrResourceConflict
	}
	if _, control := node.Labels["node-role.kubernetes.io/control-plane"]; control {
		return ErrNodeRemovalUnsafe
	}
	if _, master := node.Labels["node-role.kubernetes.io/master"]; master {
		return ErrNodeRemovalUnsafe
	}
	if !node.Spec.Unschedulable || nodeReady(node) {
		return ErrNodeRemovalUnsafe
	}
	lease, err := client.CoordinationV1().Leases("kube-node-lease").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return resourceError("nodes", err)
	}
	if lease.Spec.RenewTime == nil || time.Since(lease.Spec.RenewTime.Time) < 2*time.Minute {
		return ErrNodeRemovalUnsafe
	}
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return resourceError("nodes", err)
	}
	available := false
	for i := range nodes.Items {
		if nodes.Items[i].Name != name && nodeReady(&nodes.Items[i]) {
			available = true
			break
		}
	}
	if !available {
		return ErrNodeRemovalUnsafe
	}
	continuation := ""
	for page := 0; ; page++ {
		pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name, Limit: 500, Continue: continuation})
		if err != nil {
			return resourceError("pods", err)
		}
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}
			daemon := false
			for _, owner := range pod.OwnerReferences {
				if owner.Kind == "DaemonSet" && owner.Controller != nil && *owner.Controller {
					daemon = true
				}
			}
			if !daemon {
				return ErrNodeRemovalUnsafe
			}
		}
		continuation = pods.Continue
		if continuation == "" {
			break
		}
		if page >= 19 {
			return ErrNodeRemovalUnsafe
		}
	}
	uid := types.UID(input.UID)
	return resourceError("nodes", client.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &input.ResourceVersion}}))
}

func nodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
