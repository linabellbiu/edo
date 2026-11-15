package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"edo/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

var ErrDeploymentTemplateConflict = errors.New("同名 Kubernetes 资源已被其他部署占用，不能执行，请更换 Deployment 名称")
var ErrDeploymentTemplateCheck = errors.New("检查 Kubernetes 部署目标失败，请检查集群连接和资源权限")

func deploymentTemplateOwner(projectID, appID, targetID string) map[string]string {
	return map[string]string{"edo.io/project-id": projectID, "edo.io/application-id": appID, "edo.io/target-id": targetID, "app.kubernetes.io/managed-by": "edo"}
}
func templateOwned(object map[string]any, projectID, appID, targetID string) bool {
	item := &metav1.PartialObjectMetadata{}
	metadata, _ := object["metadata"].(map[string]any)
	if runtime.DefaultUnstructuredConverter.FromUnstructured(map[string]any{"metadata": metadata}, item) != nil {
		return false
	}
	a := item.Annotations
	return appID != "" && a["edo.io/project-id"] == projectID && a["edo.io/application-id"] == appID && (targetID == "" || a["edo.io/target-id"] == targetID)
}
func templateObject(value any) map[string]any {
	result, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(value)
	delete(result, "status")
	if meta, ok := result["metadata"].(map[string]any); ok {
		for key := range meta {
			switch key {
			case "name", "namespace", "uid", "resourceVersion", "labels", "annotations":
			default:
				delete(meta, key)
			}
		}
	}
	return result
}
func templateFailure(ctx context.Context, clusterID, namespace, name string, err error) error {
	slog.WarnContext(ctx, "Kubernetes 模板部署失败", "operation", "kubernetes_template", "cluster_id", clusterID, "namespace", namespace, "workload_name", name, "err", err)
	for _, stable := range []error{ErrDeploymentTemplateConflict, ErrDeploymentTemplateReference, ErrDeploymentTemplateHeadlessType, ErrDeploymentTemplateExternalNamePorts, ErrRBACForbidden, ErrRBACSyncPending, ErrResourceForbidden, ErrDeploymentRolloutTimeout, ErrDeploymentProgressDeadline, ErrDeploymentTargetChanged, ErrDeploymentStatusUnavailable, model.ErrKubernetesDeploymentSettings} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	return ErrDeploymentTemplateCheck
}

type deploymentTemplateState struct{ namespace, deployment, service map[string]any }

// CheckDeploymentTemplate 只读核验重名资源和操作权限，不创建命名空间或工作负载。
func (s *Service) CheckDeploymentTemplate(ctx context.Context, target model.DeploymentTarget, appID string, cfg *model.KubernetesDeploymentSettings) (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = templateFailure(ctx, target.RuntimeID, target.Namespace, target.WorkloadName, resultErr)
		}
	}()
	_, err := s.deploymentTemplateState(ctx, target, appID, "", cfg)
	return err
}
func (s *Service) deploymentTemplateState(ctx context.Context, t model.DeploymentTarget, appID, targetID string, cfg *model.KubernetesDeploymentSettings) (*deploymentTemplateState, error) {
	if cfg == nil || appID == "" || t.ProjectID == "" || t.ID == "" || len(validation.IsDNS1123Label(t.Namespace)) != 0 || len(validation.IsDNS1123Label(t.WorkloadName)) != 0 || len(validation.IsDNS1123Label(t.ContainerName)) != 0 {
		return nil, ErrDeploymentConfigInvalid
	}
	if err := model.ValidateKubernetesDeploymentSettings(cfg); err != nil {
		return nil, err
	}
	read := func(kind, namespace, name string) (map[string]any, error) {
		object, err := s.GetResource(ctx, t.RuntimeID, kind, namespace, name, false)
		if errors.Is(err, ErrResourceNotFound) {
			return nil, nil
		}
		return object, err
	}
	state := &deploymentTemplateState{}
	var err error
	state.namespace, err = read("namespaces", "", t.Namespace)
	if err != nil {
		return nil, err
	}
	state.deployment, err = read("deployments", t.Namespace, t.WorkloadName)
	if err != nil {
		return nil, err
	}
	state.service, err = read("services", t.Namespace, t.WorkloadName)
	if err != nil {
		return nil, err
	}
	for _, object := range []map[string]any{state.deployment, state.service} {
		if object != nil && !templateOwned(object, t.ProjectID, appID, targetID) {
			return nil, ErrDeploymentTemplateConflict
		}
	}
	if err = checkDeploymentTemplateServiceType(state.service, cfg); err != nil {
		return nil, err
	}
	require := func(kind, namespace string, existing map[string]any) error {
		action := "create"
		if existing != nil {
			action = "update"
		}
		return s.RequireResourcePermission(ctx, t.RuntimeID, kind, namespace, action, false)
	}
	if state.namespace == nil {
		if err = require("namespaces", "", nil); err != nil {
			return nil, err
		}
	}
	if err = require("deployments", t.Namespace, state.deployment); err != nil {
		return nil, err
	}
	if err = s.RequireResourcePermission(ctx, t.RuntimeID, "deployments", t.Namespace, "update", false); err != nil {
		return nil, err
	}
	enabled := false
	for _, p := range cfg.Ports {
		enabled = enabled || p.Enabled
	}
	if enabled || cfg.EffectiveServiceType() == "ExternalName" || state.service != nil {
		if err = require("services", t.Namespace, state.service); err != nil {
			return nil, err
		}
	}
	if err = s.checkDeploymentTemplateOptions(ctx, t, cfg.Options); err != nil {
		return nil, err
	}
	return state, nil
}

