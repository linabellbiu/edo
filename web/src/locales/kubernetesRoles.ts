export const kubernetesRolesZh = {
  role: '节点角色',
  controlPlane: '控制平面 (Master)',
  worker: '工作节点 (Worker)',
  unknown: '角色待识别',
}

export const kubernetesRolesEn: Record<keyof typeof kubernetesRolesZh, string> = {
  role: 'Node role',
  controlPlane: 'Control plane (Master)',
  worker: 'Worker',
  unknown: 'Role unavailable',
}
