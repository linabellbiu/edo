package pipeline

import (
	"context"
	"fmt"
	"strings"

	distributionreference "github.com/distribution/reference"

	"edo/internal/model"
)

// WorkflowBuildImage 是流水线中一个镜像构建任务静态确定的产物身份。
// 部署阶段只能从这里列出的名称中选择镜像，产物名称在构建配置保存时静态校验。
type WorkflowBuildImage struct {
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	ImageName  string `json:"image_name"`
	ImageTag   string `json:"image_tag,omitempty"`
	RegistryID string `json:"registry_id,omitempty"`
}

// WorkflowBuildArtifact 是自定义（脚本）构建保存的产物名称，部署任务用 ${名称} 引用它。
type WorkflowBuildArtifact struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Name     string `json:"name"`
}

// workflowBuildProducts 静态列出流水线的构建产物名称并给出静态检查结果：
// 镜像名称非法、缺少镜像仓库、名称重复，以及自定义构建缺少或重复配置产物名称。
func (s *Service) workflowBuildProducts(ctx context.Context, application *model.Application, stages []model.WorkflowStage) ([]WorkflowBuildImage, []WorkflowBuildArtifact, []WorkflowIssue, error) {
	if application == nil || len(stages) == 0 {
		return []WorkflowBuildImage{}, []WorkflowBuildArtifact{}, []WorkflowIssue{}, nil
	}
	plans, err := s.loadWorkflowLibraryBuildPlans(ctx, stages)
	if err != nil {
		return nil, nil, nil, err
	}
	images, imageIssues := workflowBuildImages(application, stages, plans)
	artifacts, artifactIssues := workflowBuildArtifacts(stages, plans)
	issues := make([]WorkflowIssue, 0, len(imageIssues)+len(artifactIssues))
	issues = append(issues, imageIssues...)
	issues = append(issues, artifactIssues...)
	return images, artifacts, issues, nil
}

// loadWorkflowLibraryBuildPlans 只读取已启用的方案库构建方案；流水线内置方案直接从任务配置取。
func (s *Service) loadWorkflowLibraryBuildPlans(ctx context.Context, stages []model.WorkflowStage) (map[string]model.BuildPlan, error) {
	planIDs := make([]string, 0, 4)
	for _, node := range workflowTasks(enabledWorkflowStages(stages)) {
		if node.Type != model.WorkflowNodeBuild || node.Disabled || node.Config.InlineBuild != nil {
			continue
		}
		if id := strings.TrimSpace(node.Config.BuildPlanID); id != "" {
			planIDs = append(planIDs, id)
		}
	}
	plans := make(map[string]model.BuildPlan, len(planIDs))
	if len(planIDs) == 0 {
		return plans, nil
	}
	var stored []model.BuildPlan
	if err := s.db.WithContext(ctx).Where("uuid IN ? AND is_active = ?", planIDs, true).Find(&stored).Error; err != nil {
		return nil, err
	}
	for i := range stored {
		plans[stored[i].ID] = stored[i]
	}
	return plans, nil
}

// workflowBuildImageName 静态解析构建任务产出的镜像名称：显式配置优先，留空时与应用名一致。
func workflowBuildImageName(application *model.Application, node *model.WorkflowNode) string {
	if node == nil {
		return ""
	}
	value := strings.TrimSpace(node.Config.ImageName)
	if application != nil {
		value = strings.ReplaceAll(value, "${EDO_APPLICATION_NAME}", application.Name)
	}
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	if application == nil {
		return ""
	}
	name := strings.TrimSpace(application.Name)
	if applicationNamePattern.MatchString(name) {
		return name
	}
	if resolved, err := executionImageName(*application); err == nil {
		return resolved
	}
	return name
}

// validWorkflowImageName 判断镜像名称是否是合法的仓库名称：小写字母、数字、点、下划线和连字符，
// 不能带 Tag、Digest 或空路径段。运行期变量仍视为合法，由执行阶段展开后再校验。
func validWorkflowImageName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.ContainsAny(value, ":@") {
		return false
	}
	if strings.Contains(value, "${") {
		return true
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" {
			return false
		}
	}
	named, err := distributionreference.ParseNormalizedNamed("registry.example.com/" + value)
	if err != nil || named.Name() != "registry.example.com/"+value {
		return false
	}
	return true
}

