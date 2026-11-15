package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"edo/internal/artifact"
	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/model"
	composetemplate "github.com/compose-spec/compose-go/v2/template"
	"github.com/distribution/reference"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrKubernetesRegistryTag = fmt.Errorf("%w：镜像 Tag 变量无法确定，请填写固定 Tag 或检查前序构建与变量配置", ErrDeploymentInputs)

// loadKubernetesRegistryImage 在部署节点实际开始时固定镜像，不依赖目标主机的 Docker。
func (s *Service) loadKubernetesRegistryImage(ctx context.Context, prepared *executionContext) error {
	if prepared == nil {
		return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_context", "", ErrPipelineExecutionConfig)
	}
	nodeID := prepared.node.ID
	if prepared.deploymentPlan.Kind != model.DeploymentPlanKubernetes || prepared.target.Platform != model.DeploymentKubernetes || !model.UsesRegistryDeploymentImage(prepared.node.Config) || len(prepared.node.Config.DeploymentInputs) != 1 {
		return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_shape", nodeID, ErrPipelineExecutionConfig)
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if scope.ProjectID != "" && scope.ProjectID != prepared.run.ProjectID || scope.DepartmentID != "" && scope.DepartmentID != prepared.run.DepartmentID {
		return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_scope", nodeID, ErrPipelineExecutionConfig)
	}
	if scope.ProjectID == "" {
		ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: prepared.snapshot.AuthorityID, ProjectID: prepared.run.ProjectID, DepartmentID: prepared.run.DepartmentID})
	}
	var resolvedSnapshot *workflowSnapshot
	var resolvedNode model.WorkflowNode
	var resolvedRegistry model.ImageRegistry
	var encodedSnapshot string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid = ? AND project_id = ? AND department_id = ? AND application_id = ?", prepared.run.ID, prepared.run.ProjectID, prepared.run.DepartmentID, prepared.run.ApplicationID).First(&run).Error; err != nil {
			return err
		}
		if run.Status != model.PipelineRunRunning {
			return ErrPipelineExecutionConfig
		}
		snapshot, err := parseWorkflowSnapshot(&run)
		if err != nil {
			return err
		}
		current, found := workflowFindNode(snapshot.Source, snapshot.Stages, nodeID)
		plan, target := snapshot.DeploymentPlans[nodeID], snapshot.DeploymentTargets[nodeID]
		if !found || current.Type != model.WorkflowNodeDeploy || !model.UsesRegistryDeploymentImage(current.Config) || len(current.Config.DeploymentInputs) != 1 || plan.ID != prepared.deploymentPlan.ID || plan.Kind != model.DeploymentPlanKubernetes || target.ID != prepared.target.ID || target.RuntimeID != prepared.target.RuntimeID || target.Namespace != prepared.target.Namespace || target.WorkloadName != prepared.target.WorkloadName || target.ContainerName != prepared.target.ContainerName || snapshot.AuthorityID != prepared.snapshot.AuthorityID {
			return ErrPipelineExecutionConfig
		}
		input, original := current.Config.DeploymentInputs[0], prepared.node.Config.DeploymentInputs[0]
		if input.RegistryID == "" || input.RegistryID != current.Config.DeploymentRegistryID || input.RegistryID != original.RegistryID || input.Image != original.Image {
			return ErrPipelineExecutionConfig
		}
		txService := *s
		txService.db = tx
		if err := txService.requirePipelineExecutionPermission(ctx, run, *snapshot); err != nil {
			return err
		}
		if err := txService.requireDeliveryPermission(ctx, snapshot.AuthorityID, "image_registry.execute"); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Where("uuid = ? AND project_id = ? AND is_active = ?", input.RegistryID, run.ProjectID, true).First(&resolvedRegistry).Error; err != nil {
			return err
		}
		frozen := snapshot.RegistryInputs[nodeID]
		encodedSnapshot = run.WorkflowSnapshot
		if len(frozen) == 0 {
			if !snapshot.DeferredKubernetesRegistryImages || snapshot.ImageTagIncludesName || input.Tag != original.Tag {
				return ErrPipelineExecutionConfig
			}
			local := *prepared
			local.run, local.node, local.snapshot = run, current, *snapshot
			tag, err := txService.resolveKubernetesRegistryTag(ctx, &local, input.Tag)
			if err != nil {
				return err
			}
			current.Config.DeploymentInputs = append([]model.DeploymentInput(nil), current.Config.DeploymentInputs...)
			current.Config.DeploymentInputs[0].Tag = tag
			frozen, err = txService.freezeDeploymentRegistryInputs(ctx, tx, current, snapshot.AuthorityID)
			if err != nil {
				return err
			}
			if _, err = kubernetesRegistryArtifact(current, frozen, resolvedRegistry); err != nil {
				return err
			}
			encodedSnapshot, err = freezeKubernetesRegistrySnapshot(run.WorkflowSnapshot, nodeID, current.Config.DeploymentInputs, frozen)
			if err != nil {
				return err
			}
			result := tx.Model(&model.PipelineRun{}).Where("uuid = ? AND workflow_snapshot = ?", run.ID, run.WorkflowSnapshot).Update("workflow_snapshot", encodedSnapshot)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrPipelineExecutionConfig
			}
			run.WorkflowSnapshot = encodedSnapshot
			snapshot, err = parseWorkflowSnapshot(&run)
			if err != nil {
				return err
			}
		}
		if _, err = kubernetesRegistryArtifact(current, frozen, resolvedRegistry); err != nil {
			return err
		}
		resolvedSnapshot, resolvedNode = snapshot, current
		return nil
	})
	if err != nil {
		return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_freeze", nodeID, err)
	}
	image, err := kubernetesRegistryArtifact(resolvedNode, resolvedSnapshot.RegistryInputs[nodeID], resolvedRegistry)
	if err != nil {
		return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_image", nodeID, err)
	}
	prepared.node, prepared.snapshot, prepared.registry, prepared.artifact = resolvedNode, *resolvedSnapshot, resolvedRegistry, image
	prepared.run.WorkflowSnapshot = encodedSnapshot
	prepared.run.ArtifactID = ""
	return nil
}

