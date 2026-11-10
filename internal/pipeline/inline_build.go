package pipeline

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/shellcheck"
	"github.com/google/uuid"
)

var outputNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,31}$`)

func inlineBuildIdentity(workflowID, nodeID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("edo.workflow.build:"+workflowID+":"+nodeID)).String()
}

func normalizeInlineBuild(node *model.WorkflowNode) error {
	if node.Config.InlineBuild == nil && len(node.Config.BuildOutputs) == 0 {
		return nil
	}
	fail := func() error {
		slog.Warn("流水线内置构建配置无效", "operation", "workflow_inline_build_validate", "node_id", node.ID)
		return fmt.Errorf("%w：请检查构建脚本、镜像仓库、Dockerfile、Context 和暂存构建物", ErrInvalidWorkflow)
	}
	if node.Type != model.WorkflowNodeBuild || node.Config.InlineBuild == nil || node.Config.BuildPlanID != "" || len(node.Config.BuildOutputs) > 16 {
		return fail()
	}
	p := node.Config.InlineBuild
	platform, platformErr := workflowSelectedBuildPlatform(node.Config, *p)
	if p.Kind == model.BuildPlanDockerfile && platformErr != nil {
		slog.Warn("流水线构建平台无效", "operation", "workflow_inline_build_platform", "node_id", node.ID, "err", platformErr)
		return platformErr
	}
	if p.Kind == model.BuildPlanDockerfile && (!dockerengine.ValidScriptContainerRelativePath(p.ContextPath) || !dockerengine.ValidScriptContainerRelativePath(p.DockerfilePath) || len(node.Config.BuildOutputs) > 0) {
		return fail()
	}
	// 校验实际执行值，但保留内联方案自身的默认阶段，不把任务覆盖值写回模板。
	n, err := normalizeBuildPlanInput(BuildPlanInput{Name: node.Name, Kind: p.Kind, Script: p.Script, DockerfilePath: p.DockerfilePath, ContextPath: p.ContextPath, WorkingDirectory: p.WorkingDirectory, ArtifactPath: p.ArtifactPath, RuntimeImage: p.RuntimeImage, ImageRegistryID: p.ImageRegistryID, TargetStage: workflowBuildTarget(node.Config, *p), Platform: platform, Pull: &p.Pull, CacheEnabled: &p.CacheEnabled, BuildArgs: p.BuildArgs, EnvironmentVariables: p.EnvironmentVariables, TimeoutSeconds: p.TimeoutSeconds})
	if err != nil {
		if errors.Is(err, ErrInvalidBuildTarget) {
			slog.Warn("流水线构建目标阶段无效", "operation", "workflow_inline_build_validate", "node_id", node.ID, "err", err)
			return fmt.Errorf("%w：%s", ErrInvalidWorkflow, ErrInvalidBuildTarget)
		}
		return fail()
	}
	if p.Kind == model.BuildPlanScript && len(shellcheck.Check(n.Script)) > 0 {
		return fail()
	}
	if p.Kind == model.BuildPlanDockerfile {
		n.TargetStage = strings.TrimSpace(p.TargetStage)
		n.Platform = strings.TrimSpace(p.Platform)
		if n.Platform == "" {
			n.Platform = model.DefaultBuildPlatform
		}
	}
	names := map[string]bool{}
	for i := range node.Config.BuildOutputs {
		o := &node.Config.BuildOutputs[i]
		o.Name = strings.TrimSpace(o.Name)
		if !outputNamePattern.MatchString(o.Name) || names[o.Name] || strings.HasPrefix(o.Name, "EDO_") || o.Name == "IMAGE_TAG" || len(o.Paths) == 0 || len(o.Paths) > 32 {
			return fail()
		}
		if _, reserved := reservedScriptEnvironmentNames[o.Name]; reserved {
			return fail()
		}
		names[o.Name] = true
		paths := map[string]bool{}
		for j, p := range o.Paths {
			o.Paths[j] = strings.TrimSpace(p)
			clean := strings.TrimPrefix(o.Paths[j], "./")
			for prior := range paths {
				if clean == prior || strings.HasPrefix(clean, prior+"/") || strings.HasPrefix(prior, clean+"/") {
					return fail()
				}
			}
			paths[clean] = true
			if !dockerengine.ValidScriptContainerRelativePath(o.Paths[j]) || o.Paths[j] == "." {
				return fail()
			}
		}
	}
	*p = model.BuildPlan{Name: node.Name, Kind: n.Kind, ConfigVersion: 1, Script: n.Script, DockerfilePath: n.DockerfilePath, ContextPath: n.ContextPath, WorkingDirectory: n.WorkingDirectory, ArtifactPath: n.ArtifactPath, RuntimeImage: n.RuntimeImage, ImageRegistryID: n.ImageRegistryID, TargetStage: n.TargetStage, Platform: n.Platform, Pull: *n.Pull, CacheEnabled: *n.CacheEnabled, BuildArgs: n.BuildArgs, EnvironmentVariables: n.EnvironmentVariables, TimeoutSeconds: n.TimeoutSeconds, IsActive: true}
	return nil
}

// 当前任务的构建身份也用于已有制品选择，不能要求流水线内置配置拥有方案库记录。
func inlineBuildPlansForWorkflow(workflow *model.ReleaseWorkflow) map[string]model.BuildPlan {
	result := map[string]model.BuildPlan{}
	if workflow == nil {
		return result
	}
	for _, node := range workflowTasks(workflow.Stages) {
		if node.Type != model.WorkflowNodeBuild || node.Config.InlineBuild == nil {
			continue
		}
		plan := *node.Config.InlineBuild
		plan.ID = inlineBuildIdentity(workflow.ID, node.ID)
		plan.ProjectID, plan.DepartmentID = workflow.ProjectID, workflow.DepartmentID
		if len(node.Config.BuildOutputs) > 0 {
			plan.ArtifactPath = ".edo-build-outputs"
		}
		result[plan.ID] = plan
	}
	return result
}
