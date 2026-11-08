package pipeline

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strings"
	"time"

	distributionreference "github.com/distribution/reference"
	"github.com/regclient/regclient"
	registryconfig "github.com/regclient/regclient/config"
	"github.com/regclient/regclient/scheme"
	"github.com/regclient/regclient/scheme/reg"
	registryreference "github.com/regclient/regclient/types/ref"
	"gorm.io/gorm"

	"edo/internal/configfile"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

var ErrDeploymentInputs = errors.New("部署输入无效，请检查文件来源、相对路径和部署脚本")

type workflowRegistryInput struct {
	Index      int    `json:"index"`
	RegistryID string `json:"registry_id"`
	ImageRef   string `json:"image_ref"`
	Tag        string `json:"tag"`
}

func sanitizeDeploymentInputs(node *model.WorkflowNode) error {
	c := &node.Config
	if c.DeploymentInputVersion == 0 && len(c.DeploymentInputs) == 0 && c.DeploymentScript == "" {
		return nil
	}
	if node.Type != model.WorkflowNodeDeploy || c.DeploymentInputVersion != 1 || len(c.DeploymentInputs) > 32 || len(c.DeploymentScript) > 256*1024 || strings.ContainsRune(c.DeploymentScript, '\x00') {
		return ErrDeploymentInputs
	}
	files := []model.DeploymentConfigFile{}
	paths, configurations := map[string]bool{}, map[string]bool{}
	for _, input := range c.DeploymentInputs {
		if input.Kind != "registry" {
			if !configfile.ValidPath(input.Path) || paths[input.Path] {
				return ErrDeploymentInputs
			}
			for previous := range paths {
				if strings.HasPrefix(previous, input.Path+"/") || strings.HasPrefix(input.Path, previous+"/") {
					return ErrDeploymentInputs
				}
			}
			paths[input.Path] = true
		}
		switch input.Kind {
		case "configuration":
			if input.ConfigurationID == "" || len(input.ConfigurationID) > 36 || configurations[input.ConfigurationID] || input.RegistryID != "" || input.Image != "" || input.Tag != "" || input.ArtifactNodeID != "" || input.ArtifactBuildPlanID != "" {
				return ErrDeploymentInputs
			}
			configurations[input.ConfigurationID] = true
			files = append(files, model.DeploymentConfigFile{ID: input.ConfigurationID, Path: input.Path})
		case "upstream":
			if (input.ArtifactNodeID == "") == (input.ArtifactBuildPlanID == "") || len(input.ArtifactNodeID) > 64 || len(input.ArtifactBuildPlanID) > 36 || input.RegistryID != "" || input.Image != "" || input.Tag != "" || input.ConfigurationID != "" {
				return ErrDeploymentInputs
			}
		case "registry":
			if input.RegistryID == "" || len(input.RegistryID) > 36 || len(input.Image) > 255 || len(input.Tag) > 128 || input.Path != "" || input.ConfigurationID != "" || input.ArtifactNodeID != "" || input.ArtifactBuildPlanID != "" || !validInputImage(input.Image, input.Tag) {
				return ErrDeploymentInputs
			}
		default:
			return ErrDeploymentInputs
		}
	}
	c.ConfigurationFiles = files
	// 新界面以脚本表达参数，旧快照仍由原执行器解析。
	c.DeploymentParameters = ""
	return nil
}

func validInputImage(image, tag string) bool {
	if image == "" || strings.ContainsAny(image, ":@$") || strings.HasPrefix(image, "/") || path.Clean(image) != image || strings.HasPrefix(image, "../") {
		return false
	}
	parsed, err := distributionreference.ParseNormalizedNamed("registry.invalid/" + image + ":" + tag)
	if err != nil {
		return false
	}
	_, ok := parsed.(distributionreference.NamedTagged)
	return ok
}

func (s *Service) deploymentRegistryClient(registry model.ImageRegistry) (*regclient.RegClient, string, error) {
	u, err := url.Parse(registry.Endpoint)
	if err != nil || u.Host == "" {
		return nil, "", ErrInvalidRegistryEndpoint
	}
	auth, err := s.registryAuth(registry)
	if err != nil {
		return nil, "", err
	}
	host := registryconfig.HostNewName(u.Host)
	if u.Scheme == "http" {
		host.TLS = registryconfig.TLSDisabled
	}
	host.User = auth.Username
	if auth.Username == "" {
		host.Token = auth.Credential
	} else {
		host.Pass = auth.Credential
	}
	client := regclient.New(regclient.WithConfigHost(*host), regclient.WithUserAgent("edo"), regclient.WithRegOpts(reg.WithRetryLimit(1)))
	base := strings.Trim(path.Join(u.Path, registry.Namespace), "/")
	if base == "." {
		base = ""
	}
	return client, strings.TrimSuffix(u.Host+"/"+base, "/"), nil
}

