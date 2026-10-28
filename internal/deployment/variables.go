package deployment

import (
	"context"
	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/model"
	"edo/internal/secret"
	"encoding/json"
	"errors"
	"maps"
)

type releaseEnvironmentKey struct{}

// WithReleaseEnvironment 只在一次执行的上下文中传递变量内容，发布记录不保存明文。
func WithReleaseEnvironment(ctx context.Context, values map[string]string) context.Context {
	return context.WithValue(ctx, releaseEnvironmentKey{}, maps.Clone(values))
}
func releaseEnvironment(ctx context.Context) map[string]string {
	values, _ := ctx.Value(releaseEnvironmentKey{}).(map[string]string)
	return maps.Clone(values)
}

type releaseVariablesResolver struct {
	service     *cicd.Service
	permissions *access.Service
}

func (s *Service) ConfigureReleaseVariables(manager *secret.Manager, permissions *access.Service) {
	s.variables = &releaseVariablesResolver{service: cicd.New(s.db, manager, s.logger), permissions: permissions}
}
func (s *Service) resolveRecordVariables(ctx context.Context, record *model.DeploymentRecord) (map[string]string, error) {
	denied := errors.New("发布变量不可用或使用授权已失效")
	if s.variables == nil || s.variables.permissions == nil {
		s.logger.Error("发布变量权限服务不可用", "operation", "deployment_variable_authorization", "record_id", record.ID, "project_id", record.ProjectID, "err", denied)
		return nil, denied
	}
	var run model.PipelineRun
	if err := s.db.WithContext(ctx).First(&run, "uuid = ? AND project_id = ? AND application_id = ?", record.PipelineRunID, record.ProjectID, record.ApplicationID).Error; err != nil {
		s.logger.Error("读取发布变量所属运行失败", "operation", "deployment_variable_run", "run_id", record.PipelineRunID, "err", err)
		return nil, denied
	}
	var snapshot struct {
		VariableBindings map[string][]model.DeliveryVariableBinding `json:"variable_bindings"`
		AuthorityID      string                                     `json:"authority_id"`
	}
	if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		s.logger.Error("读取发布变量运行快照失败", "operation", "deployment_variable_snapshot", "record_id", record.ID, "run_id", run.ID, "err", err)
		return nil, denied
	}
	bindings := snapshot.VariableBindings[record.WorkflowNodeID]
	if len(bindings) == 0 {
		s.logger.Error("发布变量运行引用不存在", "operation", "deployment_variable_bindings", "record_id", record.ID, "run_id", run.ID, "node_id", record.WorkflowNodeID, "err", denied)
		return nil, denied
	}
	for _, id := range []string{snapshot.AuthorityID, record.RequestedBy} {
		authorization, err := access.FreshProjectPermissions(s.db, ctx, id, run.ProjectID)
		if err != nil || !authorization.Permissions["delivery_variable.execute"] {
			s.logger.Error("发布变量使用授权已失效", "operation", "deployment_variable_authorization", "record_id", record.ID, "project_id", run.ProjectID, "actor_id", id, "err", err)
			return nil, denied
		}
	}
	values, files, _, err := s.variables.service.ResolveVariables(ctx, run.ProjectID, run.Ref, bindings)
	if err != nil || len(files) > 0 {
		s.logger.Error("发布变量读取失败", "operation", "deployment_variable_resolve", "record_id", record.ID, "project_id", run.ProjectID, "err", errors.Join(err, denied))
		return nil, denied
	}
	return values, nil
}
