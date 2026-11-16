package kube

import (
	"context"
	"time"

	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes"

	"edo/internal/model"
)

// ClientsetForInput 验证临时连接配置，不提前创建可见集群记录。
func (s *Service) ClientsetForInput(ctx context.Context, input Input) (*kubernetes.Clientset, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := restConfigFromInput(input)
	if err != nil {
		return nil, err
	}
	s.configureRESTConfig(config)
	return kubernetes.NewForConfig(config)
}

// PrepareUnlinkedCreate 只准备已验收安装的加密集群记录，由安装方与任务结果同事务保存。
// 此入口不接受主机关联，避免绕过主机身份和归属校验。
func (s *Service) PrepareUnlinkedCreate(ctx context.Context, actorID string, input Input) (*model.KubernetesCluster, error) {
	if input.HostIDs != nil {
		return nil, ErrInvalidCluster
	}
	id := uuid.NewString()
	name, namespace, apiServer, encrypted, err := s.normalize(id, nil, input)
	if err != nil {
		return nil, err
	}
	if _, err := s.Test(ctx, input); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	cluster := &model.KubernetesCluster{
		ID: id, Name: name, Mode: input.Mode, APIServer: apiServer,
		DefaultNamespace: namespace, KubeconfigCiphertext: encrypted,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	cluster.DepartmentID, cluster.ProjectID = clusterScope(ctx)
	return cluster, nil
}
