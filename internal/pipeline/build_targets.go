package pipeline

import (
	"fmt"
	"log/slog"
	"strings"

	"edo/internal/model"
	"github.com/google/uuid"
)

// target 名称由 Dockerfile 定义；这里只拆分有界列表，实际阶段校验交给 Docker 构建器。
func buildTargetStages(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	targets := strings.Split(value, ",")
	seen := map[string]bool{}
	if len(value) > 128 || len(targets) > 16 {
		return nil, ErrInvalidBuildPlan
	}
	for i, target := range targets {
		target = strings.TrimSpace(target)
		key := strings.ToLower(target)
		if !buildTargetStagePattern.MatchString(target) || seen[key] {
			return nil, ErrInvalidBuildPlan
		}
		seen[key] = true
		targets[i] = target
	}
	return targets, nil
}

// 每个 target 复用独立构建任务的权限、日志、幂等登记与失败处理，不改写流水线定义。
func expandBuildTargets(workflow *model.ReleaseWorkflow, plans map[string]workflowBuildPlanSnapshot, bindings map[string][]model.DeliveryVariableBinding) error {
	sources := map[string][]string{}
	ids := map[string]bool{}
	for _, node := range workflowTasks(workflow.Stages) {
		ids[node.ID] = true
	}
	count := 0
	for i := range workflow.Stages {
		var tasks []model.WorkflowNode
		for _, node := range workflow.Stages[i].Tasks {
			plan, ok := plans[node.ID]
			if !ok || plan.Kind != model.BuildPlanDockerfile {
				tasks = append(tasks, node)
				continue
			}
			targets, err := buildTargetStages(plan.TargetStage)
			if err != nil {
				return buildTargetsError(node.ID, err)
			}
			if len(targets) < 2 {
				tasks = append(tasks, node)
				continue
			}
			for index, target := range targets {
				copy := cloneWorkflowStages([]model.WorkflowStage{{Tasks: []model.WorkflowNode{node}}})[0].Tasks[0]
				if index > 0 {
					copy.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("edo.workflow.build.target:"+node.ID+":"+target)).String()
					if ids[copy.ID] {
						return buildTargetsError(node.ID, ErrInvalidWorkflow)
					}
					ids[copy.ID] = true
				}
				name := []rune(node.Name + " · " + target)
				if len(name) > 128 {
					name = name[:128]
				}
				copy.Name = string(name)
				copy.Config.ImageName = strings.ToLower(target)
				if copy.Config.InlineBuild != nil {
					copy.Config.InlineBuild.TargetStage = target
				}
				current := plan
				current.TargetStage = target
				plans[copy.ID] = current
				if values, exists := bindings[node.ID]; exists {
					bindings[copy.ID] = append([]model.DeliveryVariableBinding(nil), values...)
				}
				sources[node.ID] = append(sources[node.ID], copy.ID)
				tasks = append(tasks, copy)
			}
		}
		count += len(tasks)
		if count > 200 {
			return buildTargetsError("", ErrInvalidWorkflow)
		}
		workflow.Stages[i].Tasks = tasks
	}
	for i := range workflow.Stages {
		for j := range workflow.Stages[i].Tasks {
			node := &workflow.Stages[i].Tasks[j]
			var expanded []string
			for _, id := range node.Config.ArtifactNodeIDs {
				if children := sources[id]; len(children) > 0 {
					expanded = append(expanded, children...)
				} else {
					expanded = append(expanded, id)
				}
			}
			if node.Config.ArtifactNodeIDs != nil {
				node.Config.ArtifactNodeIDs = expanded
			}
		}
	}
	return nil
}

func buildTargetsError(nodeID string, err error) error {
	slog.Warn("解析 Docker 构建阶段失败", "operation", "workflow_build_targets", "node_id", nodeID, "err", err)
	return fmt.Errorf("%w：Docker target 无效、重复或展开后的任务数量超过限制", ErrInvalidWorkflow)
}
