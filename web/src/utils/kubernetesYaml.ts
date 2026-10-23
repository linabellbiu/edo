import { LineCounter, parseAllDocuments, stringify, isScalar, visit } from 'yaml'
import type { KubeObject, KubeType } from '@/api/kubernetes'

export interface YamlIssue { code: string; line: number; column: number; from: number; to: number }
export interface YamlResult { object?: KubeObject; issues: YamlIssue[]; formatted?: string }
const limit = 2 * 1024 * 1024
const record = (value: unknown): value is KubeObject => !!value && typeof value === 'object' && !Array.isArray(value)
const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value))

export function readKubeYaml(source: string, target?: { type: KubeType; object: KubeObject }): YamlResult {
  const lines = new LineCounter()
  const issue = (code: string, from = 0, to = from + 1): YamlResult => {
    const position = lines.linePos(from)
    return { issues: [{ code, line: position.line || 1, column: position.col || 1, from, to: Math.min(source.length, to) }] }
  }
  if (new TextEncoder().encode(source).length > limit) return issue('size')
  try {
    const docs = parseAllDocuments(source, { lineCounter: lines, prettyErrors: false, strict: true, uniqueKeys: true, version: '1.2' })
    if (docs.length !== 1) return issue('document')
    const doc = docs[0]!
    const errors = [...doc.errors, ...doc.warnings]
    if (errors.length) return { issues: errors.slice(0, 20).map(error => {
      const from = error.pos[0], position = lines.linePos(from)
      return { code: error.code === 'DUPLICATE_KEY' ? 'duplicate' : error.code === 'TAG_RESOLVE_FAILED' ? 'tag' : 'syntax', line: position.line, column: position.col, from, to: Math.min(source.length, Math.max(from + 1, error.pos[1])) }
    }) }
    let invalidKey = false
    visit(doc, { Pair: (_key, pair) => { if (!isScalar(pair.key) || typeof pair.key.value !== 'string') invalidKey = true } })
    if (invalidKey) return issue('value')
    let object: unknown
    try { object = doc.toJS({ maxAliasCount: 50 }) } catch { return issue('alias') }
    let nodes = 0
    const visiting = new WeakSet<object>()
    const jsonValue = (value: unknown, depth: number): boolean => {
      if (++nodes > 100_000 || depth > 64) return false
      if (value === null || typeof value === 'string' || typeof value === 'boolean') return true
      if (typeof value === 'number') return Number.isFinite(value)
      if (typeof value !== 'object' || !value || visiting.has(value)) return false
      visiting.add(value)
      const valid = Object.values(value).every(item => jsonValue(item, depth + 1))
      visiting.delete(value)
      return valid
    }
    if (!jsonValue(object, 0)) return issue('value')
    if (!record(object)) return issue('document')
    if (new TextEncoder().encode(JSON.stringify(object)).length > limit) return issue('size')
    const at = (code: string, path: string[]) => {
      const node = doc.getIn(path, true) as { range?: [number, number, number] } | undefined
      return issue(code, node?.range?.[0] ?? 0, node?.range?.[1])
    }
    if (target) {
      const { type, object: original } = target
      if (!type.update || type.resource === 'secrets') return issue('readonly')
      for (const [key, expected] of Object.entries({ apiVersion: type.apiVersion, kind: type.kind })) if (object[key] !== expected) return at('identity', [key])
      if (!record(object.metadata)) return at('identity', ['metadata'])
      for (const key of ['name', 'namespace', 'uid', 'resourceVersion']) {
        if (object.metadata[key] !== original.metadata[key] && !(key === 'namespace' && !type.namespaced && object.metadata[key] === undefined)) return at('identity', ['metadata', key])
      }
      const fields = new Set(type.resource === 'configmaps' ? ['apiVersion', 'kind', 'metadata', 'data', 'binaryData', 'immutable'] : ['apiVersion', 'kind', 'metadata', 'spec'])
      for (const key of Object.keys(object)) if (!fields.has(key)) return at('readonly', [key])
      for (const key of Object.keys(object.metadata)) if (!['name', 'namespace', 'uid', 'resourceVersion', 'labels', 'annotations'].includes(key)) return at('readonly', ['metadata', key])
      if (type.resource === 'nodes' && object.spec) for (const key of Object.keys(object.spec)) if (!['unschedulable', 'taints'].includes(key)) return at('readonly', ['spec', key])
    }
    return { object, issues: [], formatted: doc.toString({ indent: 2, lineWidth: 100 }) }
  } catch { return issue('syntax') }
}

// 编辑范围与后端白名单一致，不把状态、所有者或节点身份写回集群。
export function editableKubeObject(type: KubeType, object: KubeObject): KubeObject {
  const metadata = Object.fromEntries(['name', 'namespace', 'uid', 'resourceVersion', 'labels', 'annotations'].filter(key => object.metadata[key] !== undefined).map(key => [key, object.metadata[key]]))
  const keys = type.resource === 'configmaps' ? ['data', 'binaryData', 'immutable'] : ['spec']
  const output = clone({ apiVersion: type.apiVersion, kind: type.kind, metadata, ...Object.fromEntries(keys.filter(key => object[key] !== undefined).map(key => [key, object[key]])) }) as KubeObject
  if (type.resource === 'nodes') output.spec = Object.fromEntries(['unschedulable', 'taints'].filter(key => object.spec?.[key] !== undefined).map(key => [key, clone(object.spec[key])]))
  return output
}

export function kubeYamlText(object: KubeObject, secret = false): string {
  const copy = clone(object)
  if (secret) { delete copy.data; delete copy.stringData; delete copy.keys; delete copy.metadata.annotations }
  return stringify(copy, { indent: 2, lineWidth: 100 })
}

// 仅比较编辑白名单中的 JSON 配置；删掉的键转换成 merge-patch 的 null。
export function kubeYamlPatch(type: KubeType, original: KubeObject, desired: KubeObject, current: KubeObject): KubeObject {
  const diff = (before: KubeObject, after: KubeObject): KubeObject => Object.fromEntries([...new Set([...Object.keys(before), ...Object.keys(after)])].flatMap(key => {
    if (!Object.hasOwn(after, key)) return [[key, null]]
    if (record(before[key]) && record(after[key])) { const nested = diff(before[key], after[key]); return Object.keys(nested).length ? [[key, nested]] : [] }
    return JSON.stringify(before[key]) === JSON.stringify(after[key]) ? [] : [[key, after[key]]]
  }))
  const patch = diff(editableKubeObject(type, original), desired)
  return { ...patch, apiVersion: type.apiVersion, kind: type.kind, metadata: { ...patch.metadata, name: original.metadata.name, ...(type.namespaced ? { namespace: original.metadata.namespace } : {}), uid: current.metadata.uid, resourceVersion: current.metadata.resourceVersion } }
}
