package kube

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// 只在进入详情时核对 Pod → ReplicaSet → Deployment 的控制器关系。
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
	pod, err := query.dynamic.Resource(podType.gvr()).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
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
	replica, err := query.typed.AppsV1().ReplicaSets(namespace).Get(ctx, replicaOwner.Name, metav1.GetOptions{})
	if err != nil {
		return nil, resourceError("replicasets", err)
	}
	if replica.UID != replicaOwner.UID || replica.Namespace != namespace || replica.Name != replicaOwner.Name {
		return nil, ErrResourceConflict
	}
	deploymentOwner := metav1.GetControllerOf(replica)
	if !appsWorkloadOwner(deploymentOwner, "Deployment") {
		return result, nil
	}
	if deploymentOwner.UID == "" || len(validation.IsDNS1123Subdomain(deploymentOwner.Name)) != 0 {
		return nil, ErrResourceConflict
	}
	deployment, err := query.dynamic.Resource(deploymentType.gvr()).Namespace(namespace).Get(ctx, deploymentOwner.Name, metav1.GetOptions{})
	if err != nil {
		return nil, resourceError("deployments", err)
	}
	if deployment.GetUID() != deploymentOwner.UID || deployment.GetNamespace() != namespace || deployment.GetName() != deploymentOwner.Name {
		return nil, ErrResourceConflict
	}
	result.Deployment = &PodDeployment{Object: resourceView("deployments", deployment, false), ReplicaSetUID: string(replica.UID)}
	return result, nil
}

func appsWorkloadOwner(owner *metav1.OwnerReference, kind string) bool {
	if owner == nil || owner.Kind != kind {
		return false
	}
	version, err := schema.ParseGroupVersion(owner.APIVersion)
	return err == nil && (version.Group == "apps" || version.Group == "extensions")
}
