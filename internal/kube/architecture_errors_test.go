package kube

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"edo/internal/model"
)

func TestNodeArchitecturesClassifyActualAPIResponses(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		reason metav1.StatusReason
		want   error
	}{
		{"credentials", http.StatusUnauthorized, metav1.StatusReasonUnauthorized, ErrNodeArchitectureCredentials},
		{"permission", http.StatusForbidden, metav1.StatusReasonForbidden, ErrNodeArchitecturePermission},
		{"server-timeout", http.StatusGatewayTimeout, metav1.StatusReasonTimeout, ErrNodeArchitectureTimeout},
		{"unknown", http.StatusInternalServerError, metav1.StatusReasonInternalError, ErrNodeArchitecturesUnavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(scenario.status)
				_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: "Failure", Reason: scenario.reason, Code: int32(scenario.status), Message: "https://private-api.invalid?token=private-token unauthorized forbidden timeout"})
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			cluster, err := service.Find(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			architectures, err := service.NodeArchitectures(context.Background(), id, cluster.ProjectID)
			if architectures != nil || err != scenario.want {
				t.Fatalf("API 故障类型错误: architectures=%v got=%v want=%v", architectures, err, scenario.want)
			}
			assertArchitectureErrorSafe(t, err)
		})
	}
}

func TestNodeArchitecturesClassifyActualNetworkFailures(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		refused bool
		want    error
	}{
		{"refused", true, ErrNodeArchitectureConnection},
		{"timeout", false, ErrNodeArchitectureTimeout},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
			service := newKubeTestService(t, 100*time.Millisecond)
			id := createKubeTestCluster(t, service, server)
			cluster, err := service.Find(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.refused {
				server.Close()
			}
			architectures, err := service.NodeArchitectures(context.Background(), id, cluster.ProjectID)
			if architectures != nil || err != scenario.want {
				t.Fatalf("网络故障类型错误: architectures=%v got=%v want=%v", architectures, err, scenario.want)
			}
			assertArchitectureErrorSafe(t, err)
		})
	}
}

func TestNodeArchitectureErrorTypesPreserveSafeDomains(t *testing.T) {
	certificate := &x509.Certificate{}
	for _, scenario := range []struct {
		name         string
		source, want error
	}{
		{"edo-access", fmt.Errorf("private-token: %w", ErrRBACForbidden), ErrRBACForbidden},
		{"edo-sync", fmt.Errorf("private-token: %w", ErrRBACSyncPending), ErrRBACSyncPending},
		{"config", fmt.Errorf("private-token: %w", ErrInvalidCluster), ErrInvalidCluster},
		{"scope", fmt.Errorf("private-token: %w", ErrClusterNotFound), ErrClusterNotFound},
		{"credentials-domain", fmt.Errorf("private-token: %w", ErrResourceForbidden), ErrNodeArchitecturePermission},
		{"cancel", fmt.Errorf("private-token: %w", context.Canceled), ErrNodeArchitectureCanceled},
		{"timeout", &url.Error{Op: "Get", URL: "https://private-api.invalid?token=private-token", Err: context.DeadlineExceeded}, ErrNodeArchitectureTimeout},
		{"connection", &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 6443}, Err: syscall.ECONNREFUSED}, ErrNodeArchitectureConnection},
		{"dns", &net.DNSError{Name: "private-api.invalid", Err: "private-token", IsNotFound: true}, ErrNodeArchitectureConnection},
		{"certificate-ca", x509.UnknownAuthorityError{Cert: certificate}, ErrNodeArchitectureTLS},
		{"certificate-hostname", x509.HostnameError{Certificate: certificate, Host: "private-api.invalid"}, ErrNodeArchitectureTLS},
		{"certificate-invalid", x509.CertificateInvalidError{Cert: certificate, Reason: x509.Expired, Detail: "private-token"}, ErrNodeArchitectureTLS},
		{"certificate-roots", x509.SystemRootsError{Err: errors.New("private-token")}, ErrNodeArchitectureTLS},
		{"certificate-wrapper", &tls.CertificateVerificationError{Err: errors.New("private-token")}, ErrNodeArchitectureTLS},
		{"no-keyword-matching", errors.New("private-token unauthorized forbidden timeout connection refused"), ErrNodeArchitecturesUnavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			err := nodeArchitectureReadError(scenario.source)
			if err != scenario.want {
				t.Fatalf("错误分类丢失领域身份: got=%v want=%v", err, scenario.want)
			}
			assertArchitectureErrorSafe(t, err)
		})
	}
}

func assertArchitectureErrorSafe(t *testing.T, err error) {
	t.Helper()
	for _, forbidden := range []string{"private-token", "private-api", "192.0.2.1", "http", "6443"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("对外错误泄露内部信息: %v", err)
		}
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("对外错误仍携带原始诊断: %v", err)
	}
}

func TestNodeArchitecturesPreserveEDOAuthorizationAndConfigFailures(t *testing.T) {
	t.Run("rbac", func(t *testing.T) {
		f := newRBACFixture(t)
		state := f.enable(t)
		if _, err := f.service.NodeArchitectures(f.userCtx, f.cluster.ID, f.cluster.ProjectID); err != ErrRBACSyncPending {
			t.Fatalf("EDO 授权未同步被错误归类: %v", err)
		}
		if _, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.NodeArchitectures(f.userCtx, f.cluster.ID, f.cluster.ProjectID); err != ErrRBACForbidden {
			t.Fatalf("EDO 节点权限拒绝被错误归类: %v", err)
		}
	})
	t.Run("invalid-config", func(t *testing.T) {
		server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("无效配置不应访问集群") })
		service := newKubeTestService(t, time.Second)
		id := createKubeTestCluster(t, service, server)
		cluster, err := service.Find(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		ciphertext, err := service.secrets.Encrypt("private-token: [invalid", kubeconfigAAD(id))
		if err != nil {
			t.Fatal(err)
		}
		if err = service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", id).Update("kubeconfig_ciphertext", ciphertext).Error; err != nil {
			t.Fatal(err)
		}
		if _, err = service.NodeArchitectures(context.Background(), id, cluster.ProjectID); err != ErrInvalidCluster {
			t.Fatalf("无效配置被错误归类: %v", err)
		}
		assertArchitectureErrorSafe(t, err)
	})
}
