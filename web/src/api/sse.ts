// 分帧只处理 SSE 协议；JSON 和业务事件校验留给调用者。
export function createSSEParser(onEvent: (event: string, data: string) => void) {
  let buffer = '', event = '', data: string[] = [], size = 0
  const dispatch = () => {
    const name = event || 'message', value = data.join('\n'), present = data.length > 0
    event = ''; data = []; size = 0
    if (present) onEvent(name, value)
  }
  const line = (value: string) => {
    if (!value) { dispatch(); return }
    if (value.startsWith(':')) return
    const colon = value.indexOf(':'), field = colon < 0 ? value : value.slice(0, colon)
    const content = colon < 0 ? '' : value.slice(colon + 1).replace(/^ /, '')
    if (field === 'event') event = content
    if (field === 'data') { size += content.length; if (size > 1_048_576) throw new Error('SSE event too large'); data.push(content) }
  }
  const drain = (final = false) => {
    while (true) {
      const end = buffer.search(/[\r\n]/)
      if (end < 0 || !final && end === buffer.length - 1 && buffer[end] === '\r') break
      const length = buffer[end] === '\r' && buffer[end + 1] === '\n' ? 2 : 1
      const value = buffer.slice(0, end); buffer = buffer.slice(end + length); line(value)
    }
    if (buffer.length > 1_048_576) throw new Error('SSE line too large')
  }
  return {
    push(chunk: string) { buffer += chunk; drain() },
    finish() { drain(true); if (buffer) line(buffer); buffer = ''; dispatch() },
  }
}
