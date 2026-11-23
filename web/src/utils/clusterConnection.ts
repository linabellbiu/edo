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
  checking?: boolean
  refreshing?: boolean
}

const readPermissions = ['cluster.read', 'kubernetes.node.read', 'kubernetes.workload.read', 'kubernetes.config.read', 'kubernetes.namespace_policy.read', 'kubernetes.service_account.read', 'kubernetes.storage.read', 'kubernetes.network.read', 'kubernetes.rbac.read', 'kubernetes.rbac.update']
const snapshotsByProfile = new WeakMap<object, Map<string, ClusterConnection>>()
const snapshotLimit = 1024
const passivePollInterval = 5_000
const refreshPollInterval = 1_000

export function clusterConnectionExpired(connection?: ClusterConnection) {
  return Boolean(connection?.expires_at && Date.parse(connection.expires_at) <= Date.now())
}

export function clusterConnectionLabel(cluster: ConnectionCluster, connection?: ClusterConnection) {
  if (connection?.reason === 'missing') return '不可用'
  if (!cluster.is_active) return '已停用'
  if (!connection) return '未检测'
  if (connection.reason === 'stalled') return '检测超时'
  const label = { unknown: '未检测', checking: '后台检测中', connected: '已连接', unreachable: '未连接', restricted: '访问受限' }[connection.status]
  return clusterConnectionExpired(connection) ? `${label}（上次）` : label
}

function connectionSnapshots(profile: object) {
  let snapshots = snapshotsByProfile.get(profile)
  if (!snapshots) {
    snapshots = new Map()
    snapshotsByProfile.set(profile, snapshots)
  }
  return snapshots
}

export function useClusterConnections(clusters: () => ConnectionCluster[], enabled: () => boolean = () => true) {
  const auth = useAuthStore()
  const statuses = ref<Record<string, ClusterConnection>>({})
  const error = ref('')
  const reading = ref(false)
  const projectRevision = ref(0)
  const manualIDs = new Set<string>()
  let disposed = false
  let revision = 0
  let controller: AbortController | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  const project = () => typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') || ''
  const identity = () => JSON.stringify([projectRevision.value, auth.user?.id, auth.user?.project_id, auth.user?.department_id, auth.user?.is_superuser, auth.user?.permissions, auth.authorizationReady, project()])
  const snapshotKey = (cluster: ConnectionCluster) => JSON.stringify([identity(), cluster.id, cluster.is_active, cluster.updated_at])
  const visible = () => typeof document === 'undefined' || !document.hidden
  const allowed = () => enabled() && auth.authorizationReady !== false && Boolean(auth.user?.id && auth.user?.project_id === project()) && auth.canAny(readPermissions)
  const scope = () => JSON.stringify([identity(), enabled(), clusters().map(cluster => [cluster.id, cluster.is_active, cluster.updated_at])])
  const pending = computed(() => Object.values(statuses.value).some(status => status.refreshing))

  function cancel() {
    revision++
    controller?.abort()
    clearTimeout(timer)
    reading.value = false
  }

  function schedule(delay: number) {
    clearTimeout(timer)
    if (!disposed && allowed() && visible()) timer = setTimeout(() => { void refresh(false) }, delay)
  }

  function showSnapshots(snapshots: Map<string, ClusterConnection>) {
    statuses.value = Object.fromEntries(clusters().map(cluster => {
      const previous = statuses.value[cluster.id] ?? snapshots.get(snapshotKey(cluster))
      const value: ClusterConnection = !cluster.is_active
        ? { cluster_id: cluster.id, status: 'unknown', reason: 'disabled', message: '集群已停用' }
        : previous ?? { cluster_id: cluster.id, status: 'unknown', reason: 'not_checked', message: '尚无连接检测结果，等待后台检测' }
      return [cluster.id, { ...value, refreshing: manualIDs.has(cluster.id) }]
    }))
  }

  async function refresh(force = true) {
    if (disposed || !auth.user || !allowed() || !visible() || !clusters().length || (!force && reading.value)) return
    if (force && pending.value) return
    cancel()
    const requested = clusters()
    const snapshots = connectionSnapshots(auth.user)
    if (force) {
      error.value = ''
      for (const cluster of requested) if (cluster.is_active) manualIDs.add(cluster.id)
    }
    showSnapshots(snapshots)
    const currentScope = scope(), activeRevision = revision
    controller = new AbortController()
    const signal = controller.signal
    const current = () => !disposed && !signal.aborted && revision === activeRevision && scope() === currentScope && allowed()
    reading.value = true
    try {
      const { data } = await client.get<{ statuses: ClusterConnection[] }>('/kubernetes/clusters/connection-statuses', {
        params: { ids: requested.map(cluster => cluster.id).join(','), ...(force ? { refresh: true } : {}) },
        headers: { 'X-EDO-Project-ID': project() }, signal,
      })
      if (!current()) return
      const received = new Map(data.statuses.map(status => [status.cluster_id, status]))
      const next: Record<string, ClusterConnection> = {}
      for (const cluster of requested) {
        const value = received.get(cluster.id)
        if (!value?.checking) manualIDs.delete(cluster.id)
        if (!value) {
          snapshots.delete(snapshotKey(cluster))
          continue
        }
        next[cluster.id] = { ...value, refreshing: manualIDs.has(cluster.id) }
        if (value.checked_at) snapshots.set(snapshotKey(cluster), value)
        else snapshots.delete(snapshotKey(cluster))
      }
      while (snapshots.size > snapshotLimit) {
        const oldest = snapshots.keys().next()
        if (!oldest.done) snapshots.delete(oldest.value)
      }
      statuses.value = next
      error.value = ''
    } catch (cause) {
      if (!current()) return
      error.value = apiErrorMessage(cause)
      manualIDs.clear()
      const code = (cause as { response?: { status?: number } } | undefined)?.response?.status
      if (code === 401 || code === 403) {
        for (const cluster of requested) snapshots.delete(snapshotKey(cluster))
        statuses.value = {}
      } else {
        showSnapshots(snapshots)
      }
    } finally {
      if (current()) {
        reading.value = false
        schedule(pending.value ? refreshPollInterval : passivePollInterval)
      }
    }
  }

  watch(scope, () => {
    cancel()
    statuses.value = {}
    error.value = ''
    manualIDs.clear()
    void refresh(false)
  }, { immediate: true, flush: 'sync' })
  const projectChanged = () => { projectRevision.value++ }
  const resume = () => {
    if (visible()) void refresh(false)
    else cancel()
  }
  if (typeof window !== 'undefined') {
    window.addEventListener('edo:projects-changed', projectChanged)
    window.addEventListener('focus', resume)
  }
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', resume)
  onScopeDispose(() => {
    disposed = true
    cancel()
    if (typeof window !== 'undefined') {
      window.removeEventListener('edo:projects-changed', projectChanged)
      window.removeEventListener('focus', resume)
    }
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', resume)
  })
  return { statuses, error, pending, refresh }
}
