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
)

var (
	ErrInvalidWorkflow            = errors.New("流水线配置存在错误，请先修正节点和连线")
	ErrWorkflowNotFound           = errors.New("应用流水线不存在")
	ErrWorkflowRevisionConflict   = errors.New("应用流水线已被其他人修改，请刷新后再保存")
	ErrWorkflowNotActive          = errors.New("应用流水线尚未启用")
	ErrInvalidWorkflowTransition  = errors.New("当前节点不能这样推进")
	ErrWorkflowApprovalRequired   = errors.New("该发布计划需要先完成审核")
	ErrWorkflowSelfApproval       = errors.New("发布申请人不能审核自己的发布计划")
	ErrPipelineRunNotFound        = errors.New("发布计划不存在")
	ErrPipelineRunDeleteForbidden = errors.New("执行中或等待审核的发布计划不能删除")
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
	Nodes           []model.WorkflowNode `json:"nodes"`
	Edges           []model.WorkflowEdge `json:"edges"`
	ApprovalEnabled bool                 `json:"approval_enabled"`
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
	if input.Activate && len(issues) > 0 {
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
				"revision": existing.Revision + 1, "updated_by": actorID, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkflowRevisionConflict
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
		nodes = append(nodes,
			model.WorkflowNode{ID: triggerID, Type: model.WorkflowNodeTrigger, Name: environment.Name + "代码", Position: model.WorkflowPosition{X: x, Y: 80}, Config: model.WorkflowNodeConfig{
				Environment: environment.Key, Branch: environment.Branch, Events: events, TagPattern: environment.TagPattern,
			}},
			model.WorkflowNode{ID: deployID, Type: model.WorkflowNodeDeploy, Name: "部署到" + environment.Name, Position: model.WorkflowPosition{X: x, Y: 350}, Config: model.WorkflowNodeConfig{
				Environment: environment.Key, ReleasePlanID: environment.ReleasePlanID,
				DeploymentTargetID: environment.DeploymentTargetID,
			}},
		)
		entryIDs[environment.Key] = deployID
		deployIDs = append(deployIDs, deployID)
	}
	if application.ReleaseApprovalEnabled {
		for i := range environments {
			if environments[i].Key != "prod" {
				continue
			}
			approvalID := "approval-prod"
			nodes = append(nodes, model.WorkflowNode{
				ID: approvalID, Type: model.WorkflowNodeApproval, Name: "生产发布审核",
				Position: model.WorkflowPosition{X: 120 + float64(i)*440, Y: 220},
				Config:   model.WorkflowNodeConfig{Environment: "prod", Description: "由发布申请人之外的成员审核"},
			})
			entryIDs["prod"] = approvalID
			edges = append(edges, model.WorkflowEdge{ID: uuid.NewString(), Source: approvalID, Target: "deploy-prod"})
		}
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
		input.Nodes[i].Config.ReleasePlanID = strings.TrimSpace(input.Nodes[i].Config.ReleasePlanID)
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
				issues = append(issues, WorkflowIssue{Code: "missing_event", Message: "触发节点至少选择一种监听事件", NodeID: node.ID})
			}
			for _, event := range node.Config.Events {
				if event != "pull" && event != "push" && event != "pr" && event != "tag" {
					issues = append(issues, WorkflowIssue{Code: "invalid_event", Message: "触发节点包含未知事件", NodeID: node.ID})
				}
			}
			if node.Config.Branch == "" && !containsEvent(node.Config.Events, "tag") {
				issues = append(issues, WorkflowIssue{Code: "missing_branch", Message: "触发节点需要填写监听分支", NodeID: node.ID})
			}
			if node.Config.TagPattern != "" {
				if _, err := path.Match(node.Config.TagPattern, "v1.0.0"); err != nil {
					issues = append(issues, WorkflowIssue{Code: "invalid_tag_pattern", Message: "Tag 匹配规则无效", NodeID: node.ID})
				}
			}
		case model.WorkflowNodeManualRelease:
			sourceCount++
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
				issues = append(issues, WorkflowIssue{Code: "invalid_environment", Message: "部署节点没有绑定已启用的环境", NodeID: node.ID})
			}
			if !s.activeResourceExists(ctx, &model.ReleasePlan{}, node.Config.ReleasePlanID) {
				issues = append(issues, WorkflowIssue{Code: "missing_release_plan", Message: "部署节点需要绑定可用的部署方案", NodeID: node.ID})
			}
		default:
			issues = append(issues, WorkflowIssue{Code: "invalid_node_type", Message: "节点类型无效", NodeID: node.ID})
		}
	}
	if sourceCount == 0 {
		issues = append(issues, WorkflowIssue{Code: "missing_trigger", Message: "流水线至少需要一个代码触发或手动发布节点"})
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
		if workflowSourceNode(node.Type) && indegree[id] > 0 {
			issues = append(issues, WorkflowIssue{Code: "trigger_has_upstream", Message: "代码触发和手动发布节点不能有上游连线", NodeID: id})
		}
		if len(outgoing[id]) == 0 && node.Type != model.WorkflowNodeDeploy {
			issues = append(issues, WorkflowIssue{Code: "invalid_terminal", Message: "流程的最后一个节点必须是部署节点", NodeID: id})
		}
		if !workflowSourceNode(node.Type) && indegree[id] == 0 {
			issues = append(issues, WorkflowIssue{Code: "unreachable_node", Message: "节点没有上游入口", NodeID: id})
		}
	}
	if application.ReleaseApprovalEnabled && visited == len(nodeByID) {
		issues = append(issues, approvalPathIssues(nodeByID, outgoing)...)
	}
	return uniqueIssues(issues)
}

