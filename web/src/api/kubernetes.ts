import client from './client'
import axios from 'axios'
import { apiErrorMessage } from './resources'
import type { ClusterHost } from './infrastructure'

export type KubeObject = Record<string, any>
export interface KubeCluster { id: string; name: string; default_namespace: string; api_server: string; is_active: boolean; hosts?: ClusterHost[] | null }
export interface KubeType { resource: string; kind: string; apiVersion: string; group: string; permission: string; namespaced: boolean; create: boolean; update: boolean; delete: boolean }
const type = (resource: string, kind: string, apiVersion: string, group: string, permission: string, namespaced = true, create = true, update = true, remove = true): KubeType => ({ resource, kind, apiVersion, group, permission: `kubernetes.${permission}`, namespaced, create, update, delete: remove })
export const kubeTypes = [
  type('nodes', 'Node', 'v1', 'nodes', 'node', false, false, true, false),
  type('deployments', 'Deployment', 'apps/v1', 'workloads', 'workload'),
  type('statefulsets', 'StatefulSet', 'apps/v1', 'workloads', 'workload'),
  type('daemonsets', 'DaemonSet', 'apps/v1', 'workloads', 'workload'),
  type('jobs', 'Job', 'batch/v1', 'workloads', 'workload', true, true, false),
  type('cronjobs', 'CronJob', 'batch/v1', 'workloads', 'workload'),
  type('pods', 'Pod', 'v1', 'workloads', 'workload', true, true, false),
  type('configmaps', 'ConfigMap', 'v1', 'config', 'config'),
  type('secrets', 'Secret', 'v1', 'config', 'config'),
  type('namespaces', 'Namespace', 'v1', 'config', 'config', false),
  type('persistentvolumeclaims', 'PersistentVolumeClaim', 'v1', 'storage', 'storage'),
  type('persistentvolumes', 'PersistentVolume', 'v1', 'storage', 'storage', false, false, false),
  type('storageclasses', 'StorageClass', 'storage.k8s.io/v1', 'storage', 'storage', false, false, false),
  type('services', 'Service', 'v1', 'network', 'network'),
  type('ingresses', 'Ingress', 'networking.k8s.io/v1', 'network', 'network'),
  type('networkpolicies', 'NetworkPolicy', 'networking.k8s.io/v1', 'network', 'network', true, true, false),
]

export function resourceURL(cluster: string, type: KubeType, name?: string) {
  return `/kubernetes/clusters/${encodeURIComponent(cluster)}/resources/${type.resource}${name ? `/${encodeURIComponent(name)}` : ''}`
}
export async function listKubeResources(cluster: string, type: KubeType, namespace: string, continuation = '', signal?: AbortSignal) {
  return (await client.get<{items: KubeObject[]; continue: string}>(resourceURL(cluster, type), { params: {namespace: type.namespaced ? namespace : '', continue: continuation}, signal })).data
}
export const identityOf = (object: KubeObject) => ({ uid: object.metadata.uid, resource_version: object.metadata.resourceVersion })
export const kubeKey = (object: KubeObject) => String(object.metadata.uid)

