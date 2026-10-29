package dockerengine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/config"

	containerderrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

const pushStreamTestImage = "registry.invalid/team/api:release"

func TestBuildAndPushStreamOutlivesOrdinaryRequestTimeout(t *testing.T) {
	for _, test := range []struct {
		name string
		auth RegistryAuth
	}{
		{name: "匿名仓库", auth: RegistryAuth{ServerAddress: "registry.invalid", Host: "registry.invalid"}},
		{name: "密码认证", auth: RegistryAuth{ServerAddress: "registry.invalid", Host: "registry.invalid", Username: "fixture-user", Credential: "fixture-password"}},
		{name: "令牌认证", auth: RegistryAuth{ServerAddress: "registry.invalid", Host: "registry.invalid", Credential: "fixture-token"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var pushed, inspected atomic.Int32
			closed := make(chan struct{}, 4)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if pushStreamTestPing(w, r) {
					return
				}
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/images/registry.invalid/team/api/push"):
					pushed.Add(1)
					if r.URL.Query().Get("tag") != "release" {
						t.Error("推送没有保留镜像标签")
					}
					payload, err := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
					var auth struct{ ServerAddress, Username, Password, IdentityToken string }
					if err != nil || json.Unmarshal(payload, &auth) != nil || auth.ServerAddress != test.auth.ServerAddress || auth.Username != test.auth.Username {
						t.Error("推送认证元数据没有保持原配置")
					}
					if test.auth.Username == "" && auth.IdentityToken != test.auth.Credential || test.auth.Username != "" && auth.Password != test.auth.Credential {
						t.Error("推送认证类型发生变化")
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, "{\"status\":\"Pushing\"}\n")
					w.(http.Flusher).Flush()
					select {
					case <-time.After(400 * time.Millisecond):
						_, _ = io.WriteString(w, "{\"status\":\"Pushed\"}\n")
					case <-r.Context().Done():
					}
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/images/registry.invalid/team/api:release/json"):
					inspected.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:" + strings.Repeat("a", 64), "RepoDigests": []string{"registry.invalid/team/api@sha256:" + strings.Repeat("b", 64)}, "Size": 12345})
				default:
					t.Errorf("推送产生了意外请求: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateClosed {
					select {
					case closed <- struct{}{}:
					default:
					}
				}
			}
			server.Start()
			t.Cleanup(server.Close)
			service, source := pushStreamTestService(t, server.URL)
			result, err := service.BuildAndPushArtifactWithOptions(t.Context(), source, "Dockerfile", pushStreamTestImage, test.auth, BuildOptions{CacheEnabled: true}, 3*time.Second, io.Discard)
			if err != nil {
				t.Fatalf("推送响应体不应被普通请求总超时截断: %v", err)
			}
			if pushed.Load() != 1 || inspected.Load() != 1 || result.SizeBytes != 12345 || result.ImageRef != "registry.invalid/team/api@sha256:"+strings.Repeat("b", 64) {
				t.Fatalf("推送完成后没有核验摘要和大小: pushed=%d inspected=%d result=%+v", pushed.Load(), inspected.Load(), result)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("推送完成后没有释放 HTTP 空闲连接")
			}
		})
	}
}

func TestBuildAndPushStreamHonorsCancellationAndBuildDeadline(t *testing.T) {
	for _, test := range []struct {
		name     string
		cancel   bool
		expected error
	}{
		{name: "主动取消", cancel: true, expected: context.Canceled},
		{name: "构建总时限", expected: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			started, released := make(chan struct{}), make(chan struct{})
			var inspected atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if pushStreamTestPing(w, r) {
					return
				}
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/push") {
					inspected.Add(1)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, "{\"status\":\"Pushing\"}\n")
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(released)
			}))
			t.Cleanup(server.Close)
			service, source := pushStreamTestService(t, server.URL)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.cancel {
				go func() {
					select {
					case <-started:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			begin := time.Now()
			_, err := service.BuildAndPushArtifactWithOptions(ctx, source, "Dockerfile", pushStreamTestImage, RegistryAuth{}, BuildOptions{}, 300*time.Millisecond, io.Discard)
			if !errors.Is(err, test.expected) {
				t.Fatalf("没有遵循调用方取消或构建总时限: expected=%v err=%v", test.expected, err)
			}
			if !test.cancel && time.Since(begin) < 250*time.Millisecond {
				t.Fatalf("推送被普通请求时限提前取消: elapsed=%v", time.Since(begin))
			}
			select {
			case <-released:
			case <-time.After(time.Second):
				t.Fatal("取消推送后没有及时关闭响应体")
			}
			if inspected.Load() != 0 {
				t.Fatal("推送中断后仍继续读取镜像摘要")
			}
		})
	}
}