func approvalPathIssues(nodes map[string]model.WorkflowNode, outgoing map[string][]string) []WorkflowIssue {
	issues := make([]WorkflowIssue, 0)
	type state struct {
		id       string
		approved bool
	}
	queue := make([]state, 0)
	for id, node := range nodes {
		if workflowSourceNode(node.Type) {
			queue = append(queue, state{id: id})
		}
	}
	seen := make(map[state]struct{})
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}
		node := nodes[current.id]
		approved := current.approved || node.Type == model.WorkflowNodeApproval
		if node.Type == model.WorkflowNodeDeploy && node.Config.Environment == "prod" && !approved {
			issues = append(issues, WorkflowIssue{Code: "approval_required", Message: "生产部署的每条路径都必须经过审核节点", NodeID: node.ID})
		}
		for _, target := range outgoing[current.id] {
			queue = append(queue, state{id: target, approved: approved})
		}
	}
	return issues
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

func (s *Service) activeResourceExists(ctx context.Context, target any, id string) bool {
	if id == "" {
		return false
	}
	var count int64
	return s.db.WithContext(ctx).Model(target).Where("id = ? AND is_active = ?", id, true).Count(&count).Error == nil && count == 1
}

func containsEvent(events []string, expected string) bool {
	for _, event := range events {
		if event == expected {
			return true
		}
	}
	return false
}

func workflowSnapshotJSON(workflow *model.ReleaseWorkflow, approvalEnabled bool) (string, error) {
	data, err := json.Marshal(workflowSnapshot{Nodes: workflow.Nodes, Edges: workflow.Edges, ApprovalEnabled: approvalEnabled})
	if err != nil {
		return "", fmt.Errorf("保存流水线快照失败: %w", err)
	}
	return string(data), nil
}

