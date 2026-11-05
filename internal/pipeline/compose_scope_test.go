package pipeline

import (
	"strings"
	"testing"

	"edo/internal/model"
)

func TestComposeScopeAndAutomaticImages(t *testing.T) {
	source := "services:\n  api:\n    image: registry.example.com/team/api:${IMAGE_TAG}\n    depends_on: [db]\n  worker:\n    image: registry.example.com/team/api:${IMAGE_TAG}\n  db:\n    image: redis:7-alpine\n"
	config := model.WorkflowNodeConfig{Compose: &model.ComposeDeploymentSettings{ProjectName: "example", TagVariable: "IMAGE_TAG", Services: []string{"api"}}}
	image := model.Artifact{Name: "api:release-one", ImageRef: "registry.example.com/team/api@sha256:" + strings.Repeat("a", 64)}
	resolved, images, err := autoComposeImages(source, config, map[string]model.Artifact{"build": image}, nil)
	if err != nil || len(images) != 1 || len(resolved.ComposeImages) != 1 || resolved.ComposeImages[0].Service != "api" || resolved.ArtifactNodeID != "build" {
		t.Fatalf("服务范围或镜像匹配不正确: %#v %v", resolved, err)
	}
	doc, _ := parseComposeStack(source)
	scope, err := composeServiceScope(doc, config.Compose.Services)
	if err != nil || strings.Join(scope, ",") != "api,db" {
		t.Fatalf("未保留官方依赖范围: %v %v", scope, err)
	}
	if _, _, err = autoComposeImages(source, config, map[string]model.Artifact{"one": image, "two": image}, nil); err == nil {
		t.Fatal("歧义镜像来源被静默选择")
	}
	if _, err = composeServiceScope(doc, []string{"missing"}); err == nil {
		t.Fatal("接受了不存在的服务")
	}
	config.Compose.Services = nil
	resolved, images, err = autoComposeImages(source, config, map[string]model.Artifact{"build": image}, nil)
	if err != nil || len(images) != 2 || len(resolved.ArtifactNodeIDs) != 1 {
		t.Fatal("共用镜像未自动关联")
	}
}

func TestComposeScopeCommandArguments(t *testing.T) {
	source := "services:\n  api:\n    image: alpine:3.22\n  worker:\n    image: alpine:3.22\n"
	config := model.WorkflowNodeConfig{Compose: &model.ComposeDeploymentSettings{ProjectName: "example", TagVariable: "IMAGE_TAG"}}
	if err := validateComposeBindingsYAML(source, config); err != nil {
		t.Fatalf("没有上游构建的固定镜像配置被拒绝: %v", err)
	}
	all, err := composeStackScript(source, config, "/srv/example", 120, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(all, "--wait-timeout 90 'api'") || strings.Contains(all, "--wait-timeout 90 'worker'") {
		t.Fatal("留空时追加了服务参数")
	}
	config.Compose.Services = []string{"api"}
	selected, err := composeStackScript(source, config, "/srv/example", 120, "")
	if err != nil || !strings.Contains(selected, "--wait-timeout 90 'api'") || strings.Contains(selected, "for service in 'api' 'worker'") {
		t.Fatal("服务参数未限制正常部署与失败恢复范围")
	}
	config.Compose.Services = []string{"api", "api"}
	if validateComposeNode(config) == nil {
		t.Fatal("重复服务未拒绝")
	}
}
