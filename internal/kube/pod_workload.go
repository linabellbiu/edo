package kube

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

type PodWorkloadDetail struct {
	Object     map[string]any `json:"object"`
	Deployment *PodDeployment `json:"deployment"`
}

type PodDeployment struct {
	Object        map[string]any `json:"object"`
	ReplicaSetUID string         `json:"replica_set_uid"`
}

// 使用同一连接下的 Pod、ReplicaSet 和 Deployment 精确核对控制器关系。
func (s *Service) GetPodWorkloadDetail(ctx context.Context, clusterID, namespace, name string) (*PodWorkloadDetail, error) {
	if len(validation.IsDNS1123Subdomain(name)) != 0 {
		return nil, ErrInvalidResource
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	ctx, query, err := s.authorizeResourceOperation(ctx, clusterID, "pods", namespace, "read", false)
	if err != nil {
		return nil, err
	}
	podType, _ := ResourceType("pods")
	read := func(def ResourceDefinition, name string) (*unstructured.Unstructured, error) {
		if s.inventoryRead(ctx) {
			return s.inventoryGet(ctx, query, def, namespace, name)
		}
		return query.dynamic.Resource(def.gvr()).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	pod, err := read(podType, name)
	if err != nil {
		return nil, resourceError("pods", err)
	}
	if pod.GetUID() == "" || pod.GetName() != name || pod.GetNamespace() != namespace {
		return nil, ErrResourceConflict
	}
	result := &PodWorkloadDetail{Object: resourceView("pods", pod, false)}
	replicaOwner := metav1.GetControllerOf(pod)
	if !appsWorkloadOwner(replicaOwner, "ReplicaSet") {
		return result, nil
	}
	deploymentType, _ := ResourceType("deployments")
	if !query.authorization.permitsResourceOperation(deploymentType, namespace, "read", false) {
		return nil, ErrRBACForbidden
	}
	if replicaOwner.UID == "" || len(validation.IsDNS1123Subdomain(replicaOwner.Name)) != 0 {
		return nil, ErrResourceConflict
	}
	replica, err := read(inventoryReplicaSets, replicaOwner.Name)
	if err != nil {
		return nil, resourceError("replicasets", err)
	}
	if replica.GetUID() != replicaOwner.UID || replica.GetNamespace() != namespace || replica.GetName() != replicaOwner.Name {
		return nil, ErrResourceConflict
	}
	deploymentOwner := metav1.GetControllerOf(replica)
	if !appsWorkloadOwner(deploymentOwner, "Deployment") {
		return result, nil
	}
	if deploymentOwner.UID == "" || len(validation.IsDNS1123Subdomain(deploymentOwner.Name)) != 0 {
		return nil, ErrResourceConflict
	}
	deployment, err := read(deploymentType, deploymentOwner.Name)
	if err != nil {
		return nil, resourceError("deployments", err)
	}
	if deployment.GetUID() != deploymentOwner.UID || deployment.GetNamespace() != namespace || deployment.GetName() != deploymentOwner.Name {
		return nil, ErrResourceConflict
	}
	result.Deployment = &PodDeployment{Object: resourceView("deployments", deployment, false), ReplicaSetUID: string(replica.GetUID())}
	return result, nil
}

func appsWorkloadOwner(owner *metav1.OwnerReference, kind string) bool {
	if owner == nil || owner.Kind != kind {
		return false
	}
	version, err := schema.ParseGroupVersion(owner.APIVersion)
	return err == nil && (version.Group == "apps" || version.Group == "extensions")
}
