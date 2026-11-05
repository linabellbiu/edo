package pipeline

import (
	"context"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestComposeFixedImagesQueueWithoutBuildArtifact(t *testing.T) {
	node := model.WorkflowNode{ID: "deploy", Name: "部署固定镜像", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{Compose: &model.ComposeDeploymentSettings{ProjectName: "example", TagVariable: "IMAGE_TAG"}}}
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "deploy-stage", Tasks: []model.WorkflowNode{node}}}, DeploymentPlans: map[string]workflowDeploymentPlanSnapshot{node.ID: {Kind: model.DeploymentPlanComposeStack}}}
	if id, err := parallelUpstreamArtifact(&snapshot, node, nil, "unrelated-artifact"); err != nil || id != "" {
		t.Fatal("固定镜像部署错误借用了其他制品", err)
	}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	if _, err := s.AdvanceRun(context.Background(), run.ID, "admin", ""); err != nil {
		t.Fatal("没有构建任务的 Compose 无法进入部署队列", err)
	}
	nodes, err := s.ListNodeRuns(context.Background(), run.ID)
	if err != nil || len(nodes) != 1 || nodes[0].ArtifactID != "" || nodes[0].JobID == "" {
		t.Fatal("部署任务错误要求或伪造制品", err)
	}
	var job model.Job
	if err = db.First(&job, "uuid = ?", nodes[0].JobID).Error; err != nil || job.Kind != "pipeline.deploy" {
		t.Fatal("没有创建真实部署任务", err)
	}
}

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
