export const deploymentTargetZh = {
  configurationHint: '按部署方式选择执行主机或独立接入的 Kubernetes 集群。',
  environmentHint: '环境用于对运行位置分组；Kubernetes 集群独立选择。',
  chooseEnvironmentHint: '环境用于归类运行位置；Kubernetes 集群通过已保存的集群连接访问。',
  emptyHint: '检查环境中的主机能力，或先接入 Kubernetes 集群。',
  clusterPlaceholder: '选择已接入的 Kubernetes 集群',
  clusterUnavailable: '所选 Kubernetes 集群已停用或不可用，请重新选择。',
  clusterPermission: '需要主机与集群查看权限才能选择 Kubernetes 集群。',
  targetUnavailable: '运行位置不可用',
}
export const deploymentTargetEn: Record<keyof typeof deploymentTargetZh, string> = {
  configurationHint: 'Choose an execution host or an independently connected Kubernetes cluster for this deployment type.',
  environmentHint: 'Environments group deployment destinations. Kubernetes clusters are selected independently.',
  chooseEnvironmentHint: 'Environments group deployment destinations. Kubernetes uses its saved cluster connection.',
  emptyHint: 'Check the environment host capabilities or connect a Kubernetes cluster.',
  clusterPlaceholder: 'Select a connected Kubernetes cluster',
  clusterUnavailable: 'The selected Kubernetes cluster is disabled or unavailable. Select another cluster.',
  clusterPermission: 'Cluster read permission is required to select a Kubernetes cluster.',
  targetUnavailable: 'Deployment destination unavailable',
}
