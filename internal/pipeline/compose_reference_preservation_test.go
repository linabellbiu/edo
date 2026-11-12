package pipeline

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"edo/internal/model"
)

func TestComposeKeepsConfiguredTagAndLogsActualImageWithoutManifestLookup(t *testing.T) {
	s, p, ctx := newComposeParameterTest(t, nil)
	var requests atomic.Int32
	registryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer registryServer.Close()
	registry := model.ImageRegistry{ID: "preserved-registry", ProjectID: p.run.ProjectID, Name: "所选仓库", Endpoint: registryServer.URL, Namespace: "team", AllowInsecureHTTP: true, Provider: model.RegistryGeneric, IsActive: true, Username: "registry-user"}
	var err error
	registry.CredentialCiphertext, err = s.secrets.Encrypt("private-registry-password", []byte("image_registry:"+registry.Name+":credential"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	base := strings.TrimPrefix(registryServer.URL, "http://") + "/team"
	p.node.Config.DeploymentRegistryID = registry.ID
	p.node.Config.Compose = &model.ComposeDeploymentSettings{TagVariable: "${EDO_IMAGE_TAG}"}
	p.snapshot.PreserveComposeImageReferences = true
	p.snapshot.RegistryInputs = map[string][]workflowRegistryInput{p.node.ID: {{RegistryID: registry.ID, ImageRef: base + "/web@sha256:" + strings.Repeat("a", 64), Tag: "v0.0.4"}}}
	p.variables = map[string]string{"EDO_IMAGE_TAG": "v0.0.4", "IMAGE_NAMESPACE": base, "API_TOKEN": "private-config-token"}
	source := "services:\n  web:\n    image: ${IMAGE_NAMESPACE}/web:${EDO_IMAGE_TAG}\n  other:\n    image: nginx:latest\n  secret_name:\n    image: ${IMAGE_NAMESPACE}/${API_TOKEN}:v1\n  pinned:\n    image: " + base + "/fixed@sha256:" + strings.Repeat("b", 64) + "\n"
	p.deploymentPlan.ComposeYAML = source
	content, err := s.resolveConfiguredComposeImages(ctx, p, source)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseComposeStack(content)
	if err != nil {
		t.Fatal(err)
	}
	services := doc["services"].(map[string]any)
	if services["web"].(map[string]any)["image"] != base+"/web:v0.0.4" || services["other"].(map[string]any)["image"] != "nginx:latest" || services["pinned"].(map[string]any)["image"] != base+"/fixed@sha256:"+strings.Repeat("b", 64) {
		t.Fatal("改写了用户配置的 Tag、名称或显式 Digest", content)
	}
	if p.composeArtifacts["web"].ImageRef != base+"/web:v0.0.4" || requests.Load() != 0 {
		t.Fatal("解析期间仍将 Tag 转成摘要或访问仓库")
	}
	if _, exists := p.composeArtifacts["other"]; exists {
		t.Fatal("向其他仓库应用了所选认证")
	}
	logs, _, err := s.ListNodeRunLogs(ctx, p.run.ID, p.node.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, line := range logs {
		text.WriteString(line.Message)
	}
	if !strings.Contains(text.String(), base+"/web:v0.0.4") || !strings.Contains(text.String(), "${EDO_IMAGE_TAG}") || strings.Contains(text.String(), "private-registry-password") || strings.Contains(text.String(), "private-config-token") {
		t.Fatal("日志缺少实际引用或包含秘密", text.String())
	}
}
