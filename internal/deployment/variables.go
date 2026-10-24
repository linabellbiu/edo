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
		return nil, denied
	}
	bindings := snapshot.VariableBindings[record.WorkflowNodeID]
	if len(bindings) == 0 {
		return nil, denied
	}
	for _, id := range []string{snapshot.AuthorityID, record.RequestedBy} {
		var user model.User
		if err := s.db.WithContext(ctx).First(&user, "uuid = ? AND is_active = ?", id, true).Error; err != nil {
			return nil, denied
		}
		allowed, err := s.variables.permissions.HasPermission(ctx, &user, "delivery_variable.execute")
		if err != nil || !allowed {
			return nil, denied
		}
	}
	values, files, _, err := s.variables.service.ResolveVariables(ctx, run.ProjectID, run.Ref, bindings)
	if err != nil || len(files) > 0 {
		return nil, denied
	}
	return values, nil
}
