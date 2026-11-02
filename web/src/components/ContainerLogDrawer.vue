<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { RefreshCw, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { projectSocketUrl } from '@/utils/socketUrl'

const props = withDefaults(defineProps<{ open: boolean; title: string; path: string; paused?: boolean }>(), { paused: false })
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const { t } = useI18n()

const container = ref<HTMLDivElement | null>(null)
const tail = ref(500)
const follow = ref(true)
const connection = ref<'checking' | 'connecting' | 'connected' | 'complete' | 'error'>('connecting')
let activeTerminal: Terminal | undefined
let dispose: (() => void) | undefined
let generation = 0
let disposed = false
let pausedPath = '', cancelledIntent = false

const connectionText = computed(() => ({
  checking: t('containerLogs.checking'),
  connecting: t('containerLogs.connecting'),
  connected: t('containerLogs.connected'),
  complete: t('containerLogs.complete'),
  error: t('containerLogs.error'),
}[connection.value]))

const connectionColor = computed(() => {
  if (connection.value === 'connected') return 'processing'
  if (connection.value === 'complete') return 'success'
  if (connection.value === 'error') return 'error'
  return 'default'
})

async function connect() {
  const revision = ++generation
  const target = props.path
  dispose?.()
  dispose = undefined
  connection.value = props.paused ? 'checking' : 'connecting'
  if (props.paused || cancelledIntent) return
  await nextTick()
  if (disposed || props.paused || cancelledIntent || revision !== generation || !props.open || !container.value || !target || target !== props.path) return

  const terminal = new Terminal({
    convertEol: true,
    cursorBlink: false,
    disableStdin: true,
    fontFamily: 'SFMono-Regular, Consolas, monospace',
    fontSize: 13,
    scrollback: 5000,
    theme: {
      background: '#071018',
      foreground: '#d7e3ef',
      selectionBackground: '#254156',
    },
  })
  const fit = new FitAddon()
  terminal.loadAddon(fit)
  terminal.open(container.value)
  fit.fit()
  activeTerminal = terminal

  let closed = false
  let completed = false
  const current = () => !closed && !disposed && !props.paused && !cancelledIntent && revision === generation && props.open && target === props.path
  const socket = new WebSocket(
    projectSocketUrl(target, { tail: tail.value, follow: follow.value, timestamps: true }),
    'edo-container-logs-v1',
  )
  const decoder = new TextDecoder()
  socket.binaryType = 'arraybuffer'

  socket.onmessage = event => {
    if (!current()) return
    if (event.data instanceof ArrayBuffer) {
      terminal.write(decoder.decode(event.data, { stream: true }))
      if (follow.value) terminal.scrollToBottom()
      return
    }
    try {
      const payload = JSON.parse(String(event.data)) as { type?: string; message?: string }
      if (payload.type === 'ready') connection.value = 'connected'
      if (payload.type === 'complete') {
        completed = true
        connection.value = 'complete'
      }
      if (payload.type === 'error') {
        completed = true
        connection.value = 'error'
        terminal.writeln(`\r\n\x1b[31m${payload.message || t('containerLogs.readFailed')}\x1b[0m`)
      }
    } catch {
      connection.value = 'error'
      terminal.writeln(`\r\n\x1b[31m${t('containerLogs.invalidEvent')}\x1b[0m`)
    }
  }
  socket.onclose = () => {
    if (current() && !completed) {
      connection.value = 'error'
      terminal.writeln(`\r\n\x1b[90m${t('containerLogs.disconnected')}\x1b[0m`)
    }
  }
  socket.onerror = () => {
    if (current()) connection.value = 'error'
  }

  const observer = new ResizeObserver(() => {
    try {
      fit.fit()
    } catch {
      // 抽屉关闭时终端节点可能已经释放。
    }
  })
  observer.observe(container.value)
  dispose = () => {
    closed = true
    observer.disconnect()
    socket.onmessage = socket.onclose = socket.onerror = null
    if (socket.readyState === WebSocket.CONNECTING || socket.readyState === WebSocket.OPEN) socket.close(1000, '用户关闭容器日志')
    terminal.dispose()
    if (activeTerminal === terminal) activeTerminal = undefined
  }
}

function clear() {
  activeTerminal?.clear()
}

watch([() => props.open, () => props.path, () => props.paused], ([open, path, paused]) => {
  generation++
  dispose?.()
  dispose = undefined
  if (!open) { pausedPath = ''; cancelledIntent = false; return }
  if (pausedPath && path !== pausedPath) { cancelledIntent = true; emit('update:open', false) }
  if (cancelledIntent) return
  if (paused) {
    pausedPath = path
    connection.value = 'checking'
    // 停流后不保留旧授权下已渲染的日志正文。
    container.value?.replaceChildren?.()
    return
  }
  pausedPath = ''
  void connect()
}, { immediate: true, flush: 'sync' })

onBeforeUnmount(() => {
  disposed = true
  generation++
  dispose?.()
  dispose = undefined
})
</script>

<template>
  <a-drawer
    :open="open"
    :title="title"
    placement="bottom"
    height="72vh"
    class="container-log-drawer"
    @close="emit('update:open', false)"
  >
    <template #extra>
      <a-tag :color="connectionColor">{{ connectionText }}</a-tag>
    </template>

    <div class="log-toolbar">
      <label>
        <span>{{ t('containerLogs.recent') }}</span>
        <a-select v-model:value="tail" :disabled="paused" :options="[100, 500, 1000, 2000, 5000].map(value => ({ value, label: String(value) }))" @change="connect" />
      </label>
      <label class="follow-option">
        <a-switch v-model:checked="follow" :disabled="paused" size="small" @change="connect" />
        <span>{{ t('containerLogs.follow') }}</span>
      </label>
      <span class="toolbar-spacer" />
      <a-button :disabled="paused" @click="clear"><Trash2 :size="15" />{{ t('containerLogs.clear') }}</a-button>
      <a-button :disabled="paused" @click="connect"><RefreshCw :size="15" />{{ t('containerLogs.reconnect') }}</a-button>
    </div>
    <div ref="container" class="log-console" />
  </a-drawer>
</template>

<style scoped>
.log-toolbar {
  display: flex;
  min-height: 42px;
  align-items: center;
  gap: 14px;
  margin-bottom: 10px;
}

.log-toolbar label {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--edo-muted);
  font-size: 13px;
}

.log-toolbar :deep(.ant-select) {
  width: 92px;
}

.follow-option {
  cursor: pointer;
}

.toolbar-spacer {
  flex: 1;
}

.log-console {
  height: calc(72vh - 152px);
  overflow: hidden;
  padding: 12px;
  border: 1px solid rgb(255 255 255 / 8%);
  border-radius: 8px;
  background: #071018;
}

@media (max-width: 640px) {
  .log-toolbar {
    flex-wrap: wrap;
  }

  .toolbar-spacer {
    display: none;
  }

  .log-console {
    height: calc(72vh - 196px);
  }
}
</style>
