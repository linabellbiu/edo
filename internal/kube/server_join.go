package kube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	certutil "k8s.io/client-go/util/cert"
)

var (
	ErrServerJoinUnsupported = errors.New("无法确认目标为支持完整控制平面加入的 K3s 内嵌 etcd 集群")
	ErrServerJoinCredentials = errors.New("Server Token 无效、与目标 CA 不匹配，或没有 Server 加入权限")
	ErrServerJoinConfig      = errors.New("无法完整核对目标集群关键配置，请检查独立 Agent Token 或集群配置")
	ErrServerJoinUnhealthy   = errors.New("目标控制平面或 etcd 成员未全部就绪，请恢复集群健康后重试")
)

const serverJoinResponseLimit = 1024 * 1024

type ServerJoinMember struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	PeerURLs   []string `json:"peer_urls"`
	ClientURLs []string `json:"client_urls"`
	Learner    bool     `json:"learner"`
}

type ServerJoinInfo struct {
	Datastore        string             `json:"datastore"`
	CriticalOptions  map[string]any     `json:"critical_options"`
	Members          []ServerJoinMember `json:"members"`
	CriticalDigest   string             `json:"critical_digest"`
	MembershipDigest string             `json:"membership_digest"`
	CAData           []byte             `json:"-"`
	Version          string             `json:"version"`
	NodeNames        []string           `json:"node_names"`
}

