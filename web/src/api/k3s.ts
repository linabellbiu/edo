import client from '@/api/client'
import type { InfrastructureCluster } from '@/api/infrastructure'

export type K3sScope = 'server' | 'agent' | 'installer'
export interface K3sOption {
  name: string
  label: string
  group: string
  type: 'string' | 'boolean' | 'integer' | 'string[]'
  default?: unknown
  description: string
  choices?: string[]
  secret?: boolean
  managed?: boolean
  unsupported_reason?: string
}
export interface K3sCatalog {
  version: string
  default_version?: string
  target_version?: string
  catalog_version: string
  sources: string[]
  server: K3sOption[]
  agent: K3sOption[]
  installer: K3sOption[]
}
export interface K3sPlanInput {
  action: 'create' | 'join'
  cluster_id?: string
  name: string
  server_host_id: string
  agent_host_ids: string[]
  profile: 'default' | 'custom'
  server_options: Record<string, unknown>
  agent_options: Record<string, unknown>
  installer: Record<string, unknown>
  overrides: string
  endpoint: string
}
export interface K3sPlanHost {
  id: string
  name: string
  address?: string
  role?: string
  command?: string
  config?: string
}
export interface K3sPlan {
  id: string
  expires_at: string
  action: 'create' | 'join'
  name?: string
  endpoint?: string
  version?: string
  target_version?: string
  hosts?: K3sPlanHost[]
  server_config?: string
  agent_config?: string
  installer_config?: string
  commands?: string[]
  warnings?: string[]
}
export interface K3sInstallation {
  id: string
  status: 'planned' | 'pending' | 'running' | 'succeeded' | 'failed' | 'interrupted'
  phase?: string
  message?: string
  error?: string
  logs?: string[] | string
  created_at?: string
  updated_at?: string
  cluster?: Pick<InfrastructureCluster, 'id' | 'name'> & Partial<InfrastructureCluster>
  cluster_id?: string
  action?: 'create' | 'join'
  name?: string
  hosts?: Array<{id: string; name: string; address?: string; role?: string; status?: string; message?: string}>
}

export async function getK3sCatalog(clusterID = '', signal?: AbortSignal) {
  return (await client.get<K3sCatalog>('/kubernetes/k3s/options', { params:clusterID?{cluster_id:clusterID}:{},signal })).data
}
export async function previewK3sPlan(input: K3sPlanInput, signal?: AbortSignal) {
  return (await client.post<K3sPlan>('/kubernetes/k3s/plans', input, { signal, timeout: 60_000 })).data
}
export async function startK3sInstallation(planID: string, signal?: AbortSignal) {
  return (await client.post<K3sInstallation>('/kubernetes/k3s/installations', { plan_id: planID }, { signal })).data
}
export async function getK3sInstallation(id: string, signal?: AbortSignal) {
  return (await client.get<K3sInstallation>(`/kubernetes/k3s/installations/${encodeURIComponent(id)}`, { signal })).data
}
export async function listK3sInstallations(clusterID: string, action: 'create'|'join', signal?: AbortSignal) {
  return (await client.get<{installations:K3sInstallation[]}>('/kubernetes/k3s/installations', { params:{limit:10,action,...(clusterID?{cluster_id:clusterID}:{})},signal })).data.installations ?? []
}
