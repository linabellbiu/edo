package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var (
	ErrInvalidResource   = errors.New("资源参数无效，请检查名称、命名空间和配置")
	ErrResourceNotFound  = errors.New("资源已不存在，请刷新列表")
	ErrResourceConflict  = errors.New("资源已被修改，请刷新后重试")
	ErrResourceForbidden = errors.New("集群凭据没有此操作的权限")
	ErrResourceProtected = errors.New("不允许删除系统命名空间")
)

type ResourceDefinition struct {
	Resource   string `json:"resource"`
	Kind       string `json:"kind"`
	APIVersion string `json:"api_version"`
	Group      string `json:"group"`
	Namespaced bool   `json:"namespaced"`
	Permission string `json:"permission"`
	Create     bool   `json:"create"`
	Update     bool   `json:"update"`
	Delete     bool   `json:"delete"`
}

var resourceDefinitions = []ResourceDefinition{
	{"nodes", "Node", "v1", "nodes", false, "kubernetes.node", false, true, false},
	{"namespaces", "Namespace", "v1", "config", false, "kubernetes.config", true, true, true},
	{"deployments", "Deployment", "apps/v1", "workloads", true, "kubernetes.workload", true, true, true},
	{"statefulsets", "StatefulSet", "apps/v1", "workloads", true, "kubernetes.workload", true, true, true},
	{"daemonsets", "DaemonSet", "apps/v1", "workloads", true, "kubernetes.workload", true, true, true},
	{"jobs", "Job", "batch/v1", "workloads", true, "kubernetes.workload", true, false, true},
	{"cronjobs", "CronJob", "batch/v1", "workloads", true, "kubernetes.workload", true, true, true},
	{"pods", "Pod", "v1", "workloads", true, "kubernetes.workload", false, false, true},
	{"configmaps", "ConfigMap", "v1", "config", true, "kubernetes.config", true, true, true},
	{"secrets", "Secret", "v1", "config", true, "kubernetes.config", true, true, true},
	{"persistentvolumeclaims", "PersistentVolumeClaim", "v1", "config", true, "kubernetes.storage", true, true, true},
	{"persistentvolumes", "PersistentVolume", "v1", "config", false, "kubernetes.storage", false, false, true},
	{"storageclasses", "StorageClass", "storage.k8s.io/v1", "config", false, "kubernetes.storage", false, false, true},
	{"services", "Service", "v1", "network", true, "kubernetes.network", true, true, true},
	{"ingresses", "Ingress", "networking.k8s.io/v1", "network", true, "kubernetes.network", true, true, true},
	{"networkpolicies", "NetworkPolicy", "networking.k8s.io/v1", "network", true, "kubernetes.network", true, true, true},
}

func ResourceDefinitions() []ResourceDefinition {
	return append([]ResourceDefinition(nil), resourceDefinitions...)
}

func ResourceType(resource string) (ResourceDefinition, bool) {
	for _, def := range resourceDefinitions {
		if def.Resource == resource {
			return def, true
		}
	}
	return ResourceDefinition{}, false
}

func (d ResourceDefinition) gvr() schema.GroupVersionResource {
	gv, _ := schema.ParseGroupVersion(d.APIVersion)
	return gv.WithResource(d.Resource)
}

type ResourceList struct {
	Items    []map[string]any `json:"items"`
	Continue string           `json:"continue"`
}

type ResourceIdentity struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
}

func (i ResourceIdentity) valid() bool {
	return i.UID != "" && len(i.UID) <= 128 && i.ResourceVersion != "" && len(i.ResourceVersion) <= 128
}

// 只代理已登记的资源；项目归属由 RESTConfig 查询集群时检查。
func (s *Service) resourceClient(ctx context.Context, clusterID, resource, namespace string, all bool) (dynamic.ResourceInterface, ResourceDefinition, error) {
	def, ok := ResourceType(resource)
	if !ok {
		return nil, def, ErrInvalidResource
	}
	if def.Namespaced {
		if namespace == "*" && all {
			namespace = ""
		} else if len(validation.IsDNS1123Label(namespace)) != 0 {
			return nil, def, ErrInvalidResource
		}
	} else if namespace != "" && namespace != "*" {
		return nil, def, ErrInvalidResource
	}
	cfg, err := s.RESTConfig(ctx, clusterID)
	if err != nil {
		return nil, def, err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, def, err
	}
	if def.Namespaced {
		return client.Resource(def.gvr()).Namespace(namespace), def, nil
	}
	return client.Resource(def.gvr()), def, nil
}