export interface KubeUsage { cpu:number; memory:number; timestamp:string }
export interface KubeNodeStatistics {name:string;uid:string;ready:boolean;cpu:number;memory:number;pod_capacity:number|null;pods:number|null;pod_state:'ok'|'unavailable'|'no_permission'}
export interface KubeOverview {
  counts:Record<string,{count:number;state:'ok'|'partial'|'unavailable'}>
  nodes:KubeNodeStatistics[] | null
  phases:Record<string,number> | null
  usage:Record<string,Record<string,KubeUsage>>
  metrics_state:Record<string,string>
  collected_at:string
}
export function usageFor(data:KubeOverview|null,object:KubeObject,now=Date.now()):KubeUsage|undefined {
  const kind=object.kind==='Node'?'nodes':object.kind==='Pod'?'pods':''
  const key=kind==='pods'?`${object.metadata.namespace}/${object.metadata.name}`:object.metadata.name
  const usage=data?.usage[kind]?.[key]
  return usage && now-Date.parse(usage.timestamp)<=120_000 && Date.parse(usage.timestamp)<=now+60_000 && Date.parse(usage.timestamp)>=Date.parse(object.metadata.creationTimestamp ?? '1970-01-01') ? usage : undefined
}
const validResourceAmount = (value:unknown):value is number => typeof value==='number'&&Number.isFinite(value)&&value>=0
const quantityFactors:Record<string,number>={n:1e-9,u:1e-6,m:1e-3,k:1e3,M:1e6,G:1e9,T:1e12,P:1e15,E:1e18,Ki:1024,Mi:1024**2,Gi:1024**3,Ti:1024**4,Pi:1024**5,Ei:1024**6}
// 仅为只读展示换算数量；配置校验和写入仍使用 Kubernetes 原始 Quantity。
export function parseKubeQuantity(value:unknown):number|undefined {
  if(typeof value==='number')return validResourceAmount(value)?value:undefined
  if(typeof value!=='string')return undefined
  const match=/^([+-]?(?:\d+(?:\.\d*)?|\.\d+))([eE][+-]?\d+|[numkMGTPE]|[KMGTPE]i)?$/.exec(value.trim())
  if(!match)return undefined
  const suffix=match[2]??'',factor=suffix===''?1:/^[eE][+-]?\d+$/.test(suffix)?10**Number(suffix.slice(1)):quantityFactors[suffix]!
  const amount=Number(match[1])*factor
  return validResourceAmount(amount)&&!(Number(match[1])>0&&amount===0)?amount:undefined
}
function resourceNumber(value:number,exact=false) {
  const digits=exact?6:2,minimum=10**-digits
  return value>0&&value<minimum?`<${minimum.toFixed(digits)}`:String(Number(value.toFixed(digits)))
}
export function formatKubeCPU(value:unknown,exact=false) {
  return !validResourceAmount(value)?'—':value<1?`${resourceNumber(value*1000,exact)} m`:`${resourceNumber(value,exact)} CPU`
}
export function formatKubeMemory(value:unknown,exact=false) {
  if(!validResourceAmount(value))return '—'
  const gib=value>=1024**3
  return `${resourceNumber(value/(gib?1024**3:1024**2),exact)} ${gib?'GiB':'MiB'}`
}
export function formatKubeQuantity(resource:string,value:unknown) {
  const amount=parseKubeQuantity(value)
  if(resource==='cpu')return formatKubeCPU(amount)
  if(['memory','storage','ephemeral-storage'].includes(resource)||resource.startsWith('hugepages-'))return formatKubeMemory(amount)
  return amount!==undefined&&Number.isInteger(amount)?String(amount):'—'
}

export interface NodeResourceUsage {
  key:'cpu'|'memory'|'pods'
  used:number|undefined
  total:number|undefined
  percent:number|undefined
  width:number
  state:'ok'|'loading'|'unavailable'|'no_permission'|'missing_capacity'
}
export function nodeResourceUsage(data:KubeOverview|null,node:KubeObject,failed=false,now=Date.now()):NodeResourceUsage[] {
  const stats=data?.nodes?.find(item=>item.name===node.metadata.name&&item.uid===node.metadata.uid)
  const at=Date.parse(data?.collected_at??'')
  const fresh=Number.isFinite(at)&&now-at<=120_000&&at<=now+60_000
  const usage=!failed&&fresh&&stats?usageFor(data,node,now):undefined
  const valid=(value:unknown):value is number=>typeof value==='number'&&Number.isFinite(value)&&value>=0
  const row=(key:NodeResourceUsage['key'],used:unknown,total:unknown,state:NodeResourceUsage['state']):NodeResourceUsage=>{
    const amount=valid(used)?used:undefined,capacity=valid(total)&&total>0?total:undefined
    const percent=amount!==undefined&&capacity!==undefined?amount/capacity*100:undefined
    return {key,used:amount,total:capacity,percent,width:percent===undefined?0:Math.min(100,percent),state:amount!==undefined&&capacity!==undefined?'ok':amount!==undefined?'missing_capacity':state}
  }
  const state=failed||data?'unavailable':'loading'
  return [row('cpu',usage?.cpu,stats?.cpu,state),row('memory',usage?.memory,stats?.memory,state),row('pods',!failed&&fresh&&stats?.pod_state==='ok'?stats.pods:undefined,stats?.pod_capacity,stats?.pod_state==='no_permission'?'no_permission':state)]
}

function canonical(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonical)
  if (value && typeof value==='object') return Object.fromEntries(Object.entries(value).sort(([a],[b])=>a.localeCompare(b)).map(([key,item])=>[key,canonical(item)]))
  return value
}
export function writableSnapshot(object: KubeObject): string {
  return JSON.stringify(canonical({spec:object.spec,data:object.data,binaryData:object.binaryData,type:object.type,immutable:object.immutable,labels:object.metadata.labels,annotations:object.metadata.annotations}))
}
export class KubeResourceChanged extends Error {}
export function kubeErrorMessage(error:unknown):string { return error instanceof KubeResourceChanged ? error.message : apiErrorMessage(error) }