// DeployTemplate 仅创建或更新当前应用和准确目标拥有的资源；冲突时不接管、删除或清理。
func (s *Service) DeployTemplate(ctx context.Context, record model.DeploymentRecord, cfg *model.KubernetesDeploymentSettings, registryJSON []byte, timeout time.Duration) (previous, uid string, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = templateFailure(ctx, record.RuntimeID, record.Namespace, record.WorkloadName, resultErr)
		}
	}()
	t := model.DeploymentTarget{ID: record.TargetID, ProjectID: record.ProjectID, RuntimeID: record.RuntimeID, Namespace: record.Namespace, WorkloadName: record.WorkloadName, ContainerName: record.ContainerName}
	state, err := s.deploymentTemplateState(ctx, t, record.ApplicationID, t.ID, cfg)
	if err != nil {
		return "", "", err
	}
	owner := deploymentTemplateOwner(t.ProjectID, record.ApplicationID, t.ID)
	labels := map[string]string{"edo.io/target-id": t.ID, "app.kubernetes.io/managed-by": "edo"}
	metadata := metav1.ObjectMeta{Name: t.WorkloadName, Namespace: t.Namespace, Labels: labels, Annotations: owner}
	ports := []corev1.ContainerPort{}
	servicePorts := []corev1.ServicePort{}
	type containerPortKey struct {
		port     int32
		protocol corev1.Protocol
	}
	containerPorts := map[containerPortKey]bool{}
	serviceType := cfg.EffectiveServiceType()
	for _, p := range cfg.Ports {
		if p.Enabled && serviceType != "ExternalName" {
			protocol := corev1.Protocol(p.EffectiveProtocol())
			key := containerPortKey{port: p.TargetPort, protocol: protocol}
			if !containerPorts[key] {
				ports = append(ports, corev1.ContainerPort{Name: p.Name, ContainerPort: p.TargetPort, Protocol: protocol})
				containerPorts[key] = true
			}
			sp := corev1.ServicePort{Name: p.Name, Port: p.Port, TargetPort: intstr.FromInt32(p.TargetPort), Protocol: protocol}
			if serviceType == "NodePort" || serviceType == "LoadBalancer" {
				sp.NodePort = p.NodePort
			}
			servicePorts = append(servicePorts, sp)
		}
	}
	container := corev1.Container{Name: t.ContainerName, Image: record.Image, Ports: ports, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cfg.CPURequest), corev1.ResourceMemory: resource.MustParse(cfg.MemoryRequest)}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cfg.CPULimit), corev1.ResourceMemory: resource.MustParse(cfg.MemoryLimit)}}}
	container.Command = append([]string(nil), cfg.Command...)
	container.Args = append([]string(nil), cfg.Args...)
	container.WorkingDir = cfg.WorkingDir
	container.ImagePullPolicy = corev1.PullPolicy(cfg.ImagePullPolicy)
	container.ReadinessProbe = kubernetesTemplateProbe(cfg.ReadinessProbe)
	container.LivenessProbe = kubernetesTemplateProbe(cfg.LivenessProbe)
	container.StartupProbe = kubernetesTemplateProbe(cfg.StartupProbe)
	off := false
	workload := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metadata, Spec: appsv1.DeploymentSpec{Replicas: &cfg.Replicas, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{"edo.io/deployment-id": record.ID}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &off, Containers: []corev1.Container{container}}}}}
	workload.Spec.Template.Spec.TerminationGracePeriodSeconds = cfg.TerminationGracePeriodSeconds
	var secret map[string]any
	if len(registryJSON) > 0 {
		name := t.WorkloadName
		if len(name) > 53 {
			name = name[:53]
		}
		name += "-registry"
		current, readErr := s.GetResource(ctx, t.RuntimeID, "secrets", t.Namespace, name, false)
		if errors.Is(readErr, ErrResourceNotFound) {
			current = nil
		} else if readErr != nil {
			return "", "", readErr
		}
		// 普通 Secret 详情不返回注解，因此只通过独立的原始查询核验身份，不向外暴露内容。
		if current != nil {
			_, q, qerr := s.authorizeResourceOperation(ctx, t.RuntimeID, "secrets", t.Namespace, "update", false)
			if qerr != nil {
				return "", "", qerr
			}
			live, qerr := q.typed.CoreV1().Secrets(t.Namespace).Get(ctx, name, metav1.GetOptions{})
			if qerr != nil {
				return "", "", qerr
			}
			current = templateObject(live)
			if !templateOwned(current, t.ProjectID, record.ApplicationID, t.ID) {
				return "", "", ErrDeploymentTemplateConflict
			}
		}
		secret = map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": name, "namespace": t.Namespace, "annotations": owner}, "type": "kubernetes.io/dockerconfigjson", "data": map[string]any{corev1.DockerConfigJsonKey: base64.StdEncoding.EncodeToString(registryJSON)}}
		bindTemplateIdentity(secret, current)
		action := "create"
		if current != nil {
			action = "update"
		}
		if err = s.RequireResourcePermission(ctx, t.RuntimeID, "secrets", t.Namespace, action, false); err != nil {
			return "", "", err
		}
		workload.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: name}}
	}
	applyDeploymentTemplateOptions(workload, cfg.Options)
	desired := templateObject(workload)
	clearDeploymentTemplateOptions(desired, state.deployment, cfg.Options)
	// 可选配置恢复默认时，MergePatch 必须显式清除上一次模板写入的值。
	desired["spec"].(map[string]any)["strategy"] = nil
	if cfg.TerminationGracePeriodSeconds == nil {
		desired["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["terminationGracePeriodSeconds"] = nil
	}
	if cfg.Strategy != "" || cfg.MaxSurge != "" || cfg.MaxUnavailable != "" {
		strategy := map[string]any{"type": cfg.Strategy}
		if cfg.Strategy == "Recreate" {
			strategy["rollingUpdate"] = nil
		} else {
			strategy["type"] = "RollingUpdate"
			rolling := map[string]any{"maxSurge": nil, "maxUnavailable": nil}
			if cfg.MaxSurge != "" {
				rolling["maxSurge"] = kubernetesTemplateRolloutValue(cfg.MaxSurge)
			}
			if cfg.MaxUnavailable != "" {
				rolling["maxUnavailable"] = kubernetesTemplateRolloutValue(cfg.MaxUnavailable)
			}
			strategy["rollingUpdate"] = rolling
		}
		desired["spec"].(map[string]any)["strategy"] = strategy
	}
	bindTemplateIdentity(desired, state.deployment)
	service := deploymentTemplateService(state.service, cfg, record.ID, metadata, labels, servicePorts)
	validate := func(kind, name string, object map[string]any) error {
		if object == nil {
			return nil
		}
		_, existing := object["metadata"].(map[string]any)["uid"]
		if existing {
			return s.ValidateResource(ctx, t.RuntimeID, kind, t.Namespace, name, object)
		}
		return s.ValidateResourceCreate(ctx, t.RuntimeID, kind, t.Namespace, name, object)
	}
	if state.namespace != nil {
		if err = validate("deployments", t.WorkloadName, desired); err != nil {
			return "", "", err
		}
		if err = validate("services", t.WorkloadName, service); err != nil {
			return "", "", err
		}
	}
	if state.namespace == nil {
		ns := &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: t.Namespace}}
		if _, err = s.SaveResource(ctx, t.RuntimeID, "namespaces", "", t.Namespace, templateObject(ns), true); err != nil {
			return "", "", err
		}
	}
	save := func(kind, name string, object map[string]any) (map[string]any, error) {
		_, exists := object["metadata"].(map[string]any)["uid"]
		return s.SaveResource(ctx, t.RuntimeID, kind, t.Namespace, name, object, !exists)
	}
	if secret != nil {
		name := secret["metadata"].(map[string]any)["name"].(string)
		if _, err = save("secrets", name, secret); err != nil {
			return "", "", err
		}
	}
	if state.deployment != nil {
		var old appsv1.Deployment
		_ = runtime.DefaultUnstructuredConverter.FromUnstructured(state.deployment, &old)
		for _, c := range old.Spec.Template.Spec.Containers {
			if c.Name == t.ContainerName {
				previous = c.Image
			}
		}
	}
	result, err := save("deployments", t.WorkloadName, desired)
	if err != nil {
		return previous, "", err
	}
	uid, _ = result["metadata"].(map[string]any)["uid"].(string)
	if service != nil {
		if _, err = save("services", t.WorkloadName, service); err != nil {
			return previous, uid, err
		}
	}
	_, err = s.DeployImageIfCurrent(ctx, t.RuntimeID, t.Namespace, t.WorkloadName, t.ContainerName, record.Image, record.ID, timeout, DeploymentImageIdentity{UID: uid, Image: record.Image, DeploymentID: record.ID})
	return previous, uid, err
}
func bindTemplateIdentity(desired, current map[string]any) {
	if current == nil {
		return
	}
	meta := desired["metadata"].(map[string]any)
	original, _ := current["metadata"].(map[string]any)
	meta["uid"], meta["resourceVersion"] = original["uid"], original["resourceVersion"]
}

