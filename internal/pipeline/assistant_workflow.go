package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 助手只组合已有方案和内置模板，不接收临时脚本、环境变量或主机命令。
type AssistantWorkflowInput struct {
	ApplicationID     string                    `json:"application_id,omitempty"`
	ApplicationName   string                    `json:"application_name,omitempty"`
	RepositoryID      string                    `json:"repository_id,omitempty"`
	Name              string                    `json:"name"`
	Preset            string                    `json:"preset"`
	RuntimeVersion    string                    `json:"runtime_version,omitempty"`
	Branch            string                    `json:"branch"`
	Events            []string                  `json:"events"`
	BuildPlanID       string                    `json:"build_plan_id,omitempty"`
	DeploymentPlanIDs []string                  `json:"deployment_plan_ids,omitempty"`
	Approval          bool                      `json:"approval"`
	NewBuild          *AssistantBuildInput      `json:"new_build,omitempty"`
	NewDeployment     *AssistantDeploymentInput `json:"new_deployment,omitempty"`
}

type AssistantBuildInput struct {
	Name       string `json:"name"`
	Dockerfile string `json:"dockerfile"`
	Context    string `json:"context"`
	RegistryID string `json:"registry_id,omitempty"`
}

type AssistantDeploymentInput struct {
	Name          string                    `json:"name"`
	Kind          model.DeploymentPlanKind  `json:"kind"`
	EnvironmentID string                    `json:"environment_id,omitempty"`
	ClusterID     string                    `json:"cluster_id,omitempty"`
	Namespace     string                    `json:"namespace,omitempty"`
	Workload      string                    `json:"workload,omitempty"`
	Container     string                    `json:"container,omitempty"`
	Ports         []model.DockerPortMapping `json:"ports,omitempty"`
}

type AssistantWorkflowPlan struct {
	Fingerprint     string          `json:"-"`
	ApplicationID   string          `json:"application_id,omitempty"`
	ApplicationName string          `json:"application_name"`
	WorkflowID      string          `json:"workflow_id,omitempty"`
	Name            string          `json:"name"`
	Steps           []string        `json:"steps"`
	Resources       []string        `json:"resources"`
	Issues          []WorkflowIssue `json:"issues"`
}

var errAssistantPreview = errors.New("助手草稿校验回滚")

// PreviewAssistantWorkflow 使用和创建相同的业务校验，所有试写都在事务内回滚。
func (s *Service) PreviewAssistantWorkflow(ctx context.Context, actor string, input AssistantWorkflowInput) (*AssistantWorkflowPlan, error) {
	return s.assistantWorkflow(ctx, actor, input, "", true)
}

func (s *Service) CreateAssistantWorkflowExpected(ctx context.Context, actor string, input AssistantWorkflowInput, fingerprint string) (*AssistantWorkflowPlan, error) {
	if fingerprint == "" {
		return nil, ErrRetrySnapshotChanged
	}
	return s.assistantWorkflow(ctx, actor, input, fingerprint, false)
}

