package cicd

import (
	"context"
	"strings"

	"edo/internal/configfile"
	"edo/internal/model"
)

// FreezeConfigFiles 按精确 UUID 选择配置，环境归属只用于校验，不会自动关联部署。
func (s *Service) FreezeConfigFiles(ctx context.Context, projectID, applicationID, environmentID, ref string, refs []model.DeploymentConfigFile) ([]model.DeploymentConfigBinding, error) {
	scope, err := scopeFrom(ctx)
	if err != nil || scope.ProjectID != projectID || len(refs) > 32 {
		return nil, s.failure(ctx, "configuration_freeze_scope", ErrScope, ErrScope)
	}
	result := []model.DeploymentConfigBinding{}
	paths := map[string]bool{}
	ids := map[string]bool{}
	for _, reference := range refs {
		if !configfile.ValidFilePath(reference.Path) || paths[reference.Path] || ids[reference.ID] {
			return nil, s.failure(ctx, "configuration_freeze_path", ErrInvalidVariable, ErrInvalidVariable)
		}
		for previous := range paths {
			if strings.HasPrefix(previous, reference.Path+"/") || strings.HasPrefix(reference.Path, previous+"/") {
				return nil, s.failure(ctx, "configuration_freeze_collision", ErrInvalidVariable, ErrInvalidVariable)
			}
		}
		var item model.DeliveryVariable
		if err = s.db.WithContext(ctx).Where(map[string]any{"uuid": reference.ID, "project_id": projectID, "is_active": true, "usage": "deploy"}).First(&item).Error; err != nil {
			return nil, s.failure(ctx, "configuration_freeze_read", err, ErrVariableUnavailable)
		}
		if !configfile.ValidKind(item.Kind) || (item.ApplicationID != "" && item.ApplicationID != applicationID) || (item.EnvironmentID != "" && item.EnvironmentID != environmentID) || !refAllowed(item.ProtectedRefs, ref) {
			return nil, s.failure(ctx, "configuration_freeze_identity", ErrVariableUnavailable, ErrVariableUnavailable)
		}
		paths[reference.Path] = true
		ids[reference.ID] = true
		result = append(result, model.DeploymentConfigBinding{DeliveryVariableBinding: model.DeliveryVariableBinding{Name: item.Name, VariableID: item.ID, Version: item.Version, Kind: item.Kind, IsSecret: item.IsSecret}, Path: reference.Path})
	}
	return result, nil
}
func (s *Service) ResolveConfigFiles(ctx context.Context, projectID, applicationID, environmentID, ref string, bindings []model.DeploymentConfigBinding) (map[string]string, []string, error) {
	files := map[string]string{}
	redactions := []string{}
	for _, binding := range bindings {
		refs, err := s.FreezeConfigFiles(ctx, projectID, applicationID, environmentID, ref, []model.DeploymentConfigFile{{ID: binding.VariableID, Path: binding.Path}})
		if err != nil || len(refs) != 1 || refs[0].Kind != binding.Kind || refs[0].Name != binding.Name {
			return nil, nil, s.failure(ctx, "configuration_resolve_identity", err, ErrVariableUnavailable)
		}
		var revision model.DeliveryVariableRevision
		if err = s.db.WithContext(ctx).Where("project_id = ? AND variable_id = ? AND version = ?", projectID, binding.VariableID, binding.Version).First(&revision).Error; err != nil {
			return nil, nil, s.failure(ctx, "configuration_resolve_revision", err, ErrVariableUnavailable)
		}
		if revision.IsSecret != binding.IsSecret {
			return nil, nil, s.failure(ctx, "configuration_resolve_secret", ErrVariableUnavailable, ErrVariableUnavailable)
		}
		value := revision.Value
		if revision.IsSecret {
			value, err = s.secrets.Decrypt(revision.Ciphertext, []byte(variableAAD(binding.VariableID, binding.Version)))
			if err != nil {
				return nil, nil, s.failure(ctx, "configuration_resolve_decrypt", err, ErrVariableUnavailable)
			}
			if value != "" {
				redactions = append(redactions, value)
				for _, line := range strings.Split(value, "\n") {
					if strings.TrimSpace(line) != "" {
						redactions = append(redactions, line)
					}
				}
			}
		}
		if _, exists := files[binding.Path]; exists {
			return nil, nil, s.failure(ctx, "configuration_resolve_duplicate", ErrVariableConflict, ErrVariableConflict)
		}
		files[binding.Path] = value
	}
	return files, redactions, nil
}

// LatestConfigFiles 保留上次部署选择的 UUID、路径及格式，只更新内容版本。
// 调用方须将返回引用绑定到操作确认，再按该版本执行；不能在确认后静默换成更新版本。
func (s *Service) LatestConfigFiles(ctx context.Context, projectID, applicationID, environmentID, ref string, bindings []model.DeploymentConfigBinding) ([]model.DeploymentConfigBinding, error) {
	refs := make([]model.DeploymentConfigFile, 0, len(bindings))
	for _, binding := range bindings {
		refs = append(refs, model.DeploymentConfigFile{ID: binding.VariableID, Path: binding.Path})
	}
	latest, err := s.FreezeConfigFiles(ctx, projectID, applicationID, environmentID, ref, refs)
	if err != nil {
		return nil, err
	}
	for i := range latest {
		if latest[i].Kind != bindings[i].Kind {
			return nil, s.failure(ctx, "configuration_latest_format", ErrVariableUnavailable, ErrVariableUnavailable)
		}
	}
	return latest, nil
}