// 只接受状态变化；配置变化或同名资源重建后，仍要求用户重新确认。
export async function withFreshKubeResource<T>(cluster:string,type:KubeType,original:KubeObject,conflict:string,mutate:(current:KubeObject)=>Promise<T>,signal?:AbortSignal):Promise<T> {
  for(let attempt=0;attempt<2;attempt++) {
    signal?.throwIfAborted()
    const {data}=await client.get<{object:KubeObject}>(resourceURL(cluster,type,original.metadata.name),{params:{namespace:original.metadata.namespace??''},signal})
    signal?.throwIfAborted()
    const current=data.object
    if(current.metadata.uid!==original.metadata.uid||writableSnapshot(current)!==writableSnapshot(original)||type.resource==='secrets'&&current.metadata.resourceVersion!==original.metadata.resourceVersion) throw new KubeResourceChanged(conflict)
    try { return await mutate(current) } catch(error) {
      if(attempt===1||!axios.isAxiosError(error)||error.response?.status!==409)throw error
    }
  }
  throw new KubeResourceChanged(conflict)
}
export function podSpec(object: KubeObject): KubeObject {
  return object.kind === 'Pod' ? object.spec ?? {} : object.kind === 'CronJob' ? object.spec?.jobTemplate?.spec?.template?.spec ?? {} : object.spec?.template?.spec ?? {}
}
export function kubeSummary(object: KubeObject): {status: string; tone: string; primary: string; secondary: string} {
  const spec = object.spec ?? {}, status = object.status ?? {}, containers = podSpec(object).containers ?? []
  const summary = {status: '', tone: 'default', primary: '', secondary: ''}
  switch (object.kind) {
    case 'Node': {
      const ready = status.conditions?.find((item: KubeObject) => item.type === 'Ready')?.status === 'True'
      summary.status = ready ? 'Ready' : 'NotReady'; summary.tone = ready ? 'success' : 'error'
      summary.primary = (status.addresses ?? []).filter((item: KubeObject) => item.type === 'InternalIP').map((item: KubeObject) => item.address).join(', ')
      summary.secondary = `${status.nodeInfo?.kubeletVersion ?? '—'} · ${status.nodeInfo?.architecture ?? '—'} · ${formatKubeQuantity('cpu',status.allocatable?.cpu)} / RAM ${formatKubeQuantity('memory',status.allocatable?.memory)}`
      break
    }
    case 'Deployment': case 'StatefulSet': case 'DaemonSet': {
      const desired = object.kind === 'DaemonSet' ? status.desiredNumberScheduled ?? 0 : spec.replicas ?? 1
      const ready = status.readyReplicas ?? status.numberReady ?? 0
      summary.status = `${ready} / ${desired}`; summary.tone = ready === desired ? 'success' : 'processing'
      summary.primary = containers.map((item: KubeObject) => item.image).join(', ')
      summary.secondary = Object.entries(spec.selector?.matchLabels ?? {}).map(([key, value]) => `${key}=${value}`).join(', ')
      break
    }
    case 'Pod':
      summary.status = status.containerStatuses?.find((item: KubeObject) => item.state?.waiting)?.state?.waiting?.reason ?? status.phase ?? 'Unknown'
      summary.tone = status.phase === 'Running' && status.containerStatuses?.every((item: KubeObject) => item.ready) ? 'success' : status.phase === 'Failed' ? 'error' : 'processing'
      summary.primary = spec.nodeName ?? '—'; summary.secondary = `${status.podIP ?? '—'} · ${containers.map((item: KubeObject) => item.image).join(', ')}`; break
    case 'Job': summary.status = status.succeeded ? 'Complete' : status.failed ? 'Failed' : 'Running'; summary.tone = status.failed ? 'error' : status.succeeded ? 'success' : 'processing'; summary.primary = containers.map((item: KubeObject) => item.image).join(', '); summary.secondary = `${status.succeeded ?? 0} / ${spec.completions ?? 1}`; break
    case 'CronJob': summary.status = spec.suspend ? 'Suspended' : 'Active'; summary.tone = spec.suspend ? 'default' : 'success'; summary.primary = spec.schedule ?? ''; summary.secondary = status.lastScheduleTime ?? '—'; break
    case 'ConfigMap': summary.status = `${Object.keys(object.data ?? {}).length + Object.keys(object.binaryData ?? {}).length} keys`; summary.primary = Object.keys(object.data ?? {}).join(', '); break
    case 'Secret': summary.status = `${object.keys?.length ?? 0} keys`; summary.primary = object.type ?? 'Opaque'; summary.secondary = (object.keys ?? []).join(', '); break
    case 'PersistentVolumeClaim': summary.status = status.phase ?? 'Pending'; summary.tone = status.phase === 'Bound' ? 'success' : 'processing'; summary.primary = `${formatKubeQuantity('storage',status.capacity?.storage ?? spec.resources?.requests?.storage)} · ${spec.storageClassName ?? 'default'}`; summary.secondary = spec.volumeName ?? '—'; break
    case 'PersistentVolume': summary.status = status.phase ?? '—'; summary.tone = status.phase === 'Bound' ? 'success' : 'default'; summary.primary = `${formatKubeQuantity('storage',spec.capacity?.storage)} · ${spec.storageClassName ?? '—'}`; summary.secondary = `${spec.persistentVolumeReclaimPolicy ?? '—'} · ${spec.claimRef?.namespace ?? ''}/${spec.claimRef?.name ?? ''}`; break
    case 'StorageClass': summary.status = object.reclaimPolicy ?? 'Delete'; summary.primary = object.provisioner ?? ''; summary.secondary = object.volumeBindingMode ?? 'Immediate'; break
    case 'Service': summary.status = spec.type ?? 'ClusterIP'; summary.primary = spec.clusterIP ?? '—'; summary.secondary = (spec.ports ?? []).map((item: KubeObject) => `${item.port} → ${item.targetPort} / ${item.protocol ?? 'TCP'}${item.nodePort ? ` :${item.nodePort}` : ''}`).join(', '); break
    case 'Ingress': summary.status = spec.ingressClassName ?? 'default'; summary.primary = (spec.rules ?? []).map((item: KubeObject) => item.host || '*').join(', '); summary.secondary = (status.loadBalancer?.ingress ?? []).map((item: KubeObject) => item.ip ?? item.hostname).join(', '); break
    case 'NetworkPolicy': summary.status = (spec.policyTypes ?? ['Ingress']).join(', '); summary.primary = Object.entries(spec.podSelector?.matchLabels ?? {}).map(([key, value]) => `${key}=${value}`).join(', ') || '*'; summary.secondary = `${spec.ingress?.length ?? 0} ingress / ${spec.egress?.length ?? 0} egress`; break
    default: summary.status = status.phase ?? 'Active'
  }
  return summary
}