func (s *Service) resolveKubernetesRegistryTag(ctx context.Context, prepared *executionContext, template string) (string, error) {
	bindings := prepared.snapshot.VariableBindings[prepared.node.ID]
	if len(bindings) > 0 {
		if err := s.requireDeliveryPermission(ctx, prepared.snapshot.AuthorityID, "delivery_variable.execute"); err != nil {
			return "", err
		}
	}
	values, files, redactions, err := s.Delivery().ResolveVariables(ctx, prepared.run.ProjectID, prepared.run.Ref, bindings)
	if err != nil || len(files) > 0 {
		return "", ErrDeploymentInputs
	}
	local := *prepared
	local.variables = values
	// 复用上下文变量；镜像 Tag 的前序校验在下面单独批量完成，不改变旧部署行为。
	local.node.Config.DeploymentTagReference, local.node.Config.DeploymentScript, local.node.Config.DeploymentParameters = "", "", ""
	local.node.Config.Compose = nil
	local.deploymentPlan.Script, local.deploymentPlan.ComposeYAML, local.deploymentPlan.DockerConfig.DeploymentScript = "", "", ""
	if err = s.addDeploymentPipelineVariables(ctx, &local); err != nil {
		return "", err
	}
	delete(values, "EDO_IMAGE_TAG")
	delete(values, "IMAGE_TAG")
	references := composetemplate.ExtractVariables(map[string]any{"tag": template}, nil)
	_, usesTag := references["EDO_IMAGE_TAG"]
	_, usesLegacyTag := references["IMAGE_TAG"]
	if usesTag || usesLegacyTag {
		tag, err := s.kubernetesPriorImageTag(ctx, prepared)
		if err != nil {
			return "", err
		}
		values["EDO_IMAGE_TAG"], values["IMAGE_TAG"] = tag, tag
	}
	missing := false
	expanded, err := composetemplate.SubstituteWithOptions(template, func(name string) (string, bool) {
		value, exists := values[name]
		missing = missing || !exists
		return value, exists
	}, composetemplate.WithoutLogging)
	if err != nil || missing || !pipelineImageTagPattern.MatchString(expanded) {
		return "", ErrKubernetesRegistryTag
	}
	for _, secret := range redactions {
		if secret != "" && strings.Contains(expanded, secret) {
			return "", ErrKubernetesRegistryTag
		}
	}
	return expanded, nil
}