func (s *Service) assistantWorkflow(ctx context.Context, actor string, input AssistantWorkflowInput, expected string, preview bool) (*AssistantWorkflowPlan, error) {
	if scope, ok := database.DepartmentScopeFromContext(ctx); !ok || scope.ProjectID == "" || scope.DepartmentID == "" {
		return nil, ErrInvalidWorkflow
	}
	if input.Name == "" || input.Branch == "" || len(input.Events) == 0 || len(input.Events) > 4 || len(input.DeploymentPlanIDs) > 5 || (input.BuildPlanID != "" && input.NewBuild != nil) || (input.ApplicationID != "" && (input.ApplicationName != "" || input.RepositoryID != "")) {
		return nil, ErrInvalidWorkflow
	}
	if input.NewDeployment != nil && len(input.DeploymentPlanIDs) >= 5 {
		return nil, ErrInvalidWorkflow
	}
	if input.Preset != "blank" && ((input.BuildPlanID == "" && input.NewBuild == nil) || (len(input.DeploymentPlanIDs) == 0 && input.NewDeployment == nil)) {
		return nil, ErrPipelineIncomplete
	}
	if input.Preset == "blank" && (input.NewBuild != nil || input.NewDeployment != nil || input.BuildPlanID != "" || len(input.DeploymentPlanIDs) > 0) {
		return nil, ErrInvalidWorkflow
	}
	var result *AssistantWorkflowPlan
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := s.assistantTransaction(tx)
		var application *model.Application
		facts := []any{input}
		if input.ApplicationID != "" {
			var err error
			application, err = service.FindApplication(ctx, input.ApplicationID)
			if err != nil {
				return err
			}
			if !application.IsActive {
				return ErrInvalidApplication
			}
			facts = append(facts, []any{application.ID, application.Name, application.RepositoryID, application.IsActive})
			input.RepositoryID = application.RepositoryID
		}
		var repo model.GitRepository
		if input.RepositoryID == "" {
			return ErrInvalidApplication
		}
		if err := tx.First(&repo, &model.GitRepository{ID: input.RepositoryID}).Error; err != nil {
			return err
		}
		facts = append(facts, repo)
		for _, id := range input.DeploymentPlanIDs {
			var plan model.DeploymentPlan
			if id == "" {
				return ErrInvalidDeploymentPlan
			}
			if err := tx.First(&plan, &model.DeploymentPlan{ID: id}).Error; err != nil {
				return err
			}
			var target model.DeploymentTarget
			if plan.DeploymentTargetID == "" || !plan.IsActive {
				return ErrInvalidDeploymentPlan
			}
			if err := tx.First(&target, &model.DeploymentTarget{ID: plan.DeploymentTargetID}).Error; err != nil {
				return err
			}
			facts = append(facts, plan, target)
			bindings, err := assistantTargetFacts(tx, &target)
			if err != nil {
				return err
			}
			facts = append(facts, bindings)
		}
		if input.BuildPlanID != "" {
			var plan model.BuildPlan
			if err := tx.First(&plan, &model.BuildPlan{ID: input.BuildPlanID}).Error; err != nil {
				return err
			}
			if !plan.IsActive {
				return ErrInvalidBuildPlan
			}
			facts = append(facts, plan)
			if plan.ImageRegistryID != "" {
				stamp, err := assistantResourceFact(tx, &model.ImageRegistry{ID: plan.ImageRegistryID})
				if err != nil {
					return err
				}
				facts = append(facts, stamp)
			}
		}
		if input.NewBuild != nil && input.NewBuild.RegistryID != "" {
			var registry model.ImageRegistry
			if err := tx.First(&registry, &model.ImageRegistry{ID: input.NewBuild.RegistryID}).Error; err != nil {
				return err
			}
			facts = append(facts, registry)
		}
		if input.NewDeployment != nil {
			if id := input.NewDeployment.EnvironmentID; id != "" {
				var environment model.Environment
				if err := tx.First(&environment, &model.Environment{ID: id}).Error; err != nil {
					return err
				}
				var hosts []model.EnvironmentHost
				if err := tx.Where("environment_id = ?", id).Order("host_id ASC").Find(&hosts).Error; err != nil {
					return err
				}
				facts = append(facts, environment, hosts)
				for _, binding := range hosts {
					stamp, err := assistantResourceFact(tx, &model.Host{ID: binding.HostID})
					if err != nil {
						return err
					}
					facts = append(facts, stamp)
					var capabilities []model.HostCapability
					if err := tx.Where("host_id = ?", binding.HostID).Order("kind ASC").Find(&capabilities).Error; err != nil {
						return err
					}
					facts = append(facts, capabilities)
					for _, capability := range capabilities {
						if capability.Kind == model.HostCapabilityDocker && capability.RuntimeID != "" && !dockerengine.IsLocalEndpointID(capability.RuntimeID) {
							endpoint, err := assistantResourceFact(tx, &model.DockerEndpoint{ID: capability.RuntimeID})
							if err != nil {
								return err
							}
							facts = append(facts, endpoint)
						}
					}
				}
			}
			if id := input.NewDeployment.ClusterID; id != "" {
				var cluster model.KubernetesCluster
				if err := tx.First(&cluster, &model.KubernetesCluster{ID: id}).Error; err != nil {
					return err
				}
				facts = append(facts, cluster)
			}
		}
		data, err := json.Marshal(facts)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		fingerprint := hex.EncodeToString(digest[:])
		if expected != "" && fingerprint != expected {
			return ErrRetrySnapshotChanged
		}
		if application == nil {
			application, err = service.CreateApplication(ctx, actor, ApplicationInput{Name: input.ApplicationName, RepositoryID: input.RepositoryID, EmptyWorkflow: true})
			if err != nil {
				return err
			}
		}
		buildID := input.BuildPlanID
		if input.NewBuild != nil {
			value := input.NewBuild
			plan, err := service.CreateBuildPlan(ctx, actor, BuildPlanInput{Name: value.Name, Kind: model.BuildPlanDockerfile, DockerfilePath: value.Dockerfile, ContextPath: value.Context, ImageRegistryID: value.RegistryID})
			if err != nil {
				return err
			}
			buildID = plan.ID
		}
		deployIDs := append([]string(nil), input.DeploymentPlanIDs...)
		if input.NewDeployment != nil {
			value := input.NewDeployment
			if value.Kind == model.DeploymentPlanDocker && (value.EnvironmentID == "" || value.ClusterID != "") {
				return ErrInvalidDeploymentPlan
			}
			platform := model.DeploymentDocker
			if value.Kind == model.DeploymentPlanKubernetes {
				platform = model.DeploymentKubernetes
			} else if value.Kind != model.DeploymentPlanDocker {
				return ErrInvalidDeploymentPlan
			}
			plan, err := service.CreateDeploymentPlan(ctx, actor, DeploymentPlanInput{Name: value.Name, Kind: value.Kind, DeploymentTarget: &deployment.TargetInput{Name: value.Name, Platform: platform, EnvironmentID: value.EnvironmentID, RuntimeID: value.ClusterID, Namespace: value.Namespace, WorkloadName: value.Workload, ContainerName: value.Container, RolloutTimeout: 300}, DockerConfig: model.DockerContainerConfig{PortMappings: value.Ports}})
			if err != nil {
				return err
			}
			deployIDs = append(deployIDs, plan.ID)
		}
		preset, ok := findWorkflowPreset(input.Preset)
		if !ok {
			return ErrInvalidWorkflow
		}
		runtime := WorkflowRuntimeVersion{}
		if preset.language != "" {
			runtime, err = service.requirePreparedWorkflowRuntime(ctx, preset.language, input.RuntimeVersion)
			if err != nil {
				return err
			}
		}
		stages := buildWorkflowPresetStages(preset, runtime)
		if preset.Key == workflowPresetBlank {
			stages = []model.WorkflowStage{}
		}
		resolved := []model.WorkflowStage{}
		steps := []string{}
		for _, stage := range stages {
			if len(stage.Tasks) == 1 && stage.Tasks[0].Type == model.WorkflowNodeBuild {
				stage.Tasks[0].Config.BuildPlanID = buildID
			}
			if len(stage.Tasks) == 1 && stage.Tasks[0].Type == model.WorkflowNodeDeploy {
				if input.Approval {
					resolved = append(resolved, model.WorkflowStage{ID: "approval", Name: "审核", Tasks: []model.WorkflowNode{{ID: "approval", Type: model.WorkflowNodeApproval, Name: "发布审核"}}})
					steps = append(steps, "发布审核")
				}
				for i, id := range deployIDs {
					node := stage.Tasks[0]
					node.ID = "deploy-" + id
					node.Config.DeploymentPlanID = id
					resolved = append(resolved, model.WorkflowStage{ID: node.ID, Name: stage.Name, Tasks: []model.WorkflowNode{node}})
					steps = append(steps, stage.Name)
					if i > 4 {
						return ErrInvalidWorkflow
					}
				}
				continue
			}
			resolved = append(resolved, stage)
			steps = append(steps, stage.Name)
		}
		created, err := service.CreateApplicationWorkflow(ctx, application.ID, actor, WorkflowCreateInput{Name: input.Name, PresetKey: "blank"})
		if err != nil {
			return err
		}
		workflow := created.Workflow
		source := workflow.Source
		source.Config.Branch = input.Branch
		source.Config.Events = input.Events
		saved, err := service.SaveApplicationWorkflow(ctx, application.ID, workflow.ID, actor, WorkflowInput{SchemaVersion: model.WorkflowSchemaVersion, Name: input.Name, Revision: workflow.Revision, Activate: false, Source: source, Stages: resolved})
		if err != nil {
			return err
		}
		resources, err := assistantWorkflowDescriptions(tx, buildID, deployIDs)
		if err != nil {
			return err
		}
		resources = append([]string{"代码仓库：" + repo.Name}, resources...)
		result = &AssistantWorkflowPlan{Fingerprint: fingerprint, ApplicationName: application.Name, Name: input.Name, Steps: steps, Resources: resources, Issues: saved.Issues}
		if !preview {
			result.ApplicationID = application.ID
			result.WorkflowID = workflow.ID
			return nil
		}
		return errAssistantPreview
	})
	if err != nil && !errors.Is(err, errAssistantPreview) {
		if s.logger != nil {
			s.logger.ErrorContext(ctx, "助手流水线规划失败", "operation", "assistant_workflow_plan", "application_id", input.ApplicationID, "err", err)
		}
		return nil, err
	}
	return result, nil
}

