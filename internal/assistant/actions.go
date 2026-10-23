package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"edo/internal/database"
	"edo/internal/model"
	"gorm.io/gorm"
)

type actionSnapshot struct {
	State string `json:"state"`
	Retry string `json:"retry,omitempty"`
}

func actionView(row model.AssistantAction) ActionView {
	view := ActionView{ID: row.ID, Kind: row.Kind, Title: row.Title, Summary: row.Summary, Status: row.Status, TargetName: row.TargetName, ExpiresAt: row.ExpiresAt}
	if row.Status == "pending" && time.Now().After(row.ExpiresAt) {
		view.Status = "expired"
	}
	if row.Result != "" {
		var result ActionResult
		if json.Unmarshal([]byte(row.Result), &result) == nil {
			view.Result = &result
		}
	}
	return view
}

func (s *Service) scopedRun(ctx context.Context, id string) (model.PipelineRun, error) {
	var run model.PipelineRun
	scope, _ := database.DepartmentScopeFromContext(ctx)
	err := s.deps.Database.WithContext(ctx).Where("project_id = ? AND department_id = ?", scope.ProjectID, scope.DepartmentID).First(&run, "uuid = ?", id).Error
	if err != nil {
		public := ErrStorage
		if errors.Is(err, gorm.ErrRecordNotFound) {
			public = ErrNotFound
		}
		return run, s.failure(ctx, "assistant_run_scope", err, public, "run_id", id)
	}
	return run, nil
}

func allowedAction(kind string, run model.PipelineRun) bool {
	if kind == "pipeline_retry" {
		return run.Status == model.PipelineRunFailed && run.Ref != "" && run.CommitSHA != ""
	}
	if kind != "pipeline_stop" || run.Stage == "stopping" {
		return false
	}
	switch run.Status {
	case model.PipelineRunDetected, model.PipelineRunReady, model.PipelineRunBlocked, model.PipelineRunAwaitingApproval, model.PipelineRunRunning:
		return true
	}
	return false
}

