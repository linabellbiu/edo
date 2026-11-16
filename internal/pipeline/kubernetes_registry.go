package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"reflect"
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
	initial, err := s.readKubernetesRegistryState(ctx, s.db, prepared, false)
	if err != nil {
		return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_prepare", nodeID, err)
	}
	candidate := initial.node
	frozen := initial.snapshot.RegistryInputs[nodeID]
	if len(frozen) == 0 {
		input := candidate.Config.DeploymentInputs[0]
		if !initial.snapshot.DeferredKubernetesRegistryImages || initial.snapshot.ImageTagIncludesName || input.Tag != prepared.node.Config.DeploymentInputs[0].Tag {
			return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_deferred", nodeID, ErrPipelineExecutionConfig)
		}
		local := *prepared
		local.run, local.node, local.snapshot = initial.run, initial.node, *initial.snapshot
		tag, err := s.resolveKubernetesRegistryTag(ctx, &local, input.Tag)
		if err != nil {
			return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_tag", nodeID, err)
		}
		candidate.Config.DeploymentInputs = append([]model.DeploymentInput(nil), candidate.Config.DeploymentInputs...)
		candidate.Config.DeploymentInputs[0].Tag = tag
		// 网络等待只属于当前任务，不持有父运行的行锁或事务连接。
		image, err := s.freezeDeploymentRegistryInput(ctx, nodeID, 0, candidate.Config.DeploymentInputs[0], initial.registry)
		if err != nil {
			return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_resolve", nodeID, err)
		}
		frozen = []workflowRegistryInput{image}
	}
	if _, err := kubernetesRegistryArtifact(candidate, frozen, initial.registry); err != nil {
		return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_image", nodeID, err)
	}

	var resolved *kubernetesRegistryState
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := s.readKubernetesRegistryState(ctx, tx, prepared, true)
		if err != nil {
			return err
		}
		stored := current.snapshot.RegistryInputs[nodeID]
		if len(stored) == 0 {
			// 仅允许其他节点更新自己的快照；当前来源、授权和仓库必须仍与远端查询一致。
			if !sameKubernetesRegistrySource(initial, current, nodeID) {
				return ErrPipelineExecutionConfig
			}
			encoded, err := freezeKubernetesRegistrySnapshot(current.run.WorkflowSnapshot, nodeID, candidate.Config.DeploymentInputs, frozen)
			if err != nil {
				return err
			}
			result := tx.Model(&model.PipelineRun{}).
				Where("uuid = ? AND workflow_snapshot = ? AND status = ? AND stage <> ?", current.run.ID, current.run.WorkflowSnapshot, model.PipelineRunRunning, stoppingStage).
				Update("workflow_snapshot", encoded)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrPipelineExecutionConfig
			}
			current.run.WorkflowSnapshot = encoded
			current.snapshot, err = parseWorkflowSnapshot(&current.run)
			if err != nil {
				return err
			}
			current.node, stored = candidate, frozen
		}
		// 同节点重投已有固定摘要时复用先完成的冻结，不能用迟到的 Tag 查询覆盖。
		if _, err := kubernetesRegistryArtifact(current.node, stored, current.registry); err != nil {
			return err
		}
		resolved = current
		return nil
	})
	if err != nil {
		return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_freeze", nodeID, err)
	}
	image, err := kubernetesRegistryArtifact(resolved.node, resolved.snapshot.RegistryInputs[nodeID], resolved.registry)
	if err != nil {
		return s.kubernetesRegistryFailure(ctx, "kubernetes_registry_image", nodeID, err)
	}
	prepared.node, prepared.snapshot, prepared.registry, prepared.artifact = resolved.node, *resolved.snapshot, resolved.registry, image
	prepared.run.WorkflowSnapshot = resolved.run.WorkflowSnapshot
	prepared.run.ArtifactID = ""
	return nil
}

type kubernetesRegistryState struct {
	run      model.PipelineRun
	snapshot *workflowSnapshot
	node     model.WorkflowNode
	registry model.ImageRegistry
}

