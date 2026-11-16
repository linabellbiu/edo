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

export function clusterConnectionLabel(cluster: ConnectionCluster, connection?: ClusterConnection) {
  if (connection?.reason === 'missing') return '不可用'
  if (!cluster.is_active) return '已停用'
  if (!connection) return '未检测'
  return { unknown: '未检测', checking: '检测中', connected: '已连接', unreachable: '未连接', restricted: '访问受限' }[connection.status]
}

export function useClusterConnections(clusters: () => ConnectionCluster[], enabled: () => boolean = () => true) {
  const auth = useAuthStore()
  const statuses = ref<Record<string, ClusterConnection>>({})
  const error = ref(''), pending = ref(false), projectRevision = ref(0)
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
  async function refresh(force = true) {
    cancel()
    if (disposed || !allowed() || !clusters().length) return
    const currentScope = scope(), currentRevision = revision
    controller = new AbortController()
    const signal = controller.signal
    const current = () => !disposed && !signal.aborted && revision === currentRevision && scope() === currentScope && allowed()
    pending.value = true
    error.value = ''
    // 过期的成功状态不能继续显示为当前已连接，刷新期间保留其余列表与选择。
    statuses.value = Object.fromEntries(clusters().map(cluster => {
      const previous = statuses.value[cluster.id]
      const expired = previous?.expires_at && Date.parse(previous.expires_at) <= Date.now()
      return [cluster.id, !cluster.is_active
        ? { cluster_id: cluster.id, status: 'unknown', reason: 'disabled', message: '集群已停用' }
        : force || !previous || expired
          ? { cluster_id: cluster.id, status: 'checking', reason: '', message: '正在检测集群连接' }
          : previous]
    }))
    try {
      const { data } = await client.get<{ statuses: ClusterConnection[] }>('/kubernetes/clusters/connection-statuses', {
        params: { ids: clusters().map(cluster => cluster.id).join(','), ...(force ? { refresh: true } : {}) },
        headers: { 'X-EDO-Project-ID': project() }, signal,
      })
      if (!current()) return
      const ids = new Set(clusters().map(cluster => cluster.id))
      statuses.value = Object.fromEntries((data.statuses || []).filter(status => ids.has(status.cluster_id)).map(status => [status.cluster_id, status]))
      const active = Object.values(statuses.value).filter(status => status.reason !== 'disabled')
      const nextExpiry = Math.min(...active.map(status => Date.parse(status.expires_at || '')).filter(Number.isFinite))
      schedule(active.some(status => status.status === 'checking') ? 2_000 : Number.isFinite(nextExpiry) ? Math.max(1_000, nextExpiry - Date.now()) : 30_000)
    } catch (cause) {
      if (!current()) return
      error.value = apiErrorMessage(cause)
      statuses.value = Object.fromEntries(clusters().map(cluster => [cluster.id, { cluster_id: cluster.id, status: 'unknown', reason: 'status_unavailable', message: '连接状态读取失败，请重试' }]))
      schedule(30_000)
    } finally {
      if (current()) pending.value = false
    }
  }
  watch(scope, () => { cancel(); statuses.value = {}; error.value = ''; void refresh(false) }, { immediate: true, flush: 'sync' })
  const projectChanged = () => { projectRevision.value++ }
  if (typeof window !== 'undefined') window.addEventListener('edo:projects-changed', projectChanged)
  onScopeDispose(() => { disposed = true; cancel(); if (typeof window !== 'undefined') window.removeEventListener('edo:projects-changed', projectChanged) })
  return { statuses, error, pending, refresh, cancel, allowed: computed(allowed) }
}
