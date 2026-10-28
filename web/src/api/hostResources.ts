import client from '@/api/client'

export interface HostResources {
  state: 'ok' | 'unavailable'
  source: 'local' | 'ssh'
  scope: 'current_environment'
  cpu_cores: number | null
  memory_total_bytes: number | null
  disk_total_bytes: number | null
  disk_mount: string
  os: string
  collected_at: string
}

export async function getHostResources(hostID: string, signal?: AbortSignal): Promise<HostResources> {
  const response = await client.get<{ resources: HostResources }>(`/hosts/${encodeURIComponent(hostID)}/resources`, {
    signal,
    timeout: 15_000,
  })
  return response.data.resources
}
