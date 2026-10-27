import type { KubeObject } from '@/api/kubernetes'

export interface IngressDraft { hostname: string; path: string; backend: string; ingressClass: string; ingressPort: string }
export interface NetworkPolicyDraft { selector: Record<string, string>; peer: Record<string, string>; policyPort: number | null; denyAll: boolean }
const copy = <T>(value: T): T => JSON.parse(JSON.stringify(value))
const onlyKeys = (value: KubeObject | undefined, keys: string[]) => Boolean(value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(key => keys.includes(key)))
const labels = (value: KubeObject | undefined): Record<string, string> => copy(value?.matchLabels ?? {})
const sameLabels = (left: Record<string, string>, right: Record<string, string>) => Object.keys(left).length === Object.keys(right).length && Object.entries(left).every(([key, value]) => Object.hasOwn(right, key) && right[key] === value)
const simpleSelector = (value: KubeObject | undefined) => onlyKeys(value, ['matchLabels', 'matchExpressions']) && (!value?.matchExpressions || Array.isArray(value.matchExpressions) && value.matchExpressions.length === 0)
const validNumberPort = (value: unknown): value is number => typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 65535

export function ingressBackendPort(value: string): { number: number } | { name: string } {
  const text = value.trim()
  if (/^\d+$/.test(text) && validNumberPort(Number(text))) return { number: Number(text) }
  if (text.length <= 15 && /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(text) && /[a-z]/.test(text) && !text.includes('--')) return { name: text }
  throw new Error('networkEditor.invalidIngressPort')
}

export function ingressForm(original?: KubeObject): { fields: IngressDraft; readonly: boolean } {
  if (!original) return { readonly: false, fields: { hostname: '', path: '/', backend: '', ingressClass: '', ingressPort: '80' } }
  const spec = original.spec ?? {}, rule = spec.rules?.[0], path = rule?.http?.paths?.[0], backend = path?.backend, service = backend?.service, port = service?.port
  const portText = validNumberPort(port?.number) ? String(port.number) : typeof port?.name === 'string' ? port.name : ''
  let supported = Boolean(rule && path && service?.name && !backend.resource && port && Object.keys(port).length === 1)
  try { ingressBackendPort(portText) } catch { supported = false }
  return { readonly: !supported, fields: { hostname: rule?.host ?? '', path: path?.path ?? '', backend: service?.name ?? '', ingressClass: spec.ingressClassName ?? '', ingressPort: portText } }
}

export function ingressFormPatch(original: KubeObject | undefined, fields: IngressDraft): KubeObject | undefined {
  const initial = ingressForm(original), changed = (key: keyof IngressDraft) => fields[key] !== initial.fields[key]
  if (original && !Object.keys(fields).some(key => changed(key as keyof IngressDraft))) return undefined
  if (initial.readonly) throw new Error('networkEditor.readonly')
  const routeChanged = !original || (['hostname', 'path', 'backend', 'ingressPort'] as const).some(changed)
  const patch: KubeObject = {}
  if (routeChanged) {
    if (!fields.backend.trim() || !fields.path.startsWith('/')) throw new Error('k8s.backendRequired')
    const port = ingressBackendPort(fields.ingressPort)
    const rules = copy(original?.spec?.rules ?? [{ http: { paths: [{}] } }]), rule = rules[0], path = rule.http.paths[0]
    if (!original || changed('hostname')) { if (fields.hostname.trim()) rule.host = fields.hostname.trim(); else delete rule.host }
    if (!original || changed('path')) path.path = fields.path
    if (!original) path.pathType = 'Prefix'
    if (!original || changed('backend')) path.backend = { ...path.backend, service: { ...path.backend?.service, name: fields.backend.trim() } }
    if (!original || changed('ingressPort')) path.backend.service.port = port
    patch.rules = rules
  }
  if (!original || changed('ingressClass')) {
    if (fields.ingressClass.trim()) patch.ingressClassName = fields.ingressClass.trim()
    else if (original?.spec?.ingressClassName !== undefined) patch.ingressClassName = null
  }
  return Object.keys(patch).length ? patch : undefined
}

export function networkPolicyForm(original?: KubeObject): { fields: NetworkPolicyDraft; readonly: boolean } {
  if (!original) return { readonly: false, fields: { selector: {}, peer: {}, policyPort: 80, denyAll: false } }
  const spec = original.spec ?? {}, rules = spec.ingress ?? [], rule = rules[0], peer = rule?.from?.[0], port = rule?.ports?.[0]
  const denyAll = Array.isArray(rules) && rules.length === 0
  const ingressOnly = !Object.hasOwn(spec, 'egress') && (spec.policyTypes === undefined || Array.isArray(spec.policyTypes) && spec.policyTypes.length === 1 && spec.policyTypes[0] === 'Ingress')
  const simpleRule = rules.length === 1 && onlyKeys(rule, ['from', 'ports']) && Array.isArray(rule.from) && rule.from.length === 1 && onlyKeys(peer, ['podSelector']) && simpleSelector(peer.podSelector)
    && Array.isArray(rule.ports) && rule.ports.length === 1 && onlyKeys(port, ['port', 'protocol']) && (!port.protocol || port.protocol === 'TCP') && validNumberPort(port.port)
  // 现有简易表单只表达同命名空间、单来源和单个 TCP 端口；其余结构只读，不猜测或折叠规则。
  const supported = ingressOnly && simpleSelector(spec.podSelector) && Array.isArray(rules) && (denyAll || simpleRule)
  return { readonly: !supported, fields: { selector: labels(spec.podSelector), peer: labels(peer?.podSelector), policyPort: validNumberPort(port?.port) ? port.port : null, denyAll } }
}

export function networkPolicyFormPatch(original: KubeObject | undefined, fields: NetworkPolicyDraft): KubeObject | undefined {
  const initial = networkPolicyForm(original), before = initial.fields
  const selectorChanged = !sameLabels(before.selector, fields.selector), peerChanged = !sameLabels(before.peer, fields.peer)
  const changed = selectorChanged || peerChanged || before.policyPort !== fields.policyPort || before.denyAll !== fields.denyAll
  if (original && !changed) return undefined
  if (initial.readonly) throw new Error('networkEditor.readonly')
  const patch: KubeObject = {}
  if (!original || selectorChanged) {
    // Merge Patch 的对象键需显式置 null 才能删除；只改标签条件，不覆盖未改的选择器字段。
    const entries = [...new Set([...Object.keys(before.selector), ...Object.keys(fields.selector)])].map(key => [key, Object.hasOwn(fields.selector, key) ? fields.selector[key] : null])
    patch.podSelector = { matchLabels: original ? Object.fromEntries(entries) : copy(fields.selector) }
  }
  if (!original) patch.policyTypes = ['Ingress']
  if (!original || before.denyAll !== fields.denyAll || !fields.denyAll && (peerChanged || before.policyPort !== fields.policyPort)) {
    if (fields.denyAll) patch.ingress = []
    else {
      if (!validNumberPort(fields.policyPort)) throw new Error('k8s.invalidPort')
      const rule = copy(original?.spec?.ingress?.[0] ?? { from: [{ podSelector: {} }], ports: [{ protocol: 'TCP' }] })
      rule.from[0].podSelector.matchLabels = copy(fields.peer)
      rule.ports[0].port = fields.policyPort
      patch.ingress = [rule]
    }
  }
  return Object.keys(patch).length ? patch : undefined
}
