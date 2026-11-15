package kube

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sort"

	"edo/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	templateOptionsMarker         = "edo.io/template-options"
	templateOptionsFields         = "edo.io/template-option-fields"
	templateOptionsLabelKeys      = "edo.io/template-option-label-keys"
	templateOptionsAnnotationKeys = "edo.io/template-option-annotation-keys"
)

var ErrDeploymentTemplateReference = errors.New("引用的配置、密钥或存储声明不可用，请检查名称、键和查看权限")

// checkDeploymentTemplateOptions 仅核验同一命名空间内的引用，不读取密钥内容或创建依赖资源。
func (s *Service) checkDeploymentTemplateOptions(ctx context.Context, t model.DeploymentTarget, options *model.KubernetesDeploymentOptions) error {
	if options == nil {
		return nil
	}
	type sourceReference struct{ resource, name string }
	references := map[sourceReference][]string{}
	for _, env := range options.Environment {
		resource := ""
		if env.Kind == "configmap" {
			resource = "configmaps"
		} else if env.Kind == "secret" {
			resource = "secrets"
		}
		if resource != "" {
			ref := sourceReference{resource, env.SourceName}
			references[ref] = append(references[ref], env.SourceKey)
		}
	}
	for _, volume := range options.Volumes {
		resource := map[string]string{"configmap": "configmaps", "secret": "secrets", "pvc": "persistentvolumeclaims"}[volume.Kind]
		if resource != "" {
			ref := sourceReference{resource, volume.SourceName}
			if _, exists := references[ref]; !exists {
				references[ref] = nil
			}
		}
	}
	for _, name := range options.ImagePullSecrets {
		ref := sourceReference{"secrets", name}
		if _, exists := references[ref]; !exists {
			references[ref] = nil
		}
	}
	ordered := make([]sourceReference, 0, len(references))
	for ref := range references {
		ordered = append(ordered, ref)
	}
	slices.SortFunc(ordered, func(a, b sourceReference) int {
		if a.resource < b.resource || a.resource == b.resource && a.name < b.name {
			return -1
		}
		if a == b {
			return 0
		}
		return 1
	})
	for _, ref := range ordered {
		object, err := s.GetResource(ctx, t.RuntimeID, ref.resource, t.Namespace, ref.name, false)
		if err == nil {
			for _, key := range references[ref] {
				if !templateReferenceHasKey(object, ref.resource, key) {
					err = ErrResourceNotFound
					break
				}
			}
		}
		if err == nil && ref.resource == "secrets" && slices.Contains(options.ImagePullSecrets, ref.name) {
			typ, _ := object["type"].(string)
			if typ != "" && typ != string(corev1.SecretTypeDockerConfigJson) && typ != string(corev1.SecretTypeDockercfg) {
				err = ErrInvalidResource
			}
		}
		if err != nil {
			slog.WarnContext(ctx, "Kubernetes 部署引用核验失败", "operation", "kubernetes_template_reference", "cluster_id", t.RuntimeID, "namespace", t.Namespace, "resource", ref.resource, "name", ref.name, "err", err)
			for _, permission := range []error{ErrRBACForbidden, ErrRBACSyncPending, ErrResourceForbidden} {
				if errors.Is(err, permission) {
					return permission
				}
			}
			return ErrDeploymentTemplateReference
		}
	}
	return nil
}

func templateReferenceHasKey(object map[string]any, resource, key string) bool {
	if resource == "secrets" {
		// 普通详情提供键名时才校验键；绝不为此获取原始密钥。
		if keys, present := object["keys"].([]any); present {
			return slices.ContainsFunc(keys, func(value any) bool { return value == key })
		}
		return true
	}
	data, _ := object["data"].(map[string]any)
	_, exists := data[key]
	return exists
}

