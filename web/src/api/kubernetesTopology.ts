import client from '@/api/client'
import type { NodeTopologyData } from '@/utils/kubernetesTopology'

export async function getClusterNodeTopology(cluster: string, signal?: AbortSignal): Promise<NodeTopologyData> {
  const { data } = await client.get<NodeTopologyData>(`/kubernetes/clusters/${encodeURIComponent(cluster)}/topology`, { signal, timeout: 35_000 })
  return data
}

const hostReads = new Map<string, { at: number; data?: NodeTopologyData; pending?: Promise<NodeTopologyData> }>()

// 同集群的主机徽标共用一次请求，缓存按用户、项目和权限隔离。
export function getHostNodeTopology(cluster: string, scope: string): Promise<NodeTopologyData> {
  const key = `${scope}:${cluster}`
  const existing = hostReads.get(key)
  if (existing?.pending) return existing.pending
  if (existing?.data && Date.now() - existing.at < 20_000) return Promise.resolve(existing.data)
  if (hostReads.size >= 64) hostReads.delete(hostReads.keys().next().value!)
  const entry: { at: number; data?: NodeTopologyData; pending?: Promise<NodeTopologyData> } = { at: 0 }
  entry.pending = getClusterNodeTopology(cluster).then(data => {
    entry.at = Date.now(); entry.data = data; entry.pending = undefined
    return data
  }, error => {
    if (hostReads.get(key) === entry) hostReads.delete(key)
    throw error
  })
  hostReads.set(key, entry)
  return entry.pending
}
