import { projectSocketUrl } from './socketUrl'

const snapshotLimit = 8 * 1024 * 1024
export class PodLogDownloadLimit extends Error {}

// 下载复用只读日志协议，只有服务端确认读完后才生成文件。
export function readPodLogSnapshot(path: string, cancellation: AbortSignal): Promise<Blob> {
  const signal = AbortSignal.any([cancellation, AbortSignal.timeout(60_000)])
  return new Promise((resolve, reject) => {
    signal.throwIfAborted()
    const socket = new WebSocket(projectSocketUrl(path, { tail: 5000, follow: false, timestamps: true }), 'edo-container-logs-v1')
    const chunks: ArrayBuffer[] = []
    let bytes = 0, ready = false
    socket.binaryType = 'arraybuffer'
    const abort = () => fail(signal.reason)
    const close = () => {
      signal.removeEventListener('abort', abort)
      socket.onmessage = socket.onerror = socket.onclose = null
      if (socket.readyState < WebSocket.CLOSING) socket.close()
    }
    function fail(error = new Error('日志快照未完成')) {
      close()
      reject(error)
    }
    signal.addEventListener('abort', abort, { once: true })
    socket.onerror = () => fail()
    socket.onclose = () => fail()
    socket.onmessage = ({ data }) => {
      if (data instanceof ArrayBuffer && ready) {
        bytes += data.byteLength
        if (bytes > snapshotLimit) fail(new PodLogDownloadLimit())
        else chunks.push(data)
        return
      }
      try {
        const { type } = JSON.parse(String(data))
        if (type === 'ready') ready = true
        if (type === 'error' || type === 'complete' && !ready) fail()
        if (type === 'complete' && ready) {
          close()
          resolve(new Blob(chunks, { type: 'text/plain;charset=utf-8' }))
        }
      } catch {
        fail()
      }
    }
  })
}
