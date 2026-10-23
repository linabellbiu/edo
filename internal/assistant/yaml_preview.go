package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"edo/internal/database"
	"edo/internal/kube"
	"edo/internal/model"
	"go.yaml.in/yaml/v3"
)

func checkedYAML(text string) (map[string]any, string, error) {
	if len(text) > 16*1024 || strings.TrimSpace(text) == "" {
		return nil, "", ErrInvalidInput
	}
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return nil, "", err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, "", ErrInvalidInput
	}
	var inspect func(*yaml.Node, int) bool
	inspect = func(current *yaml.Node, depth int) bool {
		if depth > 40 || current.Kind == yaml.AliasNode {
			return false
		}
		for _, child := range current.Content {
			if !inspect(child, depth+1) {
				return false
			}
		}
		return true
	}
	if !inspect(&node, 0) {
		return nil, "", ErrInvalidInput
	}
	var value map[string]any
	if err := node.Decode(&value); err != nil {
		return nil, "", err
	}
	if value == nil {
		return nil, "", ErrInvalidInput
	}
	formatted, err := yaml.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return value, string(formatted), nil
}

func (t *assistantTool) yamlPreview(ctx context.Context, input toolInput) (any, error) {
	allowed := map[string]bool{"deployments": true, "statefulsets": true, "daemonsets": true, "services": true, "ingresses": true, "networkpolicies": true, "configmaps": true, "persistentvolumeclaims": true}
	def, ok := kube.ResourceType(input.Resource)
	if !ok || !allowed[input.Resource] || input.ClusterID == "" || input.Name == "" || input.UID == "" {
		return nil, t.service.failure(ctx, "assistant_yaml_type", ErrInvalidInput, ErrInvalidInput)
	}
	permissions := []string{def.Permission + ".read", def.Permission + ".update"}
	for _, p := range permissions {
		if err := t.require(ctx, p); err != nil {
			return nil, err
		}
	}
	value, formatted, err := checkedYAML(input.YAML)
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_yaml_format", errors.New(safeText(redact(err.Error()), 300)), errors.New("YAML格式无效，请修正缩进、重复字段或多文档内容"))
	}
	if redact(formatted) != formatted {
		return nil, t.service.failure(ctx, "assistant_yaml_sensitive", ErrInvalidInput, errors.New("配置可能包含密钥，请移除后再生成预览"))
	}
	var cluster model.KubernetesCluster
	if err := t.diagnosticQuery(ctx).Where("is_active = ?", true).First(&cluster, &model.KubernetesCluster{ID: input.ClusterID}).Error; err != nil {
		return nil, t.service.failure(ctx, "assistant_yaml_scope", err, ErrNotFound)
	}
	validator, ok := t.service.deps.Kubernetes.(interface {
		ValidateResource(context.Context, string, string, string, string, map[string]any) error
	})
	if !ok {
		return nil, t.service.failure(ctx, "assistant_yaml_validator", ErrUnavailable, errors.New("目前没有连接集群校验服务，不能声称YAML已通过检查"))
	}
	current, err := t.service.deps.Kubernetes.GetResource(ctx, cluster.ID, input.Resource, input.Namespace, input.Name, false)
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_yaml_current", err, ErrDiagnosticRead)
	}
	meta, _ := value["metadata"].(map[string]any)
	original, _ := current["metadata"].(map[string]any)
	version, versionOK := original["resourceVersion"].(string)
	if !versionOK || version == "" || original["uid"] != input.UID || meta["uid"] != input.UID || meta["resourceVersion"] != version || meta["name"] != input.Name || (def.Namespaced && meta["namespace"] != input.Namespace) || value["kind"] != current["kind"] || value["apiVersion"] != current["apiVersion"] {
		return nil, t.service.failure(ctx, "assistant_yaml_identity", ErrActionState, ErrActionState)
	}
	if err := validator.ValidateResource(ctx, cluster.ID, input.Resource, input.Namespace, input.Name, value); err != nil {
		return nil, t.service.failure(ctx, "assistant_yaml_dry_run", err, errors.New("集群未接受该配置，请在资源编辑页面查看校验结果"))
	}
	preview := OperationPreview{YAML: formatted, Changes: []string{"集群：" + cluster.Name, "命名空间：" + input.Namespace, "资源：" + input.Resource + "/" + input.Name}, Warnings: []string{"仅生成预览和执行dry-run，没有修改集群；需要修改时前往资源页面核对后操作"}, Checks: []string{"YAML格式、重复字段与单文档检查", "资源UID与版本检查", "Kubernetes服务端dry-run检查"}}
	stored := operationSnapshot{Preview: preview, ReadPermissions: permissions[:1]}
	encoded, _ := json.Marshal(stored)
	scope, _ := database.DepartmentScopeFromContext(ctx)
	now := time.Now().UTC()
	row := model.AssistantAction{ID: newID(), ConversationID: t.conversationID, UserID: t.user.ID, DepartmentID: scope.DepartmentID, ProjectID: scope.ProjectID, Kind: "yaml_draft", Title: "YAML预览", TargetName: input.Name, Snapshot: string(encoded), Status: "draft", ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now, UpdatedAt: now}
	if _, err := t.service.conversation(ctx, t.user, t.conversationID); err != nil {
		return nil, err
	}
	if err := t.service.deps.Database.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, t.service.failure(ctx, "assistant_yaml_preview_save", err, ErrStorage)
	}
	if err := t.service.authorizeUsed(ctx, t.user); err != nil {
		return nil, err
	}
	view := actionView(row)
	t.emit(Event{Type: "action_proposed", Action: &view})
	return view, nil
}