// RegistryPullConfig 仅在执行期间生成，不写入任务快照、日志或审计。
func RegistryPullConfig(server, username, password string) []byte {
	if server == "" || username == "" || password == "" {
		return nil
	}
	value, _ := json.Marshal(map[string]any{"auths": map[string]any{server: map[string]string{"username": username, "password": password, "auth": base64.StdEncoding.EncodeToString([]byte(username + ":" + password))}}})
	return value
}

func kubernetesTemplateProbe(value *model.KubernetesDeploymentProbe) *corev1.Probe {
	if value == nil {
		return nil
	}
	probe := &corev1.Probe{InitialDelaySeconds: value.InitialDelaySeconds, PeriodSeconds: value.PeriodSeconds, TimeoutSeconds: value.TimeoutSeconds, FailureThreshold: value.FailureThreshold}
	if value.Kind == "tcp" {
		probe.TCPSocket = &corev1.TCPSocketAction{Port: intstr.FromInt32(value.Port)}
	} else {
		probe.HTTPGet = &corev1.HTTPGetAction{Port: intstr.FromInt32(value.Port), Path: value.Path, Scheme: corev1.URIScheme(value.Scheme)}
	}
	return probe
}
func kubernetesTemplateRolloutValue(value string) any {
	parsed := intstr.Parse(value)
	if parsed.Type == intstr.Int {
		return int64(parsed.IntVal)
	}
	return parsed.StrVal
}
