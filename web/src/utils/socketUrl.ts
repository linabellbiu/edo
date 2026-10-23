export function projectSocketUrl(path: string, parameters: Record<string, string | number | boolean> = {}): string {
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = new URL(`${protocol}//${location.host}${path}`)
  for (const [key, value] of Object.entries(parameters)) {
    if (!url.searchParams.has(key)) url.searchParams.set(key, String(value))
  }
  // WebSocket 不能设置项目请求头；沿用 HTTP 当前项目，显式范围和已有参数保持不变。
  const project = typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project')
  if (project && !url.searchParams.has('project_id')) url.searchParams.set('project_id', project)
  return url.toString()
}
