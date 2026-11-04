package pipeline

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/deployment"
	"edo/internal/model"
)

var ErrInvalidComposeDelivery = errors.New("请检查 Compose 项目、文件路径、服务与镜像构建方案配置")

//go:embed templates/compose_images.js
var composeImagesTemplate string

//go:embed templates/compose_existing.sh
var composeExistingTemplate string

type ComposeServiceBuild struct {
	Service     string `json:"service"`
	BuildPlanID string `json:"build_plan_id"`
}

type ComposeDeliveryInput struct {
	Name           string                 `json:"name"`
	ProjectName    string                 `json:"project_name"`
	ComposeFile    string                 `json:"compose_file"`
	EnvFile        string                 `json:"env_file"`
	RuntimeImage   string                 `json:"runtime_image"`
	HealthURL      string                 `json:"health_url"`
	UseSudo        bool                   `json:"use_sudo"`
	TimeoutSeconds int                    `json:"timeout_seconds"`
	Services       []ComposeServiceBuild  `json:"services"`
	Target         deployment.TargetInput `json:"-"`
}

var composeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
var composeServicePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func normalizeComposeDelivery(input ComposeDeliveryInput) (ComposeDeliveryInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.ProjectName = strings.TrimSpace(input.ProjectName)
	input.ComposeFile = strings.TrimSpace(input.ComposeFile)
	input.EnvFile = strings.TrimSpace(input.EnvFile)
	input.RuntimeImage = strings.TrimSpace(input.RuntimeImage)
	input.HealthURL = strings.TrimSpace(input.HealthURL)
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 600
	}
	if input.RuntimeImage == "" {
		input.RuntimeImage = "node:22-bookworm-slim"
	}
	validPath := func(value string) bool {
		return value != "" && len(value) <= 1024 && !strings.ContainsAny(value, "\x00\r\n") && path.Clean(value) == value && value != "." && value != "/" && value != ".." && !strings.HasPrefix(value, "../")
	}
	if (input.Name != "" && !validResourceName(input.Name)) || !composeProjectPattern.MatchString(input.ProjectName) ||
		!validPath(input.ComposeFile) || (input.EnvFile != "" && !validPath(input.EnvFile)) ||
		!path.IsAbs(input.Target.WorkingDirectory) || !validPath(input.Target.WorkingDirectory) ||
		len(input.Services) == 0 || len(input.Services) > 32 || input.TimeoutSeconds < 60 || input.TimeoutSeconds > 3600 {
		return input, ErrInvalidComposeDelivery
	}
	seen := map[string]bool{}
	for i := range input.Services {
		binding := &input.Services[i]
		binding.Service, binding.BuildPlanID = strings.TrimSpace(binding.Service), strings.TrimSpace(binding.BuildPlanID)
		if !composeServicePattern.MatchString(binding.Service) || seen[binding.Service] || binding.BuildPlanID == "" || len(binding.BuildPlanID) > 36 {
			return input, ErrInvalidComposeDelivery
		}
		seen[binding.Service] = true
	}
	if input.HealthURL != "" {
		u, err := url.Parse(input.HealthURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(input.HealthURL) > 2048 {
			return input, ErrInvalidComposeDelivery
		}
	}
	input.Target.Platform = model.DeploymentSSH
	input.Target.RuntimeID = ""
	return input, nil
}

