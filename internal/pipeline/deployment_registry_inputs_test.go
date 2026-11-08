package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

func TestDeploymentRegistrySourcePinsDigestAndFiltersRepositoryNamespace(t *testing.T) {
	s, db, _, _ := newPipelineTestService(t)
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project, DepartmentID: database.DefaultDepartmentID, UserID: "registry-authority"})
	actor := model.User{ID: "registry-authority", Username: "registry_authority", DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureDeliveryAccess(permissions)
	var revision atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(200)
		case r.URL.Path == "/v2/_catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"repositories":["team/app","other/private"]}`))
		case r.URL.Path == "/v2/team/app/tags/list":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"team/app","tags":["v1","v2"]}`))
		case r.URL.Path == "/v2/team/app/manifests/v1":
			digit := "a"
			if revision.Load() > 0 {
				digit = "b"
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat(digit, 64))
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(200)
		default:
			t.Errorf("未知仓库请求: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	registry, err := s.CreateRegistry(ctx, actor.ID, RegistryInput{Name: "来源仓库", Provider: model.RegistryGeneric, Endpoint: server.URL, Namespace: "team", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	node := model.WorkflowNode{ID: "deploy", Config: model.WorkflowNodeConfig{DeploymentInputVersion: 1, DeploymentInputs: []model.DeploymentInput{{Kind: "registry", RegistryID: registry.ID, Image: "app", Tag: "v1"}}}}
	first, err := s.freezeDeploymentRegistryInputs(ctx, db, node, actor.ID)
	if err != nil || len(first) != 1 || !strings.HasSuffix(first[0].ImageRef, "@sha256:"+strings.Repeat("a", 64)) {
		t.Fatalf("摘要未固定: %+v %v", first, err)
	}
	revision.Store(1)
	second, err := s.freezeDeploymentRegistryInputs(ctx, db, node, actor.ID)
	if err != nil || !strings.HasSuffix(second[0].ImageRef, strings.Repeat("b", 64)) || !strings.HasSuffix(first[0].ImageRef, strings.Repeat("a", 64)) {
		t.Fatal("Tag 变化影响了原快照", err)
	}
	repos, err := s.DeploymentRegistryOptions(ctx, registry.ID, "")
	if err != nil || fmt.Sprint(repos) != "[app]" {
		t.Fatalf("仓库命名空间未过滤: %v %v", repos, err)
	}
	tags, err := s.DeploymentRegistryOptions(ctx, registry.ID, "app")
	if err != nil || fmt.Sprint(tags) != "[v1 v2]" {
		t.Fatalf("未读取实际 Tag: %v %v", tags, err)
	}
	other := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "other-project", DepartmentID: database.DefaultDepartmentID, UserID: actor.ID})
	if _, err := s.DeploymentRegistryOptions(other, registry.ID, "app"); err == nil {
		t.Fatal("跨项目读取了仓库")
	}
}
