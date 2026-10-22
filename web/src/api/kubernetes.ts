import client from './client'
import axios from 'axios'
import { apiErrorMessage } from './resources'

export type KubeObject = Record<string, any>
export interface KubeCluster { id: string; name: string; default_namespace: string; api_server: string; is_active: boolean }
export interface KubeType { resource: string; kind: string; apiVersion: string; group: string; permission: string; namespaced: boolean; create: boolean; update: boolean; delete: boolean }
const type = (resource: string, kind: string, apiVersion: string, group: string, permission: string, namespaced = true, create = true, update = true, remove = true): KubeType => ({ resource, kind, apiVersion, group, permission: `kubernetes.${permission}`, namespaced, create, update, delete: remove })
export const kubeTypes = [
  type('nodes', 'Node', 'v1', 'nodes', 'node', false, false, true, false),
  type('deployments', 'Deployment', 'apps/v1', 'workloads', 'workload'),
  type('statefulsets', 'StatefulSet', 'apps/v1', 'workloads', 'workload'),
  type('daemonsets', 'DaemonSet', 'apps/v1', 'workloads', 'workload'),
  type('jobs', 'Job', 'batch/v1', 'workloads', 'workload', true, true, false),
  type('cronjobs', 'CronJob', 'batch/v1', 'workloads', 'workload'),
  type('pods', 'Pod', 'v1', 'workloads', 'workload', true, false, false),
  type('configmaps', 'ConfigMap', 'v1', 'config', 'config'),
  type('secrets', 'Secret', 'v1', 'config', 'config'),
  type('namespaces', 'Namespace', 'v1', 'config', 'config', false),
  type('persistentvolumeclaims', 'PersistentVolumeClaim', 'v1', 'config', 'storage'),
  type('persistentvolumes', 'PersistentVolume', 'v1', 'config', 'storage', false, false, false),
  type('storageclasses', 'StorageClass', 'storage.k8s.io/v1', 'config', 'storage', false, false, false),
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
export async function withFreshKubeResource<T>(cluster:string,type:KubeType,original:KubeObject,conflict:string,mutate:(current:KubeObject)=>Promise<T>):Promise<T> {
  for(let attempt=0;attempt<2;attempt++) {
    const {data}=await client.get<{object:KubeObject}>(resourceURL(cluster,type,original.metadata.name),{params:{namespace:original.metadata.namespace??''}})
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
      summary.secondary = `${status.nodeInfo?.kubeletVersion ?? '—'} · ${status.nodeInfo?.architecture ?? '—'} · CPU ${status.allocatable?.cpu ?? '—'} / RAM ${status.allocatable?.memory ?? '—'}`
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
    case 'PersistentVolumeClaim': summary.status = status.phase ?? 'Pending'; summary.tone = status.phase === 'Bound' ? 'success' : 'processing'; summary.primary = `${status.capacity?.storage ?? spec.resources?.requests?.storage ?? '—'} · ${spec.storageClassName ?? 'default'}`; summary.secondary = spec.volumeName ?? '—'; break
    case 'PersistentVolume': summary.status = status.phase ?? '—'; summary.tone = status.phase === 'Bound' ? 'success' : 'default'; summary.primary = `${spec.capacity?.storage ?? '—'} · ${spec.storageClassName ?? '—'}`; summary.secondary = `${spec.persistentVolumeReclaimPolicy ?? '—'} · ${spec.claimRef?.namespace ?? ''}/${spec.claimRef?.name ?? ''}`; break
    case 'StorageClass': summary.status = object.reclaimPolicy ?? 'Delete'; summary.primary = object.provisioner ?? ''; summary.secondary = object.volumeBindingMode ?? 'Immediate'; break
    case 'Service': summary.status = spec.type ?? 'ClusterIP'; summary.primary = spec.clusterIP ?? '—'; summary.secondary = (spec.ports ?? []).map((item: KubeObject) => `${item.port} → ${item.targetPort} / ${item.protocol ?? 'TCP'}${item.nodePort ? ` :${item.nodePort}` : ''}`).join(', '); break
    case 'Ingress': summary.status = spec.ingressClassName ?? 'default'; summary.primary = (spec.rules ?? []).map((item: KubeObject) => item.host || '*').join(', '); summary.secondary = (status.loadBalancer?.ingress ?? []).map((item: KubeObject) => item.ip ?? item.hostname).join(', '); break
    case 'NetworkPolicy': summary.status = (spec.policyTypes ?? ['Ingress']).join(', '); summary.primary = Object.entries(spec.podSelector?.matchLabels ?? {}).map(([key, value]) => `${key}=${value}`).join(', ') || '*'; summary.secondary = `${spec.ingress?.length ?? 0} ingress / ${spec.egress?.length ?? 0} egress`; break
    default: summary.status = status.phase ?? 'Active'
  }
  return summary
}
