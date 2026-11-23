import type { KubeObject } from '@/api/kubernetes'

export const limitTypes = ['Container', 'Pod', 'PersistentVolumeClaim'] as const
export const limitFields = ['min', 'max', 'defaultRequest', 'default', 'maxLimitRequestRatio'] as const
export type LimitField = typeof limitFields[number]
export type LimitAmounts = Partial<Record<LimitField, string>>
export interface LimitResource extends LimitAmounts { name: string }
export interface LimitRule { type: string; resources: LimitResource[] }
export interface QuotaLimit { name: string; amount: string }
export type NamespaceResourceDraft =
  | { kind: 'LimitRange'; rules: LimitRule[] }
  | { kind: 'ResourceQuota'; limits: QuotaLimit[] }
  | { kind: 'ServiceAccount'; automount: 'default' | 'enabled' | 'disabled'; imagePullSecrets: string }

export function limitRules(object: KubeObject): LimitRule[] {
  return ((object.spec?.limits ?? []) as KubeObject[]).map(rule => {
    const names = new Set(limitFields.flatMap(field => Object.keys(rule[field] ?? {})))
    return {
      type: String(rule.type ?? ''),
      resources: [...names].sort().map(name => {
        const entry: LimitResource = { name }
        for (const field of limitFields) {
          if (rule[field]?.[name] !== undefined) entry[field] = String(rule[field][name])
        }
        return entry
      }),
    }
  })
}

export function quotaRows(object: KubeObject) {
  const configured = object.spec?.hard ?? {}, effective = object.status?.hard ?? {}, used = object.status?.used ?? {}
  const names = new Set([...Object.keys(configured), ...Object.keys(effective), ...Object.keys(used)])
  return [...names].sort().map(name => ({
    name,
    configured: configured[name] as string | undefined,
    effective: effective[name] as string | undefined,
    used: used[name] as string | undefined,
  }))
}

export function serviceAccountAutomount(object: KubeObject) {
  return object.automountServiceAccountToken === true ? 'enabled'
    : object.automountServiceAccountToken === false ? 'disabled' : 'default'
}

export function readNamespaceResourceDraft(kind: string, object: KubeObject): NamespaceResourceDraft | undefined {
  switch (kind) {
    case 'LimitRange':
      return { kind, rules: object.spec ? limitRules(object) : [{ type: 'Container', resources: [{ name: '' }] }] }
    case 'ResourceQuota': {
      const limits = Object.entries(object.spec?.hard ?? {}).map(([name, amount]) => ({ name, amount: String(amount) }))
      return { kind, limits: limits.length ? limits : [{ name: '', amount: '' }] }
    }
    case 'ServiceAccount':
      return {
        kind,
        automount: serviceAccountAutomount(object),
        imagePullSecrets: (object.imagePullSecrets ?? []).map((item: KubeObject) => item.name).join(', '),
      }
  }
}

function uniqueResourceNames(rows: Array<{name: string}>) {
  const names = rows.map(row => row.name.trim())
  return names.every(name => name.length > 0 && !/\s/.test(name)) && new Set(names).size === rows.length
}

// 数量保留为原始字符串。合法单位、类型和准入约束由 Kubernetes dry-run 校验。
export function namespaceResourcePatch(draft: NamespaceResourceDraft, original?: KubeObject): KubeObject {
  if (draft.kind === 'ResourceQuota') {
    if (!draft.limits.length || !uniqueResourceNames(draft.limits) || draft.limits.some(row => !row.amount.trim())) {
      throw new Error('k8s.invalidQuotaLimits')
    }
    const hard: Record<string, string | null> = Object.fromEntries(draft.limits.map(row => [row.name.trim(), row.amount.trim()]))
    for (const name of Object.keys(original?.spec?.hard ?? {})) {
      if (!Object.hasOwn(hard, name)) Object.defineProperty(hard, name, { value: null, enumerable: true })
    }
    return { spec: { hard } }
  }
  if (draft.kind === 'LimitRange') {
    if (!draft.rules.length || new Set(draft.rules.map(rule => rule.type)).size !== draft.rules.length) {
      throw new Error('k8s.invalidLimitRules')
    }
    const limits = draft.rules.map(rule => {
      if (!rule.type || !rule.resources.length || !uniqueResourceNames(rule.resources) ||
        rule.resources.some(row => !limitFields.some(field => row[field]?.trim()))) {
        throw new Error('k8s.invalidLimitRules')
      }
      const amounts = limitFields.flatMap(field => {
        const values = rule.resources.flatMap(row => row[field]?.trim() ? [[row.name.trim(), row[field]!.trim()]] : [])
        return values.length ? [[field, Object.fromEntries(values)]] : []
      })
      return { type: rule.type, ...Object.fromEntries(amounts) }
    })
    return { spec: { limits } }
  }
  const names = draft.imagePullSecrets.split(',').map(name => name.trim()).filter(Boolean)
  if (names.some(name => name.length > 253 || !/^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*$/.test(name))) {
    throw new Error('k8s.invalidImagePullSecretNames')
  }
  return {
    ...(draft.automount !== 'default' ? {automountServiceAccountToken: draft.automount === 'enabled'}
      : typeof original?.automountServiceAccountToken === 'boolean' ? {automountServiceAccountToken: null} : {}),
    ...(names.length || original?.imagePullSecrets?.length ? {imagePullSecrets: [...new Set(names)].map(name => ({name}))} : {}),
  }
}
