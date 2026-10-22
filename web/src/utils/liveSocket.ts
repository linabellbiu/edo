export type LiveSocketState = 'connecting' | 'connected' | 'disconnected' | 'stopped'

export function openLiveSocket(options: {
  path: string
  protocol: string
  onEvent: (data: unknown) => boolean | void
  onState: (state: LiveSocketState) => void
  onDenied?: () => void
}) {
  let stopped = false
  let socket: WebSocket | undefined
  let retry: ReturnType<typeof setTimeout> | undefined
  let watchdog: ReturnType<typeof setTimeout> | undefined
  let attempt = 0

  function releaseSocket(abortHandshake = false) {
    const previous = socket
    socket = undefined
    if (!previous) return
    previous.onmessage = previous.onclose = previous.onerror = null
    if (previous.readyState === WebSocket.CONNECTING) {
      // 正常离开时给 Upgrade 留出关闭机会，超时的握手必须回收。
      if (abortHandshake) {
        previous.onopen = null
        previous.close()
      } else {
        const retirement = setTimeout(() => {
          previous.onopen = previous.onclose = null
          if (previous.readyState === WebSocket.CONNECTING) previous.close()
        }, 5000)
        previous.onclose = () => clearTimeout(retirement)
        previous.onopen = () => {
          clearTimeout(retirement)
          previous.onopen = previous.onclose = null
          previous.close(1000, '页面已离开')
        }
      }
    } else {
      previous.onopen = null
      if (previous.readyState === WebSocket.OPEN) previous.close(1000, '会话结束')
    }
  }

  function stop() {
    if (stopped) return
    stopped = true
    clearTimeout(retry)
    clearTimeout(watchdog)
    releaseSocket()
    options.onState('stopped')
  }

  function reconnect() {
    if (stopped) return
    clearTimeout(watchdog)
    releaseSocket(true)
    options.onState('disconnected')
    const delay = Math.min(30_000, 1000 * 2 ** Math.min(attempt++, 5)) + Math.random() * 500
    clearTimeout(retry)
    retry = setTimeout(connect, delay)
  }

  function watchConnection(delay: number) {
    clearTimeout(watchdog)
    watchdog = setTimeout(reconnect, delay)
  }

  function connect() {
    if (stopped) return
    options.onState('connecting')
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const project = typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project')
    const path = options.path + (project ? (options.path.includes('?') ? '&' : '?') + 'project_id=' + encodeURIComponent(project) : '')
    const current = new WebSocket(`${protocol}//${location.host}${path}`, options.protocol)
    socket = current
    watchConnection(10_000)
    current.onopen = () => {
      if (stopped || socket !== current) { current.close(1000); return }
      options.onState('connected')
      watchConnection(45_000)
    }
    current.onmessage = event => {
      if (stopped || socket !== current) return
      try {
        const data: unknown = JSON.parse(String(event.data))
        attempt = 0
        watchConnection(45_000)
        if (options.onEvent(data) === false) stop()
      } catch { reconnect() }
    }
    current.onerror = () => { /* close 或握手超时统一安排重连。 */ }
    current.onclose = event => {
      if (stopped || socket !== current) return
      if (event.code === 1008) { stop(); options.onDenied?.(); return }
      reconnect()
    }
  }

  connect()
  return stop
}
