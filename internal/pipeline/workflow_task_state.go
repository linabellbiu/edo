package pipeline

import (
	"fmt"

	"edo/internal/model"
)

// 任务开关默认开启；缺少 disabled 的既有配置继续沿用原执行行为。
func enabledWorkflowStages(stages []model.WorkflowStage) []model.WorkflowStage {
	result := make([]model.WorkflowStage, 0, len(stages))
	for _, stage := range stages {
		tasks := make([]model.WorkflowNode, 0, len(stage.Tasks))
		for _, node := range stage.Tasks {
			if !node.Disabled {
				tasks = append(tasks, node)
			}
		}
		if len(tasks) > 0 {
			stage.Tasks = tasks
			result = append(result, stage)
		}
	}
	return result
}

func workflowStructureIssues(source model.WorkflowNode, stages []model.WorkflowStage) []WorkflowIssue {
	issues := make([]WorkflowIssue, 0)
	ids, stageIDs := map[string]bool{source.ID: true}, map[string]bool{}
	for _, stage := range stages {
		if stageIDs[stage.ID] {
			issues = append(issues, WorkflowIssue{Code: "duplicate_stage", Message: "阶段标识重复", StageID: stage.ID})
		}
		stageIDs[stage.ID] = true
		if len(stage.Tasks) == 0 {
			issues = append(issues, WorkflowIssue{Code: "empty_stage", Message: "阶段至少需要一个任务", StageID: stage.ID})
		}
		for _, node := range stage.Tasks {
			if ids[node.ID] {
				issues = append(issues, WorkflowIssue{Code: "duplicate_node", Message: "任务标识重复", NodeID: node.ID, StageID: stage.ID})
			}
			ids[node.ID] = true
		}
	}
	return append(issues, workflowDisabledDependencyIssues(stages)...)
}

func workflowDisabledDependencyIssues(stages []model.WorkflowStage) []WorkflowIssue {
	disabled := map[string]string{}
	for _, node := range workflowTasks(stages) {
		if node.Disabled {
			disabled[node.ID] = node.Name
		}
	}
	issues := []WorkflowIssue{}
	for _, stage := range stages {
		for _, node := range stage.Tasks {
			if node.Disabled {
				continue
			}
			references := append([]string{node.Config.ArtifactNodeID}, node.Config.ArtifactNodeIDs...)
			if source := node.Config.ComposeSource; source != nil && source.Kind == "upstream" {
				references = append(references, source.ArtifactNodeID)
			}
			for _, input := range node.Config.DeploymentInputs {
				if input.Kind == "upstream" {
					references = append(references, input.ArtifactNodeID)
				}
			}
			for _, id := range references {
				if name, closed := disabled[id]; closed {
					issues = append(issues, WorkflowIssue{Code: "disabled_upstream_task", Message: fmt.Sprintf("依赖的任务“%s”已关闭，请开启该任务或修改引用", name), NodeID: node.ID, StageID: stage.ID})
				}
			}
		}
	}
	return issues
}

func workflowActiveAfterSave(active bool, input WorkflowInput) bool {
	if input.SaveOnly || input.TaskID != "" {
		return active
	}
	return input.Activate
}

// 任务面板只保存当前任务；其他任务、代码源、阶段名称和排序使用数据库版本。
// 新任务所在阶段尚未保存时，仅带入该阶段及当前任务，不提交其他新任务。
func isolateWorkflowTaskSave(input *WorkflowInput, source model.WorkflowNode, saved []model.WorkflowStage) error {
	var selected model.WorkflowNode
	var selectedStage model.WorkflowStage
	matches := 0
	for _, stage := range input.Stages {
		for _, node := range stage.Tasks {
			if node.ID == input.TaskID {
				selected, selectedStage = node, stage
				matches++
			}
		}
	}
	if matches != 1 || input.TaskID == source.ID || input.Activate {
		return ErrInvalidWorkflow
	}
	stages := cloneWorkflowStages(saved)
	found := false
	for i := range stages {
		for j := range stages[i].Tasks {
			if stages[i].Tasks[j].ID == input.TaskID {
				stages[i].Tasks[j] = selected
				found = true
			}
		}
	}
	if !found {
		for i := range stages {
			if stages[i].ID == selectedStage.ID {
				stages[i].Tasks = append(stages[i].Tasks, selected)
				found = true
				break
			}
		}
		if !found {
			selectedStage.Tasks = []model.WorkflowNode{selected}
			position := len(stages)
			afterSelected := false
			for _, draftStage := range input.Stages {
				if draftStage.ID == selectedStage.ID {
					afterSelected = true
					continue
				}
				if !afterSelected {
					continue
				}
				for i := range stages {
					if stages[i].ID == draftStage.ID && i < position {
						position = i
					}
				}
			}
			stages = append(stages, model.WorkflowStage{})
			copy(stages[position+1:], stages[position:])
			stages[position] = selectedStage
		}
	}
	input.Source = source
	input.Stages = stages
	input.SaveOnly = true
	return nil
}

func workflowSaveStages(input WorkflowInput) []model.WorkflowStage {
	if input.TaskID == "" {
		return input.Stages
	}
	for _, stage := range input.Stages {
		for i, node := range stage.Tasks {
			if node.ID == input.TaskID {
				stage.Tasks = stage.Tasks[i : i+1]
				return []model.WorkflowStage{stage}
			}
		}
	}
	return nil
}

func workflowTaskSaveIssues(input WorkflowInput, issues []WorkflowIssue) []WorkflowIssue {
	if input.TaskID == "" {
		return issues
	}
	result := make([]WorkflowIssue, 0)
	for _, issue := range issues {
		if issue.NodeID == input.TaskID {
			result = append(result, issue)
		}
	}
	return result
}
