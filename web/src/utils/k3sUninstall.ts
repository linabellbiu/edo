import type { K3sInstallation, K3sPlan, K3sUninstallSummary, K3sUninstallTarget } from '@/api/k3s'

const text=(value:unknown,limit=4096):value is string=>typeof value==='string'&&value.length>0&&value.length<=limit&&!/[\x00-\x1f\x7f]/.test(value)
const statuses=['planned','pending','running','succeeded','failed','interrupted']
export function validK3sUninstallSummary(value:unknown,hostID:string):value is K3sUninstallSummary {
  if(!value||typeof value!=='object')return false
  const summary=value as K3sUninstallSummary
  const expected=summary.role==='server'?{script:'/usr/local/bin/k3s-uninstall.sh',service:'k3s'}:summary.role==='agent'?{script:'/usr/local/bin/k3s-agent-uninstall.sh',service:'k3s-agent'}:undefined
  return Boolean(expected&&text(summary.source_installation_id,128)&&summary.host_id===hostID&&summary.script_path===expected.script&&summary.service_name===expected.service&&text(summary.data_dir)&&summary.data_dir.startsWith('/')&&summary.data_dir!=='/'&&!summary.data_dir.split('/').includes('..'))
}
export function sameK3sUninstallTarget(left?:K3sUninstallSummary,right?:K3sUninstallSummary) {
  return Boolean(left&&right&&(['source_installation_id','host_id','role','script_path','service_name','data_dir'] as const).every(key=>left[key]===right[key]))
}
export function validK3sUninstallTarget(value:unknown,hostID:string):value is K3sUninstallTarget {
  return validK3sUninstallSummary(value,hostID)&&text((value as K3sUninstallTarget).installation_name,256)&&statuses.includes((value as K3sUninstallTarget).status)
}
export function validK3sUninstallPlan(value:unknown,target:K3sUninstallTarget,address:string):value is K3sPlan {
  if(!value||typeof value!=='object')return false
  const plan=value as K3sPlan
  const report=plan.preflight
  const checkPassed=(check:unknown)=>Boolean(check&&typeof check==='object'&&['passed','warning'].includes((check as {status:string}).status))
  const checksPassed=!report||report.passed===true&&Array.isArray(report.checks)&&report.checks.every(checkPassed)&&Array.isArray(report.hosts)&&report.hosts.length===1&&report.hosts.every(host=>host&&host.host_id===target.host_id&&host.role===target.role&&host.passed===true&&Array.isArray(host.checks)&&host.checks.length>0&&host.checks.every(checkPassed))
  return Boolean(text(plan.id,128)&&plan.action==='uninstall'&&plan.mode==='native'&&Number.isFinite(Date.parse(plan.expires_at))&&validK3sUninstallSummary(plan.uninstall,target.host_id)&&sameK3sUninstallTarget(plan.uninstall,target)&&Array.isArray(plan.hosts)&&plan.hosts.length===1&&plan.hosts[0]?.id===target.host_id&&plan.hosts[0]?.role===target.role&&plan.hosts[0]?.address===address&&text(plan.hosts[0]?.name,256)&&checksPassed)
}
export function validK3sUninstallation(value:unknown,hostID:string):value is K3sInstallation {
  if(!value||typeof value!=='object')return false
  const task=value as K3sInstallation
  return Boolean(text(task.id,128)&&task.action==='uninstall'&&task.mode==='native'&&statuses.includes(task.status)&&validK3sUninstallSummary(task.uninstall,hostID)&&Array.isArray(task.hosts)&&task.hosts.length===1&&task.hosts[0]?.id===hostID&&task.hosts[0]?.role===task.uninstall?.role)
}
export function k3sUninstallResultUncertain(value:unknown) {
  if(!value||typeof value!=='object')return false
  const task=value as K3sInstallation,hostID=task.uninstall?.host_id
  return Boolean(typeof hostID==='string'&&hostID.length>0&&validK3sUninstallation(task,hostID)&&['failed','interrupted'].includes(task.status)&&task.hosts?.some(host=>host.stage==='uninstall'||host.stage==='uninstall_verify'))
}
