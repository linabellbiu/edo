package kube

import (
	"context"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// 诊断接口仅返回当前对象的关联信息，不开放通用资源透传或写入能力。
type ResourceDiagnostics struct {
	UID        string               `json:"uid"`
	State      string               `json:"state"`
	Endpoints  []ServiceEndpoint    `json:"endpoints,omitempty"`
	Revisions  []DeploymentRevision `json:"revisions,omitempty"`
	Containers []ContainerMetric    `json:"containers,omitempty"`
	MetricsAt  string               `json:"metrics_at,omitempty"`
	Errors     map[string]error     `json:"-"`
}
type ServiceEndpoint struct {
	Slice       string                     `json:"slice"`
	AddressType string                     `json:"address_type"`
	Addresses   []string                   `json:"addresses"`
	Ports       []discoveryv1.EndpointPort `json:"ports"`
	Ready       *bool                      `json:"ready"`
	Serving     *bool                      `json:"serving"`
	Terminating *bool                      `json:"terminating"`
	NodeName    *string                    `json:"node_name"`
	Target      *corev1.ObjectReference    `json:"target,omitempty"`
}
type DeploymentRevision struct {
	Name      string        `json:"name"`
	UID       types.UID     `json:"uid"`
	Revision  string        `json:"revision"`
	Current   bool          `json:"current"`
	Created   metav1.Time   `json:"created"`
	Desired   *int32        `json:"desired"`
	Replicas  int32         `json:"replicas"`
	Ready     int32         `json:"ready"`
	Available int32         `json:"available"`
	Images    []string      `json:"images"`
	Pods      []RevisionPod `json:"pods"`
}
type RevisionPod struct {
	Name  string          `json:"name"`
	UID   types.UID       `json:"uid"`
	Phase corev1.PodPhase `json:"phase"`
}
type ContainerMetric struct {
	Name   string   `json:"name"`
	CPU    *float64 `json:"cpu"`
	Memory *float64 `json:"memory"`
}

func (s *Service) ResourceDiagnostics(ctx context.Context, clusterID, resource, namespace, name, uid string) (*ResourceDiagnostics, error) {
	if len(validation.IsDNS1123Subdomain(name)) != 0 || uid == "" || (resource != "services" && resource != "deployments" && resource != "pods") {
		return nil, ErrInvalidResource
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	reader, _, err := s.resourceClient(ctx, clusterID, resource, namespace, false)
	if err != nil {
		return nil, err
	}
	object, err := reader.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, resourceError(resource, err)
	}
	if string(object.GetUID()) != uid {
		return nil, ErrResourceConflict
	}
	cfg, err := s.RESTConfig(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	result := &ResourceDiagnostics{UID: uid, State: "ok", Errors: map[string]error{}}
	switch resource {
	case "services":
		err = serviceEndpoints(ctx, client, object, result)
	case "deployments":
		err = deploymentRevisions(ctx, client, object, result)
	case "pods":
		metrics, e := dynamic.NewForConfig(cfg)
		if e != nil {
			return nil, e
		}
		err = podContainerMetrics(ctx, metrics, object, result)
	}
	if err != nil {
		return nil, resourceError(resource, err)
	}
	// 查询期间同名资源可能被替换，不能把旧对象的关联信息展示在新对象下。
	latest, err := reader.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, resourceError(resource, err)
	}
	if latest.GetUID() != object.GetUID() {
		return nil, ErrResourceConflict
	}
	return result, nil
}

func serviceEndpoints(ctx context.Context, client kubernetes.Interface, object *unstructured.Unstructured, result *ResourceDiagnostics) error {
	continuation := ""
	for page := 0; page < 20; page++ {
		list, err := client.DiscoveryV1().EndpointSlices(object.GetNamespace()).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{discoveryv1.LabelServiceName: object.GetName()}.String(), Limit: 500, Continue: continuation})
		if err != nil {
			return err
		}
		for _, slice := range list.Items {
			// 无 owner 的手动端点合法；已绑定到其他 Service UID 的旧端点不能混入。
			stale := false
			for _, owner := range slice.OwnerReferences {
				if owner.Kind == "Service" && owner.UID != object.GetUID() {
					stale = true
				}
			}
			if stale {
				continue
			}
			for _, endpoint := range slice.Endpoints {
				if len(result.Endpoints) >= 10000 {
					result.State = "partial"
					return nil
				}
				target := endpoint.TargetRef
				if target != nil && target.Namespace != "" && target.Namespace != object.GetNamespace() {
					target = nil
				}
				result.Endpoints = append(result.Endpoints, ServiceEndpoint{Slice: slice.Name, AddressType: string(slice.AddressType), Addresses: endpoint.Addresses, Ports: slice.Ports, Ready: endpoint.Conditions.Ready, Serving: endpoint.Conditions.Serving, Terminating: endpoint.Conditions.Terminating, NodeName: endpoint.NodeName, Target: target})
			}
		}
		continuation = list.Continue
		if continuation == "" {
			return nil
		}
	}
	result.State = "partial"
	return nil
}