func TestBuildAndPushStreamStillBoundsResponseHeaderWait(t *testing.T) {
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pushStreamTestPing(w, r) {
			return
		}
		<-r.Context().Done()
		close(closed)
	}))
	t.Cleanup(server.Close)
	service, source := pushStreamTestService(t, server.URL)
	started := time.Now()
	_, err := service.BuildAndPushArtifactWithOptions(t.Context(), source, "Dockerfile", pushStreamTestImage, RegistryAuth{}, BuildOptions{}, 3*time.Second, io.Discard)
	if err == nil || time.Since(started) >= time.Second {
		t.Fatalf("没有保留普通请求的响应头等待上限: elapsed=%v err=%v", time.Since(started), err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("响应头超时后没有释放请求")
	}
}

func TestBuildAndPushStreamReleasesFailedResponse(t *testing.T) {
	for _, test := range []struct {
		name, body string
		denied     bool
	}{
		{name: "仓库拒绝", body: "{\"errorDetail\":{\"message\":\"fixture denied\",\"code\":401}}\n", denied: true},
		{name: "无效响应", body: "not-json\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			released := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if pushStreamTestPing(w, r) {
					return
				}
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/push") {
					t.Error("推送失败后不应继续读取镜像摘要")
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.body)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(released)
			}))
			t.Cleanup(server.Close)
			service, source := pushStreamTestService(t, server.URL)
			_, err := service.BuildAndPushArtifactWithOptions(t.Context(), source, "Dockerfile", pushStreamTestImage, RegistryAuth{}, BuildOptions{}, 3*time.Second, io.Discard)
			if err == nil || test.denied && !containerderrdefs.IsUnauthorized(err) {
				t.Fatalf("推送错误类型没有保留: %v", err)
			}
			select {
			case <-released:
			case <-time.After(time.Second):
				t.Fatal("推送失败后没有关闭响应体")
			}
		})
	}
}

func TestBuilderExecutionClientPreservesUnixSocketAndNegotiation(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "edo-push-unix-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pushStreamTestPing(w, r) {
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1.47/") {
			t.Errorf("没有使用协商后的 API 版本: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, "{\"status\":\"Pushing\"}\n")
		w.(http.Flusher).Flush()
		select {
		case <-time.After(250 * time.Millisecond):
			_, _ = io.WriteString(w, "{\"status\":\"Pushed\"}\n")
		case <-r.Context().Done():
		}
	}))
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	service := NewService(nil, nil, config.Runtime{DockerBuilderHost: "unix://" + socket, ConnectTimeout: 100 * time.Millisecond, RequestTimeout: 100 * time.Millisecond})
	api, err := service.builderExecutionClient()
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	stream, err := api.ImagePush(ctx, pushStreamTestImage, client.ImagePushOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Wait(ctx); err != nil || api.ClientVersion() != "1.47" {
		t.Fatalf("Unix Socket 长推送或 API 协商失败: version=%s err=%v", api.ClientVersion(), err)
	}
}

func TestBuilderExecutionClientPreservesMTLSAndBoundsHandshake(t *testing.T) {
	certificate, certPath, roots := pushStreamTestCertificate(t)
	for _, test := range []struct {
		name  string
		stall bool
	}{
		{name: "双向证书验证"},
		{name: "TLS握手超时", stall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var clientVerified atomic.Bool
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				clientVerified.Store(r.TLS != nil && len(r.TLS.VerifiedChains) > 0)
				pushStreamTestPing(w, r)
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}
			releaseHandshake := make(chan struct{})
			defer close(releaseHandshake)
			if test.stall {
				server.TLS.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
					<-releaseHandshake
					return nil, nil
				}
			}
			server.StartTLS()
			t.Cleanup(server.Close)
			service := NewService(nil, nil, config.Runtime{DockerBuilderHost: "tcp://" + strings.TrimPrefix(server.URL, "https://"), DockerBuilderTLSCertPath: certPath, ConnectTimeout: 100 * time.Millisecond, RequestTimeout: time.Second})
			api, err := service.builderExecutionClient()
			if err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			started := time.Now()
			_, err = api.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
			if test.stall {
				if err == nil || !client.IsErrConnectionFailed(err) || ctx.Err() != nil || time.Since(started) >= time.Second {
					t.Fatalf("TLS 建连没有使用连接阶段时限: elapsed=%v err=%v", time.Since(started), err)
				}
			} else if err != nil || !clientVerified.Load() || api.ClientVersion() != "1.47" {
				t.Fatalf("双向 TLS 或版本协商发生变化: verified=%v version=%s err=%v", clientVerified.Load(), api.ClientVersion(), err)
			}
		})
	}
}

func pushStreamTestCertificate(t *testing.T) (tls.Certificate, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "EDO 本地推送测试"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for name, content := range map[string][]byte{"ca.pem": certPEM, "cert.pem": certPEM, "key.pem": keyPEM} {
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("生成本地测试证书失败")
	}
	return certificate, directory, roots
}

func pushStreamTestPing(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/_ping" {
		return false
	}
	w.Header().Set("API-Version", "1.47")
	w.WriteHeader(http.StatusOK)
	return true
}

func pushStreamTestService(t *testing.T, serverURL string) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	t.Setenv("DOCKER_CONFIG", root)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(serverURL, "http://"))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	return NewService(nil, nil, config.Runtime{ConnectTimeout: 100 * time.Millisecond, RequestTimeout: 100 * time.Millisecond}), root
}
