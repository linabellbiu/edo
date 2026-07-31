package pipeline

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"zrt/internal/deployment"
	"zrt/internal/dockerengine"
	"zrt/internal/model"
)

const (
	initialDeliveryActor               = "system"
	initialBuildPlanName               = "快速开始 Dockerfile 构建"
	initialDeploymentPlanName          = "快速开始 本地 Docker 部署"
	initialDeploymentTargetName        = "快速开始 本地 Docker"
	initialWorkflowTemplateName        = "快速开始 Dockerfile 流水线"
	initialDockerContainerName         = "zrt-quickstart"
	initialBuildPlanDescription        = "默认读取仓库根目录的 Dockerfile，在当前 ZRT 构建运行时生成本地 OCI 镜像。"
	initialDeploymentPlanDescription   = "把上游镜像部署到当前 ZRT 的本地 Docker。默认容器名为 zrt-quickstart，多应用使用前请复制方案并修改容器名。"
	initialDeploymentTargetDescription = "当前 ZRT 构建运行时中的本地 Docker，仅用于快速开始。"
)

type InitialDeliverySettings struct {
	Created               bool
	LocalDockerDeployment bool
	BuildPlanID           string
	DeploymentPlanID      string
	WorkflowTemplateID    string
}

// EnsureInitialDeliverySettings 只在交付资源从未创建过时写入一次快速开始基线。
// 构建和部署方案使用软删除，因此管理员删除默认资源后，后续启动也不会静默恢复。
func (s *Service) EnsureInitialDeliverySettings(ctx context.Context) (InitialDeliverySettings, error) {
	if s == nil || s.db == nil {
		return InitialDeliverySettings{}, fmt.Errorf("初始化默认交付设置失败：流水线服务不可用")
	}
	exists, err := deliveryResourcesExist(s.db.WithContext(ctx))
	if err != nil {
		return InitialDeliverySettings{}, fmt.Errorf("初始化默认交付设置失败: %w", err)
	}
	if exists {
		return InitialDeliverySettings{}, nil
	}
	localDockerReady, err := s.localDockerReady(ctx)
	if err != nil {
		return InitialDeliverySettings{}, err
	}

	result := InitialDeliverySettings{LocalDockerDeployment: localDockerReady}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		exists, err := deliveryResourcesExist(tx)
		if err != nil {
			return err
		}
		if exists {
			result.LocalDockerDeployment = false
			return nil
		}

		seed := &Service{
			db:           tx,
			repositories: s.repositories,
			secrets:      s.secrets,
			docker:       s.docker,
			scriptRunner: s.scriptRunner,
			artifacts:    s.artifacts,
			deployments:  s.deployments,
			logger:       s.logger,
		}
		if s.docker != nil {
			seed.docker = s.docker.WithTransaction(tx)
		}
		if s.deployments != nil {
			seed.deployments = s.deployments.WithTransaction(tx)
		}

		buildPlan, err := seed.CreateBuildPlan(ctx, initialDeliveryActor, BuildPlanInput{
			Name: initialBuildPlanName, Kind: model.BuildPlanDockerfile,
			Description:    initialBuildPlanDescription,
			DockerfilePath: "Dockerfile", ContextPath: ".", TimeoutSeconds: 1800,
		})
		if err != nil {
			return fmt.Errorf("创建默认构建方案失败: %w", err)
		}
		result.BuildPlanID = buildPlan.ID

		var deploymentPlan *model.DeploymentPlan
		if localDockerReady {
			deploymentPlan, err = seed.CreateDeploymentPlan(ctx, initialDeliveryActor, DeploymentPlanInput{
				Name: initialDeploymentPlanName, Kind: model.DeploymentPlanDocker,
				Description: initialDeploymentPlanDescription,
				ServiceName: initialDockerContainerName,
				DeploymentTarget: &deployment.TargetInput{
					Name: initialDeploymentTargetName, Description: initialDeploymentTargetDescription,
					Platform: model.DeploymentDocker, RuntimeID: dockerengine.LocalEndpointID,
					WorkloadName: initialDockerContainerName, RolloutTimeout: 300,
				},
			})
			if err != nil {
				return fmt.Errorf("创建默认部署方案失败: %w", err)
			}
			result.DeploymentPlanID = deploymentPlan.ID
		}

		stages := []model.WorkflowStage{{
			ID: "build", Name: "构建",
			Tasks: []model.WorkflowNode{{
				ID: "build-image", Type: model.WorkflowNodeBuild, Name: "构建镜像",
				Config: model.WorkflowNodeConfig{BuildPlanID: buildPlan.ID},
			}},
		}}
		description := "手动选择分支或 Tag 后，使用仓库根目录 Dockerfile 构建镜像。"
		if deploymentPlan != nil {
			stages = append(stages, model.WorkflowStage{
				ID: "deploy", Name: "部署",
				Tasks: []model.WorkflowNode{{
					ID: "deploy-local-docker", Type: model.WorkflowNodeDeploy, Name: "部署到本地 Docker",
					Config: model.WorkflowNodeConfig{DeploymentPlanID: deploymentPlan.ID},
				}},
			})
			description = "手动选择分支或 Tag 后，使用仓库根目录 Dockerfile 构建镜像并部署到本地 Docker。"
		}
		template, err := seed.CreateWorkflowTemplate(ctx, initialDeliveryActor, WorkflowTemplateInput{
			WorkflowInput: WorkflowInput{
				SchemaVersion: model.WorkflowSchemaVersion, Name: initialWorkflowTemplateName, Activate: true,
				Source: model.WorkflowNode{
					ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源",
					Config: model.WorkflowNodeConfig{Branch: "*", Events: []string{"manual"}},
				},
				Stages: stages,
			},
			Description: description,
		})
		if err != nil {
			return fmt.Errorf("创建默认流水线方案失败: %w", err)
		}
		result.WorkflowTemplateID = template.WorkflowTemplate.ID
		result.Created = true
		return nil
	})
	if err != nil {
		// 多个实例首次同时启动时，唯一约束只允许一个事务写入默认资源。
		// 失败方确认另一方已经完成初始化后即可继续，不把竞争误报为启动故障。
		exists, checkErr := deliveryResourcesExist(s.db.WithContext(ctx))
		if checkErr == nil && exists {
			return InitialDeliverySettings{}, nil
		}
		return InitialDeliverySettings{}, fmt.Errorf("初始化默认交付设置失败: %w", err)
	}
	return result, nil
}

func (s *Service) localDockerReady(ctx context.Context) (bool, error) {
	if s.docker == nil || s.deployments == nil {
		return false, nil
	}
	endpoint, err := s.docker.Find(ctx, dockerengine.LocalEndpointID)
	if err != nil {
		return false, fmt.Errorf("检查本地 Docker 默认交付能力失败: %w", err)
	}
	return endpoint != nil && endpoint.IsActive, nil
}

func deliveryResourcesExist(tx *gorm.DB) (bool, error) {
	resources := []struct {
		name  string
		value any
	}{
		{name: "构建方案", value: &model.BuildPlan{}},
		{name: "部署方案", value: &model.DeploymentPlan{}},
		{name: "部署目标", value: &model.DeploymentTarget{}},
		{name: "流水线方案", value: &model.ReleaseWorkflowTemplate{}},
	}
	for _, resource := range resources {
		var count int64
		if err := tx.Unscoped().Model(resource.value).Count(&count).Error; err != nil {
			return false, fmt.Errorf("检查已有%s失败: %w", resource.name, err)
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}
