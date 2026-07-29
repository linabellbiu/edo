package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"zrt/internal/model"
	"zrt/internal/repository"
	"zrt/internal/task"
)

var (
	ErrInvalidWorkflow                 = errors.New("流水线配置存在错误，请先修正节点和连线")
	ErrWorkflowNotFound                = errors.New("应用流水线不存在")
	ErrWorkflowRevisionConflict        = errors.New("应用流水线已被其他人修改，请刷新后再保存")
	ErrWorkflowNotActive               = errors.New("应用流水线尚未启用")
	ErrInvalidWorkflowTransition       = errors.New("当前节点不能这样推进")
	ErrWorkflowApprovalRequired        = errors.New("该流水线运行需要先完成审核")
	ErrWorkflowSelfApproval            = errors.New("执行申请人不能审核自己的流水线运行")
	ErrPipelineRunNotFound             = errors.New("流水线运行不存在")
	ErrPipelineRunNotRetryable         = errors.New("只有失败的流水线运行可以重新执行")
	ErrPipelineRunManagedByReleasePlan = errors.New("发布计划中的流水线运行不能单独删除")
	ErrPipelineRunAwaitingReleasePlan  = errors.New("流水线运行由发布计划统一调度")
)

type WorkflowInput struct {
	Name     string
	Revision uint64
	Activate bool
	Nodes    []model.WorkflowNode
	Edges    []model.WorkflowEdge
	Viewport model.WorkflowViewport
}

type WorkflowIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	NodeID  string `json:"node_id,omitempty"`
	EdgeID  string `json:"edge_id,omitempty"`
}

type WorkflowResult struct {
	Workflow *model.ReleaseWorkflow `json:"workflow"`
	Valid    bool                   `json:"valid"`
	Issues   []WorkflowIssue        `json:"issues"`
}

type workflowSnapshot struct {
	Nodes             []model.WorkflowNode                        `json:"nodes"`
	Edges             []model.WorkflowEdge                        `json:"edges"`
	DeploymentPlans   map[string]workflowDeploymentPlanSnapshot   `json:"deployment_plans,omitempty"`
	DeploymentTargets map[string]workflowDeploymentTargetSnapshot `json:"deployment_targets,omitempty"`
	ApprovalEnabled   bool                                        `json:"approval_enabled"`
}

type workflowDeploymentPlanSnapshot struct {
	ID             string                   `json:"id"`
	Kind           model.DeploymentPlanKind `json:"kind"`
	Script         string                   `json:"script,omitempty"`
	HelmChart      string                   `json:"helm_chart,omitempty"`
	HelmValues     string                   `json:"helm_values,omitempty"`
	ComposeFile    string                   `json:"compose_file,omitempty"`
	ServiceName    string                   `json:"service_name,omitempty"`
	TimeoutSeconds int                      `json:"timeout_seconds"`
}

type workflowDeploymentTargetSnapshot struct {
	ID               string                   `json:"id"`
	Name             string                   `json:"name"`
	Platform         model.DeploymentPlatform `json:"platform"`
	EnvironmentID    string                   `json:"environment_id,omitempty"`
	HostID           string                   `json:"host_id,omitempty"`
	RuntimeID        string                   `json:"runtime_id,omitempty"`
	WorkingDirectory string                   `json:"working_directory,omitempty"`
	Namespace        string                   `json:"namespace,omitempty"`
	WorkloadName     string                   `json:"workload_name,omitempty"`
	ContainerName    string                   `json:"container_name,omitempty"`
	RolloutTimeout   int                      `json:"rollout_timeout"`
}

