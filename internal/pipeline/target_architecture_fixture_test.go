package pipeline

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"edo/internal/config"
	"edo/internal/deployment"
	"edo/internal/kube"
	"edo/internal/model"
)

func configureKubernetesArchitectureFixture(t *testing.T, service *Service, clusterID string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = io.WriteString(w, `{"gitVersion":"v1.35.2"}`)
		case "/api/v1/nodes":
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"architecture-fixture"},"status":{"nodeInfo":{"architecture":"amd64","operatingSystem":"linux"}}}]}`)
		default:
			t.Errorf("架构 fixture 不应处理其他集群操作: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	encoded, err := clientcmd.Write(clientcmdapi.Config{
		Clusters:  map[string]*clientcmdapi.Cluster{"fixture": {Server: server.URL, CertificateAuthorityData: certificate}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"fixture": {Token: "architecture-fixture-token"}},
		Contexts:  map[string]*clientcmdapi.Context{"fixture": {Cluster: "fixture", AuthInfo: "fixture"}}, CurrentContext: "fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := service.secrets.Encrypt(string(encoded), []byte("kubernetes_cluster:"+clusterID+":kubeconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.db.Model(&model.KubernetesCluster{}).Where("id = ?", clusterID).
		Updates(map[string]any{"api_server": server.URL, "kubeconfig_ciphertext": ciphertext}).Error; err != nil {
		t.Fatal(err)
	}
	connection := kube.NewService(service.db, service.secrets, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second})
	service.deployments = deployment.NewService(service.db, service.docker, connection, nil, nil, "", service.logger)
}
