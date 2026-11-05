export type DeliveryVariableKind = 'env' | 'file' | 'env_text' | 'env_file'

export function envTextError(source: string): string {
  if (new TextEncoder().encode(source).length > 65536 || source.includes('\0')) return 'env 文本过长或包含无效字符'
  const keys = new Set<string>()
  const lines = source.replace(/\r\n/g, '\n').split('\n')
  for (const [index, line] of lines.entries()) {
    if (!line.trim() || line.trimStart().startsWith('#')) continue
    const separator = line.indexOf('='), key = line.slice(0, separator).trim()
    if (separator < 0 || !/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(key) || key.startsWith('EDO_') || line.includes('\r')) return `env 文本第 ${index + 1} 行无效，请使用 KEY=value，且不能覆盖系统变量`
    if (keys.has(key)) return `env 文本第 ${index + 1} 行的变量名称重复`
    keys.add(key)
    if (keys.size > 100) return 'env 文本最多配置 100 个变量'
  }
  return keys.size ? '' : '请至少输入一行 KEY=value'
}

export const variableKindLabel = (kind: DeliveryVariableKind) => ({ env: '逐条变量', file: '构建文件', env_text: 'env 文本 · environment', env_file: 'env 文本 · env_file' })[kind]