var serverJoinVersion = regexp.MustCompile(`^v1\.[0-9]+\.[0-9]+\+k3s[1-9][0-9]*$`)
var serverJoinComponent = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// InspectServerJoin reads only public CA, health, membership and the explicitly
// selected configuration fields. It never requests bootstrap data or keys.
func (s *Service) InspectServerJoin(ctx context.Context, clusterID, endpoint, secureServerToken string) (*ServerJoinInfo, error) {
	if err := s.RequireClusterPermission(ctx, clusterID, "", "kubernetes.node.create"); err != nil {
		for _, safe := range []error{ErrRBACForbidden, ErrRBACSyncPending, ErrClusterNotFound} {
			if errors.Is(err, safe) {
				return nil, safe
			}
		}
		return nil, ErrServerJoinUnsupported
	}
	ctx, cancel := context.WithTimeout(WithInternalClusterAccess(ctx, "node-bootstrap"), 45*time.Second)
	defer cancel()
	cfg, err := s.RESTConfig(ctx, clusterID)
	if err != nil || cfg == nil || cfg.Insecure || len(cfg.CAData) == 0 || cfg.CAFile != "" {
		return nil, ErrServerJoinUnsupported
	}
	if endpoint == "" {
		endpoint = cfg.Host
	}
	origin, err := serverJoinEndpoint(endpoint)
	if err != nil {
		return nil, ErrServerJoinUnsupported
	}
	password, tokenHash, ok := serverJoinToken(secureServerToken)
	if !ok {
		return nil, ErrServerJoinCredentials
	}
	// Management credentials are used only against the stored Kubernetes API.
	management, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, ErrServerJoinUnsupported
	}
	management.Timeout = 10 * time.Second
	management.CheckRedirect = serverJoinNoRedirect
	management.Transport = serverJoinBoundedTransport{management.Transport}
	client, err := kubernetes.NewForConfigAndClient(cfg, management)
	if err != nil {
		return nil, ErrServerJoinUnsupported
	}
	if _, err := client.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx); err != nil {
		return nil, ErrServerJoinUnhealthy
	}
	versionData, err := client.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(ctx)
	var liveVersion version.Info
	if err != nil || json.Unmarshal(versionData, &liveVersion) != nil || !serverJoinVersion.MatchString(liveVersion.GitVersion) {
		return nil, ErrServerJoinUnsupported
	}
	nodes := []corev1.Node{}
	continuation, seen := "", map[string]bool{}
	for {
		page, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil || len(nodes)+len(page.Items) > 10000 {
			return nil, ErrServerJoinUnhealthy
		}
		nodes = append(nodes, page.Items...)
		continuation = page.Continue
		if continuation == "" {
			break
		}
		if seen[continuation] || len(seen) >= 100 {
			return nil, ErrServerJoinUnhealthy
		}
		seen[continuation] = true
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cfg.CAData) {
		return nil, ErrServerJoinUnsupported
	}
	// Build from scratch: no management client certificate, bearer token,
	// impersonation, exec plugin, auth provider, proxy or credential wrapper.
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 * 1024}
	defer transport.CloseIdleConnections()
	supervisor := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: serverJoinNoRedirect}
	ca, status, err := serverJoinRead(ctx, supervisor, origin, "/cacerts", "", "")
	if err != nil || status != http.StatusOK {
		return nil, ErrServerJoinCredentials
	}
	actualHash, err := serverJoinCAHash(ca)
	if err != nil || subtle.ConstantTimeCompare([]byte(actualHash), []byte(tokenHash)) != 1 {
		return nil, ErrServerJoinCredentials
	}
	// /db/info trusts loopback connections. This separate strictly server-role
	// endpoint authenticates the supplied token even behind a loopback proxy.
	if _, status, err := serverJoinRead(ctx, supervisor, origin, "/v1-k3s/encrypt/status", "server", password); err != nil || status != http.StatusOK {
		return nil, ErrServerJoinCredentials
	}
	data, status, err := serverJoinRead(ctx, supervisor, origin, "/db/info", "server", password)
	if err != nil || status != http.StatusOK {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, ErrServerJoinCredentials
		}
		return nil, ErrServerJoinUnsupported
	}
	members, err := serverJoinMembers(data)
	if err != nil {
		return nil, ErrServerJoinUnhealthy
	}
	names, err := serverJoinHealthyNodes(nodes, members, liveVersion.GitVersion)
	if err != nil {
		return nil, err
	}
	data, status, err = serverJoinRead(ctx, supervisor, origin, "/v1-k3s/config", "node", password)
	if err != nil || status != http.StatusOK {
		return nil, ErrServerJoinConfig
	}
	critical, err := serverJoinCriticalOptions(data)
	if err != nil {
		if errors.Is(err, ErrServerJoinUnsupported) {
			return nil, ErrServerJoinUnsupported
		}
		return nil, ErrServerJoinConfig
	}
	return &ServerJoinInfo{Datastore: "embedded-etcd", CriticalOptions: critical, Members: members, CriticalDigest: serverJoinDigest(critical), MembershipDigest: serverJoinDigest(members), CAData: append([]byte(nil), ca...), Version: liveVersion.GitVersion, NodeNames: names}, nil
}

func serverJoinNoRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func serverJoinEndpoint(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.Path != "" && u.Path != "/" || strings.ContainsAny(value, "\r\n\t ") {
		return nil, ErrServerJoinUnsupported
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, ErrServerJoinUnsupported
		}
	}
	u.Path = ""
	return u, nil
}

func serverJoinToken(value string) (string, string, bool) {
	if len(value) > 4096 || len(value) < 77 || !strings.HasPrefix(value, "K10") || strings.ContainsAny(value, "\r\n\t ") {
		return "", "", false
	}
	hash, credentials, ok := strings.Cut(value[3:], "::")
	if !ok || len(hash) != 64 {
		return "", "", false
	}
	if digest, err := hex.DecodeString(hash); err != nil || len(digest) != sha256.Size {
		return "", "", false
	}
	user, password, ok := strings.Cut(credentials, ":")
	return password, strings.ToLower(hash), ok && user == "server" && password != ""
}

