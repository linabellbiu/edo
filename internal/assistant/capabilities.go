package assistant

import (
	"context"
	"edo/internal/model"
	"edo/internal/pipeline"
)

type Capability struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Name         string   `json:"name"`
	Supported    bool     `json:"supported"`
	Available    bool     `json:"available"`
	Confirmation bool     `json:"confirmation"`
	Reason       string   `json:"reason,omitempty"`
	Permissions  []string `json:"permissions,omitempty"`
}

func (s *Service) Capabilities(ctx context.Context, user *model.User) ([]Capability, error) {
	if _, err := s.authorize(ctx, user, "assistant.read"); err != nil {
		return nil, err
	}
	items := []Capability{
		{ID: "diagnostics", Version: "V1", Name: "资源定位与只读排障", Supported: true, Permissions: []string{"assistant.execute"}},
		{ID: "workflow_create", Version: "V2", Name: "创建应用、方案与流水线草稿", Supported: true, Confirmation: true, Permissions: []string{"workflow.create", "repository.read", "build_plan.read", "deployment_plan.read"}},
		{ID: "workflow_activate", Version: "V2", Name: "单独确认后启用流水线", Supported: true, Confirmation: true, Permissions: []string{"workflow.update", "workflow.read"}},
		{ID: "pipeline_deploy", Version: "V2", Name: "按固定代码或已有制品部署", Supported: true, Confirmation: true, Permissions: []string{"pipeline_run.execute", "pipeline_run.read", "workflow.read"}},
		{ID: "yaml_preview", Version: "V2", Name: "YAML格式检查与集群预校验", Supported: true, Permissions: []string{"assistant.execute"}},
		{ID: "knowledge_search", Version: "V3", Name: "检索EDO说明、项目说明与历史问题", Supported: true, Permissions: []string{"assistant.execute"}},
		{ID: "delivery_orchestration", Version: "V4", Name: "关联应用、构建方案、部署方案和多步流水线", Supported: true, Confirmation: true, Permissions: []string{"workflow.create"}},
		{ID: "runtime_control", Version: "V2", Name: "重启、停止实例与扩缩容", Reason: "目前助手还没有这个功能，可在应用页面按权限操作"},
		{ID: "external_knowledge", Version: "V3", Name: "导入外部文件或向量知识库", Reason: "目前还没有外部知识库导入功能"},
		{ID: "yaml_apply", Version: "V2", Name: "直接应用任意YAML", Reason: "助手只预览和检查，不直接应用任意YAML"},
		{ID: "kubernetes_workload_create", Name: "直接新建Kubernetes工作负载", Reason: "目前助手还没有这个功能；流水线部署只更新已有Deployment，新建资源请前往Kubernetes页面"},
		{ID: "delete", Name: "删除资源", Reason: "助手不执行删除，只提供管理页导航"},
		{ID: "arbitrary_commands", Name: "任意命令、SQL、权限修改或读取密钥", Reason: "这些能力不向助手开放"},
	}
	for i := range items {
		item := &items[i]
		if item.ID == "workflow_create" || item.ID == "workflow_activate" || item.ID == "pipeline_deploy" {
			item.Permissions = operationPermissions(operationInput{Kind: item.ID})
		}
		item.Available = item.Supported
		if !item.Supported {
			continue
		}
		if _, err := s.authorize(ctx, user, "assistant.execute"); err != nil {
			item.Available = false
			item.Reason = "当前账户没有使用助手的权限"
			continue
		}
		if item.ID == "yaml_preview" {
			if _, ok := s.deps.Kubernetes.(interface {
				ValidateResource(context.Context, string, string, string, string, map[string]any) error
			}); !ok {
				item.Available = false
				item.Reason = "集群校验服务尚未连接"
				continue
			}
		}
		if s.deps.Pipeline == nil && (item.ID == "workflow_create" || item.ID == "workflow_activate" || item.ID == "pipeline_deploy" || item.ID == "delivery_orchestration") {
			item.Available = false
			item.Reason = "相关业务服务尚未连接"
			continue
		}
		for _, permission := range item.Permissions {
			if _, err := s.authorize(ctx, user, permission); err != nil {
				item.Available = false
				item.Reason = "当前账户缺少所需权限"
				break
			}
		}
	}
	return items, nil
}

func (t *assistantTool) nativeCapabilities(ctx context.Context, input toolInput) (any, error) {
	items, err := t.service.Capabilities(ctx, t.user)
	if err != nil {
		return nil, err
	}
	if input.Resource != "" {
		for _, item := range items {
			if item.ID == input.Resource {
				return item, nil
			}
		}
		return map[string]any{"id": input.Resource, "supported": false, "available": false, "reason": "目前还没有这个功能，不能用其他命令绕过"}, nil
	}
	return map[string]any{"capabilities": items, "presets": pipeline.ListWorkflowPresets(), "notes": []string{"先查询能力与资源，不能猜测ID", "缺少配置先追问", "创建、启用和部署分别确认", "没有API Key时不能开始模型对话"}}, nil
}
