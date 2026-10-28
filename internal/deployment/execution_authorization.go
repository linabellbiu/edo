package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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
	scope, scoped := database.DepartmentScopeFromContext(ctx)
	// 无效范围不得借读取到的记录重写项目，也不能改动成功或已结束的历史。
	if !scoped || scope.ProjectID == "" || scope.DepartmentID == "" || scope.ProjectID != record.ProjectID || scope.DepartmentID != record.DepartmentID || (record.Status != model.DeploymentQueued && record.Status != model.DeploymentRunning) {
		return cause
	}
	return s.markFailed(ctx, record.ID, "deployment_authorization_revoked", ErrExecutionAuthorization.Error(), cause, record.PreviousImage, nil)
}