// 空数组表示未选择；星号表示整个集群，不能与具体命名空间混用。
export function namespaceSelection(value: unknown, fallback = 'default'): string[] {
  if (value === undefined || value === null) return [fallback]
  const values = Array.isArray(value) ? value : [value]
  if (values.includes('*')) return ['*']
  return [...new Set(values.filter((item): item is string => typeof item === 'string' && item.length <= 63 && /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(item)))].sort()
}

export async function mapNamespaceRequests<T>(names: string[], request: (namespace: string) => Promise<T>, signal?: AbortSignal): Promise<T[]> {
  const result = new Array<T>(names.length)
  let next = 0, failed = false
  // 多选仍限制同时请求数，避免命名空间较多时集中冲击集群 API。
  await Promise.all(Array.from({length: Math.min(4, names.length)}, async () => {
    while (!failed && next < names.length) {
      signal?.throwIfAborted()
      const index = next++
      try { result[index] = await request(names[index]!) } catch (error) { failed = true; throw error }
    }
  }))
  return result
}

export async function listKubeResourceScopes(cluster: string, type: KubeType, selection: string[], cursor?: Record<string,string>, signal?: AbortSignal) {
  const names = cursor ? Object.keys(cursor) : type.namespaced ? selection : ['*']
  const pages = await mapNamespaceRequests(names, async namespace => ({namespace, ...await listKubeResources(cluster, type, namespace, cursor?.[namespace] ?? '', signal)}), signal)
  return {items: pages.flatMap(page => page.items), cursor: Object.fromEntries(pages.filter(page => page.continue).map(page => [page.namespace, page.continue]))}
}

// 节点和集群级资源只保留一份；仅累加命名空间资源，缺失统计不补零。
export function mergeNamespaceOverviews(results: KubeOverview[]): KubeOverview | null {
  const first = results[0]
  if (!first || results.length === 1) return first ?? null
  const counts: KubeOverview['counts'] = {...first.counts}
  for (const type of kubeTypes.filter(type => type.namespaced)) {
    const values = results.map(result => result.counts[type.resource])
    if (values.every(value => !value)) continue
    const unavailable = values.some(value => !value || value.state === 'unavailable')
    counts[type.resource] = {count: unavailable ? 0 : values.reduce((total,value) => total + value!.count, 0), state: unavailable ? 'unavailable' : values.some(value => value?.state === 'partial') ? 'partial' : 'ok'}
  }
  const phases = results.every(result => result.phases !== null) ? results.reduce<Record<string,number>>((sum,result) => {
    for (const [phase,count] of Object.entries(result.phases!)) sum[phase] = (sum[phase] ?? 0) + count
    return sum
  }, {}) : null
  const pods = Object.assign({}, ...results.map(result => result.usage?.pods ?? {}))
  return {...first, counts, phases, usage: {...first.usage, pods}, metrics_state: {...first.metrics_state, pods: results.every(result => result.metrics_state?.pods === 'ok') ? 'ok' : 'unavailable'}, collected_at: results.map(result => result.collected_at).sort()[0]!}
}
