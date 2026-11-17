import { computed, onScopeDispose, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

export interface ConnectionCluster {
  id: string
  name: string
  is_active: boolean
  updated_at?: string
}

export interface ClusterConnection {
  cluster_id: string
  status: 'unknown' | 'checking' | 'connected' | 'unreachable' | 'restricted'
  reason: string
  message: string
  checked_at?: string
  expires_at?: string
}

const readPermissions = ['cluster.read', 'kubernetes.node.read', 'kubernetes.workload.read', 'kubernetes.config.read', 'kubernetes.storage.read', 'kubernetes.network.read', 'kubernetes.rbac.read', 'kubernetes.rbac.update']

// 后台检查最长容忍窗口：一次检查包含 5 秒超时加调度余量；超过后不再显示“检测中”，
// 改为明确的检测超时结论并降低轮询频率，避免页面一直转圈或持续重试。
export const connectionCheckingLimit = 8_000
const checkingPollDelay = 2_000
const stalledPollDelay = 10_000

export const stalledConnectionMessage = '连接检测超时，请重试'

export function clusterConnectionLabel(cluster: ConnectionCluster, connection?: ClusterConnection) {
  if (connection?.reason === 'missing') return '不可用'
  if (!cluster.is_active) return '已停用'
  if (!connection) return '未检测'
  if (connection.reason === 'stalled') return '检测超时'
  return { unknown: '未检测', checking: '检测中', connected: '已连接', unreachable: '未连接', restricted: '访问受限' }[connection.status]
}

function stalledStatus(clusterID: string): ClusterConnection {
  return { cluster_id: clusterID, status: 'unknown', reason: 'stalled', message: stalledConnectionMessage }
}

export function useClusterConnections(clusters: () => ConnectionCluster[], enabled: () => boolean = () => true) {
  const auth = useAuthStore()
  const statuses = ref<Record<string, ClusterConnection>>({})
  const error = ref(''), pending = ref(false), projectRevision = ref(0)
  const stallSince = new Map<string, number>(), stalled = new Set<string>()
  let disposed = false, revision = 0, controller: AbortController | undefined, timer: ReturnType<typeof setTimeout> | undefined
  const project = () => typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') || ''
  const identity = () => JSON.stringify([projectRevision.value, auth.user?.id, auth.user?.project_id, auth.user?.is_superuser, auth.user?.permissions, auth.authorizationReady, project()])
  const allowed = () => enabled() && auth.authorizationReady !== false && Boolean(auth.user?.id && auth.user?.project_id === project()) && auth.canAny(readPermissions)
  const scope = () => JSON.stringify([identity(), enabled(), clusters().map(cluster => [cluster.id, cluster.is_active, cluster.updated_at])])
  function cancel() {
    revision++
    controller?.abort()
    clearTimeout(timer)
    pending.value = false
  }
  function schedule(delay: number) {
    clearTimeout(timer)
    if (!disposed && allowed()) timer = setTimeout(() => { void refresh(false) }, delay)
  }
  // 检测中的结论有上限：超过窗口即按真实结果展示为检测超时，需要显式刷新才会重新开始计时。
  function guard(entries: Record<string, ClusterConnection>, now: number) {
    const ids = new Set(Object.keys(entries))
    for (const id of [...stallSince.keys()]) if (!ids.has(id)) stallSince.delete(id)
    for (const id of [...stalled]) if (!ids.has(id)) stalled.delete(id)
    for (const [id, status] of Object.entries(entries)) {
      if (status.status !== 'checking') {
        stallSince.delete(id)
        stalled.delete(id)
        continue
      }
      if (stalled.has(id)) {
        entries[id] = stalledStatus(id)
        continue
      }
      const since = stallSince.get(id)
      if (since === undefined) {
        stallSince.set(id, now)
        continue
      }
      if (now - since < connectionCheckingLimit) continue
      stalled.add(id)
      stallSince.delete(id)
      entries[id] = stalledStatus(id)
    }
    return entries
  }
  function scheduleNext() {
    const values = Object.values(statuses.value)
    const active = values.filter(status => status.reason !== 'disabled')
    const nextExpiry = Math.min(...active.map(status => Date.parse(status.expires_at || '')).filter(Number.isFinite))
    schedule(active.some(status => status.status === 'checking') ? checkingPollDelay : active.some(status => status.reason === 'stalled') ? stalledPollDelay : Number.isFinite(nextExpiry) ? Math.max(1_000, nextExpiry - Date.now()) : 30_000)
  }
  async function refresh(force = true) {
    cancel()
    if (disposed || !allowed() || !clusters().length) return
    // 手动刷新是用户明确要求重新检测：清掉上限结论，重新显示检测中并立即重查。
    if (force) {
      stallSince.clear()
      stalled.clear()
    }
    const currentScope = scope(), currentRevision = revision, startedAt = Date.now()
    controller = new AbortController()
    const signal = controller.signal
    const current = () => !disposed && !signal.aborted && revision === currentRevision && scope() === currentScope && allowed()
    pending.value = true
    error.value = ''
    // 过期的成功状态不能继续显示为当前已连接，刷新期间保留其余列表与选择。
    const seeded: Record<string, ClusterConnection> = {}
    for (const cluster of clusters()) {
      const previous = statuses.value[cluster.id]
      const expired = previous?.expires_at && Date.parse(previous.expires_at) <= Date.now()
      seeded[cluster.id] = !cluster.is_active
        ? { cluster_id: cluster.id, status: 'unknown', reason: 'disabled', message: '集群已停用' }
        : force || !previous || expired
          ? { cluster_id: cluster.id, status: 'checking', reason: '', message: '正在检测集群连接' }
          : previous
    }
    statuses.value = guard(seeded, startedAt)
    try {
      const { data } = await client.get<{ statuses: ClusterConnection[] }>('/kubernetes/clusters/connection-statuses', {
        params: { ids: clusters().map(cluster => cluster.id).join(','), ...(force ? { refresh: true } : {}) },
        headers: { 'X-EDO-Project-ID': project() }, signal,
      })
      if (!current()) return
      const ids = new Set(clusters().map(cluster => cluster.id))
      const received: Record<string, ClusterConnection> = {}
      for (const status of data.statuses || []) if (ids.has(status.cluster_id)) received[status.cluster_id] = status
      statuses.value = guard(received, Date.now())
      scheduleNext()
    } catch (cause) {
      if (!current()) return
      error.value = apiErrorMessage(cause)
      statuses.value = Object.fromEntries(clusters().map(cluster => [cluster.id, { cluster_id: cluster.id, status: 'unknown', reason: 'status_unavailable', message: '连接状态读取失败，请重试' }]))
      schedule(30_000)
    } finally {
      if (current()) pending.value = false
    }
  }
  watch(scope, () => { cancel(); statuses.value = {}; error.value = ''; stallSince.clear(); stalled.clear(); void refresh(false) }, { immediate: true, flush: 'sync' })
  const projectChanged = () => { projectRevision.value++ }
  if (typeof window !== 'undefined') window.addEventListener('edo:projects-changed', projectChanged)
  onScopeDispose(() => { disposed = true; cancel(); if (typeof window !== 'undefined') window.removeEventListener('edo:projects-changed', projectChanged) })
  return { statuses, error, pending, refresh, cancel, allowed: computed(allowed) }
}
