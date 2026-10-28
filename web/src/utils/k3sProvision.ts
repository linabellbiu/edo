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

const installationStages: Record<string,string> = {
  guard:'检查安装保护',preflight:'复查主机环境',download:'下载安装脚本',configuration:'写入安装配置',installer:'执行官方安装器',
  installer_download:'下载安装包',installer_verify:'校验安装包',installer_binary:'安装 K3s 二进制',installer_environment:'配置运行环境',installer_service:'配置系统服务',installer_start:'等待 K3s 服务启动',
  version:'核验安装版本',service:'核验系统服务',api:'等待集群 API 就绪',import:'读取集群连接配置',node:'等待节点就绪',association:'关联主机与集群',components:'等待系统组件就绪',
  component_coredns:'等待 CoreDNS 就绪','component_metrics-server':'等待 Metrics Server 就绪','component_local-path-provisioner':'等待本地存储组件就绪',
  k3d_tool:'准备 k3d 工具',k3d_create:'创建 k3d 集群',k3d_resources:'核验 k3d 资源',
}

export function k3sStageLabel(stage?: string) {
  return stage ? installationStages[stage] || '执行安装步骤' : ''
}

export function k3sElapsed(start?: string, end?: string | number) {
  if (!start || end === undefined || end === '') return '—'
  const from=Date.parse(start),to=typeof end==='number'?end:Date.parse(end)
  if (!Number.isFinite(from)||!Number.isFinite(to)||to<from) return '—'
  const seconds=Math.floor((to-from)/1000),hours=Math.floor(seconds/3600),minutes=Math.floor(seconds%3600/60)
  return hours ? `${hours} 小时 ${minutes} 分 ${seconds%60} 秒` : minutes ? `${minutes} 分 ${seconds%60} 秒` : `${seconds} 秒`
}

export function k3sInstallationLogView(installation?: K3sInstallation) {
  const source=k3sInstallationLogs(installation),limit=64*1024
  // 日志仅按纯文本呈现，限制异常响应大小，不执行 ANSI 或 HTML 控制内容。
  let tail=source.slice(-limit)
  if(tail.length&&tail.charCodeAt(0)>=0xdc00&&tail.charCodeAt(0)<=0xdfff)tail=tail.slice(1)
  const bytes=new TextEncoder().encode(tail),truncated=source.length>tail.length||bytes.length>limit
  let start=Math.max(0,bytes.length-limit)
  while(start<bytes.length&&(bytes[start]!&0xc0)===0x80)start++
  return {text:new TextDecoder().decode(bytes.subarray(start)).replace(/\x1b\[[0-?]*[ -/]*[@-~]/g,'').replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g,''),truncated}
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
