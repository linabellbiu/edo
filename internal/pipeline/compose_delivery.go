package pipeline

import (
	"context"
	_ "embed"
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

var ErrInvalidComposeDelivery = errors.New("请检查 Compose 正文、Tag 变量、服务与镜像构建方案配置")

//go:embed templates/compose_stack.sh
var composeStackTemplate string

type ComposeServiceBuild struct {
	Service     string `json:"service"`
	BuildPlanID string `json:"build_plan_id"`
	ImageName   string `json:"image_name"`
}

type ComposeDeliveryInput struct {
	Name           string                 `json:"name"`
	ProjectName    string                 `json:"project_name"`
	ComposeYAML    string                 `json:"compose_yaml"`
	EnvFile        string                 `json:"env_file"`
	ImageTag       string                 `json:"image_tag"`
	TagVariable    string                 `json:"tag_variable"`
	HealthURL      string                 `json:"health_url"`
	UseSudo        bool                   `json:"use_sudo"`
	AdoptExisting  bool                   `json:"adopt_existing"`
	TimeoutSeconds int                    `json:"timeout_seconds"`
	Services       []ComposeServiceBuild  `json:"services"`
	Target         deployment.TargetInput `json:"-"`
}

var composeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
var composeServicePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validComposeFilePath(value string) bool {
	return value != "" && len(value) <= 1024 && !strings.ContainsAny(value, "\x00\r\n") && path.Clean(value) == value && value != "." && value != "/" && value != ".." && !strings.HasPrefix(value, "../")
}

func validComposeHealthURL(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && len(value) <= 2048
}

func normalizeComposeDelivery(input ComposeDeliveryInput) (ComposeDeliveryInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.ProjectName = strings.TrimSpace(input.ProjectName)
	input.EnvFile = strings.TrimSpace(input.EnvFile)
	input.ImageTag = strings.TrimSpace(input.ImageTag)
	input.TagVariable = strings.TrimSpace(input.TagVariable)
	input.HealthURL = strings.TrimSpace(input.HealthURL)
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 600
	}
	if input.ImageTag == "" {
		input.ImageTag = "${EDO_GIT_TAG}"
	}
	if input.TagVariable == "" {
		input.TagVariable = "${IMAGE_TAG}"
	}
	if (input.Name != "" && !validResourceName(input.Name)) || !composeProjectPattern.MatchString(input.ProjectName) || input.EnvFile != "" || (input.Target.WorkingDirectory != "" && (!path.IsAbs(input.Target.WorkingDirectory) || !validComposeFilePath(input.Target.WorkingDirectory))) || len(input.Services) == 0 || len(input.Services) > 32 || input.TimeoutSeconds < 60 || input.TimeoutSeconds > 3600 || len(input.ImageTag) > 512 || !validComposeHealthURL(input.HealthURL) || composeTagName(input.TagVariable) == "" {
		return input, ErrInvalidComposeDelivery
	}
	doc, err := parseComposeStack(input.ComposeYAML)
	if err != nil {
		return input, err
	}
	services := doc["services"].(map[string]any)
	seen := map[string]bool{}
	for i := range input.Services {
		binding := &input.Services[i]
		binding.Service = strings.TrimSpace(binding.Service)
		binding.BuildPlanID = strings.TrimSpace(binding.BuildPlanID)
		binding.ImageName = strings.TrimSpace(binding.ImageName)
		if binding.ImageName == "" {
			binding.ImageName = binding.Service
		}
		if !composeServicePattern.MatchString(binding.Service) || seen[binding.Service] || binding.BuildPlanID == "" || len(binding.BuildPlanID) > 36 || len(binding.ImageName) > 255 {
			return input, ErrInvalidComposeDelivery
		}
		service, ok := services[binding.Service].(map[string]any)
		if !ok {
			return input, ErrInvalidComposeDelivery
		}
		image, ok := service["image"].(string)
		if !ok || !strings.Contains(image, "${"+composeTagName(input.TagVariable)+"}") {
			return input, ErrInvalidComposeDelivery
		}
		seen[binding.Service] = true
	}
	input.Target.Platform = model.DeploymentSSH
	input.Target.RuntimeID = ""
	return input, nil
}

// CreateComposeDeliveryWorkflow 生成普通并行构建与直接 Compose 部署两个阶段，配置与身份均进入正常运行快照。
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
			name = "Compose 发布"
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
		byOutput := map[string]string{}
		bindings := []model.ComposeImageBinding{}
		for _, binding := range input.Services {
			key := binding.BuildPlanID + "\x00" + binding.ImageName
			nodeID := byOutput[key]
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
				byOutput[key] = nodeID
				dependencies = append(dependencies, nodeID)
				nodes = append(nodes, model.WorkflowNode{ID: nodeID, Type: model.WorkflowNodeBuild, Name: binding.ImageName, Config: model.WorkflowNodeConfig{BuildPlanID: build.ID, ImageName: binding.ImageName, ImageTag: input.ImageTag}})
			}
			bindings = append(bindings, model.ComposeImageBinding{Service: binding.Service, ArtifactNodeID: nodeID})
		}
		input.Target.Name, input.Target.RolloutTimeout = base, input.TimeoutSeconds
		plan, err := service.CreateDeploymentPlan(ctx, actorID, DeploymentPlanInput{Name: base, Kind: model.DeploymentPlanComposeStack, Description: "EDO 保存 Compose 正文；部署直接读取并行构建的镜像 Tag，并固定其摘要执行。", DeploymentTarget: &input.Target, ComposeYAML: input.ComposeYAML, TimeoutSeconds: input.TimeoutSeconds})
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
		var cap model.HostCapability
		if err := tx.First(&cap, "host_id = ? AND kind = ? AND status = ?", host.ID, model.HostCapabilityDocker, model.HostCapabilityReady).Error; err != nil {
			return ErrInvalidComposeDelivery
		}
		now := time.Now().UTC()
		settings := &model.ComposeDeploymentSettings{ProjectName: input.ProjectName, EnvFile: input.EnvFile, TagVariable: input.TagVariable, UseSudo: input.UseSudo, HealthURL: input.HealthURL, AdoptExisting: input.AdoptExisting}
		workflow := model.ReleaseWorkflow{ID: id, ApplicationID: app.ID, Name: name, SchemaVersion: model.WorkflowSchemaVersion, Revision: 1, IsActive: false, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Branch: "*", Events: []string{"manual"}, TagPattern: defaultWorkflowTagPattern}}, Stages: []model.WorkflowStage{{ID: "images", Name: "构建与推送镜像", Tasks: nodes}, {ID: "deploy", Name: "部署", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy, Name: "Compose 部署", Config: model.WorkflowNodeConfig{DeploymentPlanID: plan.ID, ArtifactNodeID: dependencies[0], ArtifactNodeIDs: dependencies, ComposeImages: bindings, Compose: settings}}}}}, CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&workflow).Error; err != nil {
			return err
		}
		createdID = workflow.ID
		return nil
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Error("创建 Compose 发布草稿失败", "operation", "compose_delivery_create", "application_id", applicationID, "err", err)
		}
		return nil, err
	}
	return s.GetApplicationWorkflow(ctx, applicationID, createdID)
}

func composeShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
