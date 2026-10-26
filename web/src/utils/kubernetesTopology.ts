import { formatKubeCPU, formatKubeMemory, type KubeObject } from '@/api/kubernetes'
import { kubernetesNodeRoles, type NodeRole } from '@/utils/kubernetesRoles'

export interface TopologyHostBrief { id: string; name: string; address: string; is_active: boolean }
export interface TopologyHost extends TopologyHostBrief {
  node_uids: string[]
  roles: NodeRole[]
  state: 'matched' | 'unmatched' | 'unavailable'
}
export interface TopologyResource { capacity: number | null; allocatable: number | null; used: number | null }
export interface NodeTopologyEntry {
  node: KubeObject
  roles: NodeRole[]
  cpu: TopologyResource
  memory: TopologyResource
  metrics_timestamp?: string | null
  pods: { count: number | null; capacity: number | null; state: 'ok' | 'unavailable' | 'no_permission' }
  hosts: TopologyHostBrief[] | null
}
export interface NodeTopologyData {
  cluster_id: string
  nodes: NodeTopologyEntry[]
  hosts: TopologyHost[] | null
  hosts_state?: 'ok' | 'partial' | 'unavailable' | 'no_permission'
  metrics_state: 'ok' | 'partial' | 'unavailable'
  collected_at: string
}
export interface TopologyMeter {
  key: 'cpu' | 'memory' | 'pods'
  used: number | undefined
  total: number | undefined
  capacity: number | undefined
  percent: number | undefined
  width: number
  state: 'ok' | 'unavailable' | 'no_permission' | 'missing_capacity'
}

const validAmount = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value) && value >= 0

export function topologyRoles(entry: NodeTopologyEntry): NodeRole[] {
  const roles = Array.isArray(entry.roles) ? entry.roles.filter((role): role is NodeRole => role === 'control-plane' || role === 'worker') : []
  return roles.length ? [...new Set(roles)] : kubernetesNodeRoles(entry.node)
}

export function topologyNodeKey(entry: NodeTopologyEntry) {
  return String(entry.node.metadata?.uid || entry.node.metadata?.name || '')
}

export function topologyReady(entry: NodeTopologyEntry): 'ready' | 'not_ready' | 'unknown' {
  const condition = (entry.node.status?.conditions ?? []).find((item: KubeObject) => item.type === 'Ready')
  return condition?.status === 'True' ? 'ready' : condition?.status === 'False' ? 'not_ready' : 'unknown'
}

export function topologyAddresses(entry: NodeTopologyEntry): string[] {
  const addresses = entry.node.status?.addresses ?? []
  const ordered = ['InternalIP', 'ExternalIP'].flatMap(type => addresses.filter((item: KubeObject) => item.type === type).map((item: KubeObject) => String(item.address || '')))
  return [...new Set(ordered.filter(Boolean))]
}

export function topologySearchText(entry: NodeTopologyEntry): string {
  const roles = topologyRoles(entry)
  return [
    entry.node.metadata?.name,
    ...topologyAddresses(entry),
    ...(entry.hosts ?? []).flatMap(host => [host.name, host.address]),
    entry.node.status?.nodeInfo?.kubeletVersion,
    ...(roles.includes('control-plane') ? ['control-plane', 'master', '控制平面'] : []),
    ...(roles.includes('worker') ? ['worker', 'slave', '工作节点'] : []),
  ].filter(Boolean).join(' ').toLocaleLowerCase()
}

export function topologyFresh(collectedAt: string, now = Date.now()) {
  const timestamp = Date.parse(collectedAt)
  return Number.isFinite(timestamp) && now - timestamp <= 120_000 && timestamp <= now + 60_000
}

export function topologyMeters(entry: NodeTopologyEntry, collectedAt: string, failed = false, now = Date.now()): TopologyMeter[] {
  const fresh = !failed && topologyFresh(collectedAt, now)
  const sampledAt = entry.metrics_timestamp === undefined ? collectedAt : entry.metrics_timestamp
  const usageFresh = fresh && typeof sampledAt === 'string' && topologyFresh(sampledAt, now)
  const row = (key: TopologyMeter['key'], rawUsed: unknown, rawTotal: unknown, rawCapacity: unknown, state: TopologyMeter['state']): TopologyMeter => {
    const used = (key === 'pods' ? fresh : usageFresh) && state === 'ok' && validAmount(rawUsed) ? rawUsed : undefined
    const total = validAmount(rawTotal) ? rawTotal : undefined
    const capacity = validAmount(rawCapacity) ? rawCapacity : undefined
    const percent = used !== undefined && total !== undefined && total > 0 ? used / total * 100 : undefined
    return { key, used, total, capacity, percent, width: percent === undefined ? 0 : Math.min(100, percent), state: used === undefined ? state === 'no_permission' ? state : 'unavailable' : percent === undefined ? 'missing_capacity' : 'ok' }
  }
  return [
    row('cpu', entry.cpu?.used, entry.cpu?.allocatable, entry.cpu?.capacity, 'ok'),
    row('memory', entry.memory?.used, entry.memory?.allocatable, entry.memory?.capacity, 'ok'),
    row('pods', entry.pods?.count, entry.pods?.capacity, entry.pods?.capacity, entry.pods?.state === 'no_permission' ? 'no_permission' : entry.pods?.state === 'ok' ? 'ok' : 'unavailable'),
  ]
}

export function topologyAmount(value: number | undefined, key: TopologyMeter['key']) {
  if (!validAmount(value)) return '—'
  return key === 'cpu' ? formatKubeCPU(value) : key === 'memory' ? formatKubeMemory(value) : String(value)
}

export function topologyPercent(meter: TopologyMeter) {
  return meter.percent === undefined ? '—' : `${meter.percent.toFixed(1)}%`
}

export function topologyTone(meter: TopologyMeter): 'unknown' | 'normal' | 'warning' | 'danger' {
  return meter.percent === undefined ? 'unknown' : meter.percent >= 90 ? 'danger' : meter.percent >= 75 ? 'warning' : 'normal'
}
