import type { ClusterHost, InfrastructureCluster, InfrastructureHost } from '@/api/infrastructure'

export function hostsForCluster(cluster: InfrastructureCluster, hosts: InfrastructureHost[]): ClusterHost[] {
  if (Array.isArray(cluster.hosts)) return cluster.hosts
  return hosts.filter(host => host.capabilities.some(capability => capability.kind === 'kubernetes' && capability.runtime_id === cluster.id))
}

export function clusterForHost(host: InfrastructureHost, clusters: InfrastructureCluster[]) {
  const linked = clusters.find(cluster => cluster.hosts?.some(item => item.id === host.id))
  if (linked) return linked
  const runtimeID = host.capabilities.find(capability => capability.kind === 'kubernetes')?.runtime_id
  return clusters.find(cluster => cluster.id === runtimeID && !Array.isArray(cluster.hosts))
}

export function clusterHostIDs(cluster: InfrastructureCluster, hosts: InfrastructureHost[], hostID: string, linked: boolean) {
  const ids = hostsForCluster(cluster, hosts).map(host => host.id).filter(id => id !== hostID)
  if (linked) ids.push(hostID)
  return [...new Set(ids)]
}