// applyDeploymentTemplateOptions 只扩展已生成的容器和模板，身份选择器保持平台生成值。
func applyDeploymentTemplateOptions(workload *appsv1.Deployment, options *model.KubernetesDeploymentOptions) {
	if options == nil || len(workload.Spec.Template.Spec.Containers) != 1 {
		return
	}
	workload.Labels = copyTemplateLabels(workload.Labels)
	workload.Annotations = copyTemplateLabels(workload.Annotations)
	workload.Spec.Template.Labels = copyTemplateLabels(workload.Spec.Template.Labels)
	workload.Spec.Template.Annotations = copyTemplateLabels(workload.Spec.Template.Annotations)
	for key, value := range options.Labels {
		workload.Labels[key], workload.Spec.Template.Labels[key] = value, value
	}
	for key, value := range options.Annotations {
		workload.Annotations[key], workload.Spec.Template.Annotations[key] = value, value
	}
	workload.Annotations[templateOptionsMarker] = "1"
	fields, _ := json.Marshal(templateOptionFields(options))
	workload.Annotations[templateOptionsFields] = string(fields)
	workload.Annotations[templateOptionsLabelKeys] = templateOptionKeys(options.Labels)
	workload.Annotations[templateOptionsAnnotationKeys] = templateOptionKeys(options.Annotations)
	if options.MinReadySeconds != nil {
		workload.Spec.MinReadySeconds = *options.MinReadySeconds
	}
	workload.Spec.RevisionHistoryLimit = options.RevisionHistoryLimit
	workload.Spec.ProgressDeadlineSeconds = options.ProgressDeadlineSeconds
	pod := &workload.Spec.Template.Spec
	container := &pod.Containers[0]
	for _, env := range options.Environment {
		item := corev1.EnvVar{Name: env.Name}
		switch env.Kind {
		case "value":
			item.Value = env.Value
		case "configmap":
			item.ValueFrom = &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: env.SourceName}, Key: env.SourceKey}}
		case "secret":
			item.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: env.SourceName}, Key: env.SourceKey}}
		}
		container.Env = append(container.Env, item)
	}
	for _, volume := range options.Volumes {
		item := corev1.Volume{Name: volume.Name}
		mount := corev1.VolumeMount{Name: volume.Name, MountPath: volume.MountPath, SubPath: volume.SubPath, ReadOnly: volume.ReadOnly}
		switch volume.Kind {
		case "pvc":
			item.PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{ClaimName: volume.SourceName, ReadOnly: volume.ReadOnly}
		case "configmap":
			item.ConfigMap = &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: volume.SourceName}}
			mount.ReadOnly = true
		case "secret":
			item.Secret = &corev1.SecretVolumeSource{SecretName: volume.SourceName}
			mount.ReadOnly = true
		case "emptyDir":
			item.EmptyDir = &corev1.EmptyDirVolumeSource{}
		}
		pod.Volumes = append(pod.Volumes, item)
		container.VolumeMounts = append(container.VolumeMounts, mount)
	}
	pod.NodeSelector = copyTemplateLabels(options.NodeSelector)
	for _, tolerance := range options.Tolerations {
		operator := corev1.TolerationOperator(tolerance.Operator)
		if operator == "" {
			operator = corev1.TolerationOpEqual
		}
		pod.Tolerations = append(pod.Tolerations, corev1.Toleration{Key: tolerance.Key, Operator: operator, Value: tolerance.Value, Effect: corev1.TaintEffect(tolerance.Effect), TolerationSeconds: tolerance.TolerationSeconds})
	}
	for _, name := range options.ImagePullSecrets {
		if !slices.ContainsFunc(pod.ImagePullSecrets, func(ref corev1.LocalObjectReference) bool { return ref.Name == name }) {
			pod.ImagePullSecrets = append(pod.ImagePullSecrets, corev1.LocalObjectReference{Name: name})
		}
	}
}

