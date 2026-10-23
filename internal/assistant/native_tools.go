package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"edo/internal/model"
	"github.com/cloudwego/eino/schema"
)

func (t *assistantTool) native(ctx context.Context, input toolInput) (any, error) {
	s := t.service
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch t.name {
	case "edo_capabilities":
		return t.nativeCapabilities(ctx, input)
	case "delivery_catalog":
		return t.deliveryCatalog(ctx, input)
	case "knowledge_search":
		return t.knowledge(ctx, input)
	case "workflow_refs":
		for _, p := range []string{"application.read", "workflow.read", "pipeline_run.execute"} {
			if err := t.require(ctx, p); err != nil {
				return nil, err
			}
		}
		if input.ApplicationID == "" || input.WorkflowID == "" || s.deps.Pipeline == nil {
			return nil, s.failure(ctx, "assistant_refs_input", ErrInvalidInput, ErrInvalidInput)
		}
		refs, err := s.deps.Pipeline.ListWorkflowRefs(ctx, input.ApplicationID, input.WorkflowID)
		if err != nil {
			return nil, s.operationFailure(ctx, "assistant_refs", err)
		}
		if len(refs.Branches) > 30 {
			refs.Branches = refs.Branches[:30]
		}
		if len(refs.Tags) > 30 {
			refs.Tags = refs.Tags[:30]
		}
		if len(refs.Artifacts) > 20 {
			refs.Artifacts = refs.Artifacts[:20]
		}
		return refs, nil
	case "operation_status":
		if input.ActionID == "" {
			return nil, s.failure(ctx, "assistant_status_id", ErrInvalidInput, ErrInvalidInput)
		}
		row, err := s.action(ctx, t.user, input.ActionID)
		if err != nil {
			return nil, err
		}
		if operationKind(row.Kind) || row.Kind == "yaml_draft" {
			var stored operationSnapshot
			if json.Unmarshal([]byte(row.Snapshot), &stored) != nil {
				return nil, s.failure(ctx, "assistant_status_snapshot", ErrForbidden, ErrForbidden)
			}
			for _, permission := range stored.ReadPermissions {
				if err := t.require(ctx, permission); err != nil {
					return nil, err
				}
			}
		} else {
			if err := t.require(ctx, "pipeline_run.read"); err != nil {
				return nil, err
			}
		}
		return actionView(row), nil
	case "prepare_workflow", "prepare_deploy", "prepare_workflow_activation":
		op := operationInput{}
		switch t.name {
		case "prepare_workflow":
			op.Kind = "workflow_create"
			op.Workflow = input.Workflow
		case "prepare_deploy":
			op.Kind = "pipeline_deploy"
			op.Run = input.Selection
		default:
			op.Kind = "workflow_activate"
			op.ApplicationID = input.ApplicationID
			op.WorkflowID = input.WorkflowID
		}
		for _, p := range operationPermissions(op) {
			if err := t.require(ctx, p); err != nil {
				return nil, err
			}
		}
		action, err := s.proposeOperation(ctx, t.user, t.conversationID, op)
		if err != nil {
			if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrStorage) {
				return nil, err
			}
			// 配置不完整不是执行失败。返回安全说明供模型追问，不能伪造确认卡。
			return map[string]any{"prepared": false, "reason": err.Error(), "next": "请根据目录查到的资源检查目标、模板、版本和缺少的配置；不支持的请求明确说明目前还没有这个功能"}, nil
		}
		if err := s.authorizeUsed(ctx, t.user); err != nil {
			return nil, err
		}
		t.emit(Event{Type: "action_proposed", Action: &action})
		return action, nil
	case "preview_kubernetes_yaml":
		return t.yamlPreview(ctx, input)
	}
	return nil, s.failure(ctx, "assistant_native_whitelist", ErrForbidden, ErrForbidden)
}

func (t *assistantTool) deliveryCatalog(ctx context.Context, input toolInput) (any, error) {
	permission := ""
	var value any
	columns := []string{"ID", "name", "is_active"}
	switch input.Resource {
	case "repositories":
		permission = "repository.read"
		value = &[]model.GitRepository{}
		columns = append(columns, "default_branch", "provider")
	case "build_plans":
		permission = "build_plan.read"
		value = &[]model.BuildPlan{}
		columns = append(columns, "kind", "dockerfile_path", "context_path", "image_registry_id")
	case "deployment_plans":
		permission = "deployment_plan.read"
		value = &[]model.DeploymentPlan{}
		columns = append(columns, "kind", "deployment_target_id")
	case "environments":
		permission = "cluster.read"
		value = &[]model.Environment{}
	case "registries":
		permission = "image_registry.read"
		value = &[]model.ImageRegistry{}
		columns = append(columns, "provider", "endpoint", "namespace")
	case "workflows":
		permission = "workflow.read"
		value = &[]model.ReleaseWorkflow{}
		columns = append(columns, "application_id", "revision")
	case "defaults":
		for _, permission := range []string{"build_plan.read", "deployment_plan.read"} {
			if err := t.require(ctx, permission); err != nil {
				return nil, err
			}
		}
		var rows []model.DeliveryDefault
		if err := t.diagnosticQuery(ctx).Select("kind", "workflow_template_id", "deployment_plan_id", "version").Limit(10).Find(&rows).Error; err != nil {
			return nil, t.service.failure(ctx, "assistant_defaults", err, ErrDiagnosticRead)
		}
		return rows, nil
	default:
		return nil, t.service.failure(ctx, "assistant_catalog_type", ErrInvalidInput, ErrInvalidInput)
	}
	if err := t.require(ctx, permission); err != nil {
		return nil, err
	}
	query := t.diagnosticQuery(ctx).Select(columns).Order("name ASC").Limit(30)
	if input.ApplicationID != "" && input.Resource == "workflows" {
		query = query.Where("application_id = ?", input.ApplicationID)
	}
	if input.Query != "" {
		if len(input.Query) > 160 {
			return nil, t.service.failure(ctx, "assistant_catalog_search", ErrInvalidInput, ErrInvalidInput)
		}
		query = query.Where("name LIKE ? ESCAPE '!'", "%"+strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(input.Query)+"%")
	}
	if err := query.Find(value).Error; err != nil {
		return nil, t.service.failure(ctx, "assistant_catalog", err, ErrDiagnosticRead)
	}
	// 只序列化查询出的白名单字段；json字段可能省略未加载的内容，不返回完整模型。
	data, _ := json.Marshal(value)
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, t.service.failure(ctx, "assistant_catalog_projection", err, ErrStorage)
	}
	allowed := map[string]bool{"id": true, "name": true, "is_active": true, "default_branch": true, "platform": true, "kind": true, "dockerfile_path": true, "context_path": true, "image_registry_id": true, "deployment_target_id": true, "application_id": true, "revision": true, "provider": true, "endpoint": true, "namespace": true}
	for _, row := range decoded {
		for key := range row {
			if !allowed[key] {
				delete(row, key)
			}
		}
	}
	return map[string]any{"resource": input.Resource, "items": decoded, "limit": 30, "possibly_more": len(decoded) == 30}, nil
}

