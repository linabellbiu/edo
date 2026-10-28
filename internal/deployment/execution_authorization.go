package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

var ErrExecutionAuthorization = errors.New("发布执行授权已失效，请检查当前项目角色权限")

func (s *Service) requireExecutionContext(ctx context.Context, deploymentID string) error {
	scope, scoped := database.DepartmentScopeFromContext(ctx)
	if !scoped || scope.ProjectID == "" || scope.DepartmentID == "" {
		return s.executionAuthorizationDenied(ctx, &model.DeploymentRecord{ID: deploymentID}, "deployment_execution_context", scope.UserID, "", database.ErrProjectScope)
	}
	return nil
}

func (s *Service) requireExecutionScope(ctx context.Context, record *model.DeploymentRecord) error {
	scope, scoped := database.DepartmentScopeFromContext(ctx)
	if record == nil || !scoped || record.ProjectID == "" || record.DepartmentID == "" || scope.ProjectID != record.ProjectID || scope.DepartmentID != record.DepartmentID {
		return s.executionAuthorizationDenied(ctx, record, "deployment_execution_scope", "", "", database.ErrProjectScope)
	}
	return nil
}

// requireExecutionPermission 使用任务冻结的归属和当前数据库授权，不依赖策略重载。
func (s *Service) requireExecutionPermission(ctx context.Context, record *model.DeploymentRecord) error {
	if err := s.requireExecutionScope(ctx, record); err != nil {
		return err
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if strings.TrimSpace(record.RequestedBy) == "" || (record.Operation != model.DeploymentRelease && record.Operation != model.DeploymentRollback) {
		return s.executionAuthorizationDenied(ctx, record, "deployment_execution_identity", record.RequestedBy, "", ErrExecutionAuthorization)
	}
	actorID, permission := record.RequestedBy, access.PermissionDeploymentExecute
	if record.Operation == model.DeploymentRelease && record.PipelineRunID != "" {
		if record.ApplicationID == "" || record.WorkflowNodeID == "" {
			return s.executionAuthorizationDenied(ctx, record, "deployment_pipeline_identity", actorID, "pipeline_run.execute", ErrExecutionAuthorization)
		}
		var run model.PipelineRun
		if err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ? AND application_id = ?", record.PipelineRunID, record.ProjectID, record.DepartmentID, record.ApplicationID).First(&run).Error; err != nil {
			return s.executionAuthorizationDenied(ctx, record, "deployment_pipeline_run", actorID, "pipeline_run.execute", err)
		}
		var snapshot struct {
			AuthorityID string `json:"authority_id"`
		}
		if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
			return s.executionAuthorizationDenied(ctx, record, "deployment_pipeline_snapshot", actorID, "pipeline_run.execute", err)
		}
		if strings.TrimSpace(snapshot.AuthorityID) == "" || run.CreatedBy != record.RequestedBy || (scope.UserID != "" && scope.UserID != record.RequestedBy && scope.UserID != snapshot.AuthorityID) {
			return s.executionAuthorizationDenied(ctx, record, "deployment_pipeline_authority", actorID, "pipeline_run.execute", ErrExecutionAuthorization)
		}
		// 自动流水线的请求人可能是 system，只沿快照中已冻结的实际授权人执行。
		actorID, permission = snapshot.AuthorityID, "pipeline_run.execute"
	} else if scope.UserID != "" && scope.UserID != record.RequestedBy {
		return s.executionAuthorizationDenied(ctx, record, "deployment_execution_requester", actorID, permission, ErrExecutionAuthorization)
	}
	return s.requireCurrentActorPermission(ctx, record, actorID, permission)
}

// requireCurrentActorPermission 复用冻结归属，以数据库中的当前身份和授权判定副作用执行权限。
func (s *Service) requireCurrentActorPermission(ctx context.Context, record *model.DeploymentRecord, actorID, permission string) error {
	if err := s.requireExecutionScope(ctx, record); err != nil {
		return err
	}
	if strings.TrimSpace(actorID) == "" {
		return s.executionAuthorizationDenied(ctx, record, "deployment_execution_identity", actorID, permission, ErrExecutionAuthorization)
	}
	authorization, err := access.FreshProjectPermissions(s.db, ctx, actorID, record.ProjectID)
	if err != nil || !authorization.Permissions[permission] {
		return s.executionAuthorizationDenied(ctx, record, "deployment_execution_permission", actorID, permission, err)
	}
	return nil
}

func (s *Service) executionAuthorizationDenied(ctx context.Context, record *model.DeploymentRecord, operation, actorID, permission string, cause error) error {
	if cause == nil {
		cause = access.ErrAccessDelegationDenied
	}
	var id, projectID, departmentID, runID string
	if record != nil {
		id, projectID, departmentID, runID = record.ID, record.ProjectID, record.DepartmentID, record.PipelineRunID
	}
	s.logger.ErrorContext(ctx, "发布执行授权已失效", "operation", operation, "deployment_id", id, "project_id", projectID, "department_id", departmentID, "pipeline_run_id", runID, "actor_id", actorID, "permission", permission, "err", cause)
	return ErrExecutionAuthorization
}

func (s *Service) failExecutionAuthorization(ctx context.Context, record *model.DeploymentRecord, cause error) error {
	if !errors.Is(cause, ErrExecutionAuthorization) {
		cause = errors.Join(ErrExecutionAuthorization, cause)
	}
	scope, scoped := database.DepartmentScopeFromContext(ctx)
	// 无效范围不得借读取到的记录重写项目，也不能改动成功或已结束的历史。
	if record == nil || !scoped || scope.ProjectID == "" || scope.DepartmentID == "" || scope.ProjectID != record.ProjectID || scope.DepartmentID != record.DepartmentID || (record.Status != model.DeploymentQueued && record.Status != model.DeploymentRunning) {
		return cause
	}
	updateContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	// 状态检查必须进入同一条 UPDATE，不能用内存中的旧状态覆盖其他执行的结果。
	result := s.db.WithContext(updateContext).Model(&model.DeploymentRecord{}).
		Where("uuid = ? AND project_id = ? AND department_id = ? AND status = ?", record.ID, record.ProjectID, record.DepartmentID, record.Status).
		Updates(map[string]any{
			"status": model.DeploymentFailed, "error_code": "deployment_authorization_revoked", "error_message": ErrExecutionAuthorization.Error(),
			"finished_at": now, "updated_at": now,
		})
	if result.Error != nil {
		s.logger.ErrorContext(ctx, "保存发布授权失败状态失败", "operation", "deployment_authorization_failure_persist", "deployment_id", record.ID, "project_id", record.ProjectID, "department_id", record.DepartmentID, "expected_status", record.Status, "err", result.Error, "cause", cause)
		return cause
	}
	if result.RowsAffected != 1 {
		s.logger.WarnContext(ctx, "发布状态已变化，未覆盖既有执行结果", "operation", "deployment_authorization_failure_stale", "deployment_id", record.ID, "project_id", record.ProjectID, "department_id", record.DepartmentID, "expected_status", record.Status, "rows_affected", result.RowsAffected, "err", cause)
	}
	return cause
}