func (s *Service) ListResources(ctx context.Context, clusterID, resource, namespace, continuation string) (*ResourceList, error) {
	if len(continuation) > 8192 {
		return nil, ErrInvalidResource
	}
	client, _, err := s.resourceClient(ctx, clusterID, resource, namespace, true)
	if err != nil {
		return nil, err
	}
	list, err := client.List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
	if err != nil {
		return nil, resourceError(resource, err)
	}
	result := &ResourceList{Items: make([]map[string]any, 0, len(list.Items)), Continue: list.GetContinue()}
	for _, item := range list.Items {
		result.Items = append(result.Items, resourceView(resource, &item, false))
	}
	return result, nil
}

func (s *Service) GetResource(ctx context.Context, clusterID, resource, namespace, name string, reveal bool) (map[string]any, error) {
	if len(validation.IsDNS1123Subdomain(name)) != 0 {
		return nil, ErrInvalidResource
	}
	client, _, err := s.resourceClient(ctx, clusterID, resource, namespace, false)
	if err != nil {
		return nil, err
	}
	item, err := client.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, resourceError(resource, err)
	}
	return resourceView(resource, item, reveal), nil
}

func resourceView(resource string, item *unstructured.Unstructured, reveal bool) map[string]any {
	copy := item.DeepCopy()
	unstructured.RemoveNestedField(copy.Object, "metadata", "managedFields")
	annotations := copy.GetAnnotations()
	delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
	copy.SetAnnotations(annotations)
	if resource == "secrets" {
		keys := []any{}
		data, _, _ := unstructured.NestedMap(copy.Object, "data")
		for key := range data {
			keys = append(keys, key)
		}
		copy.Object["keys"] = keys
		// 注解也可能带有密钥，不能通过普通详情绕过密钥查看权限。
		copy.SetAnnotations(nil)
		if !reveal {
			delete(copy.Object, "data")
			delete(copy.Object, "stringData")
		}
	}
	return copy.Object
}

func (s *Service) SaveResource(ctx context.Context, clusterID, resource, namespace, name string, object map[string]any, create bool) (map[string]any, error) {
	client, def, err := s.resourceClient(ctx, clusterID, resource, namespace, false)
	if err != nil {
		return nil, err
	}
	if (create && !def.Create) || (!create && !def.Update) {
		return nil, ErrInvalidResource
	}
	if err := validateResourceObject(def, namespace, name, object, create); err != nil {
		return nil, err
	}
	var result *unstructured.Unstructured
	if create {
		result, err = client.Create(ctx, &unstructured.Unstructured{Object: object}, metav1.CreateOptions{FieldManager: "edo", FieldValidation: "Strict"})
	} else {
		body, encodeErr := json.Marshal(object)
		if encodeErr != nil {
			return nil, ErrInvalidResource
		}
		result, err = client.Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{FieldManager: "edo", FieldValidation: "Strict"})
	}
	if err != nil {
		return nil, resourceError(resource, err)
	}
	return resourceView(resource, result, false), nil
}

func validateResourceObject(def ResourceDefinition, namespace, name string, object map[string]any, create bool) error {
	item := &unstructured.Unstructured{Object: object}
	if item.GetName() != name || len(validation.IsDNS1123Subdomain(name)) != 0 || item.GetNamespace() != namespace {
		return ErrInvalidResource
	}
	if item.GetKind() != def.Kind || item.GetAPIVersion() != def.APIVersion {
		return ErrInvalidResource
	}
	if !create && !(ResourceIdentity{UID: string(item.GetUID()), ResourceVersion: item.GetResourceVersion()}).valid() {
		return ErrInvalidResource
	}
	if create && (item.GetUID() != "" || item.GetResourceVersion() != "") {
		return ErrInvalidResource
	}
	allowed := map[string]bool{"apiVersion": true, "kind": true, "metadata": true, "spec": true}
	if def.Resource == "configmaps" {
		allowed["data"], allowed["binaryData"], allowed["immutable"] = true, true, true
	}
	if def.Resource == "secrets" {
		allowed["data"], allowed["stringData"], allowed["type"], allowed["immutable"] = true, true, true, true
	}
	for key := range object {
		if !allowed[key] {
			return ErrInvalidResource
		}
	}
	metadata, ok := object["metadata"].(map[string]any)
	if !ok {
		return ErrInvalidResource
	}
	for key := range metadata {
		switch key {
		case "name", "namespace", "uid", "resourceVersion", "labels", "annotations":
		default:
			return ErrInvalidResource
		}
	}
	// 节点只能改调度、标签和污点，不能从控制台修改节点身份或能力。
	if def.Resource == "nodes" {
		spec, _, err := unstructured.NestedMap(object, "spec")
		if err != nil {
			return ErrInvalidResource
		}
		for key := range spec {
			if key != "unschedulable" && key != "taints" {
				return ErrInvalidResource
			}
		}
	}
	return nil
}

