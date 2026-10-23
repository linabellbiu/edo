package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/model"
)

var (
	ErrInvalidDeliveryDefault     = errors.New("默认发布方案无效，请选择已启用且包含一个构建和一个部署任务的流水线及匹配的部署方案")
	ErrDeliveryDefaultConflict    = errors.New("默认发布方案已被修改，请刷新后重试")
	ErrDeliveryDefaultUnavailable = errors.New("默认发布方案已停用或删除，请重新选择")
)

const builtinDeliveryScript = "# 请替换为该应用已审核的部署脚本，再启用流水线。\nprintf '%s\\n' '请先配置主机部署脚本' >&2\nexit 1\n"

var deliveryKinds = []model.DeploymentPlanKind{model.DeploymentPlanDocker, model.DeploymentPlanCompose, model.DeploymentPlanKubernetes, model.DeploymentPlanScript}

// BuiltinDeliveryDefinition 与现有执行器共用模型，不另建发布通道。
type BuiltinDeliveryDefinition struct {
	Kind       model.DeploymentPlanKind `json:"kind"`
	Name       string                   `json:"name"`
	Source     model.WorkflowNode       `json:"source"`
	Stages     []model.WorkflowStage    `json:"stages"`
	Deployment model.DeploymentPlan     `json:"deployment"`
}

type DeliveryDefaultView struct {
	model.DeliveryDefault
	Builtin             BuiltinDeliveryDefinition `json:"builtin"`
	WorkflowAvailable   bool                      `json:"workflow_available"`
	DeploymentAvailable bool                      `json:"deployment_available"`
}

type DeliveryDefaultInput struct {
	WorkflowTemplateID string `json:"workflow_template_id"`
	DeploymentPlanID   string `json:"deployment_plan_id"`
	ExpectedVersion    uint64 `json:"expected_version"`
}

type DeliveryWorkflowInput struct {
	Kind        model.DeploymentPlanKind
	Name        string
	BuildPlanID string
	// 空值使用当前默认；builtin 显式使用内置定义。
	WorkflowTemplateID     string
	DeploymentPlanID       string
	Target                 deployment.TargetInput
	ExpectedDefaultVersion uint64
}

func validDeliveryKind(kind model.DeploymentPlanKind) bool {
	for _, candidate := range deliveryKinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func BuiltinDelivery(kind model.DeploymentPlanKind) (BuiltinDeliveryDefinition, error) {
	if !validDeliveryKind(kind) {
		return BuiltinDeliveryDefinition{}, ErrInvalidDeliveryDefault
	}
	name := map[model.DeploymentPlanKind]string{model.DeploymentPlanDocker: "Docker 容器", model.DeploymentPlanCompose: "Docker Compose", model.DeploymentPlanKubernetes: "Kubernetes", model.DeploymentPlanScript: "主机脚本"}[kind]
	plan := model.DeploymentPlan{Kind: kind, Name: "默认 " + name + " 部署方案", TimeoutSeconds: 600, IsActive: true, DockerConfig: model.DockerContainerConfig{}}
	if kind == model.DeploymentPlanCompose {
		plan.ComposeYAML = "services:\n  app:\n    image: ${EDO_IMAGE}\n    restart: unless-stopped\n"
		plan.ServiceName = "app"
	}
	if kind == model.DeploymentPlanScript {
		plan.Script = builtinDeliveryScript
	}
	return BuiltinDeliveryDefinition{Kind: kind, Name: "默认 " + name + " 流水线", Deployment: plan,
		Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Branch: "*", Events: []string{"manual"}}},
		Stages: []model.WorkflowStage{
			{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{{ID: "build", Type: model.WorkflowNodeBuild, Name: "构建制品"}}},
			{ID: "deploy", Name: "部署", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy, Name: name + " 部署"}}},
		}}, nil
}

func (s *Service) deliveryScope(ctx context.Context) (database.DepartmentScope, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.ProjectID == "" || scope.DepartmentID == "" {
		return scope, database.ErrProjectScope
	}
	return scope, nil
}

func (s *Service) GetDeliveryDefaults(ctx context.Context) ([]DeliveryDefaultView, error) {
	scope, err := s.deliveryScope(ctx)
	if err != nil {
		return nil, err
	}
	var rows []model.DeliveryDefault
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]DeliveryDefaultView, 0, len(deliveryKinds))
	for _, kind := range deliveryKinds {
		builtin, _ := BuiltinDelivery(kind)
		row := model.DeliveryDefault{ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Kind: kind}
		for _, stored := range rows {
			if stored.Kind == kind {
				row = stored
				break
			}
		}
		view := DeliveryDefaultView{DeliveryDefault: row, Builtin: builtin, WorkflowAvailable: true, DeploymentAvailable: true}
		if row.WorkflowTemplateID != "" {
			_, err := s.deliveryTemplate(ctx, row.WorkflowTemplateID)
			view.WorkflowAvailable = err == nil
			if err != nil && !errors.Is(err, ErrDeliveryDefaultUnavailable) && !errors.Is(err, ErrInvalidDeliveryDefault) {
				return nil, err
			}
		}
		if row.DeploymentPlanID != "" {
			_, err := s.deliveryPlan(ctx, kind, row.DeploymentPlanID)
			view.DeploymentAvailable = err == nil
			if err != nil && !errors.Is(err, ErrDeliveryDefaultUnavailable) && !errors.Is(err, ErrInvalidDeliveryDefault) {
				return nil, err
			}
		}
		result = append(result, view)
	}
	return result, nil
}