func (s *Service) readKubernetesRegistryState(ctx context.Context, db *gorm.DB, prepared *executionContext, lock bool) (*kubernetesRegistryState, error) {
	state := &kubernetesRegistryState{}
	query := db.WithContext(ctx)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Where("uuid = ? AND project_id = ? AND department_id = ? AND application_id = ?", prepared.run.ID, prepared.run.ProjectID, prepared.run.DepartmentID, prepared.run.ApplicationID).First(&state.run).Error; err != nil {
		return nil, err
	}
	if state.run.Status != model.PipelineRunRunning || state.run.Stage == stoppingStage {
		return nil, ErrPipelineExecutionConfig
	}
	nodeID := prepared.node.ID
	if prepared.run.NodeExecution {
		var node model.PipelineNodeRun
		query := db.WithContext(ctx)
		if lock {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.Where("pipeline_run_id = ? AND node_id = ? AND job_id = ? AND status = ?", state.run.ID, nodeID, prepared.run.ExecutionJobID, model.PipelineRunRunning).First(&node).Error; err != nil {
			s.logger.WarnContext(ctx, "核对 Kubernetes 镜像冻结执行任务失败", "operation", "kubernetes_registry_node_state", "pipeline_run_id", state.run.ID, "node_id", nodeID, "job_id", prepared.run.ExecutionJobID, "err", err)
			return nil, ErrPipelineExecutionConfig
		}
	} else if state.run.ExecutionJobID != prepared.run.ExecutionJobID || state.run.CurrentNodeID != nodeID {
		return nil, ErrPipelineExecutionConfig
	}
	var err error
	state.snapshot, err = parseWorkflowSnapshot(&state.run)
	if err != nil {
		return nil, err
	}
	current, found := workflowFindNode(state.snapshot.Source, state.snapshot.Stages, nodeID)
	plan, target := state.snapshot.DeploymentPlans[nodeID], state.snapshot.DeploymentTargets[nodeID]
	if !found || current.Type != model.WorkflowNodeDeploy || !model.UsesRegistryDeploymentImage(current.Config) || len(current.Config.DeploymentInputs) != 1 || plan.ID != prepared.deploymentPlan.ID || plan.Kind != model.DeploymentPlanKubernetes || target.ID != prepared.target.ID || target.Platform != prepared.target.Platform || target.RuntimeID != prepared.target.RuntimeID || target.Namespace != prepared.target.Namespace || target.WorkloadName != prepared.target.WorkloadName || target.ContainerName != prepared.target.ContainerName || state.snapshot.AuthorityID != prepared.snapshot.AuthorityID {
		return nil, ErrPipelineExecutionConfig
	}
	input, original := current.Config.DeploymentInputs[0], prepared.node.Config.DeploymentInputs[0]
	if input.RegistryID == "" || input.RegistryID != current.Config.DeploymentRegistryID || input.RegistryID != original.RegistryID || input.Image != original.Image {
		return nil, ErrPipelineExecutionConfig
	}
	// 事务内只共享所需依赖，不复制服务的互斥锁和运行中状态。
	service := &Service{db: db, secrets: s.secrets, logger: s.logger, deliveryAccess: s.deliveryAccess}
	if err := service.requirePipelineExecutionPermission(ctx, state.run, *state.snapshot); err != nil {
		return nil, err
	}
	if err := service.requireDeliveryPermission(ctx, state.snapshot.AuthorityID, "image_registry.execute"); err != nil {
		return nil, err
	}
	if len(state.snapshot.VariableBindings[nodeID]) > 0 {
		if err := service.requireDeliveryPermission(ctx, state.snapshot.AuthorityID, "delivery_variable.execute"); err != nil {
			return nil, err
		}
	}
	if err := db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND is_active = ?", input.RegistryID, state.run.ProjectID, true).First(&state.registry).Error; err != nil {
		return nil, err
	}
	state.node = current
	return state, nil
}

func sameKubernetesRegistrySource(before, after *kubernetesRegistryState, nodeID string) bool {
	return reflect.DeepEqual(before.node, after.node) && reflect.DeepEqual(before.registry, after.registry) &&
		reflect.DeepEqual(before.snapshot.DeploymentPlans[nodeID], after.snapshot.DeploymentPlans[nodeID]) &&
		reflect.DeepEqual(before.snapshot.DeploymentTargets[nodeID], after.snapshot.DeploymentTargets[nodeID]) &&
		reflect.DeepEqual(before.snapshot.VariableBindings[nodeID], after.snapshot.VariableBindings[nodeID]) &&
		before.snapshot.DeferredKubernetesRegistryImages == after.snapshot.DeferredKubernetesRegistryImages &&
		before.snapshot.ImageTagIncludesName == after.snapshot.ImageTagIncludesName &&
		before.run.Ref == after.run.Ref && before.run.CommitSHA == after.run.CommitSHA &&
		before.run.Trigger == after.run.Trigger && before.run.RetryOfID == after.run.RetryOfID
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
