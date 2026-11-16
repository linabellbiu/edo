export type SessionKind = 'container-log' | 'terminal' | 'pipeline-log' | 'installation-log' | 'system-log' | 'preflight-log'
export interface SessionTarget {
  kind: SessionKind
  path?: string
  title?: string
  runID?: string
  nodeID?: string
  installationID?: string
  sourceDeploymentID?: string
  query?: string
  level?: string
  channel?: string
}
const segment = '[A-Za-z0-9_.~-]+'
const socketTargets = [
  { kind: 'container-log', permission: 'cluster.read', pattern: new RegExp(`^/api/v1/docker/endpoints/(${segment})/containers/(${segment})/logs/ws$`) },
  { kind: 'container-log', permission: 'kubernetes.workload.read', pattern: new RegExp(`^/api/v1/kubernetes/clusters/(${segment})/namespaces/(${segment})/pods/(${segment})/containers/(${segment})/logs/ws$`) },
  { kind: 'terminal', permission: 'terminal.open', pattern: new RegExp(`^/api/v1/terminals/docker/(${segment})/containers/(${segment})/ws$`) },
  { kind: 'terminal', permission: 'terminal.open', pattern: new RegExp(`^/api/v1/terminals/kubernetes/(${segment})/namespaces/(${segment})/pods/(${segment})/containers/(${segment})/ws$`) },
  { kind: 'terminal', permission: 'host.terminal.open', pattern: new RegExp(`^/api/v1/terminals/hosts/(${segment})/ws$`) },
]
const validID = (value?: string) => Boolean(value && /^[A-Za-z0-9_.~-]{1,200}$/.test(value) && value !== '.' && value !== '..')

// 链接只接受已登记的日志/终端路由，不允许 URL 指定任意 API 或附带认证信息。
export function sessionSocketTarget(kind: SessionKind, path = '') {
  if (!path.startsWith('/api/v1/') || /[#\\\s]/.test(path)) return null
  const url = new URL(path, 'https://edo.invalid')
  if (path.split('?')[0] !== url.pathname) return null
  const target = socketTargets.find(item => item.kind === kind && item.pattern.test(url.pathname))
  if (!target || [...url.searchParams.keys()].some(key => !['project_id', 'pod_uid'].includes(key))) return null
  const parts = target.pattern.exec(url.pathname)!.slice(1)
  if (parts.some(part => !validID(part)) || [...url.searchParams.keys()].some(key => url.searchParams.getAll(key).length !== 1)) return null
  const project = url.searchParams.get('project_id'), podUID = url.searchParams.get('pod_uid')
  if (project && !validID(project) || podUID !== null && !validID(podUID)) return null
  return { permission: target.permission, parts, url, kubernetes: url.pathname.includes('/kubernetes/') }
}

export function sessionPermissions(target: SessionTarget): string[] {
  if (target.kind === 'pipeline-log') return ['pipeline_run.read']
  if (target.kind === 'installation-log') return ['cluster.create', 'kubernetes.node.create', 'kubernetes.k3s.uninstall']
  if (target.kind === 'preflight-log') return ['cluster.create', 'kubernetes.node.create']
  if (target.kind === 'system-log') return ['monitor.read']
  const socket = sessionSocketTarget(target.kind, target.path)
  return socket ? [socket.permission] : []
}

export function validSessionTarget(target: SessionTarget): boolean {
  if (target.sourceDeploymentID && !validID(target.sourceDeploymentID)) return false
  if (target.kind === 'pipeline-log') return validID(target.runID) && (!target.nodeID || validID(target.nodeID))
  if (target.kind === 'installation-log') return validID(target.installationID)
  if (target.kind === 'preflight-log') return validID(target.channel)
  if (target.kind === 'system-log') return !target.level || ['debug', 'info', 'warn', 'warning', 'error'].includes(target.level)
  return Boolean(sessionSocketTarget(target.kind, target.path))
}

export function sessionPopoutURL(target: SessionTarget, project = typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? ''): string {
  if (!validSessionTarget(target)) return ''
  const socket = target.path ? sessionSocketTarget(target.kind, target.path) : null
  const frozenProject = socket?.url.searchParams.get('project_id') ?? project
  if (frozenProject && !validID(frozenProject)) return ''
  const parameters = new URLSearchParams({ kind: target.kind, project: frozenProject })
  for (const key of ['title', 'runID', 'nodeID', 'installationID', 'sourceDeploymentID', 'query', 'level', 'channel'] as const) {
    if (target[key]) parameters.set(key, target[key]!)
  }
  if (socket) {
    socket.url.searchParams.set('project_id', frozenProject)
    parameters.set('path', socket.url.pathname + socket.url.search)
  }
  return `/sessions/view?${parameters}`
}

export function parseSessionTarget(query: Record<string, unknown>): SessionTarget | null {
  const target: Record<string, string> = {}
  for (const key of ['kind', 'path', 'title', 'runID', 'nodeID', 'installationID', 'sourceDeploymentID', 'query', 'level', 'channel']) {
    if (query[key] !== undefined && typeof query[key] !== 'string') return null
    if (typeof query[key] === 'string') target[key] = query[key] as string
  }
  return validSessionTarget(target as unknown as SessionTarget) ? target as unknown as SessionTarget : null
}
