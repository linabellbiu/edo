package pipeline

import (
	"context"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/shellcheck"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

var ErrComposeSource = errors.New("无法读取或解析 docker-compose.yaml，请检查来源、权限和文件内容")

type ComposeServicesInput struct {
	Source        *model.WorkflowComposeSource `json:"source"`
	Content       string                       `json:"content"`
	EnvironmentID string                       `json:"environment_id"`
	Ref           string                       `json:"ref"`
}
type ComposeServicesResult struct {
	Services  []string `json:"services"`
	CommitSHA string   `json:"commit_sha,omitempty"`
}

func validComposeSource(source *model.WorkflowComposeSource) bool {
	if source == nil {
		return true
	}
	switch source.Kind {
	case "repository":
		return source.ArtifactReference == "" && source.ConfigurationID == "" && source.ArtifactNodeID == "" && dockerengine.ValidScriptContainerRelativePath(source.Path) && source.Path != "." && source.ArtifactName == ""
	case "configuration":
		return source.ArtifactReference == "" && source.ConfigurationID != "" && len(source.ConfigurationID) <= 36 && source.Path == "" && source.ArtifactNodeID == "" && source.ArtifactName == ""
	case "upstream":
		return source.ConfigurationID == "" && (source.ArtifactNodeID != "" || source.ArtifactReference != "") && (source.ArtifactReference == "" || upstreamOutputName(source.ArtifactReference) != "") && len(source.ArtifactNodeID) <= 64 && dockerengine.ValidScriptContainerRelativePath(source.Path) && source.Path != "." && (source.ArtifactName == "" || outputNamePattern.MatchString(source.ArtifactName))
	}
	return false
}
func composeServiceNames(content string) ([]string, error) {
	document, err := parseComposeStack(content)
	if err != nil {
		return nil, ErrComposeSource
	}
	services := document["services"].(map[string]any)
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
func (s *Service) PreviewComposeServices(ctx context.Context, applicationID string, input ComposeServicesInput) (*ComposeServicesResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if applicationID == "" {
		if input.Source != nil || len(input.Content) > maximumComposeStackBytes {
			return nil, s.composeSourceError(ctx, "compose_text_preview_validate", "", ErrComposeSource)
		}
		names, err := composeServiceNames(input.Content)
		if err != nil {
			return nil, s.composeSourceError(ctx, "compose_text_preview_parse", "", err)
		}
		return &ComposeServicesResult{Services: names}, nil
	}
	app, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if !validComposeSource(input.Source) || len(input.Content) > maximumComposeStackBytes {
		return nil, s.composeSourceError(ctx, "compose_preview_validate", applicationID, ErrComposeSource)
	}
	content, commit := input.Content, ""
	if input.Source != nil {
		scope, ok := database.DepartmentScopeFromContext(ctx)
		if !ok {
			return nil, s.composeSourceError(ctx, "compose_preview_scope", applicationID, ErrInvalidWorkflow)
		}
		permission := "repository.read"
		if input.Source.Kind == "configuration" {
			permission = "delivery_variable.execute"
		}
		if err = s.requireDeliveryPermission(ctx, scope.UserID, permission); err != nil {
			return nil, err
		}
		ref := input.Ref
		if ref == "" || strings.ContainsAny(ref, "*?[") {
			ref = app.Repository.DefaultBranch
		}
		if !strings.HasPrefix(ref, "refs/") {
			ref = "refs/heads/" + ref
		}
		if input.Source.Kind == "repository" {
			refs, readErr := s.repositories.TestConnection(ctx, app.RepositoryID)
			if readErr != nil {
				return nil, s.composeSourceError(ctx, "compose_preview_refs", applicationID, readErr)
			}
			for _, candidate := range refs.Branches {
				if "refs/heads/"+strings.TrimPrefix(candidate.Name, "refs/heads/") == ref {
					commit = candidate.SHA
					break
				}
			}
			if commit == "" {
				for _, candidate := range refs.Tags {
					if "refs/tags/"+strings.TrimPrefix(candidate.Name, "refs/tags/") == ref {
						commit = candidate.SHA
						break
					}
				}
			}

			if commit == "" {
				return nil, s.composeSourceError(ctx, "compose_preview_ref", applicationID, ErrComposeSource)
			}
		}
		content, err = s.readComposeSource(ctx, *app, input.EnvironmentID, ref, commit, input.Source)
		if err != nil {
			return nil, s.composeSourceError(ctx, "compose_preview_read", applicationID, err)
		}
	}
	names, err := composeServiceNames(content)
	if err != nil {
		return nil, s.composeSourceError(ctx, "compose_preview_parse", applicationID, err)
	}
	return &ComposeServicesResult{Services: names, CommitSHA: commit}, nil
}
func (s *Service) composeSourceError(ctx context.Context, operation, app string, err error) error {
	s.logger.WarnContext(ctx, "读取 Compose 服务失败", "operation", operation, "application_id", app, "err", err)
	return ErrComposeSource
}
func (s *Service) readComposeSource(ctx context.Context, app model.Application, environmentID, ref, commit string, source *model.WorkflowComposeSource) (string, error) {
	if !validComposeSource(source) || source == nil {
		return "", ErrComposeSource
	}
	switch source.Kind {
	case "repository":
		if commit == "" || s.repositories == nil {
			return "", ErrComposeSource
		}
		lease, err := s.repositories.AcquireTaskCheckout(ctx, app.RepositoryID, ref, commit, "compose:"+app.ID+":"+source.Path)
		if err != nil {
			return "", err
		}
		defer lease.Release()
		filePath, err := secureWorkspacePath(lease.Directory, source.Path)
		if err != nil {
			return "", err
		}
		file, err := os.Open(filePath)
		if err != nil {
			return "", err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maximumComposeStackBytes {
			return "", ErrComposeSource
		}
		content, err := io.ReadAll(io.LimitReader(file, maximumComposeStackBytes+1))
		return string(content), err
	case "configuration":
		bindings, err := s.Delivery().FreezeConfigFiles(ctx, app.ProjectID, app.ID, environmentID, ref, []model.DeploymentConfigFile{{ID: source.ConfigurationID, Path: "docker-compose.yaml"}})
		if err != nil {
			return "", err
		}
		if len(bindings) != 1 || bindings[0].IsSecret {
			return "", ErrComposeSource
		}
		values, _, err := s.Delivery().ResolveConfigFiles(ctx, app.ProjectID, app.ID, environmentID, ref, bindings)
		if err != nil {
			return "", err
		}
		content := values["docker-compose.yaml"]
		if len(content) > maximumComposeStackBytes {
			return "", ErrComposeSource
		}
		return content, nil
	}
	return "", ErrComposeSource
}

func normalizeTaskRuntimeServices(node *model.WorkflowNode) error {
	if len(node.Config.RuntimeServices) == 0 {
		return nil
	}
	if node.Type != model.WorkflowNodeDeploy || len(node.Config.RuntimeServices) > 32 || node.Config.InlineDeploymentPlan == nil || node.Config.InlineDeploymentPlan.Kind != model.DeploymentPlanScript {
		return ErrInvalidWorkflow
	}
	names := map[string]bool{}
	for i := range node.Config.RuntimeServices {
		service := &node.Config.RuntimeServices[i]
		service.Name = strings.TrimSpace(service.Name)
		if !composeServicePattern.MatchString(service.Name) || names[service.Name] || strings.TrimSpace(service.RestartScript) == "" || strings.TrimSpace(service.RecreateScript) == "" || len(service.RestartScript) > 256*1024 || len(service.RecreateScript) > 256*1024 {
			return ErrInvalidWorkflow
		}
		if len(shellcheck.Check(service.RestartScript)) > 0 || len(shellcheck.Check(service.RecreateScript)) > 0 {
			return ErrInvalidWorkflow
		}
		names[service.Name] = true
	}
	return nil
}

// runtimeComposeDefinition 从已固定的引用解析最终正文，正文进入运行快照后不再读取分支头。
func (s *Service) runtimeComposeDefinition(ctx context.Context, app model.Application, node model.WorkflowNode, environmentID, ref, commit string, authorities ...string) (string, error) {
	source := node.Config.ComposeSource
	if source == nil {
		return node.Config.InlineDeploymentPlan.ComposeYAML, nil
	}
	if source.Kind == "configuration" {
		scope, ok := database.DepartmentScopeFromContext(ctx)
		if !ok {
			return "", ErrComposeSource
		}
		authority := scope.UserID
		if len(authorities) > 0 {
			authority = authorities[0]
		}
		if err := s.requireDeliveryPermission(ctx, authority, "delivery_variable.execute"); err != nil {
			return "", err
		}
	}
	content, err := s.readComposeSource(ctx, app, environmentID, ref, commit, source)
	if err != nil {
		return "", s.composeSourceError(ctx, "compose_run_source", app.ID, err)
	}
	if _, err = composeServiceNames(content); err != nil {
		return "", s.composeSourceError(ctx, "compose_run_parse", app.ID, err)
	}
	return content, nil
}
