import { useEffect, useRef } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

interface Props {
  title: string
  path: string
  onClose: () => void
}

export default function TerminalModal({ title, path, onClose }: Props) {
  const containerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    const terminal = new Terminal({
      cursorBlink: true,
      convertEol: false,
      fontFamily: 'SFMono-Regular, Consolas, monospace',
      fontSize: 13,
      theme: { background: '#050b12', foreground: '#dbe7f4', cursor: '#55d6be', selectionBackground: '#234b55' },
    })
    const fitAddon = new FitAddon()
    terminal.loadAddon(fitAddon)
    terminal.open(container)
    fitAddon.fit()
    terminal.writeln('\x1b[36m正在建立 ZRT WebSocket 终端…\x1b[0m')

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const separator = path.includes('?') ? '&' : '?'
    const socket = new WebSocket(
      `${protocol}//${window.location.host}${path}${separator}columns=${terminal.cols}&rows=${terminal.rows}`,
      'zrt-terminal-v1',
    )
    socket.binaryType = 'arraybuffer'
    const encoder = new TextEncoder()
    const decoder = new TextDecoder()

    socket.addEventListener('message', (event) => {
      if (event.data instanceof ArrayBuffer) {
        terminal.write(decoder.decode(event.data, { stream: true }))
        return
      }
      try {
        const message = JSON.parse(String(event.data)) as { type?: string; message?: string }
        if (message.type === 'ready') terminal.writeln('\x1b[32m终端已连接。\x1b[0m')
        if (message.type === 'error' || message.type === 'exit') terminal.writeln(`\r\n\x1b[33m${message.message || '终端会话已结束'}\x1b[0m`)
      } catch {
        terminal.writeln('\r\n\x1b[31m收到无法识别的终端事件。\x1b[0m')
      }
    })
    socket.addEventListener('close', () => terminal.writeln('\r\n\x1b[90m连接已关闭。\x1b[0m'))
    socket.addEventListener('error', () => terminal.writeln('\r\n\x1b[31mWebSocket 终端连接失败。\x1b[0m'))
    const input = terminal.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(encoder.encode(data))
    })
    const resize = terminal.onResize(({ cols, rows }) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: 'resize', columns: cols, rows }))
    })
    const observer = new ResizeObserver(() => {
      try { fitAddon.fit() } catch { /* 关闭过程中可能已释放 DOM */ }
    })
    observer.observe(container)

    return () => {
      observer.disconnect()
      input.dispose()
      resize.dispose()
      if (socket.readyState === WebSocket.OPEN) socket.close(1000, '用户关闭终端')
      terminal.dispose()
    }
  }, [path])

  return (
    <div className="modal-backdrop" role="presentation">
      <section className="terminal-modal" role="dialog" aria-modal="true" aria-label={title}>
        <header><div><span className="section-label">WEBSOCKET TERMINAL</span><h3>{title}</h3></div><button type="button" onClick={onClose}>关闭</button></header>
        <div className="terminal-canvas" ref={containerRef} />
      </section>
    </div>
  )
}
