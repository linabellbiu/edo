import client from '@/api/client'
import type { InfrastructureCluster } from '@/api/infrastructure'

export type K3sScope = 'server' | 'agent' | 'installer'
export type K3sJoinRole = 'agent' | 'server'
export type K3sInstallMode = 'native' | 'k3d'
export type K3sDownloadSource = 'official' | 'official_cn'
export type K3sAction = 'create' | 'join' | 'uninstall'
export interface K3sInstallSources {
  download_source: K3sDownloadSource
  installer_url: string
  artifact_url: string
  mirror?: string
  system_image_registry?: string
}
export interface K3dConfig { agents: number; api_port: number; bind_address: string }
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
  mode?: K3sInstallMode
  k3d?: K3dConfig
  download_source?: K3sDownloadSource
  system_image_registry?: string
  cluster_id?: string
  name: string
  server_host_id?: string
  agent_host_ids?: string[]
  join_role?: K3sJoinRole
  join_host_ids?: string[]
  server_token?: string
  profile: 'default' | 'custom'
  server_options?: Record<string, unknown>
  agent_options?: Record<string, unknown>
  installer?: Record<string, unknown>
  overrides?: string
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
export interface K3sUninstallSummary {
  source_installation_id: string
  host_id: string
  role: K3sJoinRole
  script_path: string
  service_name: string
  data_dir: string
}
export interface K3sUninstallTarget extends K3sUninstallSummary {
  installation_name: string
  status: K3sInstallation['status']
  created_at?: string
}
export interface K3sPreflightCheck {
  id: string
  name: string
  status: 'passed' | 'warning' | 'failed'
  message: string
}
export interface K3sHostPreflight {
  host_id: string
  host_name: string
  role: 'server' | 'agent' | 'host'
  hostname: string
  passed: boolean
  checks: K3sPreflightCheck[]
}
export interface K3sPreflightReport {
  checked_at: string
  passed: boolean
  checks: K3sPreflightCheck[]
  hosts: K3sHostPreflight[]
}
export interface K3sPlan {
  id: string
  expires_at: string
  action: K3sAction
  mode?: K3sInstallMode
  k3d?: K3dConfig
  sources?: K3sInstallSources
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
  preflight?: K3sPreflightReport
  uninstall?: K3sUninstallSummary
}
export interface K3sInstallation {
  id: string
  mode?: K3sInstallMode
  k3d?: K3dConfig
  sources?: K3sInstallSources
  status: 'planned' | 'pending' | 'running' | 'succeeded' | 'failed' | 'interrupted'
  phase?: string
  message?: string
  error?: string
  logs?: string[] | string
  created_at?: string
  started_at?: string
  finished_at?: string
  updated_at?: string
  cluster?: Pick<InfrastructureCluster, 'id' | 'name'> & Partial<InfrastructureCluster>
  cluster_id?: string
  action?: K3sAction
  name?: string
  uninstall?: K3sUninstallSummary
  hosts?: Array<{id: string; name: string; address?: string; role?: string; status?: string; message?: string; stage?: string; stage_started_at?: string; progress_updated_at?: string}>
}

export async function getK3sCatalog(clusterID = '', signal?: AbortSignal) {
  return (await client.get<K3sCatalog>('/kubernetes/k3s/options', { params:clusterID?{cluster_id:clusterID}:{},signal })).data
}
export async function previewK3sPlan(input: K3sPlanInput, signal?: AbortSignal) {
  return (await client.post<K3sPlan>('/kubernetes/k3s/plans', input, { signal, timeout: 180_000 })).data
}
export async function checkK3sPreflight(input: K3sPlanInput, signal?: AbortSignal) {
  return (await client.post<K3sPreflightReport>('/kubernetes/k3s/preflight', input, { signal, timeout: 180_000 })).data
}
export async function startK3sInstallation(planID: string, signal?: AbortSignal) {
  return (await client.post<K3sInstallation>('/kubernetes/k3s/installations', { plan_id: planID }, { signal })).data
}
export async function getK3sInstallation(id: string, signal?: AbortSignal) {
  return (await client.get<K3sInstallation>(`/kubernetes/k3s/installations/${encodeURIComponent(id)}`, { signal })).data
}
export async function listK3sInstallations(clusterID: string, action: K3sAction, signal?: AbortSignal, hostID = '') {
  const result=await client.get<{installations:K3sInstallation[]}>('/kubernetes/k3s/installations', { params:{limit:10,action,...(clusterID?{cluster_id:clusterID}:{}),...(hostID?{host_id:hostID}:{})},signal })
  return action==='uninstall'?result.data.installations:result.data.installations??[]
}
export async function listK3sUninstallTargets(hostID: string, signal?: AbortSignal) {
  return (await client.get<{targets:K3sUninstallTarget[]}>('/kubernetes/k3s/uninstall-targets',{params:{host_id:hostID},signal})).data.targets
}
export async function previewK3sUninstallPlan(installationID: string, hostID: string, signal?: AbortSignal) {
  return (await client.post<K3sPlan>('/kubernetes/k3s/uninstall/plans',{installation_id:installationID,host_id:hostID},{signal,timeout:180_000})).data
}
export async function startK3sUninstallation(planID: string, confirmation: string, signal?: AbortSignal) {
  return (await client.post<K3sInstallation>('/kubernetes/k3s/uninstallations',{plan_id:planID,confirmation},{signal,timeout:180_000})).data
}
