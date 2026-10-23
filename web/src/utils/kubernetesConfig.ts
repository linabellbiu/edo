import { LineCounter, parseAllDocuments } from 'yaml'
import type { YamlIssue } from './kubernetesYaml'

export function configFileType(key: string): 'yaml' | 'json' | 'text' {
  return /\.ya?ml$/i.test(key) ? 'yaml' : /\.json$/i.test(key) || key === '.dockerconfigjson' ? 'json' : 'text'
}

// 配置文件可以是列表、标量或多文档，不套用 Kubernetes 资源对象的字段限制。
export function inspectConfigFile(key: string, source: string): { issues: YamlIssue[]; formatted?: string } {
  const type = configFileType(key), lines = new LineCounter()
  const failure = (code: string, from = 0, to = from + 1) => {
    const position = lines.linePos(from)
    return { issues: [{ code, line: position.line || 1, column: position.col || 1, from, to: Math.min(source.length, to) }] }
  }
  if (new TextEncoder().encode(source).length > 2 * 1024 * 1024) return failure('size')
  if (type === 'text') return { issues: [] }
  try {
    const docs = parseAllDocuments(source, { lineCounter: lines, prettyErrors: false, strict: true, uniqueKeys: true, version: '1.2' })
    const errors = docs.flatMap(doc => [...doc.errors, ...doc.warnings])
    if (errors.length) return { issues: errors.slice(0, 20).map(error => {
      const from = error.pos[0], position = lines.linePos(from)
      return { code: error.code === 'DUPLICATE_KEY' ? 'duplicate' : error.code === 'TAG_RESOLVE_FAILED' ? 'tag' : type === 'json' ? 'json' : 'syntax', line: position.line, column: position.col, from, to: Math.min(source.length, Math.max(from + 1, error.pos[1])) }
    }) }
    let nodes = 0
    const visiting = new WeakSet<object>()
    const safe = (value: unknown, depth = 0): boolean => {
      if (++nodes > 100_000 || depth > 64) return false
      if (value === null || typeof value === 'string' || typeof value === 'boolean') return true
      if (typeof value === 'number') return Number.isFinite(value)
      if (!value || typeof value !== 'object' || visiting.has(value)) return false
      visiting.add(value)
      const valid = Object.values(value).every(item => safe(item, depth + 1))
      visiting.delete(value)
      return valid
    }
    for (const doc of docs) {
      let value: unknown
      try { value = doc.toJS({ maxAliasCount: 50 }) } catch { return failure('alias') }
      if (!safe(value)) return failure('value')
    }
    if (type === 'json') {
      try { return { issues: [], formatted: JSON.stringify(JSON.parse(source), null, 2) + '\n' } }
      catch (error) {
        const position = error instanceof SyntaxError ? /position (\d+)/.exec(error.message) : null
        return failure('json', position ? Math.min(source.length, Number(position[1])) : 0)
      }
    }
    return { issues: [], formatted: docs.map(doc => doc.toString({ indent: 2, lineWidth: 100 })).join('') }
  } catch { return failure('syntax') }
}