func serverJoinCAHash(data []byte) (string, error) {
	for rest := bytes.TrimSpace(data); len(rest) > 0; {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return "", ErrServerJoinCredentials
		}
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return "", ErrServerJoinCredentials
		}
		rest = bytes.TrimSpace(remaining)
	}
	certificates, err := certutil.ParseCertsPEM(data)
	if err != nil || len(certificates) == 0 || len(certificates) > 16 {
		return "", ErrServerJoinCredentials
	}
	for _, certificate := range certificates {
		if !certificate.IsCA {
			return "", ErrServerJoinCredentials
		}
	}
	if len(certificates) > 1 {
		roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
		for _, certificate := range certificates[1:] {
			if len(certificate.AuthorityKeyId) == 0 || bytes.Equal(certificate.AuthorityKeyId, certificate.SubjectKeyId) {
				roots.AddCert(certificate)
			} else {
				intermediates.AddCert(certificate)
			}
		}
		chains, err := certificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
		if err != nil || len(chains) == 0 {
			return "", ErrServerJoinCredentials
		}
		data = chains[0][len(chains[0])-1].Raw
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func serverJoinRead(ctx context.Context, client *http.Client, origin *url.URL, path, username, password string) ([]byte, int, error) {
	switch path {
	case "/cacerts", "/v1-k3s/encrypt/status", "/db/info", "/v1-k3s/config":
	default:
		return nil, 0, ErrServerJoinUnsupported
	}
	target := *origin
	target.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, 0, ErrServerJoinUnsupported
	}
	if username != "" {
		request.SetBasicAuth(username, password)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, ErrServerJoinUnsupported
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, serverJoinResponseLimit+1))
	if err != nil || len(data) > serverJoinResponseLimit {
		return nil, response.StatusCode, ErrServerJoinUnsupported
	}
	return data, response.StatusCode, nil
}

type serverJoinBoundedTransport struct{ transport http.RoundTripper }

func (t serverJoinBoundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.transport.RoundTrip(request)
	if err == nil {
		response.Body = &serverJoinBoundedBody{ReadCloser: response.Body, remaining: serverJoinResponseLimit}
	}
	return response, err
}

type serverJoinBoundedBody struct {
	io.ReadCloser
	remaining int
}

func (r *serverJoinBoundedBody) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		var extra [1]byte
		if n, err := r.ReadCloser.Read(extra[:]); n == 0 {
			return 0, err
		}
		return 0, ErrServerJoinUnsupported
	}
	if len(buffer) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.ReadCloser.Read(buffer)
	r.remaining -= n
	return n, err
}

func serverJoinMembers(data []byte) ([]ServerJoinMember, error) {
	var wire struct {
		Members []struct {
			ID         json.Number `json:"ID"`
			Name       string      `json:"name"`
			PeerURLs   []string    `json:"peerURLs"`
			ClientURLs []string    `json:"clientURLs"`
			Learner    bool        `json:"isLearner"`
		} `json:"members"`
	}
	if json.Unmarshal(data, &wire) != nil || len(wire.Members) == 0 || len(wire.Members) > 100 {
		return nil, ErrServerJoinUnhealthy
	}
	result, ids, names := []ServerJoinMember{}, map[string]bool{}, map[string]bool{}
	for _, member := range wire.Members {
		id, err := strconv.ParseUint(member.ID.String(), 10, 64)
		if err != nil || id == 0 || len(validation.IsDNS1123Subdomain(member.Name)) != 0 || member.Learner || len(member.PeerURLs) == 0 || len(member.PeerURLs) > 8 || len(member.ClientURLs) == 0 || len(member.ClientURLs) > 8 || ids[strconv.FormatUint(id, 10)] || names[member.Name] {
			return nil, ErrServerJoinUnhealthy
		}
		for _, value := range append(append([]string{}, member.PeerURLs...), member.ClientURLs...) {
			u, err := serverJoinEndpoint(value)
			if err != nil {
				return nil, ErrServerJoinUnhealthy
			}
			ip := net.ParseIP(u.Hostname())
			if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				return nil, ErrServerJoinUnhealthy
			}
		}
		ids[strconv.FormatUint(id, 10)], names[member.Name] = true, true
		slices.Sort(member.PeerURLs)
		slices.Sort(member.ClientURLs)
		result = append(result, ServerJoinMember{ID: strconv.FormatUint(id, 10), Name: member.Name, PeerURLs: member.PeerURLs, ClientURLs: member.ClientURLs})
	}
	slices.SortFunc(result, func(a, b ServerJoinMember) int { return strings.Compare(a.ID, b.ID) })
	return result, nil
}

