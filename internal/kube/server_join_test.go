package kube

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"edo/internal/model"
)

type serverJoinTestReply struct {
	status         int
	body, redirect string
	wait           bool
}

type serverJoinTestFixture struct {
	service                  *Service
	server                   *httptest.Server
	id, token, agentPassword string
	ca                       []byte
	mu                       sync.Mutex
	config                   map[string]any
	members                  []map[string]any
	nodes                    []corev1.Node
	replies                  map[string]serverJoinTestReply
	requests                 []string
}

func serverJoinTestTLS(t *testing.T) ([]byte, []byte, []byte, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "join-test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "join-test-server"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: caTemplate.NotBefore, NotAfter: caTemplate.NotAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return ca, cert, key, pair
}

func serverJoinTestConfig() map[string]any {
	_, cluster, _ := net.ParseCIDR("10.42.0.0/16")
	_, service, _ := net.ParseCIDR("10.43.0.0/16")
	result := map[string]any{
		"ClusterDNS": net.ParseIP("10.43.0.10"), "ClusterDNSs": []net.IP{net.ParseIP("10.43.0.10")}, "ClusterIPRange": cluster, "ClusterIPRanges": []*net.IPNet{cluster}, "ServiceIPRange": service, "ServiceIPRanges": []*net.IPNet{service},
		"ClusterDomain": "cluster.local", "EncryptProvider": "aescbc", "FlannelBackend": "vxlan", "EgressSelectorMode": "agent", "Disables": map[string]bool{"servicelb": true, "traefik": true}, "DisableServiceLB": true,
		"IPSECPSK": "private-config-psk", "Datastore": map[string]string{"password": "private-datastore-secret"},
	}
	for _, field := range []string{"ExtraAPIArgs", "ExtraControllerArgs", "ExtraCloudControllerArgs", "ExtraEtcdArgs", "ExtraSchedulerAPIArgs"} {
		result[field] = []string(nil)
	}
	for _, field := range []string{"DisableCCM", "DisableHelmController", "DisableNPC", "EncryptSecrets", "EmbeddedRegistry", "FlannelIPv6Masq", "FlannelExternalIP", "SupervisorMetrics", "DisableKubeProxy", "DisableAgent", "DisableAPIServer", "DisableETCD"} {
		result[field] = false
	}
	return result
}