func workflowBuildImages(application *model.Application, stages []model.WorkflowStage, plans map[string]model.BuildPlan) ([]WorkflowBuildImage, []WorkflowIssue) {
	result := make([]WorkflowBuildImage, 0, 4)
	issues := make([]WorkflowIssue, 0, 4)
	ownerByName := make(map[string]string, 4)
	for _, node := range workflowTasks(enabledWorkflowStages(stages)) {
		if node.Type != model.WorkflowNodeBuild || node.Disabled {
			continue
		}
		plan, ok := workflowNodeBuildPlan(node, plans)
		if !ok || plan.Kind != model.BuildPlanDockerfile {
			continue
		}
		registryID := strings.TrimSpace(plan.ImageRegistryID)
		name := workflowBuildImageName(application, &node)
		result = append(result, WorkflowBuildImage{
			NodeID: node.ID, NodeName: node.Name, ImageName: name,
			ImageTag: strings.TrimSpace(node.Config.ImageTag), RegistryID: registryID,
		})
		// 没有镜像仓库时按本地镜像执行，产物名称仍然有效；应用名无法静态解析时跳过名称检查。
		switch {
		case name == "":
		case !validWorkflowImageName(name):
			issues = append(issues, WorkflowIssue{
				Code: "build_image_name_invalid", NodeID: node.ID,
				Message: fmt.Sprintf("构建任务「%s」的镜像名称「%s」不合法：只能使用小写字母、数字、点、下划线和连字符，不能带 Tag 或 Digest", node.Name, name),
			})
		case name != "":
			if owner, exists := ownerByName[name]; exists {
				issues = append(issues, WorkflowIssue{
					Code: "build_image_name_duplicate", NodeID: node.ID,
					Message: fmt.Sprintf("构建任务「%s」与「%s」使用相同的镜像名称「%s」，部署时无法区分上游产物", owner, node.Name, name),
				})
			} else {
				ownerByName[name] = node.Name
			}
		}
	}
	return result, issues
}

// workflowBuildArtifacts 静态列出自定义构建的产物名称，并检查名称缺失、非法或跨任务重复。
func workflowBuildArtifacts(stages []model.WorkflowStage, plans map[string]model.BuildPlan) ([]WorkflowBuildArtifact, []WorkflowIssue) {
	result := make([]WorkflowBuildArtifact, 0, 4)
	issues := make([]WorkflowIssue, 0, 4)
	ownerByName := make(map[string]string, 4)
	for _, node := range workflowTasks(enabledWorkflowStages(stages)) {
		if node.Type != model.WorkflowNodeBuild || node.Disabled {
			continue
		}
		plan, ok := workflowNodeBuildPlan(node, plans)
		if !ok || plan.Kind != model.BuildPlanScript {
			continue
		}
		if len(node.Config.BuildOutputs) == 0 {
			// 未声明命名产物时只保存单个文件制品，由部署任务按上游任务引用，不需要额外名称。
			continue
		}
		taskNames := make(map[string]struct{}, len(node.Config.BuildOutputs))
		for _, output := range node.Config.BuildOutputs {
			name := strings.TrimSpace(output.Name)
			if !outputNamePattern.MatchString(name) {
				issues = append(issues, WorkflowIssue{
					Code: "build_artifact_name_invalid", NodeID: node.ID,
					Message: fmt.Sprintf("构建任务「%s」的产物名称「%s」不合法：请使用字母或下划线开头、最多 32 位字母数字或下划线", node.Name, name),
				})
				continue
			}
			if _, duplicated := taskNames[name]; duplicated {
				issues = append(issues, WorkflowIssue{
					Code: "build_artifact_name_duplicate", NodeID: node.ID,
					Message: fmt.Sprintf("构建任务「%s」重复配置了产物名称「%s」", node.Name, name),
				})
				continue
			}
			taskNames[name] = struct{}{}
			if owner, exists := ownerByName[name]; exists {
				issues = append(issues, WorkflowIssue{
					Code: "build_artifact_name_duplicate", NodeID: node.ID,
					Message: fmt.Sprintf("构建任务「%s」与「%s」的构建产物名称「%s」重复，部署时无法区分上游产物", owner, node.Name, name),
				})
				continue
			}
			ownerByName[name] = node.Name
			result = append(result, WorkflowBuildArtifact{NodeID: node.ID, NodeName: node.Name, Name: name})
		}
	}
	return result, issues
}

// workflowNodeBuildPlan 取任务当前生效的构建方案：流水线内置方案优先，其次按标识读取方案库。
func workflowNodeBuildPlan(node model.WorkflowNode, plans map[string]model.BuildPlan) (model.BuildPlan, bool) {
	if node.Config.InlineBuild != nil {
		return *node.Config.InlineBuild, true
	}
	id := strings.TrimSpace(node.Config.BuildPlanID)
	if id == "" {
		return model.BuildPlan{}, false
	}
	plan, ok := plans[id]
	return plan, ok
}