func serverJoinHealthyNodes(nodes []corev1.Node, members []ServerJoinMember, targetVersion string) ([]string, error) {
	matched, names := map[string]bool{}, []string{}
	for _, node := range nodes {
		_, cp := node.Labels["node-role.kubernetes.io/control-plane"]
		_, master := node.Labels["node-role.kubernetes.io/master"]
		_, etcd := node.Labels["node-role.kubernetes.io/etcd"]
		if !cp && !master && !etcd {
			continue
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			ready = ready || condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
		}
		if !(cp || master) || !etcd || !ready || node.DeletionTimestamp != nil || node.Status.NodeInfo.KubeletVersion != targetVersion {
			return nil, ErrServerJoinUnhealthy
		}
		memberName := node.Annotations["etcd.k3s.cattle.io/node-name"]
		address := net.ParseIP(node.Annotations["etcd.k3s.cattle.io/node-address"])
		memberIndex := slices.IndexFunc(members, func(member ServerJoinMember) bool { return member.Name == memberName })
		if memberIndex < 0 || address == nil || matched[memberName] {
			return nil, ErrServerJoinUnhealthy
		}
		member := members[memberIndex]
		for _, values := range [][]string{member.PeerURLs, member.ClientURLs} {
			if !slices.ContainsFunc(values, func(value string) bool {
				u, _ := url.Parse(value)
				return net.ParseIP(u.Hostname()).Equal(address)
			}) {
				return nil, ErrServerJoinUnhealthy
			}
		}
		matched[memberName] = true
		names = append(names, node.Name)
	}
	if len(names) == 0 || len(matched) != len(members) {
		return nil, ErrServerJoinUnhealthy
	}
	slices.Sort(names)
	return names, nil
}

