package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"edo/internal/dockerengine"
	"edo/internal/pipeline"
)

type asyncWorkflowRuntimeManager struct {
	mu        sync.Mutex
	installed bool
	release   chan struct{}
}

func (m *asyncWorkflowRuntimeManager) InspectScriptRuntimeImage(_ context.Context, image string) (dockerengine.ScriptRuntimeImageStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return dockerengine.ScriptRuntimeImageStatus{Image: image, Installed: m.installed}, nil
}

func (m *asyncWorkflowRuntimeManager) PrepareScriptRuntimeImage(_ context.Context, image string) (dockerengine.ScriptRuntimeImageStatus, error) {
	<-m.release
	m.mu.Lock()
	m.installed = true
	m.mu.Unlock()
	return dockerengine.ScriptRuntimeImageStatus{Image: image, ImageID: "sha256:runtime", Installed: true}, nil
}

func TestPrepareWorkflowRuntimeAPIReturnsAcceptedBeforePullCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := pipeline.NewService(nil, nil, nil)
	manager := &asyncWorkflowRuntimeManager{release: make(chan struct{})}
	service.ConfigureWorkflowRuntimeManager(manager)
	handler := pipelineHandler{service: service, logger: slog.Default()}
	router := gin.New()
	router.POST("/api/v1/workflow-runtime-versions/prepare", handler.prepareWorkflowRuntimeVersion)

	response := performJSONRequest(t, router, http.MethodPost, "/api/v1/workflow-runtime-versions/prepare", map[string]string{
		"language": "python", "version": "3.11",
	}, nil)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"preparation_status":"preparing"`) {
		close(manager.release)
		t.Fatalf("镜像拉取开始后应立即返回 202: status=%d body=%s", response.Code, response.Body.String())
	}
	close(manager.release)
}

func TestWorkflowPresetCatalogAndCreationAPI(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()

	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	adminCookie := login.Result().Cookies()[0]

	catalog := performJSONRequest(t, router, http.MethodGet, "/api/v1/workflow-presets", nil, adminCookie)
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), `"key":"go-kubernetes"`) ||
		!strings.Contains(catalog.Body.String(), `"key":"docker-container","category":"docker"`) ||
		!strings.Contains(catalog.Body.String(), `"key":"nodejs-artifact-host"`) ||
		!strings.Contains(catalog.Body.String(), `"key":"python-host"`) ||
		!strings.Contains(catalog.Body.String(), `"key":"blank","category":"quickstart","name":"空白流水线","description":"从一个代码源开始，自由添加测试、构建和部署任务。","steps":[]`) {
		t.Fatalf("流水线模板目录接口不完整: status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	runtimes := performJSONRequest(t, router, http.MethodGet, "/api/v1/workflow-runtime-versions?language=nodejs", nil, adminCookie)
	if runtimes.Code != http.StatusOK || !strings.Contains(runtimes.Body.String(), `"version":"24"`) ||
		!strings.Contains(runtimes.Body.String(), `"image":"node:24-alpine"`) || !strings.Contains(runtimes.Body.String(), `"installed":true`) {
		t.Fatalf("流水线语言版本目录不完整: status=%d body=%s", runtimes.Code, runtimes.Body.String())
	}

	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/workflow-templates/from-preset", map[string]string{
		"preset_key": "nodejs-artifact-host", "runtime_version": "24",
	}, adminCookie)
	var payload struct {
		WorkflowTemplate struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			IsActive bool   `json:"is_active"`
			Stages   []struct {
				Tasks []struct {
					Type   string `json:"type"`
					Config struct {
						ToolchainVersion string `json:"toolchain_version"`
					} `json:"config"`
				} `json:"tasks"`
			} `json:"stages"`
		} `json:"workflow_template"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &payload) != nil || payload.WorkflowTemplate.ID == "" {
		t.Fatalf("从模板创建流水线方案失败: status=%d body=%s", created.Code, created.Body.String())
	}
	if payload.WorkflowTemplate.IsActive || payload.WorkflowTemplate.Name != "Node.js · 测试、构建并部署到自有主机" ||
		len(payload.WorkflowTemplate.Stages) != 3 || payload.WorkflowTemplate.Stages[0].Tasks[0].Type != "shell" ||
		payload.WorkflowTemplate.Stages[0].Tasks[0].Config.ToolchainVersion != "24" {
		t.Fatalf("模板接口没有生成预期草稿: body=%s", created.Body.String())
	}

	dockerCreated := performJSONRequest(t, router, http.MethodPost, "/api/v1/workflow-templates/from-preset", map[string]string{
		"preset_key": "docker-container",
	}, adminCookie)
	if dockerCreated.Code != http.StatusCreated || !strings.Contains(dockerCreated.Body.String(), `"name":"Docker · 镜像构建并部署到容器"`) ||
		!strings.Contains(dockerCreated.Body.String(), `"name":"Docker 容器部署"`) {
		t.Fatalf("Docker 流水线模板接口不正确: status=%d body=%s", dockerCreated.Code, dockerCreated.Body.String())
	}
}
