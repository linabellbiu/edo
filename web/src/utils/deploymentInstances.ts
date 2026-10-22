export interface InstanceRecord {
  id: string
  application_id?: string
  pipeline_run_id?: string
  deployment_plan_id?: string
  target_id: string
  platform: string
  environment_id?: string
  host_id?: string
  runtime_id?: string
  namespace?: string
  workload_name?: string
  container_name?: string
  compose_service?: string
  status: string
  runtime_deleted_at?: string
  created_at: string
}

interface InstancePlan {
  id: string
  kind?: string
  service_name?: string
  deployment_target?: {
    id: string
    platform: string
    environment_id?: string
    host_id?: string
    runtime_id?: string
    namespace?: string
    workload_name?: string
    container_name?: string
  }
}

function compareNewestFirst(left: InstanceRecord, right: InstanceRecord) {
  const milliseconds = Date.parse(right.created_at) - Date.parse(left.created_at)
  if (milliseconds) return milliseconds
  // Go 返回纳秒时间；Date.parse 只保留毫秒，不能让同毫秒的不同发布顺序颠倒。
  const fraction = (value: string) => Number((value.match(/\.(\d+)/)?.[1] || '').padEnd(9, '0').slice(3, 9))
  const nanoseconds = fraction(right.created_at) - fraction(left.created_at)
  return nanoseconds || (left.id < right.id ? 1 : left.id > right.id ? -1 : 0)
}

export function selectCurrentDeploymentInstances<T extends InstanceRecord>(
  applicationID: string,
  records: T[],
  relatedRunIDs: Set<string>,
  currentPlanIDs: Set<string>,
  plans: InstancePlan[],
): T[] {
  const plansByID = new Map(plans.map(plan => [plan.id, plan]))
  const seen = new Set<string>()
  return [...records].sort(compareNewestFirst).filter(record => {
    // 新记录直接使用稳定应用外键；升级前的记录才回退到运行关联。
    const related = record.application_id ? record.application_id === applicationID : relatedRunIDs.has(record.pipeline_run_id || '')
    const planID = record.deployment_plan_id || ''
    if (!related || !record.pipeline_run_id || record.status !== 'succeeded' || !currentPlanIDs.has(planID)) return false
    // 与后端当前实例保护一致，先选最新成功记录，再检查删除标记和目标快照。
    // 最新实例删除、重命名或位置变化后，不能回退到更早的历史记录。
    const key = JSON.stringify([applicationID, planID, record.target_id])
    if (seen.has(key)) return false
    seen.add(key)
    if (record.runtime_deleted_at) return false
    const plan = plansByID.get(planID)
    const target = plan?.deployment_target
    if (!target || record.target_id !== target.id || record.platform !== target.platform ||
      (record.host_id || '') !== (target.host_id || '') || (record.environment_id || '') !== (target.environment_id || '')) return false
    if (record.platform !== 'ssh' && record.runtime_id !== target.runtime_id) return false
    if (plan.kind === 'compose' && plan.service_name && record.compose_service !== plan.service_name) return false
    if (plan.kind === 'kubernetes' && ((target.namespace && record.namespace !== target.namespace) ||
      (target.workload_name && record.workload_name !== target.workload_name))) return false
    if (plan.kind === 'docker' && target.workload_name && record.workload_name !== target.workload_name) return false
    return true
  })
}

export function retainCurrentDeploymentState<T>(state: Record<string, T>, currentIDs: Set<string>): Record<string, T> {
  return Object.fromEntries(Object.entries(state).filter(([id]) => currentIDs.has(id)))
}