func assistantWorkflowDescriptions(db *gorm.DB, buildID string, deployIDs []string) ([]string, error) {
	result := []string{}
	if buildID != "" {
		var plan model.BuildPlan
		if err := db.First(&plan, &model.BuildPlan{ID: buildID}).Error; err != nil {
			return nil, err
		}
		result = append(result, fmt.Sprintf("构建方案：%s；类型：%s；Dockerfile：%s；上下文：%s", plan.Name, plan.Kind, plan.DockerfilePath, plan.ContextPath))
	}
	for _, id := range deployIDs {
		var plan model.DeploymentPlan
		if err := db.First(&plan, &model.DeploymentPlan{ID: id}).Error; err != nil {
			return nil, err
		}
		var target model.DeploymentTarget
		if err := db.First(&target, &model.DeploymentTarget{ID: plan.DeploymentTargetID}).Error; err != nil {
			return nil, err
		}
		location := target.Name
		if target.EnvironmentID != "" {
			var env model.Environment
			if err := db.First(&env, &model.Environment{ID: target.EnvironmentID}).Error; err != nil {
				return nil, err
			}
			location = env.Name
		}
		if target.HostID != "" {
			var host model.Host
			if err := db.First(&host, &model.Host{ID: target.HostID}).Error; err != nil {
				return nil, err
			}
			location += "，主机：" + host.Name
		}
		if target.Platform == model.DeploymentKubernetes {
			var cluster model.KubernetesCluster
			if err := db.First(&cluster, &model.KubernetesCluster{ID: target.RuntimeID}).Error; err != nil {
				return nil, err
			}
			location += "，集群：" + cluster.Name
		}
		name := target.WorkloadName
		if name == "" && target.Platform == model.DeploymentDocker {
			name = "运行时自动生成"
		}
		result = append(result, fmt.Sprintf("部署方案：%s；类型：%s；环境：%s；命名空间：%s；工作负载：%s；容器：%s", plan.Name, plan.Kind, location, target.Namespace, name, target.ContainerName))
	}
	return result, nil
}