func (s *Service) snapshot(ctx context.Context, run model.PipelineRun, kind string) (string, string, error) {
	var app model.Application
	if err := s.deps.Database.WithContext(ctx).Select("uuid", "name", "is_active", "repository_id", "project_id", "department_id").First(&app, "uuid = ? AND project_id = ? AND department_id = ?", run.ApplicationID, run.ProjectID, run.DepartmentID).Error; err != nil {
		return "", "", s.failure(ctx, "assistant_action_application", err, ErrNotFound)
	}
	state := map[string]any{"run_id": run.ID, "status": run.Status, "stage": run.Stage, "node_id": run.CurrentNodeID, "job_id": run.ExecutionJobID, "ref": run.Ref, "commit": run.CommitSHA, "workflow_id": run.WorkflowID, "application_id": run.ApplicationID, "application_active": app.IsActive, "repository_id": app.RepositoryID, "kind": kind}
	if kind == "pipeline_retry" {
		var workflow model.ReleaseWorkflow
		if err := s.deps.Database.WithContext(ctx).First(&workflow, "uuid = ? AND project_id = ? AND department_id = ?", run.WorkflowID, run.ProjectID, run.DepartmentID).Error; err != nil {
			return "", "", s.failure(ctx, "assistant_action_workflow", err, ErrActionState)
		}
		state["workflow_revision"], state["workflow_active"], state["workflow_template_id"], state["workflow_template_revision"] = workflow.Revision, workflow.IsActive, workflow.WorkflowTemplateID, workflow.WorkflowTemplateRevision
		stages := workflow.Stages
		state["workflow"] = workflow
		if workflow.WorkflowTemplateID != "" {
			var template model.ReleaseWorkflowTemplate
			if err := s.deps.Database.WithContext(ctx).First(&template, "uuid = ? AND project_id = ? AND department_id = ?", workflow.WorkflowTemplateID, run.ProjectID, run.DepartmentID).Error; err != nil {
				return "", "", s.failure(ctx, "assistant_action_template", err, ErrActionState)
			}
			if template.IsActive {
				state["template"] = template
				stages = template.Stages
			}
		}
		// 独立方案和目标修改不一定推进流水线修订号，确认必须同时绑定实际配置。
		for _, stage := range stages {
			for _, node := range stage.Tasks {
				if node.Config.BuildPlanID != "" {
					var plan model.BuildPlan
					if err := s.deps.Database.WithContext(ctx).First(&plan, "uuid = ? AND project_id = ? AND department_id = ?", node.Config.BuildPlanID, run.ProjectID, run.DepartmentID).Error; err != nil {
						return "", "", s.failure(ctx, "assistant_action_build_plan", err, ErrActionState)
					}
					state["build:"+plan.ID] = plan
					if plan.ImageRegistryID != "" {
						var registry model.ImageRegistry
						if err := s.deps.Database.WithContext(ctx).First(&registry, "uuid = ? AND project_id = ? AND department_id = ?", plan.ImageRegistryID, run.ProjectID, run.DepartmentID).Error; err != nil {
							return "", "", s.failure(ctx, "assistant_action_registry", err, ErrActionState)
						}
						state["registry:"+registry.ID] = registry
					}
				}
				if node.Config.DeploymentPlanID != "" {
					var plan model.DeploymentPlan
					if err := s.deps.Database.WithContext(ctx).First(&plan, "uuid = ? AND project_id = ? AND department_id = ?", node.Config.DeploymentPlanID, run.ProjectID, run.DepartmentID).Error; err != nil {
						return "", "", s.failure(ctx, "assistant_action_deploy_plan", err, ErrActionState)
					}
					state["deploy:"+plan.ID] = plan
					var target model.DeploymentTarget
					if err := s.deps.Database.WithContext(ctx).First(&target, "uuid = ? AND project_id = ? AND department_id = ?", plan.DeploymentTargetID, run.ProjectID, run.DepartmentID).Error; err != nil {
						return "", "", s.failure(ctx, "assistant_action_target", err, ErrActionState)
					}
					state["target:"+target.ID] = target
					if target.HostID != "" {
						var host model.Host
						if err := s.deps.Database.WithContext(ctx).First(&host, "uuid = ? AND project_id = ? AND department_id = ?", target.HostID, run.ProjectID, run.DepartmentID).Error; err != nil {
							return "", "", s.failure(ctx, "assistant_action_host", err, ErrActionState)
						}
						state["host:"+host.ID] = host
						state["host_credentials:"+host.ID] = host.SSHCredentialCiphertext
					}
					if target.RuntimeID != "" {
						if target.Platform == model.DeploymentKubernetes {
							var cluster model.KubernetesCluster
							if err := s.deps.Database.WithContext(ctx).First(&cluster, "uuid = ? AND project_id = ? AND department_id = ?", target.RuntimeID, run.ProjectID, run.DepartmentID).Error; err != nil {
								return "", "", s.failure(ctx, "assistant_action_cluster_identity", err, ErrActionState)
							}
							state["cluster:"+cluster.ID] = cluster
						} else if target.Platform == model.DeploymentDocker {
							var endpoint model.DockerEndpoint
							if err := s.deps.Database.WithContext(ctx).First(&endpoint, "uuid = ? AND project_id = ? AND department_id = ?", target.RuntimeID, run.ProjectID, run.DepartmentID).Error; err != nil {
								return "", "", s.failure(ctx, "assistant_action_docker_identity", err, ErrActionState)
							}
							state["docker:"+endpoint.ID] = endpoint
						}
					}
				}
			}
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", "", s.failure(ctx, "assistant_action_snapshot", err, ErrStorage)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), app.Name, nil
}

func (s *Service) propose(ctx context.Context, user *model.User, conversationID, kind, runID string) (ActionView, error) {
	if _, err := s.conversation(ctx, user, conversationID); err != nil {
		return ActionView{}, err
	}
	run, err := s.scopedRun(ctx, runID)
	if err != nil {
		return ActionView{}, err
	}
	if !allowedAction(kind, run) {
		return ActionView{}, s.failure(ctx, "assistant_action_prepare", ErrActionState, ErrActionState, "run_id", runID)
	}
	fingerprint, name, err := s.snapshot(ctx, run, kind)
	if err != nil {
		return ActionView{}, err
	}
	expected := actionSnapshot{State: fingerprint}
	if kind == "pipeline_retry" {
		if s.deps.Pipeline == nil {
			return ActionView{}, s.failure(ctx, "assistant_retry_unavailable", ErrUnavailable, ErrUnavailable)
		}
		expected.Retry, err = s.deps.Pipeline.PreviewRetryRun(ctx, run.ID, user.ID, "")
		if err != nil {
			return ActionView{}, s.failure(ctx, "assistant_retry_preview", err, ErrActionState, "run_id", run.ID)
		}
		expected.State, _, err = s.snapshot(ctx, run, kind)
		if err != nil {
			return ActionView{}, err
		}
	}
	encodedSnapshot, _ := json.Marshal(expected)
	title, summary := "重新执行流水线", "使用原运行的代码版本重新构建；新运行将重新执行当前配置中的审批和发布步骤。"
	if kind == "pipeline_stop" {
		title, summary = "停止流水线", "停止后续任务，不会停止已经部署的应用，也不会自动回滚；远端已提交的操作仍需核对。"
	}
	var workflow model.ReleaseWorkflow
	if err := s.deps.Database.WithContext(ctx).Select("uuid", "name").First(&workflow, "uuid = ? AND project_id = ? AND department_id = ?", run.WorkflowID, run.ProjectID, run.DepartmentID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ActionView{}, s.failure(ctx, "assistant_action_workflow_name", err, ErrStorage)
	}
	summary = safeText(fmt.Sprintf("流水线：%s；运行：%s；版本：%s；提交：%s。%s", workflow.Name, run.ID, run.Ref, run.CommitSHA, summary), 1000)
	scope, _ := database.DepartmentScopeFromContext(ctx)
	now := time.Now().UTC()
	row := model.AssistantAction{ID: newID(), ConversationID: conversationID, UserID: user.ID, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, Kind: kind, Title: title, Summary: summary, TargetID: run.ID, TargetName: name, Snapshot: string(encodedSnapshot), Status: "pending", ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now, UpdatedAt: now}
	if err := s.deps.Database.WithContext(ctx).Create(&row).Error; err != nil {
		return ActionView{}, s.failure(ctx, "assistant_action_save", err, ErrStorage)
	}
	return actionView(row), nil
}

func (s *Service) action(ctx context.Context, user *model.User, id string) (model.AssistantAction, error) {
	var row model.AssistantAction
	if err := s.owned(ctx, user).First(&row, "uuid = ?", id).Error; err != nil {
		public := ErrStorage
		if errors.Is(err, gorm.ErrRecordNotFound) {
			public = ErrNotFound
		}
		return row, s.failure(ctx, "assistant_action_read", err, public, "action_id", id)
	}
	if _, err := s.conversation(ctx, user, row.ConversationID); err != nil {
		return row, err
	}
	return row, nil
}

func (s *Service) RejectAction(ctx context.Context, user *model.User, id string) (ActionView, error) {
	if _, err := s.authorize(ctx, user, "assistant.execute"); err != nil {
		return ActionView{}, err
	}
	row, err := s.action(ctx, user, id)
	if err != nil {
		return ActionView{}, err
	}
	result := s.owned(ctx, user).Model(&model.AssistantAction{}).Where("uuid = ? AND status = ?", id, "pending").Updates(map[string]any{"status": "rejected", "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return ActionView{}, s.failure(ctx, "assistant_action_reject", result.Error, ErrStorage)
	}
	if result.RowsAffected != 1 {
		return ActionView{}, s.failure(ctx, "assistant_action_reject", ErrActionState, ErrActionState)
	}
	row.Status = "rejected"
	if err := s.audit(ctx, user.ID, "assistant.action.reject", id, model.AuditSucceeded, map[string]any{"kind": row.Kind, "run_id": row.TargetID}); err != nil {
		return ActionView{}, err
	}
	return actionView(row), nil
}

func (s *Service) ConfirmAction(ctx context.Context, user *model.User, id string) (ActionView, error) {
	for _, permission := range []string{"assistant.execute", "pipeline_run.read", "pipeline_run.execute"} {
		if _, err := s.authorize(ctx, user, permission); err != nil {
			return ActionView{}, err
		}
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return ActionView{}, err
	}
	if !settings.Enabled {
		return ActionView{}, s.failure(ctx, "assistant_action_disabled", ErrDisabled, ErrDisabled)
	}
	row, err := s.action(ctx, user, id)
	if err != nil {
		return ActionView{}, err
	}
	var expected actionSnapshot
	if err := json.Unmarshal([]byte(row.Snapshot), &expected); err != nil {
		return ActionView{}, s.failure(ctx, "assistant_action_snapshot_read", err, ErrActionState)
	}
	if row.Status != "pending" {
		return ActionView{}, s.failure(ctx, "assistant_action_replay", ErrActionState, ErrActionState, "action_id", id)
	}
	if !time.Now().Before(row.ExpiresAt) {
		return ActionView{}, s.failure(ctx, "assistant_action_expired", ErrActionExpired, ErrActionExpired, "action_id", id)
	}
	run, err := s.scopedRun(ctx, row.TargetID)
	if err != nil {
		return ActionView{}, err
	}
	fingerprint, _, err := s.snapshot(ctx, run, row.Kind)
	if err != nil {
		return ActionView{}, err
	}
	if !allowedAction(row.Kind, run) || fingerprint != expected.State || s.deps.Pipeline == nil {
		return ActionView{}, s.failure(ctx, "assistant_action_freshness", ErrActionState, ErrActionState, "action_id", id, "run_id", run.ID)
	}
	// 领取成功后绝不重置为 pending；进程崩溃时保持 executing，防止重复外部副作用。
	now := time.Now().UTC()
	claimed := s.owned(ctx, user).Model(&model.AssistantAction{}).Where("uuid = ? AND status = ? AND expires_at > ? AND snapshot = ?", id, "pending", now, row.Snapshot).Updates(map[string]any{"status": "executing", "updated_at": now})
	if claimed.Error != nil {
		return ActionView{}, s.failure(ctx, "assistant_action_claim", claimed.Error, ErrStorage)
	}
	if claimed.RowsAffected != 1 {
		return ActionView{}, s.failure(ctx, "assistant_action_claim", ErrActionState, ErrActionState)
	}
	if err := s.audit(ctx, user.ID, "assistant.action.execute", id, model.AuditSucceeded, map[string]any{"kind": row.Kind, "run_id": run.ID}); err != nil {
		return ActionView{}, err
	}
	// 每次真正执行前再检查权限；领取不能成为撤权后的通行证。
	for _, permission := range []string{"assistant.execute", "pipeline_run.execute"} {
		if _, err := s.authorize(ctx, user, permission); err != nil {
			return ActionView{}, err
		}
	}
	run, err = s.scopedRun(ctx, row.TargetID)
	if err == nil {
		fingerprint, _, err = s.snapshot(ctx, run, row.Kind)
		if err == nil && (fingerprint != expected.State || !allowedAction(row.Kind, run)) {
			err = ErrActionState
		}
	}
	var output *model.PipelineRun
	if err == nil {
		if row.Kind == "pipeline_retry" {
			output, err = s.deps.Pipeline.RetryRunSelectionExpected(ctx, run.ID, user.ID, "", expected.Retry)
		} else if row.Kind == "pipeline_stop" {
			output, err = s.deps.Pipeline.StopRun(ctx, run.ID, user.ID)
		} else {
			err = ErrForbidden
		}
	}
	row.Status = "succeeded"
	publicResult := ActionResult{Message: "操作已受理，请在流水线运行记录查看实际状态"}
	auditResult := model.AuditSucceeded
	if err != nil {
		row.Status, auditResult, publicResult.Message = "failed", model.AuditFailed, "操作未完成，目标状态可能已变化，请重新查询"
		s.failure(ctx, "assistant_action_execute", err, ErrActionState, "action_id", id, "run_id", run.ID)
	} else if output != nil {
		publicResult.PipelineRunID = output.ID
	}
	encoded, _ := json.Marshal(publicResult)
	row.Result = string(encoded)
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result := s.owned(finishCtx, user).Model(&model.AssistantAction{}).Where("uuid = ? AND status = ?", id, "executing").Updates(map[string]any{"status": row.Status, "result": row.Result, "updated_at": time.Now().UTC()})
	if result.Error != nil || result.RowsAffected != 1 {
		return ActionView{}, s.failure(finishCtx, "assistant_action_result", result.Error, ErrStorage)
	}
	if auditErr := s.audit(finishCtx, user.ID, "assistant.action.result", id, auditResult, map[string]any{"kind": row.Kind, "run_id": row.TargetID, "status": row.Status}); auditErr != nil {
		return ActionView{}, auditErr
	}
	return actionView(row), nil
}
