package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
)

// WorkflowDeploymentOverride 保存选用时的初始快照及当前任务差异，不反向修改通用部署方案。
// 目标一旦提交即完整替换；Kubernetes 使用 RFC 7396 对象补丁保留显式关闭和清空值。
type WorkflowDeploymentOverride struct {
	PlanSnapshot           *WorkflowDeploymentPlanSnapshot `json:"plan_snapshot,omitempty"`
	DeploymentRegistryID   *string                         `json:"deployment_registry_id,omitempty"`
	DeploymentInputVersion *int                            `json:"deployment_input_version,omitempty"`
	DeploymentInputs       *[]DeploymentInput              `json:"deployment_inputs,omitempty"`
	DeploymentTarget       *DeploymentTarget               `json:"deployment_target,omitempty"`
	Kubernetes             json.RawMessage                 `json:"kubernetes,omitempty"`
	TimeoutSeconds         *int                            `json:"timeout_seconds,omitempty"`
	ArtifactNodeID         *string                         `json:"artifact_node_id,omitempty"`
}

var ErrDeploymentOverride = errors.New("部署阶段覆盖配置无效，请检查部署方案、目标和覆盖参数")

func invalidDeploymentOverride(cause error) error {
	if cause == nil {
		cause = ErrDeploymentOverride
	}
	slog.Warn("部署阶段覆盖参数无效", "operation", "workflow_deployment_override_normalize", "err", cause)
	return ErrDeploymentOverride
}

func HasWorkflowDeploymentOverride(input *WorkflowDeploymentOverride) bool {
	return input != nil && (input.PlanSnapshot != nil || HasWorkflowDeploymentChanges(input))
}

// HasWorkflowDeploymentChanges 不把来源快照本身当作用户参数覆盖。
func HasWorkflowDeploymentChanges(input *WorkflowDeploymentOverride) bool {
	if input == nil {
		return false
	}
	if input.DeploymentTarget != nil || input.TimeoutSeconds != nil || input.ArtifactNodeID != nil || input.DeploymentRegistryID != nil || input.DeploymentInputVersion != nil || input.DeploymentInputs != nil {
		return true
	}
	if len(input.Kubernetes) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(input.Kubernetes, &object) != nil || object == nil || len(object) > 0
}

func ValidateWorkflowDeploymentOverride(input *WorkflowDeploymentOverride) error {
	if input == nil {
		return nil
	}
	if input.PlanSnapshot != nil {
		if err := ValidateWorkflowDeploymentPlanSnapshot(input.PlanSnapshot); err != nil {
			return err
		}
	}
	if input.TimeoutSeconds != nil && (*input.TimeoutSeconds < 30 || *input.TimeoutSeconds > 3600) {
		return invalidDeploymentOverride(nil)
	}
	if input.ArtifactNodeID != nil && (len(*input.ArtifactNodeID) > 64 || strings.TrimSpace(*input.ArtifactNodeID) != *input.ArtifactNodeID) {
		return invalidDeploymentOverride(nil)
	}
	if input.DeploymentRegistryID != nil && (len(*input.DeploymentRegistryID) > 36 || strings.TrimSpace(*input.DeploymentRegistryID) != *input.DeploymentRegistryID) {
		return invalidDeploymentOverride(nil)
	}
	if input.DeploymentInputVersion != nil && *input.DeploymentInputVersion != 0 && *input.DeploymentInputVersion != 1 {
		return invalidDeploymentOverride(nil)
	}
	if input.DeploymentInputs != nil && len(*input.DeploymentInputs) > 1 {
		return invalidDeploymentOverride(nil)
	}
	if t := input.DeploymentTarget; t != nil {
		if (t.Platform != "" && t.Platform != DeploymentKubernetes) || t.RowID != 0 || t.ID != "" || t.ProjectID != "" || t.DepartmentID != "" || t.CreatedBy != "" || !t.CreatedAt.IsZero() || !t.UpdatedAt.IsZero() || t.EnvironmentID != "" || t.HostID != "" || t.HostIDs != nil || t.WorkingDirectory != "" || t.IsActive {
			return invalidDeploymentOverride(nil)
		}
	}
	if len(input.Kubernetes) > 0 {
		if len(input.Kubernetes) > 64*1024 {
			return invalidDeploymentOverride(nil)
		}
		if err := validateJSONPatchShape(input.Kubernetes, reflect.TypeFor[KubernetesDeploymentSettings](), true); err != nil {
			return invalidDeploymentOverride(err)
		}
	}
	return nil
}

func validateJSONPatchShape(raw json.RawMessage, typ reflect.Type, objectRequired bool) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		if objectRequired {
			return ErrDeploymentOverride
		}
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Struct {
		if len(raw) == 0 || raw[0] != '{' {
			return ErrDeploymentOverride
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		allowed := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = field.Type
			}
		}
		for key, value := range fields {
			field, ok := allowed[key]
			if !ok {
				return ErrDeploymentOverride
			}
			if err := validateJSONPatchShape(value, field, false); err != nil {
				return err
			}
		}
	} else if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		for _, item := range items {
			if err := validateJSONPatchShape(item, typ.Elem(), false); err != nil {
				return err
			}
		}
	} else if typ.Kind() == reflect.Map {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return err
		}
		for _, item := range entries {
			if err := validateJSONPatchShape(item, typ.Elem(), false); err != nil {
				return err
			}
		}
	} else {
		value := reflect.New(typ).Interface()
		if err := json.Unmarshal(raw, value); err != nil {
			return err
		}
	}
	return nil
}

// ApplyWorkflowKubernetesPatch 使用已有标准 MergePatch 实现，返回独立副本并拒绝未知字段。
func ApplyWorkflowKubernetesPatch(base *KubernetesDeploymentSettings, patch json.RawMessage) (*KubernetesDeploymentSettings, error) {
	if len(patch) > 0 {
		if err := ValidateWorkflowDeploymentOverride(&WorkflowDeploymentOverride{Kubernetes: patch}); err != nil {
			return nil, err
		}
	}
	if base == nil && !HasWorkflowDeploymentOverride(&WorkflowDeploymentOverride{Kubernetes: patch}) {
		return nil, nil
	}
	if base == nil {
		base = DefaultKubernetesDeploymentSettings()
	}
	data, err := json.Marshal(base)
	if err != nil {
		return nil, invalidDeploymentOverride(err)
	}
	if len(patch) > 0 {
		data, err = jsonpatch.MergePatch(data, patch)
		if err != nil {
			return nil, invalidDeploymentOverride(err)
		}
	}
	var result KubernetesDeploymentSettings
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return nil, invalidDeploymentOverride(err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, invalidDeploymentOverride(nil)
	}
	if err = ValidateKubernetesDeploymentSettings(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
