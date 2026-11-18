import { v5 as uuidv5 } from 'uuid'
import type { WorkflowDeploymentOverride } from '@/types/pipeline'

const oidNamespace = '6ba7b812-9dad-11d1-80b4-00c04fd430c8'

export function workflowDeploymentIDs(workflowID: string, nodeID: string) {
  const identity = `edo.workflow.deployment:${workflowID}:${nodeID}`
  return { planID: uuidv5(identity + ':plan', oidNamespace), targetID: uuidv5(identity + ':target', oidNamespace) }
}

export function deploymentHostNodeID(nodeID: string, hostID: string) {
  return uuidv5(`edo.deployment.host:${nodeID}:${hostID}`, oidNamespace)
}

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

export interface InstancePlan {
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

export function kubernetesInstancePlan(
  workflowID: string,
  nodeID: string,
  plan: InstancePlan,
  override?: WorkflowDeploymentOverride,
): InstancePlan | undefined {
  const snapshot = override?.plan_snapshot
  const baseTarget = snapshot
    ? (snapshot.deployment_target && snapshot.deployment_target_id
        ? { ...snapshot.deployment_target, id: snapshot.deployment_target_id }
        : undefined)
    : plan.deployment_target
  // 与 ResolveWorkflowLibraryPlan 一致，仅绑定快照时仍使用原方案和目标的身份。
  const changed = override != null && (
    [override.deployment_target, override.timeout_seconds, override.artifact_node_id,
      override.deployment_registry_id, override.deployment_input_version, override.deployment_inputs]
      .some(value => value != null) || Object.keys(override.kubernetes ?? {}).length > 0
  )
  if (baseTarget && !changed) return { ...plan, deployment_target: baseTarget }
  const selection = override?.deployment_target ?? baseTarget
  if (!selection) return undefined
  const runtimeID = selection.runtime_id?.trim()
  const namespace = selection.namespace?.trim()
  const workloadName = selection.workload_name?.trim()
  const containerName = selection.container_name?.trim()
  if (!runtimeID || !namespace || !workloadName || !containerName) return undefined
  const { planID, targetID } = workflowDeploymentIDs(workflowID, nodeID)
  const sameTarget = baseTarget && baseTarget.runtime_id === runtimeID && baseTarget.namespace === namespace &&
    baseTarget.workload_name === workloadName && baseTarget.container_name === containerName
  return {
    id: planID,
    kind: 'kubernetes',
    deployment_target: {
      id: sameTarget ? baseTarget.id : targetID,
      platform: 'kubernetes',
      environment_id: baseTarget?.environment_id || '',
      runtime_id: runtimeID,
      namespace,
      workload_name: workloadName,
      container_name: containerName,
    },
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
  const plansByTarget = new Map(plans.map(plan => [JSON.stringify([plan.id, plan.deployment_target?.id]), plan]))
  const seen = new Set<string>()
  return [...records].sort(compareNewestFirst).filter(record => {
    // 优先读取 application_id，旧记录通过流水线运行查找应用。
    const related = record.application_id ? record.application_id === applicationID : relatedRunIDs.has(record.pipeline_run_id || '')
    const planID = record.deployment_plan_id || ''
    if (!related || !record.pipeline_run_id || record.status !== 'succeeded' || !currentPlanIDs.has(planID)) return false
    // 先选最新成功记录，再检查是否已删除、是否仍对应当前目标。
    // 最新实例删除、重命名或位置变化后，不能回退到更早的历史记录。
    const key = JSON.stringify([applicationID, planID, record.target_id])
    if (seen.has(key)) return false
    seen.add(key)
    if (record.runtime_deleted_at) return false
    const plan = plansByTarget.get(JSON.stringify([planID, record.target_id]))
    const target = plan?.deployment_target
    if (!target || record.target_id !== target.id || record.platform !== target.platform ||
      (record.host_id || '') !== (target.host_id || '') || (record.environment_id || '') !== (target.environment_id || '')) return false
    if (record.platform !== 'ssh' && record.runtime_id !== target.runtime_id) return false
    if (plan.kind === 'compose' && plan.service_name && record.compose_service !== plan.service_name) return false
    if (plan.kind === 'kubernetes' && ((target.namespace && record.namespace !== target.namespace) ||
      (target.workload_name && record.workload_name !== target.workload_name) ||
      (target.container_name && record.container_name !== target.container_name))) return false
    if (plan.kind === 'docker' && target.workload_name && record.workload_name !== target.workload_name) return false
    return true
  })
}

export function selectLatestFailedDeploymentAttempts<T extends InstanceRecord>(
  applicationID: string,
  records: T[],
  relatedRunIDs: Set<string>,
  currentPlanIDs: Set<string>,
  plans: InstancePlan[],
): T[] {
  const plansByTarget = new Map(plans.map(plan => [JSON.stringify([plan.id, plan.deployment_target?.id]), plan]))
  const seen = new Set<string>()
  return [...records].sort(compareNewestFirst).filter(record => {
    const related = record.application_id ? record.application_id === applicationID : relatedRunIDs.has(record.pipeline_run_id || '')
    const planID = record.deployment_plan_id || ''
    if (!related || !record.pipeline_run_id || !currentPlanIDs.has(planID)) return false
    const key = JSON.stringify([applicationID, planID, record.target_id])
    if (seen.has(key)) return false
    seen.add(key)
    const target = plansByTarget.get(JSON.stringify([planID, record.target_id]))?.deployment_target
    return record.platform === 'kubernetes' && record.status === 'failed' && !record.runtime_deleted_at && Boolean(target) &&
      record.target_id === target?.id && record.platform === target.platform && record.runtime_id === target.runtime_id &&
      (record.host_id || '') === (target.host_id || '') && (record.environment_id || '') === (target.environment_id || '') &&
      (!target.namespace || record.namespace === target.namespace) && (!target.workload_name || record.workload_name === target.workload_name) &&
      (!target.container_name || record.container_name === target.container_name)
  })
}

export function retainCurrentDeploymentState<T>(state: Record<string, T>, currentIDs: Set<string>): Record<string, T> {
  return Object.fromEntries(Object.entries(state).filter(([id]) => currentIDs.has(id)))
}
