import type { InfrastructureHost } from '@/api/infrastructure'
import type { K3sInstallation, K3sOption, K3sJoinRole } from '@/api/k3s'
import { parseDocument } from 'yaml'

export function k3sOptionGroups(options: K3sOption[], query: string) {
  const search = query.trim().toLocaleLowerCase()
  const groups = new Map<string, K3sOption[]>()
  for (const option of options) {
    if (search && ![option.name, option.label, option.group, option.description].join(' ').toLocaleLowerCase().includes(search)) continue
    const group = option.group || '其他'
    groups.set(group, [...(groups.get(group) ?? []), option])
  }
  return [...groups].map(([name, items]) => ({ name, items }))
}

export function k3sDefaultValue(option: K3sOption): unknown {
  if (option.default !== undefined) return Array.isArray(option.default) ? [...option.default] : option.default
  if (option.type === 'boolean') return false
  if (option.type === 'integer') return 0
  return option.type === 'string[]' ? [] : ''
}

export function k3sOptionDefaultText(option: K3sOption) {
  if (option.secret) return option.default === undefined ? '未设置' : '已设置'
  if (option.default === undefined || option.default === null || option.default === '') return '由 K3s 决定'
  if (Array.isArray(option.default)) return option.default.length ? option.default.join(', ') : '空列表'
  if (typeof option.default === 'boolean') return option.default ? '开启' : '关闭'
  return String(option.default)
}

export function availableK3sHosts(hosts: InfrastructureHost[]) {
  return hosts.filter(host => host.mode === 'ssh' && host.is_active && !host.is_builtin)
}

export function k3sHostDisabledReason(host: InfrastructureHost) {
  if (host.credential_configured === false) return '未配置 SSH 凭据'
  if (host.capabilities.some(capability => capability.kind === 'kubernetes' && capability.runtime_id)) return '已关联 Kubernetes 集群'
  return ''
}

export function k3sInstallationRunning(installation?: K3sInstallation) {
  return installation?.status === 'pending' || installation?.status === 'running'
}

export function k3sInstallationLabel(status?: K3sInstallation['status']) {
  return ({planned:'待确认',pending:'等待执行',running:'正在安装',succeeded:'安装完成',failed:'安装失败',interrupted:'执行中断'} as const)[status ?? 'pending']
}

export function k3sHostRoleLabel(role?: string) {
  return role === 'server' ? '控制平面（Master）' : role === 'agent' ? '工作节点（Worker）' : '节点'
}

export const k3sMasterRawArgumentOptions = ['etcd-arg','kube-apiserver-arg','kube-controller-manager-arg','kube-scheduler-arg','kube-cloud-controller-manager-arg','kube-controller-arg','kube-cloud-controller-arg','cloud-controller-manager-arg'] as const
export const k3sMasterRawArgumentReason = '添加 Master 不能通过原始参数覆盖目标集群的成员、认证、TLS 和关键控制平面配置'

export function k3sInstallationLogs(installation?: K3sInstallation) {
  return Array.isArray(installation?.logs) ? installation.logs.join('\n') : installation?.logs ?? ''
}

export function k3sHostStatusLabel(status?: string) {
  if (!status) return '等待执行'
  return ({pending:'等待执行',checking:'检查中',ready:'检查通过',installing:'安装中',running:'执行中',succeeded:'已完成',failed:'失败',skipped:'已跳过',not_started:'未开始'} as Record<string,string>)[status] ?? '状态未知'
}

export function k3sAutomaticEndpoint(host?: InfrastructureHost, options: Record<string,unknown> = {}, overrides = '') {
  if (!host) return ''
  let port = options['https-listen-port']
  if (overrides.trim()) {
    try {
      const document=parseDocument(overrides,{uniqueKeys:true})
      const value=document.errors.length?undefined:document.toJS({maxAliasCount:0})
      if(value?.server&&typeof value.server==='object'&&Object.hasOwn(value.server,'https-listen-port'))port=value.server['https-listen-port']
    } catch { /* 输入中的 YAML 可能尚未完成，最终配置由预览接口校验。 */ }
  }
  const parsed=Number(port),number=port!==null&&port!==undefined&&Number.isInteger(parsed)&&parsed>0&&parsed<=65535?parsed:6443
  const address=host.address.includes(':')&&!host.address.startsWith('[')?`[${host.address}]`:host.address
  return `https://${address}:${number}`
}

export function validateK3sOverrides(source: string, nodeRole?: K3sJoinRole) {
  if (!source.trim()) return
  const document = parseDocument(source, {uniqueKeys:true})
  if (document.errors.length) throw new Error(`覆盖配置不是有效的 YAML：${document.errors[0]?.message}`)
  const value: unknown = document.toJS({maxAliasCount:0})
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('覆盖配置需要是 YAML 对象')
  for (const [scope, options] of Object.entries(value)) {
    if (!['server','agent','installer'].includes(scope)) throw new Error(`不支持的覆盖范围：${scope}`)
    if (nodeRole && scope !== nodeRole && scope !== 'installer') throw new Error(`${k3sHostRoleLabel(nodeRole)}仅接受 ${nodeRole} 和 installer 覆盖配置`)
    if (!options || typeof options !== 'object' || Array.isArray(options)) throw new Error(`${scope} 需要是 YAML 对象`)
    if (nodeRole === 'server' && scope === 'server') {
      for (const name of k3sMasterRawArgumentOptions) {
        const value=(options as Record<string,unknown>)[name]
        if (value!==undefined&&value!==null&&value!==''&&!(Array.isArray(value)&&value.length===0)) throw new Error(k3sMasterRawArgumentReason)
      }
    }
  }
}