func (s *Service) deliveryTemplate(ctx context.Context, id string) (*model.ReleaseWorkflowTemplate, error) {
	var item model.ReleaseWorkflowTemplate
	if err := s.db.WithContext(ctx).First(&item, "uuid = ? AND is_active = ?", id, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeliveryDefaultUnavailable
		}
		return nil, err
	}
	build, deploy := 0, 0
	for _, node := range workflowTasks(item.Stages) {
		if node.Type == model.WorkflowNodeBuild {
			build++
		}
		if node.Type == model.WorkflowNodeDeploy {
			deploy++
		}
	}
	if build != 1 || deploy != 1 {
		return nil, ErrInvalidDeliveryDefault
	}
	return &item, nil
}

func (s *Service) deliveryPlan(ctx context.Context, kind model.DeploymentPlanKind, id string) (*model.DeploymentPlan, error) {
	var item model.DeploymentPlan
	if err := s.db.WithContext(ctx).First(&item, "uuid = ? AND is_active = ?", id, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeliveryDefaultUnavailable
		}
		return nil, err
	}
	if item.Kind != kind {
		return nil, ErrInvalidDeliveryDefault
	}
	return &item, nil
}

func (s *Service) SetDeliveryDefault(ctx context.Context, actorID string, kind model.DeploymentPlanKind, input DeliveryDefaultInput) error {
	scope, err := s.deliveryScope(ctx)
	if err != nil {
		return err
	}
	if !validDeliveryKind(kind) || len(input.WorkflowTemplateID) > 36 || len(input.DeploymentPlanID) > 36 {
		return ErrInvalidDeliveryDefault
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := s.deliveryTransaction(tx)
		// 先锁定项目，使切换默认和创建应用配置使用同一默认版本。
		var project model.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&project, "uuid = ?", scope.ProjectID).Error; err != nil {
			return err
		}
		if input.WorkflowTemplateID != "" {
			if _, err := service.deliveryTemplate(ctx, input.WorkflowTemplateID); err != nil {
				return err
			}
		}
		if input.DeploymentPlanID != "" {
			if _, err := service.deliveryPlan(ctx, kind, input.DeploymentPlanID); err != nil {
				return err
			}
		}
		var current model.DeliveryDefault
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "kind = ?", kind).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.Version != input.ExpectedVersion {
			return ErrDeliveryDefaultConflict
		}
		now := time.Now().UTC()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row := model.DeliveryDefault{ID: uuid.NewString(), Kind: kind, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, WorkflowTemplateID: input.WorkflowTemplateID, DeploymentPlanID: input.DeploymentPlanID, Version: 1, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&row).Error; errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrDeliveryDefaultConflict
			} else {
				return err
			}
		}
		result := tx.Model(&model.DeliveryDefault{}).Where("uuid = ? AND version = ?", current.ID, current.Version).Updates(map[string]any{"workflow_template_id": input.WorkflowTemplateID, "deployment_plan_id": input.DeploymentPlanID, "version": current.Version + 1, "updated_by": actorID, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDeliveryDefaultConflict
		}
		return nil
	})
}

func (s *Service) deliveryTransaction(tx *gorm.DB) *Service {
	// 不复制包含互斥锁的 Service；这里只复用配置阶段需要的依赖。
	service := &Service{db: tx, repositories: s.repositories, secrets: s.secrets, docker: s.docker, scriptRunner: s.scriptRunner, artifacts: s.artifacts, deployments: s.deployments, logger: s.logger, workflowRuntimes: s.workflowRuntimes}
	if s.deployments != nil {
		service.deployments = s.deployments.WithTransaction(tx)
	}
	return service
}

