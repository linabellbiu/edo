import client from '@/api/client'

export type HostMode = 'local' | 'ssh'
export type HostAuthType = 'password' | 'private_key' | 'legacy' | ''
export type HostCapabilityKind = 'ssh' | 'docker' | 'kubernetes' | 'local_exec'
export type HostCapabilityStatus = 'ready' | 'unchecked' | 'unreachable'
export type HostArchitecture = 'amd64' | 'arm64' | ''

export interface HostCapability {
  kind: HostCapabilityKind
  runtime_id: string
  status: HostCapabilityStatus
  version?: string
  use_sudo?: boolean
}

export interface HostCapabilityOption {
  kind: HostCapabilityKind
  available: boolean
  reason?: string
  version?: string
}

export interface InfrastructureHost {
  id: string
  name: string
  mode: HostMode
  address: string
  ssh_port: number
  ssh_username: string
  ssh_auth_type: HostAuthType
  ssh_host_key_fingerprint: string
  architecture: HostArchitecture
  environment_ids: string[]
  /** 兼容迁移期间的旧接口；新逻辑以 environment_ids 为准。 */
  environment_id?: string
  is_builtin: boolean
  is_active: boolean
  capabilities: HostCapability[]
  capability_options?: HostCapabilityOption[]
  credential_configured?: boolean
  use_sudo?: boolean
  created_at?: string
  updated_at?: string
}

export interface InfrastructureHostStatus {
  id: string
  is_active: boolean
  capabilities: HostCapability[]
}

export interface ClusterHost {
  id: string
  name: string
  address: string
  is_active: boolean
}

export interface InfrastructureCluster {
  id: string
  name: string
  api_server: string
  default_namespace: string
  is_active: boolean
  hosts?: ClusterHost[] | null
  created_at?: string
  updated_at?: string
}

export async function setClusterHosts(clusterID: string, hostIDs: string[]) {
  const response = await client.put<{ cluster: InfrastructureCluster }>(
    `/kubernetes/clusters/${encodeURIComponent(clusterID)}/hosts`,
    { host_ids: [...new Set(hostIDs)] },
    { timeout: 35_000 },
  )
  return response.data.cluster
}

export interface InfrastructureEnvironment {
  id: string
  name: string
  description: string
  is_active: boolean
  hosts: InfrastructureHost[]
  created_at?: string
  updated_at?: string
}

export async function listHosts(signal?: AbortSignal): Promise<InfrastructureHost[]> {
  const response = await client.get<{ hosts: InfrastructureHost[] }>('/hosts', { signal })
  return response.data.hosts ?? []
}

export async function listHostStatuses(signal?: AbortSignal): Promise<InfrastructureHostStatus[]> {
  const response = await client.get<{ hosts: InfrastructureHostStatus[] }>('/hosts/statuses', { signal })
  return response.data.hosts ?? []
}

export function mergeHostStatuses(
  hosts: InfrastructureHost[],
  statuses: InfrastructureHostStatus[],
): InfrastructureHost[] | undefined {
  if (hosts.length !== statuses.length) return undefined
  const statusByID = new Map(statuses.map(status => [status.id, status]))
  if (hosts.some(host => !statusByID.has(host.id))) return undefined
  return hosts.map(host => {
    const status = statusByID.get(host.id)!
    return { ...host, is_active: status.is_active, capabilities: status.capabilities }
  })
}

export async function listEnvironments(signal?: AbortSignal): Promise<InfrastructureEnvironment[]> {
  const response = await client.get<{ environments: InfrastructureEnvironment[] }>('/environments', { signal })
  return response.data.environments ?? []
}

export function capabilityOf(host: InfrastructureHost, kind: HostCapabilityKind) {
  return host.capabilities.find(capability => capability.kind === kind)
}

export function environmentIDsOf(host: InfrastructureHost) {
  if (Array.isArray(host.environment_ids)) return [...new Set(host.environment_ids.filter(Boolean))]
  return host.environment_id ? [host.environment_id] : []
}

export function hostRuntimeKinds(host: InfrastructureHost) {
  return host.capabilities.map(capability => capability.kind)
}