func newServerJoinTestFixture(t *testing.T) *serverJoinTestFixture {
	t.Helper()
	ca, cert, key, pair := serverJoinTestTLS(t)
	f := &serverJoinTestFixture{service: newKubeTestService(t, 2*time.Second), id: uuid.NewString(), ca: ca, agentPassword: "private-server-password", config: serverJoinTestConfig(), replies: map[string]serverJoinTestReply{}}
	f.members = []map[string]any{{"ID": json.Number("18446744073709551615"), "name": "master-one-aabbccdd", "peerURLs": []string{"https://10.0.0.10:2380"}, "clientURLs": []string{"https://10.0.0.10:2379"}, "isLearner": false}}
	f.nodes = []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "master-one", Labels: map[string]string{"node-role.kubernetes.io/control-plane": "true", "node-role.kubernetes.io/etcd": "true"}, Annotations: map[string]string{"etcd.k3s.cattle.io/node-name": "master-one-aabbccdd", "etcd.k3s.cattle.io/node-address": "10.0.0.10"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.35.5+k3s1"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.URL.Path)
		accessReview := r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews"
		if r.Method != http.MethodGet && !(accessReview && r.Method == http.MethodPost) {
			t.Error("inspection sent a mutating request")
		}
		management := accessReview || r.URL.Path == "/readyz" || r.URL.Path == "/version" || r.URL.Path == "/api/v1/nodes"
		if management {
			if r.Header.Get("Authorization") != "Bearer test-token" || len(r.TLS.PeerCertificates) == 0 {
				t.Error("management requests did not use the stored management identity")
			}
		} else {
			if len(r.TLS.PeerCertificates) != 0 || strings.Contains(r.Header.Get("Authorization"), "test-token") || r.Header.Get("Impersonate-User") != "" {
				t.Error("management identity leaked to supervisor")
			}
			if r.URL.Path == "/cacerts" {
				if r.Header.Get("Authorization") != "" {
					t.Error("CA request contained credentials")
				}
			} else {
				user, password, ok := r.BasicAuth()
				wantUser, wantPassword := "server", "private-server-password"
				if r.URL.Path == "/v1-k3s/config" {
					wantUser, wantPassword = "node", f.agentPassword
				}
				if !ok || user != wantUser || password != wantPassword {
					http.Error(w, "private-auth-error-body", http.StatusUnauthorized)
					return
				}
			}
		}
		if reply, ok := f.replies[r.URL.Path]; ok {
			if reply.wait {
				<-r.Context().Done()
				return
			}
			if reply.redirect != "" {
				w.Header().Set("Location", reply.redirect)
			}
			w.WriteHeader(reply.status)
			_, _ = w.Write([]byte(reply.body))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			data, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
			if err != nil {
				t.Error("could not read native authorization review")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			object, _, err := scheme.Codecs.UniversalDeserializer().Decode(data, nil, nil)
			review, ok := object.(*authorizationv1.SelfSubjectAccessReview)
			if err != nil || !ok {
				t.Error("native authorization review must use the official JSON/protobuf encoding")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.Header.Get("Impersonate-User") != "edo:server-join:test" {
				t.Error("native review did not use the authorized impersonated principal")
			}
			attributes := review.Spec.ResourceAttributes
			allowed := attributes != nil && attributes.Group == "" && attributes.Resource == "nodes" && attributes.Verb == "list" && attributes.Namespace == "" && attributes.Subresource == "" && attributes.Name == "" && r.Header.Get("Impersonate-User") == "edo:server-join:test"
			_ = json.NewEncoder(w).Encode(&authorizationv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"}, Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed, Denied: !allowed}})
		case "/readyz":
			_, _ = w.Write([]byte("ok"))
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"gitVersion": "v1.35.5+k3s1"})
		case "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(&corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, Items: f.nodes})
		case "/cacerts":
			_, _ = w.Write(f.ca)
		case "/v1-k3s/encrypt/status":
			_, _ = w.Write([]byte(`{"activekey":"key-name","unknown":"private-status-secret"}`))
		case "/db/info":
			_ = json.NewEncoder(w).Encode(map[string]any{"members": f.members})
		case "/v1-k3s/config":
			_ = json.NewEncoder(w).Encode(f.config)
		default:
			t.Errorf("inspection requested forbidden endpoint %s", r.URL.Path)
			http.Error(w, "private-forbidden-endpoint", http.StatusForbidden)
		}
	}))
	f.server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequestClientCert}
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	value := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\n    certificate-authority-data: %s\nusers:\n- name: admin\n  user:\n    token: test-token\n    client-certificate-data: %s\n    client-key-data: %s\ncontexts:\n- name: test\n  context:\n    cluster: test\n    user: admin\ncurrent-context: test\n", f.server.URL, base64.StdEncoding.EncodeToString(ca), base64.StdEncoding.EncodeToString(cert), base64.StdEncoding.EncodeToString(key))
	ciphertext, err := f.service.secrets.Encrypt(value, kubeconfigAAD(f.id))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.db.Create(&model.KubernetesCluster{ID: f.id, Name: "server-join-" + f.id, Mode: model.KubernetesKubeconfig, APIServer: f.server.URL, KubeconfigCiphertext: ciphertext, IsActive: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(ca)
	f.token = "K10" + hex.EncodeToString(hash[:]) + "::server:private-server-password"
	return f
}

func (f *serverJoinTestFixture) inspect(ctx context.Context) (*ServerJoinInfo, error) {
	return f.service.InspectServerJoin(ctx, f.id, f.server.URL, f.token)
}

func TestInspectServerJoinUsesStrictCASeparateCredentialsAndWhitelistedDigests(t *testing.T) {
	f := newServerJoinTestFixture(t)
	info, err := f.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Datastore != "embedded-etcd" || info.Version != "v1.35.5+k3s1" || len(info.NodeNames) != 1 || len(info.Members) != 1 || info.Members[0].ID != "18446744073709551615" || info.CriticalDigest == "" || info.MembershipDigest == "" || !bytesEqual(info.CAData, f.ca) {
		t.Fatalf("incomplete inspection: %#v", info)
	}
	encoded, _ := json.Marshal(info)
	for _, private := range []string{"private-", "test-token", "BEGIN CERTIFICATE", "CAData", "ExtraAPIArgs", "IPSECPSK", "datastore-secret"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private field leaked: %s", private)
		}
	}
	if _, invalid := info.CriticalOptions["disable-service-lb"]; invalid {
		t.Fatal("internal field became a nonexistent K3s flag")
	}
	f.mu.Lock()
	f.config["IPSECPSK"] = "changed-private-psk"
	f.config["Datastore"] = map[string]string{"password": "changed-private-datastore-secret"}
	f.mu.Unlock()
	again, err := f.inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.CriticalDigest != info.CriticalDigest || again.MembershipDigest != info.MembershipDigest {
		t.Fatal("private response data contaminated the plan digest")
	}
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

func TestInspectServerJoinRejectsControlPlaneArgumentOverridesWithoutValues(t *testing.T) {
	for _, field := range []string{"ExtraAPIArgs", "ExtraControllerArgs", "ExtraCloudControllerArgs", "ExtraEtcdArgs", "ExtraSchedulerAPIArgs", "ExtraSchedulerArgs"} {
		t.Run(field, func(t *testing.T) {
			f := newServerJoinTestFixture(t)
			f.mu.Lock()
			f.config[field] = []string{"password=private-control-override"}
			f.mu.Unlock()
			info, err := f.inspect(context.Background())
			if info != nil || !errors.Is(err, ErrServerJoinUnsupported) || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), field) {
				t.Fatalf("unverified control-plane arguments were accepted or exposed: %v", err)
			}
		})
	}
}