func copyTemplateLabels(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func templateOptionKeys(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	encoded, _ := json.Marshal(keys)
	return string(encoded)
}

// templateOptionFields 记录实际由扩展声明的字段，不因其他参数启用而接管整个容器组。
func templateOptionFields(options *model.KubernetesDeploymentOptions) []string {
	fields := []string{}
	if options == nil {
		return fields
	}
	for field, present := range map[string]bool{
		"environment": options.Environment != nil, "volumes": options.Volumes != nil, "node_selector": options.NodeSelector != nil, "tolerations": options.Tolerations != nil, "image_pull_secrets": options.ImagePullSecrets != nil,
		"min_ready_seconds": options.MinReadySeconds != nil, "revision_history_limit": options.RevisionHistoryLimit != nil, "progress_deadline_seconds": options.ProgressDeadlineSeconds != nil,
	} {
		if present {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	return fields
}

// clearDeploymentTemplateOptions 只清除有字段管理记录的扩展，不接管其他工具添加的字段。
func clearDeploymentTemplateOptions(desired, current map[string]any, options *model.KubernetesDeploymentOptions) {
	currentMeta, _ := current["metadata"].(map[string]any)
	currentAnnotations, _ := currentMeta["annotations"].(map[string]any)
	if options == nil && currentAnnotations[templateOptionsMarker] != "1" {
		preserveUnmanagedTemplateContainerFields(desired, current, func(string) bool { return false })
		return
	}
	var previousFields []string
	encodedFields, _ := currentAnnotations[templateOptionsFields].(string)
	_ = json.Unmarshal([]byte(encodedFields), &previousFields)
	currentFields := templateOptionFields(options)
	managed := func(field string) bool {
		return slices.Contains(previousFields, field) || slices.Contains(currentFields, field)
	}
	metadata := desired["metadata"].(map[string]any)
	spec := desired["spec"].(map[string]any)
	template := spec["template"].(map[string]any)
	podMetadata := template["metadata"].(map[string]any)
	pod := template["spec"].(map[string]any)
	for field, key := range map[string]string{"labels": templateOptionsLabelKeys, "annotations": templateOptionsAnnotationKeys} {
		var previouslyManaged []string
		encoded, _ := currentAnnotations[key].(string)
		_ = json.Unmarshal([]byte(encoded), &previouslyManaged)
		for _, meta := range []map[string]any{metadata, podMetadata} {
			values, _ := meta[field].(map[string]any)
			if values == nil {
				values = map[string]any{}
				meta[field] = values
			}
			for _, name := range previouslyManaged {
				if _, present := values[name]; !present {
					values[name] = nil
				}
			}
		}
	}
	if options == nil {
		annotations := metadata["annotations"].(map[string]any)
		for _, key := range []string{templateOptionsMarker, templateOptionsFields, templateOptionsLabelKeys, templateOptionsAnnotationKeys} {
			annotations[key] = nil
		}
	}
	for field, configKey := range map[string]string{"minReadySeconds": "min_ready_seconds", "revisionHistoryLimit": "revision_history_limit", "progressDeadlineSeconds": "progress_deadline_seconds"} {
		if _, present := spec[field]; !present && managed(configKey) {
			spec[field] = nil
		}
	}
	for field, configKey := range map[string]string{"nodeSelector": "node_selector", "tolerations": "tolerations", "volumes": "volumes", "imagePullSecrets": "image_pull_secrets"} {
		if _, present := pod[field]; !present && managed(configKey) {
			pod[field] = nil
		}
	}
	if values, present := pod["nodeSelector"].(map[string]any); present && managed("node_selector") {
		currentSpec, _ := current["spec"].(map[string]any)
		currentTemplate, _ := currentSpec["template"].(map[string]any)
		currentPod, _ := currentTemplate["spec"].(map[string]any)
		previous, _ := currentPod["nodeSelector"].(map[string]any)
		for key := range previous {
			if _, declared := values[key]; !declared {
				values[key] = nil
			}
		}
	}
	preserveUnmanagedTemplateContainerFields(desired, current, managed)
}

// containers 由 MergePatch 整体替换；未声明的环境和挂载要从准确现有容器保留。
func preserveUnmanagedTemplateContainerFields(desired, current map[string]any, managed func(string) bool) {
	desiredSpec, _ := desired["spec"].(map[string]any)
	desiredTemplate, _ := desiredSpec["template"].(map[string]any)
	pod, _ := desiredTemplate["spec"].(map[string]any)
	currentSpec, _ := current["spec"].(map[string]any)
	currentTemplate, _ := currentSpec["template"].(map[string]any)
	currentPod, _ := currentTemplate["spec"].(map[string]any)
	currentContainers, _ := currentPod["containers"].([]any)
	containers, _ := pod["containers"].([]any)
	for _, item := range containers {
		container, _ := item.(map[string]any)
		for _, old := range currentContainers {
			existing, _ := old.(map[string]any)
			if container["name"] != existing["name"] {
				continue
			}
			for field, configKey := range map[string]string{"env": "environment", "volumeMounts": "volumes"} {
				if _, set := container[field]; !set && !managed(configKey) {
					if value, exists := existing[field]; exists {
						container[field] = value
					}
				}
			}
		}
	}
}