func (s *Service) kubernetesPriorImageTag(ctx context.Context, prepared *executionContext) (string, error) {
	prior := []string{}
	for _, stage := range prepared.snapshot.Stages {
		for _, node := range stage.Tasks {
			if node.ID == prepared.node.ID {
				goto collected
			}
		}
		for _, node := range stage.Tasks {
			if node.Type == model.WorkflowNodeBuild && prepared.snapshot.BuildPlans[node.ID].Kind == model.BuildPlanDockerfile {
				prior = append(prior, node.ID)
			}
		}
	}
collected:
	if len(prior) == 0 {
		return "", ErrKubernetesRegistryTag
	}
	var images []model.Artifact
	sourceRuns := []string{prepared.run.ID}
	if prepared.run.Trigger == "retry" && prepared.run.RetryOfID != "" {
		sourceRuns = append(sourceRuns, prepared.run.RetryOfID)
	}
	err := s.db.WithContext(ctx).Model(&model.Artifact{}).Select("artifacts.name").
		Joins("JOIN pipeline_node_runs ON pipeline_node_runs.artifact_id = artifacts.uuid").
		Joins("JOIN build_runs ON build_runs.uuid = artifacts.build_run_id AND build_runs.workflow_node_id = pipeline_node_runs.node_id AND build_runs.pipeline_run_id = artifacts.pipeline_run_id").
		Where("pipeline_node_runs.pipeline_run_id = ? AND pipeline_node_runs.project_id = ? AND pipeline_node_runs.node_id IN ? AND pipeline_node_runs.status IN ?", prepared.run.ID, prepared.run.ProjectID, prior, []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunSkipped}).
		Where("artifacts.pipeline_run_id IN ? AND artifacts.project_id = ? AND artifacts.application_id = ? AND artifacts.kind = ? AND artifacts.storage_kind = ? AND artifacts.status = ?", sourceRuns, prepared.run.ProjectID, prepared.run.ApplicationID, model.ArtifactKindOCIImage, model.ArtifactStorageKindRegistry, model.ArtifactStatusAvailable).
		Where("build_runs.project_id = ? AND build_runs.application_id = ? AND build_runs.commit_sha = ? AND build_runs.status = ?", prepared.run.ProjectID, prepared.run.ApplicationID, prepared.run.CommitSHA, model.BuildRunStatusSucceeded).Find(&images).Error
	if err != nil {
		return "", err
	}
	tags := map[string]bool{}
	for _, image := range images {
		named, err := reference.ParseNormalizedNamed(image.Name)
		if err != nil {
			return "", ErrKubernetesRegistryTag
		}
		tagged, ok := named.(reference.NamedTagged)
		if !ok {
			return "", ErrKubernetesRegistryTag
		}
		tags[tagged.Tag()] = true
	}
	if len(tags) == 1 {
		for tag := range tags {
			return tag, nil
		}
	}
	return "", ErrKubernetesRegistryTag
}