func (s *Service) GetWorkflow(ctx context.Context, applicationID string) (*WorkflowResult, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	workflow, err := s.ensureWorkflow(ctx, application)
	if err != nil {
		return nil, err
	}
	issues := s.validateWorkflow(ctx, application, workflow.Nodes, workflow.Edges)
	return &WorkflowResult{Workflow: workflow, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) ValidateWorkflow(ctx context.Context, applicationID string, input WorkflowInput) (*WorkflowResult, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if err := sanitizeWorkflowInput(&input); err != nil {
		return nil, err
	}
	issues := s.validateWorkflow(ctx, application, input.Nodes, input.Edges)
	return &WorkflowResult{Workflow: &model.ReleaseWorkflow{
		ApplicationID: application.ID, Name: input.Name, Revision: input.Revision,
		Nodes: input.Nodes, Edges: input.Edges, Viewport: input.Viewport,
	}, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) SaveWorkflow(ctx context.Context, applicationID, actorID string, input WorkflowInput) (*WorkflowResult, error) {
	application, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if err := sanitizeWorkflowInput(&input); err != nil {
		return nil, err
	}
	issues := s.validateWorkflow(ctx, application, input.Nodes, input.Edges)
	if len(issues) > 0 && (input.Activate || hasWorkflowIssue(issues, "deployment_plan_target_mismatch")) {
		return &WorkflowResult{Workflow: &model.ReleaseWorkflow{
			ApplicationID: application.ID, Name: input.Name, Revision: input.Revision,
			Nodes: input.Nodes, Edges: input.Edges, Viewport: input.Viewport,
		}, Valid: false, Issues: issues}, ErrInvalidWorkflow
	}

	now := time.Now().UTC()
	nodesJSON, err := json.Marshal(input.Nodes)
	if err != nil {
		return nil, ErrInvalidWorkflow
	}
	edgesJSON, err := json.Marshal(input.Edges)
	if err != nil {
		return nil, ErrInvalidWorkflow
	}
	viewportJSON, err := json.Marshal(input.Viewport)
	if err != nil {
		return nil, ErrInvalidWorkflow
	}
	var saved model.ReleaseWorkflow
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.ReleaseWorkflow
		err := tx.First(&existing, "application_id = ?", application.ID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if input.Revision != 0 {
				return ErrWorkflowRevisionConflict
			}
			saved = model.ReleaseWorkflow{
				ID: uuid.NewString(), ApplicationID: application.ID, Name: input.Name,
				Revision: 1, IsActive: input.Activate, Nodes: input.Nodes, Edges: input.Edges,
				Viewport: input.Viewport, CreatedBy: actorID, UpdatedBy: actorID,
				CreatedAt: now, UpdatedAt: now,
			}
			return tx.Create(&saved).Error
		}
		if err != nil {
			return err
		}
		if input.Revision != existing.Revision {
			return ErrWorkflowRevisionConflict
		}
		result := tx.Model(&model.ReleaseWorkflow{}).
			Where("id = ? AND revision = ?", existing.ID, existing.Revision).
			Updates(map[string]any{
				"name": input.Name, "nodes": string(nodesJSON), "edges": string(edgesJSON),
				"viewport": string(viewportJSON), "is_active": input.Activate,
				"workflow_template_id": "", "workflow_template_revision": 0,
				"revision": existing.Revision + 1, "updated_by": actorID, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkflowRevisionConflict
		}
		if existing.WorkflowTemplateID != "" {
			if err := tx.Model(&model.Application{}).Where("id = ?", application.ID).
				Updates(map[string]any{"workflow_template_id": "", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return tx.First(&saved, "id = ?", existing.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &WorkflowResult{Workflow: &saved, Valid: len(issues) == 0, Issues: issues}, nil
}

func (s *Service) ensureWorkflow(ctx context.Context, application *model.Application) (*model.ReleaseWorkflow, error) {
	if application.Workflow != nil && application.Workflow.ID != "" {
		return application.Workflow, nil
	}
	now := time.Now().UTC()
	workflow, err := s.newApplicationWorkflow(ctx, application, application.CreatedBy, now)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Create(workflow).Error; err != nil {
		if !errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, fmt.Errorf("创建默认应用流水线失败: %w", err)
		}
		if err := s.db.WithContext(ctx).First(workflow, "application_id = ?", application.ID).Error; err != nil {
			return nil, fmt.Errorf("读取应用流水线失败: %w", err)
		}
	}
	return workflow, nil
}

func defaultWorkflow(application *model.Application, environments []model.ApplicationEnvironment, actorID string, now time.Time) *model.ReleaseWorkflow {
	sort.SliceStable(environments, func(i, j int) bool { return environments[i].SortOrder < environments[j].SortOrder })
	nodes := make([]model.WorkflowNode, 0, len(environments)*3)
	edges := make([]model.WorkflowEdge, 0, len(environments)*3)
	deployIDs := make([]string, 0, len(environments))
	entryIDs := make(map[string]string, len(environments))
	for i := range environments {
		environment := environments[i]
		x := 120.0 + float64(i)*440
		triggerID, deployID := "trigger-"+environment.Key, "deploy-"+environment.Key
		events := environmentEvents(environment)
		deploymentPlanID := environment.DeploymentPlanID
		if deploymentPlanID == "" {
			deploymentPlanID = application.DeploymentPlanID
		}
		nodes = append(nodes,
			model.WorkflowNode{ID: triggerID, Type: model.WorkflowNodeTrigger, Name: environment.Name + "代码", Position: model.WorkflowPosition{X: x, Y: 80}, Config: model.WorkflowNodeConfig{
				Environment: environment.Key, Branch: environment.Branch, Events: events, TagPattern: environment.TagPattern,
			}},
			model.WorkflowNode{ID: deployID, Type: model.WorkflowNodeDeploy, Name: "部署到" + environment.Name, Position: model.WorkflowPosition{X: x, Y: 350}, Config: model.WorkflowNodeConfig{
				Environment: environment.Key, DeploymentPlanID: deploymentPlanID,
				DeploymentTargetID: environment.DeploymentTargetID,
			}},
		)
		entryIDs[environment.Key] = deployID
		deployIDs = append(deployIDs, deployID)
	}
	for i := range environments {
		edges = append(edges, model.WorkflowEdge{ID: uuid.NewString(), Source: "trigger-" + environments[i].Key, Target: entryIDs[environments[i].Key]})
		if i == 0 {
			continue
		}
		gateID := "promote-" + environments[i].Key
		nodes = append(nodes, model.WorkflowNode{
			ID: gateID, Type: model.WorkflowNodeManual, Name: "放行到" + environments[i].Name,
			Position: model.WorkflowPosition{X: 120 + float64(i)*440 - 220, Y: 350},
			Config:   model.WorkflowNodeConfig{Environment: environments[i].Key, Description: "主动接测或人工放行后继续"},
		})
		edges = append(edges,
			model.WorkflowEdge{ID: uuid.NewString(), Source: deployIDs[i-1], Target: gateID},
			model.WorkflowEdge{ID: uuid.NewString(), Source: gateID, Target: entryIDs[environments[i].Key]},
		)
	}
	return &model.ReleaseWorkflow{
		ID: uuid.NewString(), ApplicationID: application.ID, Name: application.Name + "流水线",
		Revision: 1, IsActive: false, Nodes: nodes, Edges: edges,
		Viewport:  model.WorkflowViewport{X: 60, Y: 40, Zoom: 0.85},
		CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
}

func environmentEvents(environment model.ApplicationEnvironment) []string {
	events := make([]string, 0, 4)
	if environment.PollEnabled {
		events = append(events, "pull")
	}
	if environment.WatchPush {
		events = append(events, "push")
	}
	if environment.WatchPullRequest {
		events = append(events, "pr")
	}
	if environment.WatchTags {
		events = append(events, "tag")
	}
	return events
}

func sanitizeWorkflowInput(input *WorkflowInput) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 128 || len(input.Nodes) > 200 || len(input.Edges) > 400 {
		return ErrInvalidWorkflow
	}
	if input.Viewport.Zoom < 0.2 || input.Viewport.Zoom > 2 || math.IsNaN(input.Viewport.Zoom) || math.IsInf(input.Viewport.Zoom, 0) {
		input.Viewport.Zoom = 1
	}
	for i := range input.Nodes {
		input.Nodes[i].ID = strings.TrimSpace(input.Nodes[i].ID)
		input.Nodes[i].Name = strings.TrimSpace(input.Nodes[i].Name)
		input.Nodes[i].Config.Environment = strings.ToLower(strings.TrimSpace(input.Nodes[i].Config.Environment))
		input.Nodes[i].Config.Branch = strings.TrimSpace(input.Nodes[i].Config.Branch)
		input.Nodes[i].Config.TagPattern = strings.TrimSpace(input.Nodes[i].Config.TagPattern)
		input.Nodes[i].Config.DeploymentPlanID = strings.TrimSpace(input.Nodes[i].Config.DeploymentPlanID)
		input.Nodes[i].Config.DeploymentTargetID = strings.TrimSpace(input.Nodes[i].Config.DeploymentTargetID)
		input.Nodes[i].Config.Description = strings.TrimSpace(input.Nodes[i].Config.Description)
		if input.Nodes[i].ID == "" || len(input.Nodes[i].ID) > 64 || input.Nodes[i].Name == "" || utf8.RuneCountInString(input.Nodes[i].Name) > 128 ||
			math.Abs(input.Nodes[i].Position.X) > 1_000_000 || math.Abs(input.Nodes[i].Position.Y) > 1_000_000 {
			return ErrInvalidWorkflow
		}
	}
	for i := range input.Edges {
		input.Edges[i].ID = strings.TrimSpace(input.Edges[i].ID)
		input.Edges[i].Source = strings.TrimSpace(input.Edges[i].Source)
		input.Edges[i].Target = strings.TrimSpace(input.Edges[i].Target)
		input.Edges[i].Label = strings.TrimSpace(input.Edges[i].Label)
		if input.Edges[i].ID == "" || len(input.Edges[i].ID) > 64 || utf8.RuneCountInString(input.Edges[i].Label) > 64 {
			return ErrInvalidWorkflow
		}
	}
	return nil
}

func (s *Service) validateWorkflow(ctx context.Context, application *model.Application, nodes []model.WorkflowNode, edges []model.WorkflowEdge) []WorkflowIssue {
	issues := make([]WorkflowIssue, 0)
	nodeByID := make(map[string]model.WorkflowNode, len(nodes))
	indegree := make(map[string]int, len(nodes))
	outgoing := make(map[string][]string, len(nodes))
	deployCount, sourceCount := 0, 0
	environments := make(map[string]model.ApplicationEnvironment, len(application.Environments))
	for i := range application.Environments {
		environments[application.Environments[i].Key] = application.Environments[i]
	}
	planIDs := make([]string, 0)
	deployPlanIDs := make(map[string]string, len(nodes))
	for i := range nodes {
		if nodes[i].Type != model.WorkflowNodeDeploy {
			continue
		}
		planID := nodes[i].Config.DeploymentPlanID
		if planID == "" {
			planID = application.DeploymentPlanID
		}
		if planID == "" {
			if environment, ok := environments[nodes[i].Config.Environment]; ok {
				planID = environment.DeploymentPlanID
			}
		}
		if planID != "" {
			deployPlanIDs[nodes[i].ID] = planID
			planIDs = append(planIDs, planID)
		}
	}
	activePlans := make(map[string]model.DeploymentPlan, len(planIDs))
	if len(planIDs) > 0 {
		var plans []model.DeploymentPlan
		if err := s.db.WithContext(ctx).Model(&model.DeploymentPlan{}).
			Select("id", "kind", "deployment_target_id").Where("id IN ? AND is_active = ?", planIDs, true).Find(&plans).Error; err == nil {
			for i := range plans {
				activePlans[plans[i].ID] = plans[i]
			}
		}
	}
	targetIDs := make([]string, 0, len(nodes))
	deployTargetIDs := make(map[string]string, len(nodes))
	for i := range nodes {
		if nodes[i].Type != model.WorkflowNodeDeploy {
			continue
		}
		targetID := nodes[i].Config.DeploymentTargetID
		if plan, ok := activePlans[deployPlanIDs[nodes[i].ID]]; ok && plan.DeploymentTargetID != "" {
			targetID = plan.DeploymentTargetID
		}
		if targetID == "" {
			if environment, ok := environments[nodes[i].Config.Environment]; ok {
				targetID = environment.DeploymentTargetID
			}
		}
		if targetID == "" {
			targetID = application.DeploymentTargetID
		}
		if targetID != "" {
			deployTargetIDs[nodes[i].ID] = targetID
			targetIDs = append(targetIDs, targetID)
		}
	}
	activeTargets := make(map[string]model.DeploymentTarget, len(targetIDs))
	if len(targetIDs) > 0 {
		var targets []model.DeploymentTarget
		if err := s.db.WithContext(ctx).Model(&model.DeploymentTarget{}).
			Select("id", "platform").Where("id IN ? AND is_active = ?", targetIDs, true).Find(&targets).Error; err == nil {
			for i := range targets {
				activeTargets[targets[i].ID] = targets[i]
			}
		}
	}
	for i := range nodes {
		node := nodes[i]
		if _, exists := nodeByID[node.ID]; exists {
			issues = append(issues, WorkflowIssue{Code: "duplicate_node", Message: "节点标识重复", NodeID: node.ID})
			continue
		}
		nodeByID[node.ID] = node
		indegree[node.ID] = 0
		switch node.Type {
		case model.WorkflowNodeTrigger:
			sourceCount++
			if _, ok := environments[node.Config.Environment]; !ok {
				issues = append(issues, WorkflowIssue{Code: "invalid_environment", Message: "触发节点没有绑定已启用的环境", NodeID: node.ID})
			}
			if len(node.Config.Events) == 0 {
				issues = append(issues, WorkflowIssue{Code: "missing_event", Message: "代码触发节点至少选择一种启动方式", NodeID: node.ID})
			}
			for _, event := range node.Config.Events {
				if event != "manual" && event != "pull" && event != "push" && event != "pr" && event != "tag" {
					issues = append(issues, WorkflowIssue{Code: "invalid_event", Message: "代码触发节点包含未知启动方式", NodeID: node.ID})
				}
			}
			if node.Config.Branch == "" &&
				(containsEvent(node.Config.Events, "pull") || containsEvent(node.Config.Events, "push") || containsEvent(node.Config.Events, "pr")) {
				issues = append(issues, WorkflowIssue{Code: "missing_branch", Message: "触发节点需要填写监听分支", NodeID: node.ID})
			}
			if node.Config.TagPattern != "" {
				if _, err := path.Match(node.Config.TagPattern, "v1.0.0"); err != nil {
					issues = append(issues, WorkflowIssue{Code: "invalid_tag_pattern", Message: "Tag 匹配规则无效", NodeID: node.ID})
				}
			}
		case model.WorkflowNodeManualRelease:
			sourceCount++
			issues = append(issues, WorkflowIssue{
				Code: "invalid_node_type", Message: "独立手动发布节点已停用，请在代码触发节点中启用手动发布", NodeID: node.ID,
			})
			if node.Config.Environment != "" {
				if _, ok := environments[node.Config.Environment]; !ok {
					issues = append(issues, WorkflowIssue{Code: "invalid_environment", Message: "手动发布节点没有绑定已启用的环境", NodeID: node.ID})
				}
			}
		case model.WorkflowNodeManual, model.WorkflowNodeApproval:
			if node.Config.Environment != "" {
				if _, ok := environments[node.Config.Environment]; !ok {
					issues = append(issues, WorkflowIssue{Code: "invalid_environment", Message: "节点没有绑定已启用的环境", NodeID: node.ID})
				}
			}
		case model.WorkflowNodeDeploy:
			deployCount++
			if _, ok := environments[node.Config.Environment]; !ok {
				issues = append(issues, WorkflowIssue{Code: "invalid_environment", Message: fmt.Sprintf("部署节点“%s”没有绑定已启用的流程阶段", node.Name), NodeID: node.ID})
			}
			plan, planOK := activePlans[deployPlanIDs[node.ID]]
			target, targetOK := activeTargets[deployTargetIDs[node.ID]]
			if !targetOK {
				issues = append(issues, WorkflowIssue{Code: "missing_deployment_environment", Message: fmt.Sprintf("部署节点“%s”的部署方案没有配置部署到哪里", node.Name), NodeID: node.ID})
			}
			if targetOK && planOK && !deploymentPlanSupportsTarget(plan.Kind, target.Platform) {
				issues = append(issues, WorkflowIssue{
					Code:    "deployment_plan_target_mismatch",
					Message: fmt.Sprintf("部署节点“%s”的执行方式与所选运行环境不匹配", node.Name),
					NodeID:  node.ID,
				})
			}
		default:
			issues = append(issues, WorkflowIssue{Code: "invalid_node_type", Message: "节点类型无效", NodeID: node.ID})
		}
	}
	if sourceCount == 0 {
		issues = append(issues, WorkflowIssue{Code: "missing_trigger", Message: "流水线至少需要一个代码触发节点"})
	}
	if deployCount == 0 {
		issues = append(issues, WorkflowIssue{Code: "missing_deploy", Message: "流水线至少需要一个部署节点"})
	}
	edgePairs := make(map[string]struct{}, len(edges))
	edgeIDs := make(map[string]struct{}, len(edges))
	for i := range edges {
		edge := edges[i]
		if _, exists := edgeIDs[edge.ID]; exists {
			issues = append(issues, WorkflowIssue{Code: "duplicate_edge_id", Message: "连线标识重复", EdgeID: edge.ID})
			continue
		}
		edgeIDs[edge.ID] = struct{}{}
		if _, ok := nodeByID[edge.Source]; !ok {
			issues = append(issues, WorkflowIssue{Code: "missing_source", Message: "连线的起点不存在", EdgeID: edge.ID})
			continue
		}
		if _, ok := nodeByID[edge.Target]; !ok || edge.Source == edge.Target {
			issues = append(issues, WorkflowIssue{Code: "missing_target", Message: "连线的终点无效", EdgeID: edge.ID})
			continue
		}
		pair := edge.Source + "\x00" + edge.Target
		if _, exists := edgePairs[pair]; exists {
			issues = append(issues, WorkflowIssue{Code: "duplicate_edge", Message: "两个节点之间存在重复连线", EdgeID: edge.ID})
			continue
		}
		edgePairs[pair] = struct{}{}
		outgoing[edge.Source] = append(outgoing[edge.Source], edge.Target)
		indegree[edge.Target]++
	}
	queue := make([]string, 0, len(nodes))
	degrees := make(map[string]int, len(indegree))
	for id, degree := range indegree {
		degrees[id] = degree
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, target := range outgoing[id] {
			degrees[target]--
			if degrees[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	if visited != len(nodeByID) {
		issues = append(issues, WorkflowIssue{Code: "cycle", Message: "流水线不能形成循环连线"})
	}
	for id, node := range nodeByID {
		// 继续检查迁移前结构的连线，给仍携带旧节点的保存请求返回准确问题。
		// 历史运行走不可变快照解析，不会调用当前工作流校验。
		if node.Type == model.WorkflowNodeManualRelease && indegree[id] > 0 {
			issues = append(issues, WorkflowIssue{Code: "trigger_has_upstream", Message: "手动发布节点不能有上游连线", NodeID: id})
		}
		if node.Type == model.WorkflowNodeTrigger && indegree[id] > 0 {
			issues = append(issues, WorkflowIssue{
				Code: "trigger_has_upstream", Message: "代码触发节点是流程入口，不能有上游连线", NodeID: id,
			})
		}
		if len(outgoing[id]) == 0 && node.Type != model.WorkflowNodeDeploy {
			issues = append(issues, WorkflowIssue{Code: "invalid_terminal", Message: "流程的最后一个节点必须是部署节点", NodeID: id})
		}
		if len(outgoing[id]) > 1 {
			issues = append(issues, WorkflowIssue{
				Code: "multiple_outgoing_edges", Message: "节点只能连接一个下游节点，自动推进无法选择多条路径", NodeID: id,
			})
		}
		if !workflowSourceNode(node.Type) && indegree[id] == 0 {
			issues = append(issues, WorkflowIssue{Code: "unreachable_node", Message: "节点没有上游入口", NodeID: id})
		}
	}
	return uniqueIssues(issues)
}

func hasWorkflowIssue(issues []WorkflowIssue, code string) bool {
	for i := range issues {
		if issues[i].Code == code {
			return true
		}
	}
	return false
}

func workflowSourceNode(nodeType model.WorkflowNodeType) bool {
	return nodeType == model.WorkflowNodeTrigger || nodeType == model.WorkflowNodeManualRelease
}

func uniqueIssues(issues []WorkflowIssue) []WorkflowIssue {
	result := make([]WorkflowIssue, 0, len(issues))
	seen := make(map[string]struct{}, len(issues))
	for i := range issues {
		key := issues[i].Code + "\x00" + issues[i].NodeID + "\x00" + issues[i].EdgeID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, issues[i])
	}
	return result
}

func containsEvent(events []string, expected string) bool {
	for _, event := range events {
		if event == expected {
			return true
		}
	}
	return false
}

func workflowNodeSupportsManualRelease(node model.WorkflowNode) bool {
	if node.Type == model.WorkflowNodeTrigger {
		return containsEvent(node.Config.Events, "manual")
	}
	// 旧工作流和历史运行快照仍可能保存独立手动发布节点。
	return node.Type == model.WorkflowNodeManualRelease
}

func workflowHasManualReleaseSource(workflow *model.ReleaseWorkflow) bool {
	if workflow == nil {
		return false
	}
	for i := range workflow.Nodes {
		if workflowNodeSupportsManualRelease(workflow.Nodes[i]) {
			return true
		}
	}
	return false
}

func workflowHasApprovalNode(nodes []model.WorkflowNode) bool {
	for i := range nodes {
		if nodes[i].Type == model.WorkflowNodeApproval {
			return true
		}
	}
	return false
}

func workflowSnapshotJSON(workflow *model.ReleaseWorkflow) (string, error) {
	data, err := json.Marshal(workflowSnapshot{
		Nodes: workflow.Nodes, Edges: workflow.Edges,
		ApprovalEnabled: workflowHasApprovalNode(workflow.Nodes),
	})
	if err != nil {
		return "", fmt.Errorf("保存流水线快照失败: %w", err)
	}
	return string(data), nil
}

func (s *Service) newResolvedWorkflowRun(
	ctx context.Context,
	application *model.Application,
	workflow *model.ReleaseWorkflow,
	node model.WorkflowNode,
	trigger, ref, commitSHA, actorID, message string,
	now time.Time,
) (*model.PipelineRun, error) {
	resolved := *workflow
	resolved.Nodes = append([]model.WorkflowNode(nil), workflow.Nodes...)
	environments := make(map[string]model.ApplicationEnvironment, len(application.Environments))
	for i := range application.Environments {
		environments[application.Environments[i].Key] = application.Environments[i]
	}
	plans := make(map[string]model.DeploymentPlan)
	deploymentPlans := make(map[string]workflowDeploymentPlanSnapshot)
	deploymentTargets := make(map[string]workflowDeploymentTargetSnapshot)
	for i := range resolved.Nodes {
		current := &resolved.Nodes[i]
		if current.Type != model.WorkflowNodeDeploy {
			continue
		}
		planID := current.Config.DeploymentPlanID
		if planID == "" {
			planID = application.DeploymentPlanID
		}
		if planID == "" {
			if environment, ok := environments[current.Config.Environment]; ok {
				planID = environment.DeploymentPlanID
			}
		}
		plan, ok := plans[planID]
		if !ok {
			if planID == "" || s.db.WithContext(ctx).
				First(&plan, "id = ? AND is_active = ?", planID, true).Error != nil {
				return nil, ErrInvalidWorkflow
			}
			plans[planID] = plan
		}
		targetID := plan.DeploymentTargetID
		if targetID == "" {
			targetID = current.Config.DeploymentTargetID
		}
		if targetID == "" {
			if environment, ok := environments[current.Config.Environment]; ok {
				targetID = environment.DeploymentTargetID
			}
		}
		if targetID == "" {
			targetID = application.DeploymentTargetID
		}
		var target model.DeploymentTarget
		if targetID == "" || s.db.WithContext(ctx).
			First(&target, "id = ? AND is_active = ?", targetID, true).Error != nil ||
			!deploymentPlanSupportsTarget(plan.Kind, target.Platform) {
			return nil, ErrInvalidWorkflow
		}
		current.Config.DeploymentPlanID = plan.ID
		current.Config.DeploymentTargetID = target.ID
		deploymentPlans[current.ID] = workflowDeploymentPlanSnapshot{
			ID: plan.ID, Kind: plan.Kind, Script: plan.Script,
			HelmChart: plan.HelmChart, HelmValues: plan.HelmValues,
			ComposeFile: plan.ComposeFile, ServiceName: plan.ServiceName,
			TimeoutSeconds: plan.TimeoutSeconds,
		}
		deploymentTargets[current.ID] = workflowDeploymentTargetSnapshot{
			ID: target.ID, Name: target.Name, Platform: target.Platform,
			EnvironmentID: target.EnvironmentID, HostID: target.HostID, RuntimeID: target.RuntimeID,
			WorkingDirectory: target.WorkingDirectory, Namespace: target.Namespace,
			WorkloadName: target.WorkloadName, ContainerName: target.ContainerName,
			RolloutTimeout: target.RolloutTimeout,
		}
	}
	run, err := newWorkflowRun(application, &resolved, node, trigger, ref, commitSHA, actorID, message, now)
	if err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(workflowSnapshot{
		Nodes: resolved.Nodes, Edges: resolved.Edges,
		DeploymentPlans: deploymentPlans, DeploymentTargets: deploymentTargets,
		ApprovalEnabled: workflowHasApprovalNode(resolved.Nodes),
	})
	if err != nil {
		return nil, fmt.Errorf("保存流水线快照失败: %w", err)
	}
	run.WorkflowSnapshot = string(snapshot)
	return run, nil
}

func newWorkflowRun(application *model.Application, workflow *model.ReleaseWorkflow, node model.WorkflowNode, trigger, ref, commitSHA, actorID, message string, now time.Time) (*model.PipelineRun, error) {
	approvalRequired := workflowHasApprovalNode(workflow.Nodes)
	snapshot, err := workflowSnapshotJSON(workflow)
	if err != nil {
		return nil, err
	}
	return &model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: trigger,
		Ref: ref, CommitSHA: commitSHA, Status: model.PipelineRunDetected,
		Stage: string(node.Type), Environment: node.Config.Environment,
		WorkflowID: workflow.ID, WorkflowRevision: workflow.Revision,
		CurrentNodeID: node.ID, WorkflowSnapshot: snapshot,
		ApprovalRequired: approvalRequired,
		Message:          message, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func matchingWorkflowTriggers(workflow *model.ReleaseWorkflow, event, ref string) []model.WorkflowNode {
	if workflow == nil || !workflow.IsActive {
		return nil
	}
	result := make([]model.WorkflowNode, 0)
	for i := range workflow.Nodes {
		node := workflow.Nodes[i]
		if node.Type != model.WorkflowNodeTrigger || !containsEvent(node.Config.Events, event) {
			continue
		}
		if event == "tag" {
			tag := strings.TrimPrefix(ref, "refs/tags/")
			if matchTag(node.Config.TagPattern, tag) {
				result = append(result, node)
			}
			continue
		}
		branch := strings.TrimPrefix(ref, "refs/heads/")
		if matched, err := path.Match(node.Config.Branch, branch); err == nil && matched {
			result = append(result, node)
		}
	}
	return result
}

func applicationPollSources(application *model.Application) []model.WorkflowNode {
	if application.Workflow != nil && application.Workflow.IsActive {
		result := make([]model.WorkflowNode, 0)
		for i := range application.Workflow.Nodes {
			node := application.Workflow.Nodes[i]
			// Push 和 Tag 描述的是远端引用发生了什么变化，不限定变化必须由 Webhook 告知。
			// ZRT 主动读取远端引用，Webhook 只保留为可选的低延迟通道。
			if node.Type == model.WorkflowNodeTrigger &&
				(containsEvent(node.Config.Events, "pull") || containsEvent(node.Config.Events, "push") ||
					containsEvent(node.Config.Events, "pr") || containsEvent(node.Config.Events, "tag")) {
				result = append(result, node)
			}
		}
		return result
	}
	result := make([]model.WorkflowNode, 0, len(application.Environments))
	for i := range application.Environments {
		environment := application.Environments[i]
		if !environment.PollEnabled && !environment.WatchPush && !environment.WatchPullRequest && !environment.WatchTags {
			continue
		}
		result = append(result, model.WorkflowNode{
			ID: "legacy-trigger-" + environment.Key, Type: model.WorkflowNodeTrigger,
			Name: environment.Name + "代码", Config: model.WorkflowNodeConfig{
				Environment: environment.Key, Branch: environment.Branch,
				Events: environmentEvents(environment), TagPattern: environment.TagPattern,
			},
		})
	}
	return result
}

func applicationEventSources(application *model.Application, event, ref string) []model.WorkflowNode {
	if application.Workflow != nil && application.Workflow.IsActive {
		return matchingWorkflowTriggers(application.Workflow, event, ref)
	}
	result := make([]model.WorkflowNode, 0, len(application.Environments))
	for i := range application.Environments {
		environment := application.Environments[i]
		enabled := (event == "push" && environment.WatchPush) ||
			(event == "pr" && environment.WatchPullRequest) ||
			(event == "tag" && environment.WatchTags)
		if !enabled {
			continue
		}
		node := model.WorkflowNode{
			ID: "legacy-trigger-" + environment.Key, Type: model.WorkflowNodeTrigger,
			Name: environment.Name + "代码", Config: model.WorkflowNodeConfig{
				Environment: environment.Key, Branch: environment.Branch,
				Events: environmentEvents(environment), TagPattern: environment.TagPattern,
			},
		}
		if len(matchingWorkflowTriggers(&model.ReleaseWorkflow{IsActive: true, Nodes: []model.WorkflowNode{node}}, event, ref)) == 1 {
			result = append(result, node)
		}
	}
	return result
}

func workflowEventName(repositoryEvent string) string {
	return map[string]string{"branch_push": "push", "pull_request": "pr", "tag_push": "tag"}[repositoryEvent]
}

type workflowPollCandidate struct {
	Event  string
	Ref    string
	Commit string
}

func workflowPollCandidates(source model.WorkflowNode, refs repository.RefResult) []workflowPollCandidate {
	result := make([]workflowPollCandidate, 0)
	// pull 是旧版本“定时拉取”的兼容标识，与分支变更使用同一个游标，避免重复运行。
	if containsEvent(source.Config.Events, "pull") || containsEvent(source.Config.Events, "push") {
		for i := range refs.Branches {
			if matched, err := path.Match(source.Config.Branch, refs.Branches[i].Name); err == nil && matched {
				result = append(result, workflowPollCandidate{
					Event: "push", Ref: "refs/heads/" + refs.Branches[i].Name, Commit: refs.Branches[i].SHA,
				})
			}
		}
	}
	if containsEvent(source.Config.Events, "pr") {
		for i := range refs.PullRequests {
			pullRequest := refs.PullRequests[i]
			branch := pullRequest.TargetBranch
			if branch == "" {
				branch = pullRequest.SourceBranch
			}
			if branch == "" {
				branch = branchForCommit(refs.Branches, pullRequest.SHA)
			}
			if matched, err := path.Match(source.Config.Branch, branch); err == nil && matched {
				result = append(result, workflowPollCandidate{Event: "pr", Ref: pullRequest.Ref, Commit: pullRequest.SHA})
			}
		}
	}
	if containsEvent(source.Config.Events, "tag") {
		for i := range refs.Tags {
			if matchTag(source.Config.TagPattern, refs.Tags[i].Name) {
				result = append(result, workflowPollCandidate{
					Event: "tag", Ref: "refs/tags/" + refs.Tags[i].Name, Commit: refs.Tags[i].SHA,
				})
			}
		}
	}
	return result
}

func branchForCommit(branches []repository.GitRef, commit string) string {
	for i := range branches {
		if branches[i].SHA == commit {
			return branches[i].Name
		}
	}
	return ""
}

func polledRunTrigger(event string) string {
	return map[string]string{"push": "poll_push", "pr": "poll_pr", "tag": "poll_tag"}[event]
}

func (s *Service) runFromSource(ctx context.Context, application *model.Application, source model.WorkflowNode, trigger, ref, commitSHA, actorID, message string, now time.Time) (*model.PipelineRun, error) {
	if application.Workflow != nil && application.Workflow.IsActive {
		return s.newResolvedWorkflowRun(ctx, application, application.Workflow, source, trigger, ref, commitSHA, actorID, message, now)
	}
	return &model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: trigger,
		Ref: ref, CommitSHA: commitSHA, Status: model.PipelineRunDetected,
		Stage: "source", Environment: source.Config.Environment,
		Message: message, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func parseWorkflowSnapshot(run *model.PipelineRun) (*workflowSnapshot, error) {
	var snapshot workflowSnapshot
	if run.WorkflowSnapshot == "" || json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot) != nil {
		return nil, ErrInvalidWorkflowTransition
	}
	snapshot.ApprovalEnabled = workflowHasApprovalNode(snapshot.Nodes)
	return &snapshot, nil
}

func (s *Service) DeleteRun(ctx context.Context, runID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, "id = ?", runID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPipelineRunNotFound
			}
			return fmt.Errorf("读取流水线运行失败: %w", err)
		}
		if run.ReleasePlanExecutionID != "" {
			return ErrPipelineRunManagedByReleasePlan
		}
		if err := tx.Where("pipeline_run_id = ?", run.ID).Delete(&model.PipelineRunApproval{}).Error; err != nil {
			return fmt.Errorf("删除流水线运行审核记录失败: %w", err)
		}
		if err := tx.Where("pipeline_run_id = ?", run.ID).Delete(&model.PipelineRunLog{}).Error; err != nil {
			return fmt.Errorf("删除流水线运行日志失败: %w", err)
		}
		if err := tx.Where("pipeline_run_id = ?", run.ID).Delete(&model.PipelineRunRepository{}).Error; err != nil {
			return fmt.Errorf("删除流水线运行仓库快照失败: %w", err)
		}
		if err := tx.Delete(&run).Error; err != nil {
			return fmt.Errorf("删除流水线运行失败: %w", err)
		}
		return nil
	})
}

// RetryRun 保留失败记录，并使用相同代码版本和应用当前有效配置创建一条新的可审计运行。
func (s *Service) RetryRun(ctx context.Context, runID, actorID string) (*model.PipelineRun, error) {
	var failed model.PipelineRun
	if err := s.db.WithContext(ctx).First(&failed, "id = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPipelineRunNotFound
		}
		return nil, fmt.Errorf("读取待重新执行的流水线运行失败: %w", err)
	}
	if failed.Status != model.PipelineRunFailed {
		return nil, ErrPipelineRunNotRetryable
	}
	if failed.Ref == "" || failed.CommitSHA == "" {
		return nil, ErrManualCommitRequired
	}
	application, err := s.FindApplication(ctx, failed.ApplicationID)
	if err != nil {
		return nil, err
	}
	if !application.IsActive || !pipelineExecutionConfigured(application) {
		return nil, fmt.Errorf("%w：%s", ErrPipelineIncomplete, pipelineExecutionIncompleteMessage(application))
	}
	if application.Workflow == nil || !application.Workflow.IsActive {
		return nil, ErrWorkflowNotActive
	}
	source := retryWorkflowSource(application.Workflow, &failed)
	if source == nil {
		return nil, ErrInvalidWorkflow
	}
	now := time.Now().UTC()
	run, err := s.newResolvedWorkflowRun(
		ctx,
		application, application.Workflow, *source, "retry", failed.Ref, failed.CommitSHA,
		actorID, "重新执行失败运行 "+failed.ID, now,
	)
	if err != nil {
		return nil, err
	}
	run.RetryOfID = failed.ID
	run.CommitMessage = failed.CommitMessage
	components := pipelineRunRepositories(application, run.ID, run.Ref, run.CommitSHA, now)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		return tx.Create(&components).Error
	}); err != nil {
		return nil, fmt.Errorf("创建重新执行的流水线运行失败: %w", err)
	}
	run.Repositories = components
	return s.AdvanceRun(ctx, run.ID, actorID, "")
}

func (s *Service) AdvanceRun(ctx context.Context, runID, actorID, targetNodeID string) (*model.PipelineRun, error) {
	s.pipelineAdvanceMu.Lock()
	defer s.pipelineAdvanceMu.Unlock()
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidWorkflowTransition
		}
		return nil, err
	}
	if err := s.ensureReleasePlanRunAdvanceAllowed(ctx, &run); err != nil {
		return nil, err
	}
	return s.advanceLoadedRun(ctx, &run, actorID, targetNodeID)
}

