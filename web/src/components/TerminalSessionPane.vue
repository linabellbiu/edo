<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { useAuthStore } from '@/stores/auth'
import { terminalActorContext, type HostTerminalState } from '@/stores/hostTerminals'
import { projectSocketUrl } from '@/utils/socketUrl'

const props = withDefaults(defineProps<{ path: string; visible?: boolean; permission?: string }>(), { visible: true, permission: 'terminal.open' })
const emit = defineEmits<{ state: [value: HostTerminalState] }>()
const auth = useAuthStore(), container = ref<HTMLDivElement>()
const state = ref<HostTerminalState>('connecting')
const stateText = computed(() => ({ connecting: '正在连接终端…', connected: '已连接', closed: '连接已结束', error: '连接失败' }[state.value]))
function setState(value: HostTerminalState) { state.value = value; emit('state', value) }
let terminal: Terminal | undefined, fit: FitAddon | undefined, dispose: (() => void) | undefined
let generation = 0, disposed = false, connectedContext = ''
const context = () => terminalActorContext(auth.user, typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? '')
const allowed = () => Boolean(auth.user) && auth.canAny([props.permission])
function fitVisible() {
  if (!props.visible || !container.value || !container.value.getBoundingClientRect().width || !container.value.getBoundingClientRect().height) return
  try { fit?.fit() } catch { /* 隐藏与关闭期间不发送无效终端尺寸。 */ }
}
async function focus() { await nextTick(); if (props.visible && !disposed) { fitVisible(); terminal?.focus() } }
async function connect() {
  dispose?.(); dispose = undefined
  const revision = ++generation, identity = context(), target = props.path
  await nextTick()
  if (disposed || revision !== generation || !container.value || !target || target !== props.path || !allowed() || identity !== context()) return
  connectedContext = identity
  setState('connecting')
  const view = new Terminal({ cursorBlink: true, scrollback: 1000, fontFamily: 'SFMono-Regular,Consolas,monospace', fontSize: 13, theme: { background: '#071018', foreground: '#dbe7f4', cursor: '#77d7c3', selectionBackground: '#254156', brightBlack: '#93a5b7' } })
  const addon = new FitAddon()
  terminal = view; fit = addon; view.loadAddon(addon); view.open(container.value); fitVisible()
  const socket = new WebSocket(projectSocketUrl(target, { columns: Math.min(500, Math.max(20, view.cols)), rows: Math.min(200, Math.max(5, view.rows)) }), 'edo-terminal-v1')
  const encoder = new TextEncoder(), decoder = new TextDecoder()
  let closed = false, ended = false, ready = false
  const current = () => !closed && !disposed && revision === generation && identity === context() && allowed()
  socket.binaryType = 'arraybuffer'
  socket.onmessage = event => {
    if (!current()) return
    if (event.data instanceof ArrayBuffer) { view.write(decoder.decode(event.data, { stream: true })); return }
    try {
      const value = JSON.parse(String(event.data)) as { type?: string; message?: string }
      if (value.type === 'ready' && !ready && !ended) {
        ready = true
        setState('connected'); if (props.visible) void focus()
      }
      if (value.type === 'error' || value.type === 'exit') { ended = true; setState(value.type === 'error' ? 'error' : 'closed'); view.writeln(`\r\n\x1b[33m${value.message || '终端会话已结束'}\x1b[0m`) }
    } catch { setState('error'); view.writeln('\r\n\x1b[31m终端事件无法读取。\x1b[0m') }
  }
  socket.onclose = () => { if (current() && !ended) { setState('closed'); view.writeln('\r\n\x1b[90m连接已关闭，不会自动重连。\x1b[0m') } }
  socket.onerror = () => { if (current()) { setState('error'); view.writeln('\r\n\x1b[31m终端连接失败。\x1b[0m') } }
  const input = view.onData(data => { if (current() && socket.readyState === WebSocket.OPEN) socket.send(encoder.encode(data)) })
  const resize = view.onResize(({ cols, rows }) => { if (current() && props.visible && socket.readyState === WebSocket.OPEN && cols >= 20 && cols <= 500 && rows >= 5 && rows <= 200) socket.send(JSON.stringify({ type: 'resize', columns: cols, rows })) })
  const observer = new ResizeObserver(fitVisible); observer.observe(container.value)
  dispose = () => {
    closed = true; observer.disconnect(); input.dispose(); resize.dispose()
    socket.onmessage = null; socket.onclose = null; socket.onerror = null
    if (socket.readyState === WebSocket.CONNECTING || socket.readyState === WebSocket.OPEN) socket.close(1000, '用户关闭终端')
    view.dispose(); if (terminal === view) { terminal = undefined; fit = undefined }
  }
  if (props.visible) void focus()
}
function checkContext() { if (connectedContext && (connectedContext !== context() || !allowed())) { generation++; dispose?.(); dispose = undefined; setState('closed') } }
watch(() => props.path, () => void connect(), { immediate: true })
watch(() => props.visible, visible => { if (visible) void focus() })
watch(() => context(), checkContext, { flush: 'sync' })
if (typeof window !== 'undefined') { window.addEventListener('storage', checkContext); window.addEventListener('edo:projects-changed', checkContext) }
onBeforeUnmount(() => { disposed = true; generation++; dispose?.(); if (typeof window !== 'undefined') { window.removeEventListener('storage', checkContext); window.removeEventListener('edo:projects-changed', checkContext) } })
defineExpose({ focus })
</script>

<template><div class="terminal-session-pane"><div class="terminal-session-status" role="status">{{ stateText }}</div><div ref="container" class="terminal-session-canvas" role="group" aria-label="交互式终端" /></div></template>
<style scoped>.terminal-session-pane{display:flex;flex-direction:column;height:100%;min-height:180px;min-width:0;padding:10px;background:#071018;color:#dbe7f4;overflow:hidden}.terminal-session-status{flex:none;min-height:20px;color:#93a5b7;font:13px/20px SFMono-Regular,Consolas,monospace}.terminal-session-canvas{flex:1;min-height:0;min-width:0;overflow:hidden}.terminal-session-canvas :deep(.xterm){height:100%}.terminal-session-canvas :deep(.xterm-viewport){overscroll-behavior:contain}</style>
