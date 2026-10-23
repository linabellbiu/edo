package assistant

import (
	"context"
	"io"

	"edo/internal/kube"
	"edo/internal/model"
)

func (t *assistantTool) kubeDiagnostics(ctx context.Context, input toolInput) (any, error) {
	resource := input.Resource
	if t.name == "kubernetes_pod_logs" {
		resource = "pods"
	}
	def, ok := kube.ResourceType(resource)
	if !ok || input.UID == "" || input.Name == "" || input.Namespace == "" || input.Namespace == "*" || t.service.deps.Kubernetes == nil {
		return nil, t.service.failure(ctx, "assistant_kube_diagnostic_input", ErrInvalidInput, ErrInvalidInput)
	}
	if t.name == "kubernetes_diagnostics" && resource != "pods" && resource != "deployments" && resource != "services" {
		return nil, t.service.failure(ctx, "assistant_kube_diagnostic_type", ErrInvalidInput, ErrInvalidInput)
	}
	if err := t.require(ctx, def.Permission+".read"); err != nil {
		return nil, err
	}
	// Service关联端点和Deployment关联Pod，关联读取同样需要工作负载查看权限。
	if resource == "services" {
		if err := t.require(ctx, "kubernetes.workload.read"); err != nil {
			return nil, err
		}
	}
	var cluster model.KubernetesCluster
	if input.ClusterID == "" {
		return nil, t.service.failure(ctx, "assistant_kube_cluster_input", ErrInvalidInput, ErrInvalidInput)
	}
	if err := t.diagnosticQuery(ctx).Select("ID").Where("is_active = ?", true).First(&cluster, &model.KubernetesCluster{ID: input.ClusterID}).Error; err != nil {
		return nil, t.service.failure(ctx, "assistant_kube_diagnostic_scope", err, ErrNotFound)
	}
	client := t.service.deps.Kubernetes
	object, err := client.GetResource(ctx, cluster.ID, resource, input.Namespace, input.Name, false)
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_kube_diagnostic_resource", err, ErrDiagnosticRead)
	}
	meta, _ := object["metadata"].(map[string]any)
	if meta["uid"] != input.UID {
		return nil, t.service.failure(ctx, "assistant_kube_diagnostic_identity", ErrActionState, ErrActionState)
	}
	if t.name == "kubernetes_diagnostics" {
		result, err := client.ResourceDiagnostics(ctx, cluster.ID, resource, input.Namespace, input.Name, input.UID)
		if err != nil {
			return nil, t.service.failure(ctx, "assistant_kube_related_read", err, ErrDiagnosticRead)
		}
		if result == nil || result.UID != input.UID {
			return nil, t.service.failure(ctx, "assistant_kube_related_identity", ErrActionState, ErrActionState)
		}
		// Errors含内部SDK错误，只给模型可核对的缺失项目，不序列化原始错误。
		unavailable := []string{}
		for component, cause := range result.Errors {
			unavailable = append(unavailable, component)
			t.service.logger.WarnContext(ctx, "关联诊断项不可用", "operation", "assistant_kube_related_partial", "component", component, "err", cause)
		}
		bounded, truncated := boundedKubeDiagnostics(result)
		return map[string]any{"cluster_id": cluster.ID, "resource": resourceSummary(object), "related": bounded, "unavailable": unavailable, "truncated": truncated}, nil
	}
	if input.Container == "" || !podHasContainer(object, input.Container) {
		return nil, t.service.failure(ctx, "assistant_pod_container", ErrInvalidInput, ErrInvalidInput)
	}
	if err := t.service.authorizeUsed(ctx, t.user); err != nil {
		return nil, err
	}
	stream, err := client.PodLogSnapshot(ctx, cluster.ID, input.Namespace, input.Name, input.Container, 100, input.Previous)
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_pod_log_open", err, ErrDiagnosticRead, "cluster_id", cluster.ID, "namespace", input.Namespace, "pod", input.Name, "container", input.Container)
	}
	defer stream.Close()
	raw, err := io.ReadAll(io.LimitReader(stream, maxDiagnosticRead+1))
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_pod_log_read", err, ErrDiagnosticRead, "cluster_id", cluster.ID, "namespace", input.Namespace, "pod", input.Name)
	}
	latest, err := client.GetResource(ctx, cluster.ID, "pods", input.Namespace, input.Name, false)
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_pod_log_recheck", err, ErrDiagnosticRead)
	}
	latestMeta, _ := latest["metadata"].(map[string]any)
	if latestMeta["uid"] != input.UID {
		return nil, t.service.failure(ctx, "assistant_pod_log_identity_changed", ErrActionState, ErrActionState)
	}
	text, truncated := diagnosticLogTail(string(raw), len(raw) > maxDiagnosticRead)
	return map[string]any{"cluster_id": cluster.ID, "namespace": input.Namespace, "pod": input.Name, "uid": input.UID, "container": input.Container, "previous": input.Previous, "tail_lines": 100, "logs": text, "truncated": truncated}, nil
}

