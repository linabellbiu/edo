import { podSpec, type KubeObject } from '@/api/kubernetes'
import type { KubeReference } from '@/utils/kubernetesPresentation'

export interface KubeContainerDetail {
  key: string
  kind: 'init' | 'app' | 'ephemeral'
  container: KubeObject
  runtime?: KubeObject
}

export function kubeContainerDetails(object: KubeObject): KubeContainerDetail[] {
  const spec = podSpec(object)
  return ([
    { kind: 'init', field: 'initContainers', statuses: 'initContainerStatuses' },
    { kind: 'app', field: 'containers', statuses: 'containerStatuses' },
    { kind: 'ephemeral', field: 'ephemeralContainers', statuses: 'ephemeralContainerStatuses' },
  ] as const).flatMap(group => (spec[group.field] ?? []).map((container: KubeObject) => ({
    key: `${group.kind}/${container.name}`,
    kind: group.kind,
    container,
    // 模板没有实际容器状态；不同种类中同名的容器也不能互相借用状态。
    runtime: object.kind === 'Pod'
      ? object.status?.[group.statuses]?.find((status: KubeObject) => status.name === container.name)
      : undefined,
  })))
}

export interface KubeEnvironmentSource {
  name: string
  kind: 'direct' | 'field' | 'resource' | 'configmap' | 'secret' | 'unknown'
  reference?: KubeReference
  key?: string
  prefix?: string
  optional?: boolean
  field?: string
}

export function kubeEnvironmentSources(container: KubeObject, namespace?: string): KubeEnvironmentSource[] {
  const rows: KubeEnvironmentSource[] = []
  for (const env of container.env ?? []) {
    const source = env.valueFrom ?? {}
    const reference = source.configMapKeyRef ?? source.secretKeyRef
    if (reference) {
      const kind = source.secretKeyRef ? 'secret' : 'configmap'
      rows.push({ name: env.name, kind, reference: { resource: kind === 'secret' ? 'secrets' : 'configmaps', name: reference.name, namespace }, key: reference.key, optional: reference.optional })
    } else if (source.fieldRef) {
      rows.push({ name: env.name, kind: 'field', field: source.fieldRef.fieldPath })
    } else if (source.resourceFieldRef) {
      rows.push({ name: env.name, kind: 'resource', field: [source.resourceFieldRef.containerName, source.resourceFieldRef.resource, source.resourceFieldRef.divisor].filter(value => value !== undefined && value !== '').join(' / ') })
    } else {
      // 详情只列环境变量来源，不把可能含密码的直接值再次复制到展示模型中。
      rows.push({ name: env.name, kind: Object.hasOwn(env, 'value') ? 'direct' : 'unknown' })
    }
  }
  for (const source of container.envFrom ?? []) {
    const reference = source.configMapRef ?? source.secretRef
    if (!reference) continue
    const kind = source.secretRef ? 'secret' : 'configmap'
    rows.push({ name: '*', kind, prefix: source.prefix, reference: { resource: kind === 'secret' ? 'secrets' : 'configmaps', name: reference.name, namespace }, optional: reference.optional })
  }
  return rows
}

export function kubeContainerProbes(container: KubeObject) {
  return ['startupProbe', 'readinessProbe', 'livenessProbe'].flatMap(key => {
    const probe = container[key]
    if (!probe) return []
    const method = ['httpGet', 'tcpSocket', 'exec', 'grpc'].find(value => probe[value])
    const action = method ? probe[method] : undefined
    let target = ''
    if (method === 'httpGet') target = `${action.scheme ?? 'HTTP'} ${action.host ? `${action.host}:` : ''}${action.port ?? ''}${action.path ?? '/'}`
    if (method === 'tcpSocket') target = [action.host, action.port].filter(value => value !== undefined && value !== '').join(':')
    if (method === 'exec') target = (action.command ?? []).join(' ')
    if (method === 'grpc') target = [action.port, action.service].filter(value => value !== undefined && value !== '').join(' / ')
    return [{ key, method, target, timings: ['initialDelaySeconds', 'periodSeconds', 'timeoutSeconds', 'successThreshold', 'failureThreshold'].filter(field => probe[field] !== undefined).map(field => ({ key: field, value: probe[field] })) }]
  })
}

export function kubeVolumeDetails(object: KubeObject): {name: string; kind: string; source: KubeObject; references: KubeReference[]}[] {
  const namespace = object.metadata?.namespace
  return (podSpec(object).volumes ?? []).map((volume: KubeObject) => {
    const kind = Object.keys(volume).find(key => key !== 'name') ?? ''
    const source = volume[kind] ?? {}
    const references: KubeReference[] = []
    if (source.claimName) references.push({ resource: 'persistentvolumeclaims', name: source.claimName, namespace })
    if (kind === 'configMap' && source.name) references.push({ resource: 'configmaps', name: source.name, namespace })
    if (kind === 'secret' && source.secretName) references.push({ resource: 'secrets', name: source.secretName, namespace })
    for (const item of source.sources ?? []) {
      if (item.configMap?.name) references.push({ resource: 'configmaps', name: item.configMap.name, namespace })
      if (item.secret?.name) references.push({ resource: 'secrets', name: item.secret.name, namespace })
    }
    return { name: volume.name, kind, source, references: [...new Map(references.map(reference => [`${reference.resource}/${reference.namespace}/${reference.name}`, reference])).values()] }
  })
}
