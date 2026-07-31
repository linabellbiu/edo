import client from '@/api/client'

export type HostMode = 'local' | 'ssh'
export type HostAuthType = 'password' | 'private_key' | 'legacy' | ''
export type HostCapabilityKind = 'ssh' | 'docker' | 'kubernetes' | 'local_exec'
export type HostCapabilityStatus = 'ready' | 'unchecked' | 'unreachable'

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
  environment_ids: string[]
  /** 兼容迁移期间的旧接口；新逻辑以 environment_ids 为准。 */
  environment_id?: string
  is_builtin: boolean
  is_active: boolean
  capabilities: HostCapability[]
  capability_options?: HostCapabilityOption[]
  credential_configured?: boolean
  created_at?: string
  updated_at?: string
}

export interface InfrastructureHostStatus {
  id: string
  is_active: boolean
  capabilities: HostCapability[]
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

export async function listHosts(): Promise<InfrastructureHost[]> {
  const response = await client.get<{ hosts: InfrastructureHost[] }>('/hosts')
  return response.data.hosts ?? []
}

export async function listHostStatuses(): Promise<InfrastructureHostStatus[]> {
  const response = await client.get<{ hosts: InfrastructureHostStatus[] }>('/hosts/statuses')
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

export async function listEnvironments(): Promise<InfrastructureEnvironment[]> {
  const response = await client.get<{ environments: InfrastructureEnvironment[] }>('/environments')
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