func nativeToolParameters() map[string]map[string]*schema.ParameterInfo {
	str := func(desc string, required bool) *schema.ParameterInfo {
		return &schema.ParameterInfo{Type: schema.String, Desc: desc, Required: required}
	}
	return map[string]map[string]*schema.ParameterInfo{
		"edo_capabilities":            {"resource": str("需要检查的能力ID；留空返回真实能力清单", false)},
		"delivery_catalog":            {"resource": str("repositories/build_plans/deployment_plans/environments/registries/workflows/defaults", true), "query": str("名称关键词", false), "application_id": str("列出指定应用全部流水线", false)},
		"workflow_refs":               {"application_id": str("已查到的应用ID", true), "workflow_id": str("用户选择的流水线ID", true)},
		"knowledge_search":            {"query": str("问题关键词，检索内置说明和已授权项目历史", true), "resource": str("manual/project/history/all，默认all", false)},
		"operation_status":            {"action_id": str("当前用户的确认卡ID", true)},
		"prepare_workflow_activation": {"application_id": str("应用ID", true), "workflow_id": str("需要单独确认启用的流水线ID", true)},
		"prepare_workflow": {"workflow": {Type: schema.Object, Required: true, Desc: "按查到的资源生成草稿；不能输入脚本、密钥或任意任务", SubParams: map[string]*schema.ParameterInfo{
			"application_id": str("现有应用ID，与新应用名称和仓库ID互斥", false), "application_name": str("新应用名称，仅小写字母和下划线", false), "repository_id": str("新应用使用的已有仓库ID", false), "name": str("流水线名称", true), "preset": str("edo_capabilities返回的真实模板key", true), "runtime_version": str("语言模板使用的已准备运行版本；没有时先请用户准备", false), "branch": str("用户要求的分支匹配条件", true), "events": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Required: true, Desc: "push/pr/tag/manual"}, "build_plan_id": str("现有构建方案ID", false), "deployment_plan_ids": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Desc: "现有部署方案ID列表，最多5个，按顺序部署"}, "approval": {Type: schema.Boolean, Desc: "是否需要审核"},
			"new_build":      {Type: schema.Object, Desc: "没有合适方案时新建Dockerfile构建方案", SubParams: map[string]*schema.ParameterInfo{"name": str("方案名称", true), "dockerfile": str("仓库内Dockerfile路径", true), "context": str("仓库内构建上下文", true), "registry_id": str("已有镜像仓库连接ID，Kubernetes部署需要", false)}},
			"new_deployment": {Type: schema.Object, Desc: "可新建Docker或Kubernetes部署方案；不接受脚本或宿主机挂载", SubParams: map[string]*schema.ParameterInfo{"name": str("方案名称", true), "kind": str("docker/kubernetes", true), "environment_id": str("已有逻辑环境ID，必填", true), "cluster_id": str("已有独立集群ID，Kubernetes必填", false), "namespace": str("Kubernetes命名空间", false), "workload": str("工作负载或容器名称", false), "container": str("Kubernetes容器名称", false), "ports": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.Object, SubParams: map[string]*schema.ParameterInfo{"host_ip": str("绑定地址", false), "host_port": {Type: schema.Integer}, "container_port": {Type: schema.Integer}, "protocol": str("tcp/udp", false)}}}}},
		}}},
		"prepare_deploy":          {"selection": {Type: schema.Object, Required: true, SubParams: map[string]*schema.ParameterInfo{"application_id": str("应用ID", true), "workflow_id": str("准确流水线ID", true), "ref": str("workflow_refs返回的完整引用", false), "commit": str("该引用中查到的完整Commit", false), "artifact_id": str("该流水线可用的已有制品ID；与ref/commit互斥", false)}}},
		"preview_kubernetes_yaml": {"cluster_id": str("集群ID", true), "resource": str("资源类型，不支持Secret/RBAC/Node", true), "namespace": str("命名空间", false), "name": str("资源名称", true), "uid": str("现有资源UID", true), "yaml": str("单个资源的YAML，只校验与预览，不应用", true)},
	}
}