func deploymentRevisions(ctx context.Context, client kubernetes.Interface, object *unstructured.Unstructured, result *ResourceDiagnostics) error {
	var deployment appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &deployment); err != nil {
		return err
	}
	if deployment.Spec.Selector == nil {
		return ErrInvalidResource
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return err
	}
	continuation := ""
	for page := 0; page < 20; page++ {
		list, err := client.AppsV1().ReplicaSets(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String(), Limit: 500, Continue: continuation})
		if err != nil {
			return err
		}
		for _, rs := range list.Items {
			owner := metav1.GetControllerOf(&rs)
			if owner == nil || owner.Kind != "Deployment" || owner.UID != deployment.UID {
				continue
			}
			current, wanted := rs.Spec.Template.DeepCopy(), deployment.Spec.Template.DeepCopy()
			delete(current.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
			delete(wanted.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
			revision := DeploymentRevision{Name: rs.Name, UID: rs.UID, Revision: rs.Annotations["deployment.kubernetes.io/revision"], Current: apiequality.Semantic.DeepEqual(current, wanted), Created: rs.CreationTimestamp, Desired: rs.Spec.Replicas, Replicas: rs.Status.Replicas, Ready: rs.Status.ReadyReplicas, Available: rs.Status.AvailableReplicas, Images: []string{}, Pods: []RevisionPod{}}
			for _, c := range rs.Spec.Template.Spec.Containers {
				revision.Images = append(revision.Images, c.Image)
			}
			result.Revisions = append(result.Revisions, revision)
		}
		continuation = list.Continue
		if continuation == "" {
			break
		}
		if page == 19 {
			result.State = "partial"
		}
	}
	sort.SliceStable(result.Revisions, func(i, j int) bool {
		a, _ := strconv.Atoi(result.Revisions[i].Revision)
		b, _ := strconv.Atoi(result.Revisions[j].Revision)
		if a != b {
			return a > b
		}
		return result.Revisions[j].Created.Before(&result.Revisions[i].Created)
	})
	indices := map[types.UID]int{}
	for i, r := range result.Revisions {
		indices[r.UID] = i
	}
	continuation = ""
	for page := 0; page < 20; page++ {
		pods, err := client.CoreV1().Pods(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String(), Limit: 500, Continue: continuation})
		if err != nil {
			result.State = "pods_unavailable"
			result.Errors["pods"] = err
			return nil
		}
		for _, pod := range pods.Items {
			owner := metav1.GetControllerOf(&pod)
			if owner == nil || owner.Kind != "ReplicaSet" {
				continue
			}
			if i, ok := indices[owner.UID]; ok {
				result.Revisions[i].Pods = append(result.Revisions[i].Pods, RevisionPod{Name: pod.Name, UID: pod.UID, Phase: pod.Status.Phase})
			}
		}
		continuation = pods.Continue
		if continuation == "" {
			break
		}
		if page == 19 {
			result.State = "partial"
		}
	}
	return nil
}

func podContainerMetrics(ctx context.Context, client dynamic.Interface, pod *unstructured.Unstructured, result *ResourceDiagnostics) error {
	var metrics *unstructured.Unstructured
	var err error
	for _, version := range []string{"v1", "v1beta1"} {
		metrics, err = client.Resource(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: version, Resource: "pods"}).Namespace(pod.GetNamespace()).Get(ctx, pod.GetName(), metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			break
		}
	}
	if err != nil {
		result.State = "unavailable"
		if apierrors.IsForbidden(err) {
			result.State = "forbidden"
		}
		result.Errors["metrics"] = err
		return nil
	}
	stamp, _, _ := unstructured.NestedString(metrics.Object, "timestamp")
	at, parseErr := time.Parse(time.RFC3339Nano, stamp)
	if parseErr != nil || time.Since(at) > 2*time.Minute || time.Until(at) > time.Minute || at.Before(pod.GetCreationTimestamp().Time) {
		result.State = "stale"
		return nil
	}
	if metrics.GetUID() != "" && metrics.GetUID() != pod.GetUID() {
		result.State = "stale"
		return nil
	}
	result.MetricsAt = stamp
	containers, _, _ := unstructured.NestedSlice(metrics.Object, "containers")
	for _, value := range containers {
		c, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name, _ := c["name"].(string)
		usage, _ := c["usage"].(map[string]any)
		if name == "" || !validUsage(usage) {
			continue
		}
		cpu, mem := quantity(usage["cpu"]), quantity(usage["memory"])
		result.Containers = append(result.Containers, ContainerMetric{Name: name, CPU: &cpu, Memory: &mem})
	}
	if len(result.Containers) == 0 {
		result.State = "unavailable"
	}
	return nil
}