func kubernetesRegistryArtifact(node model.WorkflowNode, inputs []workflowRegistryInput, registry model.ImageRegistry) (model.Artifact, error) {
	if len(inputs) != 1 || len(node.Config.DeploymentInputs) != 1 {
		return model.Artifact{}, ErrPipelineExecutionConfig
	}
	frozen, input := inputs[0], node.Config.DeploymentInputs[0]
	if frozen.Index != 0 || frozen.RegistryID != registry.ID || frozen.RegistryID != node.Config.DeploymentRegistryID || frozen.RegistryID != input.RegistryID || frozen.Tag != input.Tag || !validInputImage(input.Image, input.Tag) {
		return model.Artifact{}, ErrPipelineExecutionConfig
	}
	named, err := reference.ParseNormalizedNamed(frozen.ImageRef)
	if err != nil {
		return model.Artifact{}, ErrPipelineExecutionConfig
	}
	pinned, ok := named.(reference.Canonical)
	if !ok || !artifact.RegistryImageMatches(frozen.ImageRef, pinned.Digest().String(), registry.Endpoint, registry.Namespace) {
		return model.Artifact{}, ErrPipelineExecutionConfig
	}
	endpoint, err := url.Parse(registry.Endpoint)
	if err != nil || reference.Path(named) != strings.Trim(path.Join(endpoint.Path, registry.Namespace, input.Image), "/") {
		return model.Artifact{}, ErrPipelineExecutionConfig
	}
	display, err := reference.WithTag(reference.TrimNamed(named), frozen.Tag)
	if err != nil {
		return model.Artifact{}, ErrPipelineExecutionConfig
	}
	return model.Artifact{Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, StorageKind: model.ArtifactStorageKindRegistry, ImageRegistryID: registry.ID, ImageRef: frozen.ImageRef, Digest: pinned.Digest().String(), Name: display.String()}, nil
}

// 只改当前节点的 Tag 与冻结镜像，保留其他节点和未知的快照字段。
func freezeKubernetesRegistrySnapshot(raw, nodeID string, inputs []model.DeploymentInput, frozen []workflowRegistryInput) (string, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return "", err
	}
	var stages []map[string]json.RawMessage
	if err := json.Unmarshal(document["stages"], &stages); err != nil {
		return "", err
	}
	found := 0
	for _, stage := range stages {
		var tasks []map[string]json.RawMessage
		if err := json.Unmarshal(stage["tasks"], &tasks); err != nil {
			return "", err
		}
		for _, task := range tasks {
			var id string
			if json.Unmarshal(task["id"], &id) != nil || id != nodeID {
				continue
			}
			found++
			var config map[string]json.RawMessage
			if err := json.Unmarshal(task["config"], &config); err != nil {
				return "", err
			}
			config["deployment_inputs"], _ = json.Marshal(inputs)
			task["config"], _ = json.Marshal(config)
		}
		stage["tasks"], _ = json.Marshal(tasks)
	}
	if found != 1 {
		return "", ErrPipelineExecutionConfig
	}
	var registryInputs map[string]json.RawMessage
	if data := document["registry_inputs"]; len(data) > 0 {
		if err := json.Unmarshal(data, &registryInputs); err != nil {
			return "", err
		}
	}
	if registryInputs == nil {
		registryInputs = map[string]json.RawMessage{}
	}
	registryInputs[nodeID], _ = json.Marshal(frozen)
	document["registry_inputs"], _ = json.Marshal(registryInputs)
	document["stages"], _ = json.Marshal(stages)
	encoded, err := json.Marshal(document)
	return string(encoded), err
}

func (s *Service) kubernetesRegistryFailure(ctx context.Context, operation, nodeID string, cause error) error {
	s.logger.WarnContext(ctx, "固定 Kubernetes 部署镜像失败", "operation", operation, "node_id", nodeID, "err", cause)
	if errors.Is(cause, ErrPipelineExecutionConfig) {
		return ErrPipelineExecutionConfig
	}
	if errors.Is(cause, cicd.ErrExecutionAuthorization) {
		return cicd.ErrExecutionAuthorization
	}
	if errors.Is(cause, ErrKubernetesRegistryTag) {
		return ErrKubernetesRegistryTag
	}
	return ErrDeploymentInputs
}