func TestServerJoinCriticalOptionsRequiresVisibleEmptyControlPlaneArguments(t *testing.T) {
	for _, field := range []string{"ExtraAPIArgs", "ExtraControllerArgs", "ExtraCloudControllerArgs", "ExtraEtcdArgs", "ExtraSchedulerAPIArgs"} {
		for _, invalid := range []string{"missing", "scalar", "object", "non-string-list"} {
			t.Run(field+"/"+invalid, func(t *testing.T) {
				config := serverJoinTestConfig()
				switch invalid {
				case "missing":
					delete(config, field)
				case "scalar":
					config[field] = "private-control-override"
				case "object":
					config[field] = map[string]string{"password": "private-control-override"}
				case "non-string-list":
					config[field] = []any{false}
				}
				data, _ := json.Marshal(config)
				if _, err := serverJoinCriticalOptions(data); !errors.Is(err, ErrServerJoinUnsupported) {
					t.Fatalf("unknown component argument schema was accepted: %v", err)
				}
			})
		}
	}
	for _, scheduler := range []string{"ExtraSchedulerAPIArgs", "ExtraSchedulerArgs"} {
		for _, empty := range []string{"null", "empty-list"} {
			t.Run(scheduler+"/"+empty, func(t *testing.T) {
				config := serverJoinTestConfig()
				delete(config, "ExtraSchedulerAPIArgs")
				fields := []string{"ExtraAPIArgs", "ExtraControllerArgs", "ExtraCloudControllerArgs", "ExtraEtcdArgs", scheduler}
				for _, field := range fields {
					config[field] = nil
					if empty == "empty-list" {
						config[field] = []string{}
					}
				}
				data, _ := json.Marshal(config)
				options, err := serverJoinCriticalOptions(data)
				if err != nil {
					t.Fatalf("official empty component argument encoding was rejected: %v", err)
				}
				encoded, _ := json.Marshal(options)
				if strings.Contains(string(encoded), "Extra") {
					t.Fatal("raw component configuration entered the public plan")
				}
			})
		}
	}
}

func TestInspectServerJoinRejectsInvalidCredentialsAndCAWithoutResponseDetails(t *testing.T) {
	for _, scenario := range []string{"bootstrap", "agent", "wrong-password", "wrong-hash", "wrong-ca", "ca-private-key"} {
		t.Run(scenario, func(t *testing.T) {
			f := newServerJoinTestFixture(t)
			endpoint, token := f.server.URL, f.token
			switch scenario {
			case "bootstrap":
				token = "abcdef.0123456789abcdef"
			case "agent":
				token = strings.Replace(token, "::server:", "::node:", 1)
			case "wrong-password":
				token = strings.Replace(token, "private-server-password", "private-wrong-password", 1)
			case "wrong-hash":
				token = "K10" + strings.Repeat("0", 64) + "::server:private-server-password"
			case "wrong-ca":
				other := newServerJoinTestFixture(t)
				endpoint = other.server.URL
			case "ca-private-key":
				f.mu.Lock()
				f.replies["/cacerts"] = serverJoinTestReply{status: 200, body: string(f.ca) + "-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n"}
				f.mu.Unlock()
			}
			_, err := f.service.InspectServerJoin(context.Background(), f.id, endpoint, token)
			if !errors.Is(err, ErrServerJoinCredentials) || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe credential failure: %v", err)
			}
		})
	}
}