type AssistantRunInput struct {
	ApplicationID string `json:"application_id"`
	WorkflowID    string `json:"workflow_id"`
	Ref           string `json:"ref,omitempty"`
	Commit        string `json:"commit,omitempty"`
	ArtifactID    string `json:"artifact_id,omitempty"`
}

func (s *Service) PreviewAssistantRun(ctx context.Context, actor string, input AssistantRunInput) (string, error) {
	run, err := s.assistantRun(ctx, actor, input, "", true)
	if err != nil {
		return "", err
	}
	return s.assistantRunFingerprint(ctx, run)
}

func (s *Service) ExecuteAssistantRunExpected(ctx context.Context, actor string, input AssistantRunInput, expected string) (*model.PipelineRun, error) {
	if expected == "" {
		return nil, ErrRetrySnapshotChanged
	}
	return s.assistantRun(ctx, actor, input, expected, false)
}

func (s *Service) assistantRun(ctx context.Context, actor string, input AssistantRunInput, expected string, preview bool) (*model.PipelineRun, error) {
	application, err := s.FindApplication(ctx, input.ApplicationID)
	if err != nil {
		return nil, err
	}
	workflow, err := s.FindApplicationWorkflow(ctx, application.ID, input.WorkflowID)
	if err != nil {
		return nil, err
	}
	if !application.IsActive || !workflow.IsActive || !workflowHasManualReleaseSource(workflow) {
		return nil, ErrWorkflowNotActive
	}
	var selection *manualArtifactSelection
	if input.ArtifactID != "" {
		if input.Ref != "" || input.Commit != "" {
			return nil, ErrManualSelectionInvalid
		}
		selection, err = s.validateManualArtifactSelection(ctx, application, workflow, input.ArtifactID)
		if err != nil {
			return nil, err
		}
		input.Ref, input.Commit = selection.build.Ref, selection.build.CommitSHA
	} else {
		input.Ref, input.Commit, err = s.validateManualSelection(ctx, application, input.Ref, input.Commit)
		if err != nil {
			return nil, err
		}
	}
	source := manualWorkflowSource(workflow, input.Ref, "")
	if source == nil {
		return nil, ErrInvalidWorkflow
	}
	now := time.Now().UTC()
	run, err := s.newResolvedWorkflowRun(ctx, application, workflow, *source, "manual", input.Ref, input.Commit, actor, "助手确认后发起流水线", now)
	if err != nil {
		return nil, err
	}
	if selection != nil {
		run.CurrentNodeID = selection.resumeNode.ID
		run.Status = model.PipelineRunReady
		run.Stage = "task_succeeded"
		run.ArtifactID = selection.artifact.ID
		run.Image = selection.artifact.ImageRef
	}
	components, err := pipelineRunRepositories(application, run.ID, run.Ref, run.CommitSHA, now)
	if err != nil {
		return nil, err
	}
	if selection != nil {
		for i := range components {
			components[i].Status = model.PipelineRunRepositoryReady
		}
	}
	run.Repositories = components
	if preview {
		return run, nil
	}
	fingerprint, err := s.assistantRunFingerprint(ctx, run)
	if err != nil {
		return nil, err
	}
	if fingerprint != expected {
		return nil, ErrRetrySnapshotChanged
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked model.ReleaseWorkflow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, &model.ReleaseWorkflow{ID: run.WorkflowID, ApplicationID: run.ApplicationID}).Error; err != nil {
			return err
		}
		stamp, err := s.assistantTransaction(tx).assistantRunFingerprint(ctx, run)
		if err != nil {
			return err
		}
		if stamp != expected {
			return ErrRetrySnapshotChanged
		}
		run.Repositories = nil
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		return tx.Create(&components).Error
	}); err != nil {
		return nil, err
	}
	advanced, err := s.AdvanceRun(ctx, run.ID, actor, "")
	if err != nil {
		return run, err
	}
	return advanced, nil
}