// advanceRunIfCurrent 只在运行仍处于调用方观察到的节点和状态时推进。
// 调和器借此避免在用户已经进入人工放行节点后，再依据过期状态多推进一步。
func (s *Service) advanceRunIfCurrent(
	ctx context.Context,
	expected model.PipelineRun,
	actorID, targetNodeID string,
) (*model.PipelineRun, bool, error) {
	s.pipelineAdvanceMu.Lock()
	defer s.pipelineAdvanceMu.Unlock()
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", expected.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrInvalidWorkflowTransition
		}
		return nil, false, err
	}
	if run.Status != expected.Status || run.Stage != expected.Stage || run.CurrentNodeID != expected.CurrentNodeID ||
		!run.UpdatedAt.Equal(expected.UpdatedAt) {
		return &run, false, nil
	}
	if err := s.ensureReleasePlanRunAdvanceAllowed(ctx, &run); err != nil {
		return nil, false, err
	}
	advanced, err := s.advanceLoadedRun(ctx, &run, actorID, targetNodeID)
	return advanced, true, err
}

func (s *Service) ensureReleasePlanRunAdvanceAllowed(ctx context.Context, run *model.PipelineRun) error {
	if run.ReleasePlanExecutionID == "" && run.ReleasePlanExecutionItemID == "" {
		return nil
	}
	if run.ReleasePlanExecutionID == "" || run.ReleasePlanExecutionItemID == "" {
		return ErrPipelineRunAwaitingReleasePlan
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.ReleasePlanExecutionItem{}).
		Where(
			"id = ? AND release_plan_execution_id = ? AND pipeline_run_id = ? AND status = ?",
			run.ReleasePlanExecutionItemID, run.ReleasePlanExecutionID, run.ID, model.ReleasePlanExecutionItemRunning,
		).Count(&count).Error; err != nil {
		if s.logger != nil {
			s.logger.Error("检查发布计划流水线调度状态失败", "operation", "pipeline_advance_release_plan_guard", "pipeline_run_id", run.ID, "err", err)
		}
		return ErrPipelineRunAwaitingReleasePlan
	}
	if count != 1 {
		return ErrPipelineRunAwaitingReleasePlan
	}
	return nil
}

