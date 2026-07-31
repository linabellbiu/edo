package pipeline

import "edo/internal/model"

const (
	workflowPresetBlank  = "blank"
	workflowPresetGo     = "go"
	workflowPresetNodeJS = "nodejs"
	workflowPresetPython = "python"
)

type workflowLanguagePreset struct {
	name         string
	testName     string
	runtimeImage string
	testScript   string
}

var workflowLanguagePresets = map[string]workflowLanguagePreset{
	workflowPresetGo: {
		name:         "Go 流水线",
		testName:     "Go 测试",
		runtimeImage: "golang:1.26-alpine",
		testScript:   "set -eu\ngo test ./...\n",
	},
	workflowPresetNodeJS: {
		name:         "Node.js 流水线",
		testName:     "Node.js 测试",
		runtimeImage: "node:24-alpine",
		testScript:   "set -eu\nnpm ci\nnpm test\n",
	},
	workflowPresetPython: {
		name:         "Python 流水线",
		testName:     "Python 测试",
		runtimeImage: "python:3.14-alpine",
		testScript:   "set -eu\nif [ -f requirements.txt ]; then\n  python -m pip install -r requirements.txt\nfi\npython -m pytest\n",
	},
}

func applyWorkflowPreset(workflow *model.ReleaseWorkflow, key string) error {
	if key == workflowPresetBlank {
		workflow.Name = "空白流水线"
		workflow.Stages = []model.WorkflowStage{}
		return nil
	}

	preset, ok := workflowLanguagePresets[key]
	if !ok {
		return ErrInvalidWorkflow
	}
	workflow.Name = preset.name
	workflow.Stages = []model.WorkflowStage{
		{
			ID: "test", Name: "测试",
			Tasks: []model.WorkflowNode{{
				ID: "test-command", Type: model.WorkflowNodeShell, Name: preset.testName,
				Config: model.WorkflowNodeConfig{
					Script: preset.testScript, RuntimeImage: preset.runtimeImage,
					WorkingDirectory: ".", TimeoutSeconds: 600,
					EnvironmentVariables: map[string]string{},
				},
			}},
		},
		{
			ID: "build", Name: "构建",
			Tasks: []model.WorkflowNode{{
				ID: "build-artifact", Type: model.WorkflowNodeBuild, Name: "构建制品",
				Config: model.WorkflowNodeConfig{},
			}},
		},
		{
			ID: "deploy", Name: "部署",
			Tasks: []model.WorkflowNode{{
				ID: "deploy-artifact", Type: model.WorkflowNodeDeploy, Name: "部署",
				Config: model.WorkflowNodeConfig{},
			}},
		},
	}
	return nil
}