// CreateComposeDeliveryWorkflow 只生成普通构建方案、主机脚本方案及独立草稿，复用现有快照和执行器。
func (s *Service) CreateComposeDeliveryWorkflow(ctx context.Context, applicationID, actorID string, input ComposeDeliveryInput) (*WorkflowResult, error) {
	input, err := normalizeComposeDelivery(input)
	if err != nil {
		return nil, err
	}
	if _, err := s.deliveryScope(ctx); err != nil {
		return nil, err
	}
	createdID := ""
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockWorkflowApplication(tx, applicationID); err != nil {
			return err
		}
		service := s.deliveryTransaction(tx)
		app, err := service.FindApplication(ctx, applicationID)
		if err != nil {
			return err
		}
		name := input.Name
		if name == "" {
			name = "已有 Compose 项目发布"
		}
		name, err = service.nextApplicationWorkflowName(ctx, tx, app.ID, "", name)
		if err != nil {
			return err
		}
		id := uuid.NewString()
		base := app.Name + " " + name
		if utf8.RuneCountInString(base) > 90 {
			base = string([]rune(base)[:90])
		}
		base += " " + id[:8]
		nodes, dependencies := []model.WorkflowNode{}, []string{}
		byPlan := map[string]string{}
		bindings := []map[string]string{}
		for _, binding := range input.Services {
			nodeID := byPlan[binding.BuildPlanID]
			if nodeID == "" {
				var build model.BuildPlan
				if err := tx.First(&build, "uuid = ? AND is_active = ?", binding.BuildPlanID, true).Error; err != nil {
					return ErrInvalidBuildPlan
				}
				if build.Kind != model.BuildPlanDockerfile || build.ImageRegistryID == "" {
					return ErrInvalidBuildPlan
				}
				if err := service.validateBuildPlanRegistry(ctx, build.ImageRegistryID); err != nil {
					return err
				}
				nodeID = fmt.Sprintf("image_%d", len(nodes)+1)
				byPlan[build.ID] = nodeID
				dependencies = append(dependencies, nodeID)
				nodes = append(nodes, model.WorkflowNode{ID: nodeID, Type: model.WorkflowNodeBuild, Name: build.Name, Config: model.WorkflowNodeConfig{BuildPlanID: build.ID}})
			}
			bindings = append(bindings, map[string]string{"service": binding.Service, "node_id": nodeID})
		}
		encoded, err := json.Marshal(bindings)
		if err != nil {
			return err
		}
		bundle, err := service.CreateBuildPlan(ctx, actorID, BuildPlanInput{Name: base + " 镜像清单", Kind: model.BuildPlanScript, Script: "node <<'EDO_COMPOSE_IMAGES_JS'\n" + strings.ReplaceAll(composeImagesTemplate, "__EDO_BINDINGS__", string(encoded)) + "\nEDO_COMPOSE_IMAGES_JS\n", RuntimeImage: input.RuntimeImage, WorkingDirectory: ".", ArtifactPath: "compose.images.json", TimeoutSeconds: 120})
		if err != nil {
			return err
		}
		input.Target.Name, input.Target.RolloutTimeout = base, input.TimeoutSeconds
		plan, err := service.CreateDeploymentPlan(ctx, actorID, DeploymentPlanInput{Name: base, Kind: model.DeploymentPlanScript, Description: "已有 Compose 项目：保留主机配置及数据卷，仅以本次镜像摘要更新所选服务；失败尝试恢复上一镜像。", DeploymentTarget: &input.Target, Script: composeExistingScript(input), TimeoutSeconds: input.TimeoutSeconds})
		if err != nil {
			return err
		}
		if plan.DeploymentTarget == nil || plan.DeploymentTarget.HostID != input.Target.HostID {
			return ErrInvalidComposeDelivery
		}
		var host model.Host
		if err := tx.First(&host, "uuid = ? AND mode = ? AND is_active = ?", input.Target.HostID, model.HostModeSSH, true).Error; err != nil {
			return ErrInvalidComposeDelivery
		}
		var capability model.HostCapability
		if err := tx.First(&capability, "host_id = ? AND kind = ? AND status = ?", host.ID, model.HostCapabilityDocker, model.HostCapabilityReady).Error; err != nil {
			return ErrInvalidComposeDelivery
		}
		now := time.Now().UTC()
		workflow := model.ReleaseWorkflow{ID: id, ApplicationID: app.ID, Name: name, SchemaVersion: model.WorkflowSchemaVersion, Revision: 1, IsActive: false,
			Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Branch: "*", Events: []string{"manual"}, TagPattern: defaultWorkflowTagPattern}},
			Stages: []model.WorkflowStage{
				{ID: "images", Name: "构建与推送镜像", Tasks: nodes},
				{ID: "bundle", Name: "汇总镜像摘要", Tasks: []model.WorkflowNode{{ID: "bundle", Type: model.WorkflowNodeBuild, Name: "生成 Compose 镜像清单", Config: model.WorkflowNodeConfig{BuildPlanID: bundle.ID, ArtifactNodeIDs: dependencies}}}},
				{ID: "deploy", Name: "更新已有 Compose 项目", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "Compose 更新与验收", Config: model.WorkflowNodeConfig{DeploymentPlanID: plan.ID, ArtifactNodeID: "bundle"}}}},
			}, CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&workflow).Error; err != nil {
			return err
		}
		createdID = workflow.ID
		return nil
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Error("创建已有 Compose 发布草稿失败", "operation", "compose_delivery_create", "application_id", applicationID, "err", err)
		}
		return nil, err
	}
	return s.GetApplicationWorkflow(ctx, applicationID, createdID)
}

func composeShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func composeExistingScript(input ComposeDeliveryInput) string {
	docker := "docker"
	if input.UseSudo {
		docker = "sudo -n docker"
	}
	args := "--ansi never --project-name " + composeShellLiteral(input.ProjectName) + " --project-directory " + composeShellLiteral(input.Target.WorkingDirectory)
	if input.EnvFile != "" {
		args += " --env-file " + composeShellLiteral(input.EnvFile)
	}
	args += " --file " + composeShellLiteral(input.ComposeFile)
	services := []string{}
	for _, binding := range input.Services {
		services = append(services, composeShellLiteral(binding.Service))
	}
	health := ":"
	if input.HealthURL != "" {
		health = "curl --fail --silent --show-error --max-time 10 --output /dev/null " + composeShellLiteral(input.HealthURL)
	}
	return strings.NewReplacer("__EDO_DOCKER__", docker, "__EDO_COMPOSE_ARGS__", args, "__EDO_PROJECT_NAME__", composeShellLiteral(input.ProjectName), "__EDO_SERVICES__", strings.Join(services, " "), "__EDO_TIMEOUT__", fmt.Sprint(input.TimeoutSeconds-30), "__EDO_HEALTH_CHECK__", health).Replace(composeExistingTemplate)
}