func (s *Service) assistantTransaction(tx *gorm.DB) *Service {
	service := s.deliveryTransaction(tx)
	service.repositories = s.repositories.MetadataTransaction(tx)
	if s.docker != nil {
		service.docker = s.docker.WithTransaction(tx)
		if _, ok := s.workflowRuntimes.(*dockerengine.Service); ok {
			service.workflowRuntimes = service.docker
		}
	}
	return service
}

// 连接凭据仅参与摘要计算，不进入确认记录或工具输出。
func assistantResourceFact(db *gorm.DB, entity any) (map[string]any, error) {
	row := map[string]any{}
	if err := db.Model(entity).Where(entity).Take(&row).Error; err != nil {
		return nil, err
	}
	delete(row, "updated_at")
	delete(row, "created_at")
	return row, nil
}

func assistantTargetFacts(db *gorm.DB, target *model.DeploymentTarget) ([]any, error) {
	facts := []any{}
	entities := []any{}
	if target.HostID != "" {
		entities = append(entities, &model.Host{ID: target.HostID})
	}
	if target.EnvironmentID != "" {
		entities = append(entities, &model.Environment{ID: target.EnvironmentID})
	}
	if target.RuntimeID != "" {
		if target.Platform == model.DeploymentDocker && !dockerengine.IsLocalEndpointID(target.RuntimeID) {
			entities = append(entities, &model.DockerEndpoint{ID: target.RuntimeID})
		}
		if target.Platform == model.DeploymentKubernetes {
			entities = append(entities, &model.KubernetesCluster{ID: target.RuntimeID})
		}
	}
	for _, entity := range entities {
		stamp, err := assistantResourceFact(db, entity)
		if err != nil {
			return nil, err
		}
		facts = append(facts, stamp)
	}
	return facts, nil
}

