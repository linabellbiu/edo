<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { RefreshCw, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ open: boolean; title: string; path: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const { t } = useI18n()

const container = ref<HTMLDivElement | null>(null)
const tail = ref(500)
const follow = ref(true)
const connection = ref<'connecting' | 'connected' | 'complete' | 'error'>('connecting')
let activeTerminal: Terminal | undefined
let dispose: (() => void) | undefined

const connectionText = computed(() => ({
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
  dispose?.()
  dispose = undefined
  connection.value = 'connecting'
  await nextTick()
  if (!container.value || !props.path) return

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
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const separator = props.path.includes('?') ? '&' : '?'
  const query = new URLSearchParams({
    tail: String(tail.value),
    follow: String(follow.value),
    timestamps: 'true',
  })
  const socket = new WebSocket(
    `${protocol}//${location.host}${props.path}${separator}${query}`,
    'edo-container-logs-v1',
  )
  const decoder = new TextDecoder()
  socket.binaryType = 'arraybuffer'

  socket.onmessage = event => {
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
    if (!closed && !completed) {
      connection.value = 'error'
      terminal.writeln(`\r\n\x1b[90m${t('containerLogs.disconnected')}\x1b[0m`)
    }
  }
  socket.onerror = () => {
    connection.value = 'error'
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
    if (socket.readyState === WebSocket.OPEN) socket.close(1000, '用户关闭容器日志')
    terminal.dispose()
    if (activeTerminal === terminal) activeTerminal = undefined
  }
}

function clear() {
  activeTerminal?.clear()
}

watch(() => props.open, open => {
  if (open) void connect()
  else {
    dispose?.()
    dispose = undefined
  }
})

onBeforeUnmount(() => dispose?.())
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
        <a-select v-model:value="tail" :options="[100, 500, 1000, 2000, 5000].map(value => ({ value, label: String(value) }))" @change="connect" />
      </label>
      <label class="follow-option">
        <a-switch v-model:checked="follow" size="small" @change="connect" />
        <span>{{ t('containerLogs.follow') }}</span>
      </label>
      <span class="toolbar-spacer" />
      <a-button @click="clear"><Trash2 :size="15" />{{ t('containerLogs.clear') }}</a-button>
      <a-button @click="connect"><RefreshCw :size="15" />{{ t('containerLogs.reconnect') }}</a-button>
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