// 实际 Tag 在本次运行创建时解析一次，冻结 Digest 后再交给目标主机拉取。
func (s *Service) freezeDeploymentRegistryInputs(ctx context.Context, db *gorm.DB, node model.WorkflowNode, authorityID string) ([]workflowRegistryInput, error) {
	result := []workflowRegistryInput{}
	for index, input := range node.Config.DeploymentInputs {
		if input.Kind != "registry" {
			continue
		}
		scope, scoped := database.DepartmentScopeFromContext(ctx)
		if !scoped || scope.ProjectID == "" {
			return nil, s.deploymentInputError(node.ID, "deployment_registry_scope", ErrDeploymentInputs)
		}
		if err := s.requireDeliveryPermissionWithDatabase(ctx, db, authorityID, "image_registry.execute"); err != nil {
			return nil, err
		}
		var registry model.ImageRegistry
		if err := db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND is_active = ?", input.RegistryID, scope.ProjectID, true).First(&registry).Error; err != nil {
			return nil, s.deploymentInputError(node.ID, "deployment_registry_load", err)
		}
		client, prefix, err := s.deploymentRegistryClient(registry)
		if err != nil {
			return nil, s.deploymentInputError(node.ID, "deployment_registry_client", err)
		}
		ref, err := registryreference.New(strings.TrimSuffix(prefix, "/") + "/" + input.Image + ":" + input.Tag)
		if err != nil {
			return nil, s.deploymentInputError(node.ID, "deployment_registry_reference", err)
		}
		request, cancel := context.WithTimeout(ctx, 20*time.Second)
		manifest, err := client.ManifestHead(request, ref, regclient.WithManifestRequireDigest())
		cancel()
		if err != nil {
			return nil, s.deploymentInputError(node.ID, "deployment_registry_digest", err)
		}
		ref.Tag, ref.Digest = "", manifest.GetDescriptor().Digest.String()
		if !strings.HasPrefix(ref.Digest, "sha256:") {
			return nil, s.deploymentInputError(node.ID, "deployment_registry_digest", ErrDeploymentInputs)
		}
		result = append(result, workflowRegistryInput{Index: index, RegistryID: registry.ID, ImageRef: ref.CommonName(), Tag: input.Tag})
	}
	return result, nil
}

func (s *Service) deploymentInputError(nodeID, operation string, err error) error {
	s.logger.Error("处理部署输入失败", "operation", operation, "node_id", nodeID, "err", err)
	return ErrDeploymentInputs
}

// 仓库可不支持 catalog；前端仍允许准确输入镜像名，并从该镜像读取实际 Tag。
func (s *Service) DeploymentRegistryOptions(ctx context.Context, registryID, image string) ([]string, error) {
	scope, scoped := database.DepartmentScopeFromContext(ctx)
	if !scoped || scope.ProjectID == "" {
		return nil, s.deploymentInputError("", "deployment_registry_options_scope", ErrDeploymentInputs)
	}
	var registry model.ImageRegistry
	if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND is_active = ?", registryID, scope.ProjectID, true).First(&registry).Error; err != nil {
		return nil, s.deploymentInputError("", "deployment_registry_options_load", err)
	}
	client, prefix, err := s.deploymentRegistryClient(registry)
	if err != nil {
		return nil, s.deploymentInputError("", "deployment_registry_options_client", err)
	}
	request, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if image != "" {
		if !validInputImage(image, "edo-check") {
			return nil, s.deploymentInputError("", "deployment_registry_options_image", ErrDeploymentInputs)
		}
		ref, err := registryreference.New(strings.TrimSuffix(prefix, "/") + "/" + image)
		if err != nil {
			return nil, s.deploymentInputError("", "deployment_registry_options_reference", err)
		}
		result, err := client.TagList(request, ref, scheme.WithTagLimit(200))
		if err != nil {
			return nil, s.deploymentInputError("", "deployment_registry_tags", err)
		}
		return result.GetTags()
	}
	u, _ := url.Parse(registry.Endpoint)
	result, err := client.RepoList(request, u.Host, scheme.WithRepoLimit(200))
	if err != nil {
		return nil, s.deploymentInputError("", "deployment_registry_catalog", err)
	}
	repos, err := result.GetRepos()
	if err != nil {
		return nil, s.deploymentInputError("", "deployment_registry_catalog_parse", err)
	}
	base := strings.Trim(path.Join(u.Path, registry.Namespace), "/")
	if base == "." {
		base = ""
	}
	out := []string{}
	for _, repo := range repos {
		if base == "" {
			out = append(out, repo)
		} else if strings.HasPrefix(repo, base+"/") {
			out = append(out, strings.TrimPrefix(repo, base+"/"))
		}
	}
	return out, nil
}

func hasRegistryDeploymentInput(config model.WorkflowNodeConfig) bool {
	for _, input := range config.DeploymentInputs {
		if input.Kind == "registry" {
			return true
		}
	}
	return false
}

func validDeploymentScriptVariables(values map[string]string) bool {
	if !validScriptEnvironmentVariables(values) || !sshdeploy.ValidEnvironmentVariables(values) {
		return false
	}
	for key := range values {
		if strings.HasPrefix(key, "EDO_") || key == "IMAGE_TAG" {
			return false
		}
	}
	return true
}