func newWorkflowRun(application *model.Application, workflow *model.ReleaseWorkflow, node model.WorkflowNode, trigger, ref, commitSHA, actorID, message string, now time.Time) (*model.PipelineRun, error) {
	snapshot, err := workflowSnapshotJSON(workflow, application.ReleaseApprovalEnabled)
	if err != nil {
		return nil, err
	}
	return &model.PipelineRun{
		ID: uuid.NewString(), ApplicationID: application.ID, Trigger: trigger,
		Ref: ref, CommitSHA: commitSHA, Status: model.PipelineRunDetected,
		Stage: string(node.Type), Environment: node.Config.Environment,
		WorkflowID: workflow.ID, WorkflowRevision: workflow.Revision,
		CurrentNodeID: node.ID, WorkflowSnapshot: snapshot,
		ApprovalRequired: application.ReleaseApprovalEnabled,
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
			if node.Type == model.WorkflowNodeTrigger && containsEvent(node.Config.Events, "pull") {
				result = append(result, node)
			}
		}
		return result
	}
	result := make([]model.WorkflowNode, 0, len(application.Environments))
	for i := range application.Environments {
		environment := application.Environments[i]
		if !environment.PollEnabled {
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

func selectWorkflowRef(source model.WorkflowNode, refs repository.RefResult) (string, string) {
	if containsEvent(source.Config.Events, "tag") && source.Config.TagPattern != "" {
		for i := len(refs.Tags) - 1; i >= 0; i-- {
			if matchTag(source.Config.TagPattern, refs.Tags[i].Name) {
				return "refs/tags/" + refs.Tags[i].Name, refs.Tags[i].SHA
			}
		}
	}
	for i := range refs.Branches {
		if matched, err := path.Match(source.Config.Branch, refs.Branches[i].Name); err == nil && matched {
			return "refs/heads/" + refs.Branches[i].Name, refs.Branches[i].SHA
		}
	}
	return "", ""
}

func (s *Service) runFromSource(application *model.Application, source model.WorkflowNode, trigger, ref, commitSHA, actorID, message string, now time.Time) (*model.PipelineRun, error) {
	if application.Workflow != nil && application.Workflow.IsActive {
		return newWorkflowRun(application, application.Workflow, source, trigger, ref, commitSHA, actorID, message, now)
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
	return &snapshot, nil
}

func (s *Service) DeleteRun(ctx context.Context, runID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.PipelineRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, "id = ?", runID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPipelineRunNotFound
			}
			return fmt.Errorf("读取发布计划失败: %w", err)
		}
		if run.Status == model.PipelineRunRunning || run.Status == model.PipelineRunAwaitingApproval {
			return ErrPipelineRunDeleteForbidden
		}
		if err := tx.Where("pipeline_run_id = ?", run.ID).Delete(&model.PipelineRunApproval{}).Error; err != nil {
			return fmt.Errorf("删除发布计划审核记录失败: %w", err)
		}
		if err := tx.Delete(&run).Error; err != nil {
			return fmt.Errorf("删除发布计划失败: %w", err)
		}
		return nil
	})
}

func (s *Service) AdvanceRun(ctx context.Context, runID, actorID, targetNodeID string) (*model.PipelineRun, error) {
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidWorkflowTransition
		}
		return nil, err
	}
	snapshot, err := parseWorkflowSnapshot(&run)
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
	targets := make([]string, 0)
	for i := range snapshot.Edges {
		if snapshot.Edges[i].Source == current.ID {
			targets = append(targets, snapshot.Edges[i].Target)
		}
	}
	if len(targets) == 0 {
		if current.Type != model.WorkflowNodeDeploy {
			return nil, ErrInvalidWorkflowTransition
		}
		now := time.Now().UTC()
		if err := s.db.WithContext(ctx).Model(&run).Updates(map[string]any{
			"status": model.PipelineRunSucceeded, "stage": "completed",
			"message": "发布计划已完成", "updated_at": now,
		}).Error; err != nil {
			return nil, err
		}
		run.Status, run.Stage, run.Message, run.UpdatedAt = model.PipelineRunSucceeded, "completed", "发布计划已完成", now
		return &run, nil
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
		status, message = model.PipelineRunReady, "已到达部署节点，等待执行部署方案"
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"current_node_id": target.ID, "environment": target.Config.Environment,
		"status": status, "stage": stage, "message": message, "updated_at": now,
	}
	if err := s.db.WithContext(ctx).Model(&run).Updates(updates).Error; err != nil {
		return nil, err
	}
	run.CurrentNodeID, run.Environment, run.Status = target.ID, target.Config.Environment, status
	run.Stage, run.Message, run.UpdatedAt = stage, message, now
	_ = actorID
	return &run, nil
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
				"message": "审核已通过，可以继续推进", "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidWorkflowTransition
		}
		run.Status, run.ApprovedBy, run.ApprovedAt = model.PipelineRunRunning, &actorID, &now
		run.Message, run.UpdatedAt = "审核已通过，可以继续推进", now
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &run, nil
}
