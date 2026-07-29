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
  environment_id?: string
  is_builtin: boolean
  is_active: boolean
  capabilities: HostCapability[]
  capability_options?: HostCapabilityOption[]
  credential_configured?: boolean
  created_at?: string
  updated_at?: string
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

export async function listEnvironments(): Promise<InfrastructureEnvironment[]> {
  const response = await client.get<{ environments: InfrastructureEnvironment[] }>('/environments')
  return response.data.environments ?? []
}

export function capabilityOf(host: InfrastructureHost, kind: HostCapabilityKind) {
  return host.capabilities.find(capability => capability.kind === kind)
}

export function hostRuntimeKinds(host: InfrastructureHost) {
  return host.capabilities.map(capability => capability.kind)
}
