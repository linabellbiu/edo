import type { KubeObject } from '@/api/kubernetes'

export type NodeRole = 'control-plane' | 'worker'

type Translate = (key: string) => string

export function kubernetesNodeRoles(node: KubeObject | null | undefined): NodeRole[] {
  if (!node || node.kind && node.kind !== 'Node') return []
  const labels = node.metadata?.labels ?? {}
  const controlPlane = Object.hasOwn(labels, 'node-role.kubernetes.io/control-plane')
    || Object.hasOwn(labels, 'node-role.kubernetes.io/master')
  const worker = Object.hasOwn(labels, 'node-role.kubernetes.io/worker')
  const kubeletVersion = node.status?.nodeInfo?.kubeletVersion
  // K3s Server 通过 Agent 注册 Node，缺少 worker 标签时仍有工作节点角色。
  const k3sVersion = typeof kubeletVersion === 'string'
    ? /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?\+k3s[1-9]\d*$/.exec(kubeletVersion)
    : null
  const registeredK3sAgent = Boolean(k3sVersion && (!k3sVersion[4]
    || k3sVersion[4].split('.').every(part => !/^\d+$/.test(part) || part.length === 1 || !part.startsWith('0'))))
  return controlPlane ? worker || registeredK3sAgent ? ['control-plane', 'worker'] : ['control-plane'] : ['worker']
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
