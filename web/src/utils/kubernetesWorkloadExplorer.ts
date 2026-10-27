import type { KubeObject } from '@/api/kubernetes'

export interface WorkloadExplorerSelection {
  revisionUID: string
  podUID: string
  containerName: string
  podTab: string
}

export interface WorkloadRevision {
  uid: string
  name: string
  current?: boolean
  revision?: string | number
  created?: string
  desired?: number
  replicas?: number
  ready?: number
  available?: number
  images?: string[]
  pods?: Array<{ uid: string; name: string; phase?: string }>
}

// 条件为 True 只代表条件成立，中断目标或失败条件成立时不能显示为健康。
export function workloadConditionTone(condition: KubeObject): 'success' | 'warning' | 'error' | 'default' {
  if (condition.status !== 'True' && condition.status !== 'False') return 'default'
  if (['Failed', 'ReplicaFailure', 'DisruptionTarget'].includes(condition.type)) return condition.status === 'True' ? 'warning' : 'default'
  if (['Ready', 'Available', 'Initialized', 'ContainersReady', 'PodScheduled', 'PodReadyToStartContainers', 'Complete'].includes(condition.type)) return condition.status === 'True' ? 'success' : 'warning'
  return 'default'
}

// 只用控制器 UID 关联副本组，标签相同或同名重建不能代替资源身份。
export function revisionOwnedPods(pods: KubeObject[], revisionUID: string, namespace: string): KubeObject[] {
  if (!revisionUID || !namespace) return []
  return pods.filter(pod => pod.kind === 'Pod' && pod.metadata?.uid && pod.metadata.namespace === namespace
    && (pod.metadata.ownerReferences ?? []).some((owner: KubeObject) => owner.kind === 'ReplicaSet' && owner.uid === revisionUID && owner.controller === true))
}

export function selectedWorkloadPod(pods: KubeObject[], uid: string, namespace: string): KubeObject | undefined {
  if (!uid || !namespace) return undefined
  return pods.find(pod => pod.kind === 'Pod' && pod.metadata?.uid === uid && pod.metadata.namespace === namespace)
}

export function selectedWorkloadRevision(revisions: WorkloadRevision[], uid: string): WorkloadRevision | undefined {
  return uid ? revisions.find(revision => revision.uid === uid) : undefined
}

export function initialWorkloadRevisionUID(revisions: WorkloadRevision[]): string {
  return revisions.find(revision => revision.current === true && typeof revision.uid === 'string' && revision.uid)?.uid ?? ''
}

export function sortWorkloadRevisions(revisions: WorkloadRevision[]): WorkloadRevision[] {
  const timestamp = (value?: string) => { const at = Date.parse(value ?? ''); return Number.isFinite(at) ? at : 0 }
  return [...revisions].sort((left, right) => timestamp(right.created) - timestamp(left.created)
    || String(right.revision ?? '').localeCompare(String(left.revision ?? ''), 'zh-CN', { numeric: true })
    || left.uid.localeCompare(right.uid))
}

export function podExplorerEvents(events: KubeObject[], uid: string): KubeObject[] {
  if (!uid) return []
  return events.filter(event => (event.involvedObject?.uid ?? event.regarding?.uid) === uid)
}