func TestInspectServerJoinRejectsRedirectsOversizedAndPrivateErrorBodies(t *testing.T) {
	for _, scenario := range []struct {
		name, path string
		status     int
		body       string
		want       error
	}{
		{"redirect", "/db/info", 302, "private-redirect-body", ErrServerJoinUnsupported},
		{"sqlite-or-external", "/db/info", 400, "private-datastore-disabled", ErrServerJoinUnsupported},
		{"membership-limit", "/db/info", 200, strings.Repeat("x", serverJoinResponseLimit+1), ErrServerJoinUnsupported},
		{"config-limit", "/v1-k3s/config", 200, strings.Repeat("x", serverJoinResponseLimit+1), ErrServerJoinConfig},
		{"config-error", "/v1-k3s/config", 500, "private-server-token=secret", ErrServerJoinConfig},
		{"server-role-error", "/v1-k3s/encrypt/status", 403, "private-server-token=secret", ErrServerJoinCredentials},
		{"management-limit", "/api/v1/nodes", 200, strings.Repeat("x", serverJoinResponseLimit+1), ErrServerJoinUnhealthy},
		{"management-redirect", "/readyz", 302, "private-redirect", ErrServerJoinUnhealthy},
		{"config-redirect", "/v1-k3s/config", 302, "private-redirect", ErrServerJoinConfig},
		{"ca-redirect", "/cacerts", 302, "private-redirect", ErrServerJoinCredentials},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newServerJoinTestFixture(t)
			f.mu.Lock()
			f.replies[scenario.path] = serverJoinTestReply{status: scenario.status, body: scenario.body, redirect: f.server.URL + "/v1-k3s/server-bootstrap"}
			f.mu.Unlock()
			_, err := f.inspect(context.Background())
			if !errors.Is(err, scenario.want) || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe failure: %v", err)
			}
			for _, path := range f.requests {
				if path == "/v1-k3s/server-bootstrap" {
					t.Fatal("followed a redirect into a private endpoint")
				}
			}
		})
	}
}

func TestInspectServerJoinRejectsMissingConfigAndIndependentAgentCredentials(t *testing.T) {
	for _, scenario := range []string{"missing-bool", "null-bool", "mismatched-cidr", "missing-plural", "separate-agent", "partial-master", "service-lb-mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			f := newServerJoinTestFixture(t)
			want := ErrServerJoinConfig
			f.mu.Lock()
			switch scenario {
			case "missing-bool":
				delete(f.config, "DisableKubeProxy")
			case "null-bool":
				f.config["DisableCCM"] = nil
			case "mismatched-cidr":
				_, other, _ := net.ParseCIDR("10.99.0.0/16")
				f.config["ClusterIPRange"] = other
			case "missing-plural":
				delete(f.config, "ServiceIPRanges")
			case "separate-agent":
				f.agentPassword = "private-independent-agent-password"
			case "partial-master":
				f.config["DisableETCD"] = true
				want = ErrServerJoinUnsupported
			case "service-lb-mismatch":
				f.config["DisableServiceLB"] = false
			}
			f.mu.Unlock()
			if _, err := f.inspect(context.Background()); !errors.Is(err, want) {
				t.Fatalf("unknown config accepted: %v", err)
			}
		})
	}
}

func TestInspectServerJoinRequiresAllHealthyMatchedControlPlaneMembers(t *testing.T) {
	for _, scenario := range []string{"not-ready", "learner", "orphan-member", "missing-member", "mismatch-address", "partial-role", "mixed-version", "zero-member-id"} {
		t.Run(scenario, func(t *testing.T) {
			f := newServerJoinTestFixture(t)
			f.mu.Lock()
			switch scenario {
			case "not-ready":
				f.nodes[0].Status.Conditions[0].Status = corev1.ConditionFalse
			case "learner":
				f.members[0]["isLearner"] = true
			case "orphan-member":
				f.nodes = nil
			case "missing-member":
				f.members = nil
			case "mismatch-address":
				f.nodes[0].Annotations["etcd.k3s.cattle.io/node-address"] = "10.0.0.99"
			case "partial-role":
				delete(f.nodes[0].Labels, "node-role.kubernetes.io/etcd")
			case "mixed-version":
				f.nodes[0].Status.NodeInfo.KubeletVersion = "v1.35.4+k3s1"
			case "zero-member-id":
				f.members[0]["ID"] = 0
			}
			f.mu.Unlock()
			if _, err := f.inspect(context.Background()); !errors.Is(err, ErrServerJoinUnhealthy) {
				t.Fatalf("unhealthy cluster accepted: %v", err)
			}
		})
	}
}