func (s *Service) DeleteResource(ctx context.Context, clusterID, resource, namespace, name string, identity ResourceIdentity) error {
	if !identity.valid() || len(validation.IsDNS1123Subdomain(name)) != 0 {
		return ErrInvalidResource
	}
	client, def, err := s.resourceClient(ctx, clusterID, resource, namespace, false)
	if err != nil {
		return err
	}
	if !def.Delete {
		return ErrInvalidResource
	}
	if resource == "namespaces" && (name == "default" || strings.HasPrefix(name, "kube-")) {
		return ErrResourceProtected
	}
	uid := types.UID(identity.UID)
	policy := metav1.DeletePropagationBackground
	err = client.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &identity.ResourceVersion}, PropagationPolicy: &policy})
	return resourceError(resource, err)
}

type ResourceAction struct {
	ResourceIdentity
	Action   string `json:"action"`
	Replicas int64  `json:"replicas"`
}

func (s *Service) ResourceAction(ctx context.Context, clusterID, resource, namespace, name string, input ResourceAction) (map[string]any, error) {
	if !input.ResourceIdentity.valid() {
		return nil, ErrInvalidResource
	}
	def, ok := ResourceType(resource)
	if !ok {
		return nil, ErrInvalidResource
	}
	spec := map[string]any{}
	switch input.Action {
	case "cordon", "uncordon":
		if resource != "nodes" {
			return nil, ErrInvalidResource
		}
		spec["unschedulable"] = input.Action == "cordon"
	case "scale":
		if (resource != "deployments" && resource != "statefulsets") || input.Replicas < 0 || input.Replicas > 1000 {
			return nil, ErrInvalidResource
		}
		spec["replicas"] = input.Replicas
	case "restart":
		if resource != "deployments" && resource != "statefulsets" && resource != "daemonsets" {
			return nil, ErrInvalidResource
		}
		spec["template"] = map[string]any{"metadata": map[string]any{"annotations": map[string]any{"edo.dev/restarted-at": time.Now().UTC().Format(time.RFC3339Nano)}}}
	case "suspend", "resume":
		if resource != "cronjobs" {
			return nil, ErrInvalidResource
		}
		spec["suspend"] = input.Action == "suspend"
	default:
		return nil, ErrInvalidResource
	}
	object := map[string]any{"apiVersion": def.APIVersion, "kind": def.Kind, "metadata": map[string]any{"name": name, "namespace": namespace, "uid": input.UID, "resourceVersion": input.ResourceVersion}, "spec": spec}
	return s.SaveResource(ctx, clusterID, resource, namespace, name, object, false)
}

func (s *Service) ResourceEvents(ctx context.Context, clusterID, resource, namespace, name, uid string) ([]corev1.Event, error) {
	if uid == "" || len(uid) > 128 {
		return nil, ErrInvalidResource
	}
	obj, err := s.GetResource(ctx, clusterID, resource, namespace, name, false)
	if err != nil {
		return nil, err
	}
	if string((&unstructured.Unstructured{Object: obj}).GetUID()) != uid {
		return nil, ErrResourceConflict
	}
	client, err := s.Clientset(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	events, err := client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + uid, Limit: 200})
	if err != nil {
		return nil, resourceError(resource, err)
	}
	return events.Items, nil
}

func (s *Service) PodLogs(ctx context.Context, clusterID, namespace, pod, container string, tail int64, follow, timestamps bool) (io.ReadCloser, error) {
	if len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(pod)) != 0 || len(validation.IsDNS1123Label(container)) != 0 || tail < 1 || tail > 5000 {
		return nil, ErrInvalidResource
	}
	cfg, err := s.RESTConfig(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	// 日志流由请求取消和长连接授权控制，不使用普通 API 请求的总超时。
	cfg.Timeout = 0
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	stream, err := client.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container, TailLines: &tail, Follow: follow, Timestamps: timestamps}).Stream(ctx)
	if err != nil {
		return nil, resourceError("pods", err)
	}
	return stream, nil
}

// Secret 的 API 错误可能复述提交内容，只保留诊断所需的状态码和原因。
func resourceError(resource string, err error) error {
	if err == nil {
		return nil
	}
	var stable error
	switch {
	case apierrors.IsNotFound(err):
		stable = ErrResourceNotFound
	case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
		stable = ErrResourceConflict
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		stable = ErrResourceForbidden
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		stable = ErrInvalidResource
	default:
		stable = ErrClusterUnreachable
	}
	if resource == "secrets" {
		return fmt.Errorf("%w（集群响应：%s）", stable, apierrors.ReasonForError(err))
	}
	return fmt.Errorf("%w: %w", stable, err)
}
