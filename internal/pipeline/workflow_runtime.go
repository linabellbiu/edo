package pipeline

import (
	"context"
	"errors"
	"strings"

	"edo/internal/dockerengine"
)

var (
	ErrInvalidWorkflowRuntime     = errors.New("构建语言或版本无效")
	ErrWorkflowRuntimeUnavailable = errors.New("构建运行时暂不可用，请先启用 Docker 构建能力")
	ErrWorkflowRuntimeNotPrepared = errors.New("请先下载并准备所选构建版本")
)

type WorkflowRuntimeVersion struct {
	Language    string `json:"language"`
	Version     string `json:"version"`
	Image       string `json:"image"`
	Recommended bool   `json:"recommended"`
	Installed   bool   `json:"installed"`
}

// WorkflowRuntimeManager 把语言版本管理限定在 EDO 的构建运行时。
// dockerengine.Service 在本地模式下连接当前 Docker，在 Compose 模式下连接独立 DinD。
type WorkflowRuntimeManager interface {
	InspectScriptRuntimeImage(context.Context, string) (dockerengine.ScriptRuntimeImageStatus, error)
	PrepareScriptRuntimeImage(context.Context, string) (dockerengine.ScriptRuntimeImageStatus, error)
}

var workflowRuntimeVersions = map[string][]WorkflowRuntimeVersion{
	workflowPresetGo: {
		{Language: workflowPresetGo, Version: "1.26", Image: "golang:1.26-alpine", Recommended: true},
		{Language: workflowPresetGo, Version: "1.25", Image: "golang:1.25-alpine"},
		{Language: workflowPresetGo, Version: "1.24", Image: "golang:1.24-alpine"},
	},
	workflowPresetNodeJS: {
		{Language: workflowPresetNodeJS, Version: "24", Image: "node:24-alpine", Recommended: true},
		{Language: workflowPresetNodeJS, Version: "26", Image: "node:26-alpine"},
		{Language: workflowPresetNodeJS, Version: "22", Image: "node:22-alpine"},
	},
	workflowPresetPython: {
		{Language: workflowPresetPython, Version: "3.14", Image: "python:3.14-alpine", Recommended: true},
		{Language: workflowPresetPython, Version: "3.13", Image: "python:3.13-alpine"},
		{Language: workflowPresetPython, Version: "3.12", Image: "python:3.12-alpine"},
	},
}

func (s *Service) ConfigureWorkflowRuntimeManager(manager WorkflowRuntimeManager) {
	s.workflowRuntimes = manager
}

func (s *Service) ListWorkflowRuntimeVersions(ctx context.Context, language string) ([]WorkflowRuntimeVersion, error) {
	language = strings.ToLower(strings.TrimSpace(language))
	versions, ok := workflowRuntimeVersions[language]
	if !ok {
		return nil, ErrInvalidWorkflowRuntime
	}
	if s.workflowRuntimes == nil {
		return nil, ErrWorkflowRuntimeUnavailable
	}
	result := make([]WorkflowRuntimeVersion, len(versions))
	copy(result, versions)
	for i := range result {
		status, err := s.workflowRuntimes.InspectScriptRuntimeImage(ctx, result[i].Image)
		if err != nil {
			if s.logger != nil {
				s.logger.Error("检查构建语言版本失败", "operation", "workflow_runtime_inspect", "language", language, "version", result[i].Version, "image", result[i].Image, "err", err)
			}
			return nil, ErrWorkflowRuntimeUnavailable
		}
		result[i].Installed = status.Installed
	}
	return result, nil
}

func (s *Service) PrepareWorkflowRuntimeVersion(ctx context.Context, language, version string) (*WorkflowRuntimeVersion, error) {
	runtime, ok := findWorkflowRuntimeVersion(language, version)
	if !ok {
		return nil, ErrInvalidWorkflowRuntime
	}
	if s.workflowRuntimes == nil {
		return nil, ErrWorkflowRuntimeUnavailable
	}
	status, err := s.workflowRuntimes.PrepareScriptRuntimeImage(ctx, runtime.Image)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("下载构建语言版本失败", "operation", "workflow_runtime_prepare", "language", runtime.Language, "version", runtime.Version, "image", runtime.Image, "err", err)
		}
		return nil, ErrWorkflowRuntimeUnavailable
	}
	runtime.Installed = status.Installed
	if !runtime.Installed {
		return nil, ErrWorkflowRuntimeUnavailable
	}
	return &runtime, nil
}

func (s *Service) requirePreparedWorkflowRuntime(ctx context.Context, language, version string) (WorkflowRuntimeVersion, error) {
	runtime, ok := findWorkflowRuntimeVersion(language, version)
	if !ok {
		return WorkflowRuntimeVersion{}, ErrInvalidWorkflowRuntime
	}
	if s.workflowRuntimes == nil {
		return WorkflowRuntimeVersion{}, ErrWorkflowRuntimeUnavailable
	}
	status, err := s.workflowRuntimes.InspectScriptRuntimeImage(ctx, runtime.Image)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("校验流水线构建版本失败", "operation", "workflow_runtime_validate", "language", runtime.Language, "version", runtime.Version, "image", runtime.Image, "err", err)
		}
		return WorkflowRuntimeVersion{}, ErrWorkflowRuntimeUnavailable
	}
	if !status.Installed {
		return WorkflowRuntimeVersion{}, ErrWorkflowRuntimeNotPrepared
	}
	runtime.Installed = true
	return runtime, nil
}

func findWorkflowRuntimeVersion(language, version string) (WorkflowRuntimeVersion, bool) {
	language = strings.ToLower(strings.TrimSpace(language))
	version = strings.TrimSpace(version)
	for _, runtime := range workflowRuntimeVersions[language] {
		if runtime.Version == version {
			return runtime, true
		}
	}
	return WorkflowRuntimeVersion{}, false
}

func defaultWorkflowRuntime(language string) WorkflowRuntimeVersion {
	for _, runtime := range workflowRuntimeVersions[language] {
		if runtime.Recommended {
			return runtime
		}
	}
	return WorkflowRuntimeVersion{}
}

func workflowRuntimeBuildArg(language string) string {
	switch language {
	case workflowPresetGo:
		return "GO_VERSION"
	case workflowPresetNodeJS:
		return "NODE_VERSION"
	case workflowPresetPython:
		return "PYTHON_VERSION"
	default:
		return ""
	}
}
