import { createHostsPreviewAdapter, createHostsPreviewData } from './hosts-preview-data.mjs'

export const terminalPreviewPermissions = ['host.terminal.open', 'terminal.open', 'terminal.file.read', 'terminal.file.upload', 'terminal.file.edit']
const bytes = value => new TextEncoder().encode(value)
const error = (config, status, message) => Object.assign(new Error(message), { isAxiosError: true, config, response: { status, data: { message } } })
const sha256 = async value => [...new Uint8Array(await crypto.subtle.digest('SHA-256', value))].map(value => value.toString(16).padStart(2, '0')).join('')

export function createTerminalPreviewAdapter(scenario = '', requests = []) {
  const hostData = createHostsPreviewData(scenario === 'readonly' ? 'readonly' : 'ready')
  if (scenario !== 'readonly') hostData.permissions.push(...terminalPreviewPermissions)
  const hosts = createHostsPreviewAdapter(hostData, requests)
  const content = new Map([
    ['/etc/app.conf', bytes('APP_NAME=EDO Preview\nHTTP_PORT=8080\nLOG_LEVEL=info\n')],
    ['/var/log/service.log', bytes('2026-10-28 10:00:00 preview service ready\n')],
    ['/README.txt', bytes('EDO 本地终端交互预览\n文件仅保存在当前浏览器内存，不连接真实服务器。\n')],
  ])
  const entry = path => ({ name: path.split('/').at(-1), path, kind: 'file', size: content.get(path)?.byteLength ?? 0, mode: 0o644, modified_at: '2026-10-28T02:00:00Z' })
  const directories = ['/', '/etc', '/var', '/var/log']
  const adapter = async config => {
    config.signal?.throwIfAborted()
    const url = new URL(config.url, 'http://preview.invalid')
    if (!url.pathname.startsWith('/terminals/')) return hosts(config)
    if (url.origin !== 'http://preview.invalid' || !/^\/terminals\/(?:hosts\/preview-(?:local|edge)|docker\/preview-docker-local\/containers\/preview-container-web|kubernetes\/preview-k3s\/namespaces\/preview\/pods\/preview-web\/containers\/web)\/files(?:\/(?:stat|content|download|upload))?$/.test(url.pathname)) throw error(config, 404, '本地预览未登记此文件目标')
    const method = String(config.method || 'get').toLowerCase(), path = config.params?.path ?? url.searchParams.get('path') ?? '/'
    requests.push({ method, path: url.pathname, file: path })
    if (scenario === 'readonly' || scenario === 'no-files') throw error(config, 403, '没有文件操作权限')
    const response = data => ({ data, status: 200, statusText: 'OK', headers: {}, config })
    if (method === 'get' && url.pathname.endsWith('/files')) {
      if (!directories.includes(path)) throw error(config, 404, '目录不存在')
      const prefix = path === '/' ? '/' : `${path}/`, children = directories.filter(value => value !== path && value.startsWith(prefix) && !value.slice(prefix.length).includes('/')).map(value => ({ name: value.split('/').at(-1), path: value, kind: 'directory', size: 0 }))
      return response({ directory: { path, entries: [...children, ...[...content.keys()].filter(value => value.startsWith(prefix) && !value.slice(prefix.length).includes('/')).map(entry)], truncated: false } })
    }
    if (method === 'get') {
      const value = content.get(path)
      if (!value) throw error(config, 404, '文件不存在')
      if (url.pathname.endsWith('/stat')) return response({ file: { ...entry(path), etag: await sha256(value) } })
      if (url.pathname.endsWith('/content')) return response({ file: { ...entry(path), etag: await sha256(value), content: new TextDecoder().decode(value) } })
      if (url.pathname.endsWith('/download')) return response(new Blob([value]))
    }
    if (method === 'put' && url.pathname.endsWith('/content')) {
      const data = typeof config.data === 'string' ? JSON.parse(config.data) : config.data, old = content.get(data.path)
      if (!old || await sha256(old) !== data.etag) throw error(config, 409, '文件已变化，请重新读取后再操作')
      const value = bytes(data.content)
      if (value.byteLength > 1024 ** 2) throw error(config, 413, '文件超过当前操作的大小限制')
      content.set(data.path, value); return response({ file: { ...entry(data.path), etag: await sha256(value) } })
    }
    if (method === 'post' && url.pathname.endsWith('/upload')) {
      const old = content.get(path), etag = config.headers?.get?.('If-Match') ?? config.headers?.['If-Match']
      if (old && !etag) throw error(config, 409, '同名文件已存在，请确认后再覆盖')
      if (old && await sha256(old) !== etag || !old && etag) throw error(config, 409, '文件已变化，请重新读取后再操作')
      const value = new Uint8Array(await config.data.arrayBuffer())
      if (value.byteLength > 64 * 1024 ** 2) throw error(config, 413, '文件超过当前操作的大小限制')
      content.set(path, value); return response({ file: { ...entry(path), etag: await sha256(value) } })
    }
    throw error(config, 405, '本地预览未登记此文件操作')
  }
  return { adapter, hostData, content, requests }
}

export function installTerminalPreviewSocket(scope = window) {
  const connections = []
  scope.WebSocket = class PreviewSocket extends EventTarget {
    static CONNECTING=0; static OPEN=1; static CLOSING=2; static CLOSED=3
    readyState=0; binaryType='arraybuffer'
    constructor(path, protocol) {
      super(); this.url=path;this.protocol=protocol;connections.push(this)
      const url=new URL(path,location.href)
      queueMicrotask(()=>{
        if(this.readyState!==0)return
        if(url.origin!==location.origin.replace(/^http/,'ws') || !url.pathname.startsWith('/api/v1/terminals/') || protocol!=='edo-terminal-v1'){this.close(1008,'本地预览不连接真实目标');return}
        this.readyState=1;this.onopen?.(new Event('open'));this.onmessage?.({data:JSON.stringify({type:'ready'})})
        this.onmessage?.({data:bytes('\x1b[36mEDO 本地终端预览\x1b[0m\r\n\x1b[90m模拟会话，不执行真实命令\x1b[0m\r\npreview@edo:~$ ').buffer})
      })
    }
    send(value){if(this.readyState!==1||typeof value==='string')return;const input=new TextDecoder().decode(value);this.onmessage?.({data:bytes(input.replace(/\r/g,'\r\npreview@edo:~$ ')).buffer})}
    close(code=1000,reason=''){this.readyState=3;this.onclose?.(new CloseEvent('close',{code,reason}))}
  }
  return connections
}