func boundedKubeDiagnostics(input *kube.ResourceDiagnostics) (kube.ResourceDiagnostics, bool) {
	result := *input
	truncated := false
	clip := func(size int) int {
		if size > 20 {
			truncated = true
			return 20
		}
		return size
	}
	result.Endpoints = append([]kube.ServiceEndpoint(nil), input.Endpoints[:clip(len(input.Endpoints))]...)
	for i := range result.Endpoints {
		row := &result.Endpoints[i]
		row.Addresses = row.Addresses[:clip(len(row.Addresses))]
		row.Ports = row.Ports[:clip(len(row.Ports))]
	}
	result.Revisions = append([]kube.DeploymentRevision(nil), input.Revisions[:clip(len(input.Revisions))]...)
	for i := range result.Revisions {
		row := &result.Revisions[i]
		row.Pods = row.Pods[:clip(len(row.Pods))]
		row.Images = row.Images[:clip(len(row.Images))]
	}
	result.Containers = input.Containers[:clip(len(input.Containers))]
	return result, truncated
}

func podHasContainer(object map[string]any, name string) bool {
	spec, _ := object["spec"].(map[string]any)
	for _, key := range []string{"containers", "initContainers", "ephemeralContainers"} {
		items, _ := spec[key].([]any)
		for _, item := range items {
			container, _ := item.(map[string]any)
			if container["name"] == name {
				return true
			}
		}
	}
	return false
}

func resourceDiagnosticSpec(item map[string]any) map[string]any {
	if item["kind"] == "Secret" {
		return nil
	}
	spec, _ := item["spec"].(map[string]any)
	result := projectFields(spec, "replicas", "nodeName", "restartPolicy", "unschedulable", "selector", "type", "ports", "clusterIP", "externalName", "rules", "storageClassName", "volumeName", "accessModes", "capacity", "claimRef", "persistentVolumeReclaimPolicy", "volumeMode")
	for _, key := range []string{"containers", "initContainers", "ephemeralContainers"} {
		if value := diagnosticContainers(spec[key]); len(value) > 0 {
			result[key] = value
		}
	}
	if template, ok := spec["template"].(map[string]any); ok {
		if pod, ok := template["spec"].(map[string]any); ok {
			result["template_containers"] = diagnosticContainers(pod["containers"])
		}
	}
	return result
}

func projectFields(value map[string]any, keys ...string) map[string]any {
	result := map[string]any{}
	for _, key := range keys {
		if v, ok := value[key]; ok {
			result[key] = v
		}
	}
	return result
}

func diagnosticContainers(value any) []map[string]any {
	items, _ := value.([]any)
	result := []map[string]any{}
	for _, value := range items {
		if len(result) >= 20 {
			break
		}
		item, _ := value.(map[string]any)
		row := projectFields(item, "name", "image", "ports", "resources")
		for _, key := range []string{"startupProbe", "readinessProbe", "livenessProbe"} {
			if probe, ok := item[key].(map[string]any); ok {
				summary := projectFields(probe, "initialDelaySeconds", "periodSeconds", "timeoutSeconds", "failureThreshold", "successThreshold")
				if http, ok := probe["httpGet"].(map[string]any); ok {
					summary["httpGet"] = projectFields(http, "path", "port", "scheme")
				}
				if tcp, ok := probe["tcpSocket"].(map[string]any); ok {
					summary["tcpSocket"] = projectFields(tcp, "port")
				}
				if _, ok := probe["exec"]; ok {
					summary["type"] = "exec，命令不提供"
				}
				row[key] = summary
			}
		}
		result = append(result, row)
	}
	return result
}
