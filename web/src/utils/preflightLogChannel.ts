const prefix = 'edo.preflight-log.'
export const preflightLogByteLimit = 64 * 1024
export const preflightLogExpiredMessage = '原窗口已关闭或日志链接已失效，请回到检测报告重新打开。'

function randomID() {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return [...bytes].map(value => value.toString(16).padStart(2, '0')).join('')
}
export function preflightLogText(value: string) {
  const bytes = new TextEncoder().encode(value)
  let start = Math.max(0, bytes.length - preflightLogByteLimit)
  while (start < bytes.length && (bytes[start]! & 0xc0) === 0x80) start++
  return new TextDecoder().decode(bytes.subarray(start)).replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '').replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, '')
}

// 临时检测日志只在原页面内存中保留，通过同源频道按当前身份提供快照。
export function createPreflightLogSource(text: string, identity: string, allowed: () => boolean) {
  const channel = randomID(), connection = new BroadcastChannel(prefix + channel)
  const snapshot = preflightLogText(text)
  let closed = false
  connection.onmessage = event => {
    const value: unknown = event.data
    if (closed || !allowed() || !value || typeof value !== 'object') return
    const request = value as Record<string, unknown>
    if (request.type !== 'request' || request.identity !== identity || typeof request.requestID !== 'string' || !/^[a-f0-9]{32}$/.test(request.requestID)) return
    connection.postMessage({ type: 'snapshot', requestID: request.requestID, identity, text: snapshot })
  }
  return { channel, close() { closed = true; connection.onmessage = null; connection.close() } }
}

export function requestPreflightLog(channel: string, identity: string, signal: AbortSignal, timeout = 5000): Promise<string> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) { reject(new DOMException('已取消读取', 'AbortError')); return }
    if (!/^[a-f0-9]{32}$/.test(channel)) { reject(new Error(preflightLogExpiredMessage)); return }
    if (typeof BroadcastChannel === 'undefined') { reject(new Error('当前浏览器不支持独立查看临时检测日志，请在原窗口查看。')); return }
    const requestID = randomID(), connection = new BroadcastChannel(prefix + channel)
    let settled = false
    const finish = (error?: Error, text = '') => {
      if (settled) return
      settled = true; clearTimeout(timer); signal.removeEventListener('abort', abort)
      connection.onmessage = null; connection.close()
      if (error) reject(error)
      else resolve(text)
    }
    const abort = () => finish(new DOMException('已取消读取', 'AbortError'))
    const timer = setTimeout(() => finish(new Error(preflightLogExpiredMessage)), timeout)
    signal.addEventListener('abort', abort, { once: true })
    connection.onmessage = event => {
      const value: unknown = event.data
      if (!value || typeof value !== 'object') return
      const response = value as Record<string, unknown>
      if (response.type !== 'snapshot' || response.identity !== identity || response.requestID !== requestID || typeof response.text !== 'string') return
      if (new TextEncoder().encode(response.text).length > preflightLogByteLimit) { finish(new Error('检测日志超过显示限制，请在原窗口查看。')); return }
      finish(undefined, preflightLogText(response.text))
    }
    connection.postMessage({ type: 'request', requestID, identity })
  })
}