func (s *Service) advanceLoadedRun(ctx context.Context, run *model.PipelineRun, actorID, targetNodeID string) (*model.PipelineRun, error) {
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		return nil, err
	}
	nodes := make(map[string]model.WorkflowNode, len(snapshot.Nodes))
	for i := range snapshot.Nodes {
		nodes[snapshot.Nodes[i].ID] = snapshot.Nodes[i]
	}
	current, ok := nodes[run.CurrentNodeID]
	if !ok {
		return nil, ErrInvalidWorkflowTransition
	}
	if current.Type == model.WorkflowNodeApproval && snapshot.ApprovalEnabled {
		var approvalCount int64
		if err := s.db.WithContext(ctx).Model(&model.PipelineRunApproval{}).
			Where("pipeline_run_id = ? AND node_id = ?", run.ID, current.ID).Count(&approvalCount).Error; err != nil {
			return nil, err
		}
		if approvalCount == 0 {
			return nil, ErrWorkflowApprovalRequired
		}
	}
	if current.Type == model.WorkflowNodeDeploy && run.Stage != "deploy_succeeded" {
		if run.Status == model.PipelineRunRunning {
			return nil, ErrPipelineExecutionRunning
		}
		return s.enqueueDeployExecution(ctx, run, current)
	}
	targets := make([]string, 0)
	for i := range snapshot.Edges {
		if snapshot.Edges[i].Source == current.ID {
			targets = append(targets, snapshot.Edges[i].Target)
		}
	}
	if len(targets) == 0 {
		if current.Type != model.WorkflowNodeDeploy || run.Stage != "deploy_succeeded" {
			return nil, ErrInvalidWorkflowTransition
		}
		now := time.Now().UTC()
		message := "当前节点：" + current.Name + "；状态：已完成"
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(run).Updates(map[string]any{
				"status": model.PipelineRunSucceeded, "stage": "completed",
				"message": message, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			return tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", run.ID).
				Updates(map[string]any{"status": model.PipelineRunRepositorySucceeded, "updated_at": now}).Error
		}); err != nil {
			return nil, err
		}
		run.Status, run.Stage, run.Message, run.UpdatedAt = model.PipelineRunSucceeded, "completed", message, now
		return run, nil
	}
	if targetNodeID == "" && len(targets) == 1 {
		targetNodeID = targets[0]
	}
	allowed := false
	for _, id := range targets {
		if id == targetNodeID {
			allowed = true
			break
		}
	}
	target, ok := nodes[targetNodeID]
	if !allowed || !ok {
		return nil, ErrInvalidWorkflowTransition
	}
	status, stage, message := model.PipelineRunRunning, string(target.Type), "已进入“"+target.Name+"”"
	if target.Type == model.WorkflowNodeApproval && snapshot.ApprovalEnabled {
		status, message = model.PipelineRunAwaitingApproval, "等待其他成员审核"
	}
	if target.Type == model.WorkflowNodeDeploy {
		run.CurrentNodeID, run.Environment = target.ID, target.Config.Environment
		return s.enqueueDeployExecution(ctx, run, target)
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"current_node_id": target.ID, "environment": target.Config.Environment,
		"status": status, "stage": stage, "message": message, "updated_at": now,
	}
	if err := s.db.WithContext(ctx).Model(run).Updates(updates).Error; err != nil {
		return nil, err
	}
	run.CurrentNodeID, run.Environment, run.Status = target.ID, target.Config.Environment, status
	run.Stage, run.Message, run.UpdatedAt = stage, message, now
	_ = actorID
	return run, nil
}