func TestInspectServerJoinCannotUseInternalMarkerToBypassNodeCreatePermission(t *testing.T) {
	f := newServerJoinTestFixture(t)
	f.service.authorizationResolver = func(ctx context.Context, _ *model.KubernetesCluster, _ bool) (*clusterAuthorization, error) {
		if internalClusterAccess(ctx) {
			t.Error("permission check retained a preexisting internal marker")
		}
		return &clusterAuthorization{Enforced: true, Permissions: map[string][]string{"": {"kubernetes.node.read"}}}, nil
	}
	if _, err := f.inspect(WithInternalClusterAccess(context.Background(), "node-bootstrap")); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("permission fence bypassed: %v", err)
	}
	if len(f.requests) != 0 {
		t.Fatal("unauthorized inspection reached the remote cluster")
	}
}

func TestInspectServerJoinNodeCreateGrantEnablesOnlyTrustedInternalReads(t *testing.T) {
	f := newServerJoinTestFixture(t)
	var calls atomic.Int32
	f.service.authorizationResolver = func(ctx context.Context, _ *model.KubernetesCluster, _ bool) (*clusterAuthorization, error) {
		calls.Add(1)
		if internalClusterAccess(ctx) {
			t.Error("operation authorization must run before internal access")
		}
		return &clusterAuthorization{Enforced: true, CacheKey: "server-join/test", Version: "policy-v1", Principal: "edo:server-join:test", ClusterScope: true, Permissions: map[string][]string{"": {"kubernetes.node.create"}}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := f.inspect(ctx); err != nil || calls.Load() == 0 {
		t.Fatalf("authorized trusted inspection failed: calls=%d err=%v", calls.Load(), err)
	}
}

func TestInspectServerJoinHonorsCallerDeadline(t *testing.T) {
	f := newServerJoinTestFixture(t)
	f.mu.Lock()
	f.replies["/db/info"] = serverJoinTestReply{wait: true}
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := f.inspect(ctx); !errors.Is(err, ErrServerJoinUnsupported) || time.Since(started) > time.Second {
		t.Fatalf("inspection did not honor its deadline: %v", err)
	}
}

func TestServerJoinMemberURLsRejectUnsafeAddressesAndPaths(t *testing.T) {
	for _, value := range []string{
		"https://127.0.0.1:2380", "https://0.0.0.0:2380", "https://[::]:2380", "https://[::1]:2380", "https://169.254.1.1:2380", "https://localhost:2380",
		"http://10.0.0.10:2380", "https://10.0.0.10:2380/private", "https://user:private-password@10.0.0.10:2380", "https://10.0.0.10:2380?password=private",
	} {
		t.Run(value, func(t *testing.T) {
			data, _ := json.Marshal(map[string]any{"members": []map[string]any{{"ID": 1, "name": "member-one", "peerURLs": []string{value}, "clientURLs": []string{"https://10.0.0.10:2379"}}}})
			if _, err := serverJoinMembers(data); !errors.Is(err, ErrServerJoinUnhealthy) || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe member URL accepted or leaked: %v", err)
			}
		})
	}
}

func TestServerJoinMembershipDigestIsStableAcrossResponseOrdering(t *testing.T) {
	a := map[string]any{"ID": 2, "name": "member-a", "peerURLs": []string{"https://10.0.0.11:2380/", "https://10.0.0.10:2380"}, "clientURLs": []string{"https://10.0.0.11:2379/", "https://10.0.0.10:2379"}}
	b := map[string]any{"ID": 1, "name": "member-b", "peerURLs": []string{"https://10.0.0.12:2380"}, "clientURLs": []string{"https://10.0.0.12:2379"}}
	first, _ := json.Marshal(map[string]any{"members": []any{a, b}})
	a["peerURLs"] = []string{"https://10.0.0.10:2380", "https://10.0.0.11:2380/"}
	a["clientURLs"] = []string{"https://10.0.0.10:2379", "https://10.0.0.11:2379/"}
	second, _ := json.Marshal(map[string]any{"members": []any{b, a}})
	left, err := serverJoinMembers(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := serverJoinMembers(second)
	if err != nil || serverJoinDigest(left) != serverJoinDigest(right) {
		t.Fatalf("member ordering changed review digest: %v", err)
	}
}