func (s *Service) CreateDeliveryWorkflow(ctx context.Context, applicationID, actorID string, input DeliveryWorkflowInput) (*WorkflowResult, error) {
	if !validDeliveryKind(input.Kind) || input.BuildPlanID == "" || len(input.BuildPlanID) > 36 || len(input.Name) > 384 {
		return nil, ErrInvalidDeliveryDefault
	}
	scope, err := s.deliveryScope(ctx)
	if err != nil {
		return nil, err
	}
	var createdID string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project model.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&project, "uuid = ?", scope.ProjectID).Error; err != nil {
			return err
		}
		service := s.deliveryTransaction(tx)
		application, err := service.FindApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		var locked model.Application
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "uuid = ?", applicationID).Error; err != nil {
			return err
		}
		var defaults model.DeliveryDefault
		err = tx.Where("kind = ?", input.Kind).First(&defaults).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if defaults.Version != input.ExpectedDefaultVersion {
			return ErrDeliveryDefaultConflict
		}
		definition, _ := BuiltinDelivery(input.Kind)
		templateID, planID := input.WorkflowTemplateID, input.DeploymentPlanID
		if templateID == "" {
			templateID = defaults.WorkflowTemplateID
		}
		if planID == "" {
			planID = defaults.DeploymentPlanID
		}
		if templateID != "" && templateID != "builtin" {
			template, err := service.deliveryTemplate(ctx, templateID)
			if err != nil {
				return err
			}
			definition.Name, definition.Source, definition.Stages = template.Name, template.Source, cloneWorkflowStages(template.Stages)
		}
		if planID != "" && planID != "builtin" {
			plan, err := service.deliveryPlan(ctx, input.Kind, planID)
			if err != nil {
				return err
			}
			definition.Deployment = *plan
		}
		var build model.BuildPlan
		if err := tx.First(&build, "uuid = ? AND is_active = ?", input.BuildPlanID, true).Error; err != nil {
			return ErrInvalidBuildPlan
		}
		if (input.Kind == model.DeploymentPlanScript && (build.Kind != model.BuildPlanScript || build.ArtifactPath == "")) || (input.Kind != model.DeploymentPlanScript && build.Kind != model.BuildPlanDockerfile) || (input.Kind == model.DeploymentPlanKubernetes && build.ImageRegistryID == "") {
			return ErrInvalidBuildPlan
		}
		id := uuid.NewString()
		name := strings.TrimSpace(input.Name)
		if name == "" {
			name = definition.Name
		}
		if !validResourceName(name) {
			return ErrInvalidWorkflow
		}
		name, err = service.nextApplicationWorkflowName(ctx, application.ID, name)
		if err != nil {
			return err
		}
		// 默认只复制通用规则，已有方案的主机、集群和工作负载绝不进入新应用。
		base := application.Name + " " + name
		if utf8.RuneCountInString(base) > 105 {
			base = string([]rune(base)[:105])
		}
		input.Target.Name = base + " " + id[:8]
		input.Target.RolloutTimeout = definition.Deployment.TimeoutSeconds
		if input.Kind == model.DeploymentPlanScript {
			input.Target.Platform = model.DeploymentSSH
		} else if input.Kind == model.DeploymentPlanKubernetes {
			input.Target.Platform = model.DeploymentKubernetes
		} else {
			input.Target.Platform = model.DeploymentDocker
		}
		if input.Kind == model.DeploymentPlanCompose {
			input.Target.WorkloadName = definition.Deployment.ServiceName
		}
		plan, err := service.CreateDeploymentPlan(ctx, actorID, DeploymentPlanInput{
			Name: input.Target.Name, Kind: input.Kind, Description: "应用独立部署配置，创建时使用所选默认方案的规则",
			DeploymentTarget: &input.Target, Script: definition.Deployment.Script,
			ComposeYAML: definition.Deployment.ComposeYAML, ServiceName: definition.Deployment.ServiceName,
			DockerConfig: definition.Deployment.DockerConfig, TimeoutSeconds: definition.Deployment.TimeoutSeconds,
		})
		if err != nil {
			return err
		}
		stages := cloneWorkflowStages(definition.Stages)
		for i := range stages {
			for j := range stages[i].Tasks {
				node := &stages[i].Tasks[j]
				if node.Type == model.WorkflowNodeBuild {
					node.Config.BuildPlanID = build.ID
				}
				if node.Type == model.WorkflowNodeDeploy {
					node.Config.DeploymentPlanID = plan.ID
				}
			}
		}
		now := time.Now().UTC()
		workflow := model.ReleaseWorkflow{
			ID: id, ApplicationID: application.ID, Name: name, SchemaVersion: model.WorkflowSchemaVersion,
			Revision: 1, IsActive: false, Source: definition.Source, Stages: stages,
			CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
		}
		// 默认方案是创建模板；生成的是可独立编辑的草稿，不自动绑定后续版本或触发发布。
		if err := tx.Create(&workflow).Error; err != nil {
			return err
		}
		createdID = id
		return nil
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Error("创建统一发布流程失败", "operation", "delivery_workflow_create", "application_id", applicationID, "kind", input.Kind, "err", err)
		}
		return nil, err
	}
	return s.GetApplicationWorkflow(ctx, applicationID, createdID)
}
