import type { KubeObject } from '@/api/kubernetes'

export type NodeRole = 'control-plane' | 'worker'

type Translate = (key: string) => string

export function kubernetesNodeRoles(node: KubeObject | null | undefined): NodeRole[] {
  if (!node || node.kind && node.kind !== 'Node') return []
  const labels = node.metadata?.labels ?? {}
  const controlPlane = Object.hasOwn(labels, 'node-role.kubernetes.io/control-plane')
    || Object.hasOwn(labels, 'node-role.kubernetes.io/master')
  const worker = Object.hasOwn(labels, 'node-role.kubernetes.io/worker')
  return controlPlane ? worker ? ['control-plane', 'worker'] : ['control-plane'] : ['worker']
}

export function kubernetesNodeRoleKey(role: NodeRole): 'nodeRoles.controlPlane' | 'nodeRoles.worker' {
  return role === 'control-plane' ? 'nodeRoles.controlPlane' : 'nodeRoles.worker'
}

export function kubernetesNodeRoleText(node: KubeObject | null | undefined, t: Translate): string {
  return kubernetesRolesText(kubernetesNodeRoles(node), t)
}

export function kubernetesRolesText(roles: readonly NodeRole[], t: Translate): string {
  return (['control-plane', 'worker'] as const).filter(role => roles.includes(role))
    .map(role => t(kubernetesNodeRoleKey(role))).join(' / ') || '—'
}