func (s *Service) assistantWorkflowFacts(ctx context.Context, workflow *model.ReleaseWorkflow) ([]any, error) {
	db := s.db.WithContext(ctx)
	facts := []any{workflow}
	for _, entity := range []any{&model.Application{ID: workflow.ApplicationID}} {
		stamp, err := assistantResourceFact(db, entity)
		if err != nil {
			return nil, err
		}
		// 代码检查状态不属于用户确认的发布配置。
		for _, field := range []string{"last_checked_at", "sync_status", "sync_message"} {
			delete(stamp, field)
		}
		facts = append(facts, stamp)
	}
	for _, stage := range workflow.Stages {
		for _, node := range stage.Tasks {
			if id := node.Config.BuildPlanID; id != "" {
				var plan model.BuildPlan
				if err := db.First(&plan, &model.BuildPlan{ID: id}).Error; err != nil {
					return nil, err
				}
				facts = append(facts, plan)
				if plan.ImageRegistryID != "" {
					stamp, err := assistantResourceFact(db, &model.ImageRegistry{ID: plan.ImageRegistryID})
					if err != nil {
						return nil, err
					}
					facts = append(facts, stamp)
				}
			}
			if id := node.Config.DeploymentPlanID; id != "" {
				var plan model.DeploymentPlan
				if err := db.First(&plan, &model.DeploymentPlan{ID: id}).Error; err != nil {
					return nil, err
				}
				var target model.DeploymentTarget
				if err := db.First(&target, &model.DeploymentTarget{ID: plan.DeploymentTargetID}).Error; err != nil {
					return nil, err
				}
				bindings, err := assistantTargetFacts(db, &target)
				if err != nil {
					return nil, err
				}
				facts = append(facts, plan, target, bindings)
			}
		}
	}
	return facts, nil
}

func (s *Service) assistantRunFingerprint(ctx context.Context, run *model.PipelineRun) (string, error) {
	base, err := retryRunFingerprint(run)
	if err != nil {
		return "", err
	}
	workflow, err := s.FindApplicationWorkflow(ctx, run.ApplicationID, run.WorkflowID)
	if err != nil {
		return "", err
	}
	facts, err := s.assistantWorkflowFacts(ctx, workflow)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal([]any{base, facts})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) PreviewAssistantActivation(ctx context.Context, appID, workflowID string) (string, error) {
	return s.assistantActivation(ctx, appID, workflowID, "", "", true)
}

func (s *Service) ActivateAssistantWorkflowExpected(ctx context.Context, appID, workflowID, actor, expected string) error {
	if expected == "" {
		return ErrRetrySnapshotChanged
	}
	_, err := s.assistantActivation(ctx, appID, workflowID, actor, expected, false)
	return err
}

func (s *Service) assistantActivation(ctx context.Context, appID, workflowID, actor, expected string, preview bool) (string, error) {
	fingerprint := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lock model.ReleaseWorkflow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lock, &model.ReleaseWorkflow{ID: workflowID, ApplicationID: appID}).Error; err != nil {
			return err
		}
		service := s.assistantTransaction(tx)
		result, err := service.GetApplicationWorkflow(ctx, appID, workflowID)
		if err != nil {
			return err
		}
		w := result.Workflow
		if w.IsActive {
			return ErrRetrySnapshotChanged
		}
		input := WorkflowInput{SchemaVersion: w.SchemaVersion, Name: w.Name, Revision: w.Revision, Activate: true, Source: w.Source, Stages: w.Stages}
		validated, err := service.ValidateApplicationWorkflow(ctx, appID, workflowID, input)
		if err != nil {
			return err
		}
		if !validated.Valid {
			return ErrPipelineIncomplete
		}
		facts, err := service.assistantWorkflowFacts(ctx, w)
		if err != nil {
			return err
		}
		data, err := json.Marshal(facts)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		fingerprint = hex.EncodeToString(sum[:])
		if preview {
			return nil
		}
		if expected != fingerprint {
			return ErrRetrySnapshotChanged
		}
		_, err = service.SaveApplicationWorkflow(ctx, appID, workflowID, actor, input)
		return err
	})
	return fingerprint, err
}