func (s *Service) enqueueDeployExecution(ctx context.Context, run *model.PipelineRun, node model.WorkflowNode) (*model.PipelineRun, error) {
	if node.Config.DeploymentTargetID == "" {
		message := "部署节点“" + node.Name + "”没有配置部署到哪里，流水线没有执行"
		if err := s.failExecution(ctx, run.ID, message, ErrPipelineExecutionConfig); err != nil && !errors.Is(err, ErrPipelineExecutionConfig) {
			return nil, err
		}
		run.Status, run.Stage, run.Message = model.PipelineRunFailed, "failed", message
		run.UpdatedAt = time.Now().UTC()
		return run, nil
	}
	now := time.Now().UTC()
	var jobID string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := task.NewService(tx, 1).Create(ctx, task.CreateInput{
			Kind: "pipeline.deploy", Subject: "zrt.task.pipeline.deploy",
			Payload:        DeployTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID},
			IdempotencyKey: "pipeline:" + run.ID + ":deploy:" + node.ID,
			MaxAttempts:    1, Idempotent: false,
		})
		if err != nil {
			return err
		}
		jobID = job.ID
		result := tx.Model(&model.PipelineRun{}).Where("id = ? AND status <> ?", run.ID, model.PipelineRunSucceeded).
			Updates(map[string]any{
				"current_node_id": node.ID, "environment": node.Config.Environment,
				"status": model.PipelineRunRunning, "stage": "queued",
				"execution_job_id": job.ID, "deployment_id": "", "image": "",
				"message": "当前节点：" + node.Name + "；状态：等待执行", "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	run.CurrentNodeID, run.Environment = node.ID, node.Config.Environment
	run.Status, run.Stage = model.PipelineRunRunning, "queued"
	run.ExecutionJobID, run.DeploymentID, run.Image = jobID, "", ""
	run.Message, run.UpdatedAt = "当前节点："+node.Name+"；状态：等待执行", now
	return run, nil
}

func (s *Service) ApproveRun(ctx context.Context, runID, actorID string) (*model.PipelineRun, error) {
	var run model.PipelineRun
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&run, "id = ?", runID).Error; err != nil {
			return ErrInvalidWorkflowTransition
		}
		snapshot, err := parseWorkflowSnapshot(&run)
		if err != nil {
			return err
		}
		var current *model.WorkflowNode
		for i := range snapshot.Nodes {
			if snapshot.Nodes[i].ID == run.CurrentNodeID {
				current = &snapshot.Nodes[i]
				break
			}
		}
		if current == nil || current.Type != model.WorkflowNodeApproval || !snapshot.ApprovalEnabled || run.Status != model.PipelineRunAwaitingApproval {
			return ErrInvalidWorkflowTransition
		}
		if run.CreatedBy != "" && run.CreatedBy == actorID {
			return ErrWorkflowSelfApproval
		}
		now := time.Now().UTC()
		approval := model.PipelineRunApproval{
			ID: uuid.NewString(), PipelineRunID: run.ID, NodeID: current.ID,
			RequestedBy: run.CreatedBy, ApprovedBy: actorID, ApprovedAt: now,
		}
		if err := tx.Create(&approval).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrInvalidWorkflowTransition
			}
			return err
		}
		result := tx.Model(&model.PipelineRun{}).
			Where("id = ? AND status = ?", run.ID, model.PipelineRunAwaitingApproval).
			Updates(map[string]any{
				"status": model.PipelineRunRunning, "approved_by": actorID, "approved_at": now,
				"message": "审核通过", "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		run.Status, run.ApprovedBy, run.ApprovedAt = model.PipelineRunRunning, &actorID, &now
		run.Message, run.UpdatedAt = "审核通过", now
		return nil
	})
	if err != nil {
		return nil, err
	}
	advanceContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	advanced, err := s.AdvanceRun(advanceContext, run.ID, actorID, "")
	if err == nil {
		return advanced, nil
	}
	cause := fmt.Errorf("审核节点 %s 通过后自动推进失败: %w", run.CurrentNodeID, err)
	return nil, s.failExecution(advanceContext, run.ID, "审核通过后未能进入下一节点", cause)
}