func serverJoinCriticalOptions(data []byte) (map[string]any, error) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return nil, ErrServerJoinConfig
	}
	// K3s config.Control exposes these slices without json:"-" or omitempty.
	// Missing fields cannot prove that component defaults remain in force. The
	// scheduler field was renamed after v1.35, so accept either known spelling
	// only when every spelling present is explicitly null or an empty list.
	for _, fields := range [][]string{
		{"ExtraAPIArgs"}, {"ExtraControllerArgs"}, {"ExtraCloudControllerArgs"}, {"ExtraEtcdArgs"},
		{"ExtraSchedulerAPIArgs", "ExtraSchedulerArgs"},
	} {
		visible := false
		for _, field := range fields {
			value, exists := raw[field]
			if !exists {
				continue
			}
			visible = true
			var args []string
			if json.Unmarshal(value, &args) != nil || len(args) != 0 {
				return nil, ErrServerJoinUnsupported
			}
		}
		if !visible {
			return nil, ErrServerJoinUnsupported
		}
	}
	result := map[string]any{}
	for _, pair := range [][2]string{
		{"DisableCCM", "disable-cloud-controller"}, {"DisableHelmController", "disable-helm-controller"}, {"DisableNPC", "disable-network-policy"},
		{"EncryptSecrets", "secrets-encryption"}, {"EmbeddedRegistry", "embedded-registry"}, {"FlannelIPv6Masq", "flannel-ipv6-masq"}, {"FlannelExternalIP", "flannel-external-ip"}, {"SupervisorMetrics", "supervisor-metrics"}, {"DisableKubeProxy", "disable-kube-proxy"},
	} {
		var value *bool
		if json.Unmarshal(raw[pair[0]], &value) != nil || value == nil {
			return nil, ErrServerJoinConfig
		}
		result[pair[1]] = *value
	}
	for _, field := range []string{"DisableAgent", "DisableAPIServer", "DisableETCD"} {
		var value *bool
		if json.Unmarshal(raw[field], &value) != nil || value == nil || *value {
			return nil, ErrServerJoinUnsupported
		}
	}
	for _, pair := range [][2]string{{"ClusterDomain", "cluster-domain"}, {"EncryptProvider", "secrets-encryption-provider"}, {"FlannelBackend", "flannel-backend"}, {"EgressSelectorMode", "egress-selector-mode"}} {
		var value *string
		if json.Unmarshal(raw[pair[0]], &value) != nil || value == nil || len(*value) > 253 || strings.ContainsAny(*value, "\r\n\x00") || *value == "" {
			return nil, ErrServerJoinConfig
		}
		switch pair[0] {
		case "ClusterDomain":
			if len(validation.IsDNS1123Subdomain(*value)) != 0 {
				return nil, ErrServerJoinConfig
			}
		case "EncryptProvider":
			if !slices.Contains([]string{"aescbc", "secretbox"}, *value) {
				return nil, ErrServerJoinConfig
			}
		case "FlannelBackend":
			if !slices.Contains([]string{"none", "vxlan", "host-gw", "wireguard-native"}, *value) {
				return nil, ErrServerJoinConfig
			}
		case "EgressSelectorMode":
			if !slices.Contains([]string{"agent", "cluster", "pod", "disabled"}, *value) {
				return nil, ErrServerJoinConfig
			}
		}
		result[pair[1]] = *value
	}
	for _, pair := range [][3]string{{"ClusterIPRange", "ClusterIPRanges", "cluster-cidr"}, {"ServiceIPRange", "ServiceIPRanges", "service-cidr"}} {
		var single *net.IPNet
		var plural []*net.IPNet
		if json.Unmarshal(raw[pair[0]], &single) != nil || json.Unmarshal(raw[pair[1]], &plural) != nil || single == nil || len(plural) == 0 || len(plural) > 2 {
			return nil, ErrServerJoinConfig
		}
		cidrs := []string{}
		families := map[int]bool{}
		for _, network := range plural {
			if network == nil {
				return nil, ErrServerJoinConfig
			}
			ones, bits := network.Mask.Size()
			if network.IP == nil || bits != 32 && bits != 128 || ones < 0 || families[bits] {
				return nil, ErrServerJoinConfig
			}
			families[bits] = true
			cidrs = append(cidrs, network.String())
		}
		if single.String() != cidrs[0] {
			return nil, ErrServerJoinConfig
		}
		result[pair[2]] = cidrs
	}
	var dns net.IP
	var dnsList []net.IP
	if json.Unmarshal(raw["ClusterDNS"], &dns) != nil || json.Unmarshal(raw["ClusterDNSs"], &dnsList) != nil || dns == nil || len(dnsList) == 0 || len(dnsList) > 2 || !dns.Equal(dnsList[0]) {
		return nil, ErrServerJoinConfig
	}
	dnsStrings := []string{}
	for _, address := range dnsList {
		if address == nil || address.IsUnspecified() || address.IsLoopback() {
			return nil, ErrServerJoinConfig
		}
		dnsStrings = append(dnsStrings, address.String())
	}
	result["cluster-dns"] = dnsStrings
	var disabled map[string]bool
	if json.Unmarshal(raw["Disables"], &disabled) != nil || disabled == nil || len(disabled) > 100 {
		return nil, ErrServerJoinConfig
	}
	var serviceLB *bool
	if json.Unmarshal(raw["DisableServiceLB"], &serviceLB) != nil || serviceLB == nil || *serviceLB != disabled["servicelb"] {
		return nil, ErrServerJoinConfig
	}
	components := []string{}
	for component, disable := range disabled {
		if !serverJoinComponent.MatchString(component) {
			return nil, ErrServerJoinConfig
		}
		if disable {
			components = append(components, component)
		}
	}
	slices.Sort(components)
	result["disable"] = components
	return result, nil
}

func serverJoinDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
