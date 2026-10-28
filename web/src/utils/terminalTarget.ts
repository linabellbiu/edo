export interface TerminalFileTarget { base: string; parameters: Record<string, string> }

export const terminalTextLimit = 1024 * 1024
export const terminalTransferLimit = 64 * 1024 * 1024

export function terminalFileTarget(path: string): TerminalFileTarget | undefined {
  try {
    const url = new URL(path, 'http://edo.invalid')
    if (url.origin !== 'http://edo.invalid' || !/^\/api\/v1\/terminals\/(?:hosts\/[^/]+|docker\/[^/]+\/containers\/[^/]+|kubernetes\/[^/]+\/namespaces\/[^/]+\/pods\/[^/]+\/containers\/[^/]+)\/ws$/.test(url.pathname)) return
    const parameters: Record<string, string> = {}
    for (const name of ['project_id', 'pod_uid']) if (url.searchParams.has(name)) parameters[name] = url.searchParams.get(name)!
    return { base: url.pathname.replace(/^\/api\/v1/, '').replace(/\/ws$/, '/files'), parameters }
  } catch { return }
}

export function terminalParent(path: string) {
  const parts = path.split('/').filter(Boolean)
  parts.pop()
  return `/${parts.join('/')}`
}

export function terminalChild(directory: string, name: string) {
  if (!name || name === '.' || name === '..' || /[\/\\\u0000]/.test(name)) return
  return `${directory === '/' ? '' : directory.replace(/\/$/, '')}/${name}`
}

export function terminalFileSize(size: number) {
  if (!Number.isFinite(size) || size < 0) return '—'
  if (size === 0) return '0 B'
  if (size < 1024) return `${size} B`
  const unit = size >= 1024 ** 3 ? 'GiB' : size >= 1024 ** 2 ? 'MiB' : 'KiB'
  const divisor = unit === 'GiB' ? 1024 ** 3 : unit === 'MiB' ? 1024 ** 2 : 1024
  return `${Number((size / divisor).toFixed(2))} ${unit}`
}
