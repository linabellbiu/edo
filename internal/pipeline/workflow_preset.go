package pipeline

import (
	"strings"

	"edo/internal/model"
)

const (
	workflowPresetBlank  = "blank"
	workflowPresetGo     = "go"
	workflowPresetNodeJS = "nodejs"
	workflowPresetPython = "python"
)

type WorkflowPresetStep struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type WorkflowPreset struct {
	Key         string               `json:"key"`
	Category    string               `json:"category"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Steps       []WorkflowPresetStep `json:"steps"`
}

type workflowLanguagePreset struct {
	name       string
	testName   string
	testScript string
}

type workflowPresetDefinition struct {
	WorkflowPreset
	language     string
	includeTest  bool
	kubernetes   bool
	fullArtifact bool
}

var workflowLanguagePresets = map[string]workflowLanguagePreset{
	workflowPresetGo: {
		name:       "Go 流水线",
		testName:   "Golang 单元测试",
		testScript: "set -eu\ngo test ./...\n",
	},
	workflowPresetNodeJS: {
		name:       "Node.js 流水线",
		testName:   "Node.js 单元测试",
		testScript: "set -eu\nnpm ci\nnpm test\n",
	},
	workflowPresetPython: {
		name:       "Python 流水线",
		testName:   "Python 单元测试",
		testScript: "set -eu\nif [ -f requirements.txt ]; then\n  python -m pip install -r requirements.txt\nfi\npython -m pytest\n",
	},
}

var workflowPresetDefinitions = []workflowPresetDefinition{
	{
		WorkflowPreset: WorkflowPreset{
			Key: workflowPresetBlank, Category: "quickstart", Name: "空白流水线",
			Description: "从一个代码源开始，自由添加测试、构建和部署任务。",
			Steps:       []WorkflowPresetStep{},
		},
	},
	newHostPreset("go-host", workflowPresetGo, "Go · 构建并部署到自有主机", false, false),
	newHostPreset("go-artifact-host", workflowPresetGo, "Go · 测试、构建并部署到自有主机", true, true),
	newKubernetesPreset("go-kubernetes", workflowPresetGo, "Go · 测试、镜像构建并部署到 Kubernetes", true),
	newHostPreset("nodejs-host", workflowPresetNodeJS, "Node.js · 构建并部署到自有主机", false, false),
	newHostPreset("nodejs-artifact-host", workflowPresetNodeJS, "Node.js · 测试、构建并部署到自有主机", true, true),
	newKubernetesPreset("nodejs-kubernetes", workflowPresetNodeJS, "Node.js · 测试、镜像构建并部署到 Kubernetes", true),
	newHostPreset("python-host", workflowPresetPython, "Python · 构建并部署到自有主机", false, false),
	newHostPreset("python-artifact-host", workflowPresetPython, "Python · 构建制品并部署到自有主机", false, true),
	newKubernetesPreset("python-kubernetes", workflowPresetPython, "Python · 镜像构建并部署到 Kubernetes", false),
}

func newHostPreset(key, language, name string, includeTest, fullArtifact bool) workflowPresetDefinition {
	steps := make([]WorkflowPresetStep, 0, 3)
	if includeTest {
		steps = append(steps, WorkflowPresetStep{Name: workflowLanguagePresets[language].testName, Type: "test"})
	}
	buildName := languageBuildName(language)
	if fullArtifact {
		buildName += "并归档制品"
	}
	steps = append(steps,
		WorkflowPresetStep{Name: buildName, Type: "build"},
		WorkflowPresetStep{Name: "主机部署", Type: "deploy"},
	)
	return workflowPresetDefinition{
		WorkflowPreset: WorkflowPreset{
			Key: key, Category: language, Name: name,
			Description: "构建文件制品并通过主机脚本部署；创建后需要选择构建方案和主机部署方案。",
			Steps:       steps,
		},
		language: language, includeTest: includeTest, fullArtifact: fullArtifact,
	}
}

func newKubernetesPreset(key, language, name string, includeTest bool) workflowPresetDefinition {
	steps := make([]WorkflowPresetStep, 0, 3)
	if includeTest {
		steps = append(steps, WorkflowPresetStep{Name: workflowLanguagePresets[language].testName, Type: "test"})
	}
	steps = append(steps,
		WorkflowPresetStep{Name: "镜像构建", Type: "build"},
		WorkflowPresetStep{Name: "Kubernetes 部署", Type: "deploy"},
	)
	return workflowPresetDefinition{
		WorkflowPreset: WorkflowPreset{
			Key: key, Category: language, Name: name,
			Description: "构建并推送容器镜像，再更新 Kubernetes 工作负载；创建后需要选择镜像构建和 Kubernetes 部署方案。",
			Steps:       steps,
		},
		language: language, includeTest: includeTest, kubernetes: true,
	}
}

func languageBuildName(language string) string {
	switch language {
	case workflowPresetGo:
		return "Golang 构建"
	case workflowPresetNodeJS:
		return "Node.js 构建"
	case workflowPresetPython:
		return "Python 构建"
	default:
		return "构建制品"
	}
}

func ListWorkflowPresets() []WorkflowPreset {
	result := make([]WorkflowPreset, 0, len(workflowPresetDefinitions))
	for i := range workflowPresetDefinitions {
		preset := workflowPresetDefinitions[i].WorkflowPreset
		preset.Steps = append(make([]WorkflowPresetStep, 0, len(preset.Steps)), preset.Steps...)
		result = append(result, preset)
	}
	return result
}

func findWorkflowPreset(key string) (workflowPresetDefinition, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for i := range workflowPresetDefinitions {
		if workflowPresetDefinitions[i].Key == key {
			return workflowPresetDefinitions[i], true
		}
	}
	return workflowPresetDefinition{}, false
}

// applyWorkflowPreset 为应用创建保留简短语言模板键；公共流水线方案使用完整模板键。
func applyWorkflowPreset(workflow *model.ReleaseWorkflow, key string) error {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == workflowPresetBlank {
		workflow.Name = "空白流水线"
		workflow.Stages = []model.WorkflowStage{}
		return nil
	}

	if language, ok := workflowLanguagePresets[key]; ok {
		workflow.Name = language.name
		workflow.Stages = buildWorkflowPresetStages(workflowPresetDefinition{
			language: key, includeTest: true, fullArtifact: true,
		}, defaultWorkflowRuntime(key))
		return nil
	}

	preset, ok := findWorkflowPreset(key)
	if !ok || preset.Key == workflowPresetBlank {
		return ErrInvalidWorkflow
	}
	workflow.Name = preset.Name
	workflow.Stages = buildWorkflowPresetStages(preset, defaultWorkflowRuntime(preset.language))
	return nil
}

func buildWorkflowPresetStages(preset workflowPresetDefinition, runtime WorkflowRuntimeVersion) []model.WorkflowStage {
	stages := make([]model.WorkflowStage, 0, 3)
	if preset.includeTest {
		language := workflowLanguagePresets[preset.language]
		stages = append(stages, model.WorkflowStage{
			ID: "test", Name: "测试",
			Tasks: []model.WorkflowNode{{
				ID: "test-command", Type: model.WorkflowNodeShell, Name: language.testName,
				Config: model.WorkflowNodeConfig{
					Script: language.testScript, RuntimeImage: runtime.Image,
					ToolchainLanguage: runtime.Language, ToolchainVersion: runtime.Version,
					WorkingDirectory: ".", TimeoutSeconds: 600,
					EnvironmentVariables: map[string]string{},
				},
			}},
		})
	}

	buildName := languageBuildName(preset.language)
	buildDescription := "请选择脚本构建方案；构建产生的文件制品会自动归档。"
	if preset.kubernetes {
		buildName = "镜像构建"
		buildDescription = "请选择绑定镜像仓库的 Dockerfile 构建方案。"
	} else if preset.fullArtifact {
		buildName += "并归档制品"
	}
	stages = append(stages, model.WorkflowStage{
		ID: "build", Name: "构建",
		Tasks: []model.WorkflowNode{{
			ID: "build-artifact", Type: model.WorkflowNodeBuild, Name: buildName,
			Config: model.WorkflowNodeConfig{
				Description: buildDescription, RuntimeImage: runtime.Image,
				ToolchainLanguage: runtime.Language, ToolchainVersion: runtime.Version,
			},
		}},
	})

	deployName := "主机部署"
	deployDescription := "请选择主机脚本部署方案。"
	if preset.kubernetes {
		deployName = "Kubernetes 部署"
		deployDescription = "请选择 Kubernetes 部署方案。"
	}
	stages = append(stages, model.WorkflowStage{
		ID: "deploy", Name: "部署",
		Tasks: []model.WorkflowNode{{
			ID: "deploy-artifact", Type: model.WorkflowNodeDeploy, Name: deployName,
			Config: model.WorkflowNodeConfig{Description: deployDescription},
		}},
	})
	return stages
}
