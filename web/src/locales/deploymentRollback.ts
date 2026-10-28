export const deploymentRollbackZh = {
  title: '回退上一镜像', confirm: '确认回退', close: '关闭', cancel: '取消', retry: '重新检查',
  impact: '将更新以下 Deployment 中指定容器的镜像，并等待副本就绪。操作保留发布与回退记录。',
  dockerImpact: '将当前 Docker 容器替换为上一不可变镜像，并等待健康检查。操作保留发布与回退记录。',
  runtime: '部署目标', dockerContainer: 'Docker 容器', imageID: '不可变镜像 ID',
  cluster: '集群', namespace: '命名空间', workload: '工作负载', container: '容器',
  current: '来源发布镜像', target: '回退目标镜像', source: '来源部署记录', task: '回退记录',
  unavailable: '当前无法回退', permission: '没有执行部署的权限',
  stale: '目标已变化，已重新检查。请核对更新后的镜像和目标，再确认回退。',
  queued: '回退已提交，等待执行', running: '正在回退并等待实例就绪', awaiting_approval: '回退正在等待审核',
  succeeded: '已回退，就绪检查通过', failed: '回退失败，请检查错误及工作负载状态',
  recentFailure: '最近失败部署', failureFallback: '部署失败，请检查部署记录', viewRecords: '查看部署记录', recordsAndRollback: '部署记录与回退',
  recordsReadFailed: '读取关联部署记录失败', retryRecords: '重新读取', recordsLoading: '正在检查关联部署记录',
}

export const deploymentRollbackEn = {
  title: 'Restore previous image', confirm: 'Confirm rollback', close: 'Close', cancel: 'Cancel', retry: 'Check again',
  impact: 'Updates the image of the specified container in this Deployment and waits for replicas to become ready. Deployment and rollback records are retained.',
  dockerImpact: 'Replaces this Docker container with the previous immutable image and waits for its health check. Deployment and rollback records are retained.',
  runtime: 'Deployment target', dockerContainer: 'Docker container', imageID: 'Immutable image ID',
  cluster: 'Cluster', namespace: 'Namespace', workload: 'Workload', container: 'Container',
  current: 'Source deployment image', target: 'Rollback image', source: 'Source deployment', task: 'Rollback record',
  unavailable: 'Rollback is unavailable', permission: 'Deployment execution permission is required',
  stale: 'The target changed and has been checked again. Review the updated images and target before confirming.',
  queued: 'Rollback queued', running: 'Rolling back and waiting for readiness', awaiting_approval: 'Rollback awaiting approval',
  succeeded: 'Rollback completed and readiness verified', failed: 'Rollback failed. Check the error and workload state.',
  recentFailure: 'Latest failed deployment', failureFallback: 'Deployment failed. Check the deployment record.', viewRecords: 'View deployment records', recordsAndRollback: 'Deployment records and rollback',
  recordsReadFailed: 'Could not read associated deployments', retryRecords: 'Try again', recordsLoading: 'Checking associated deployments',
}
