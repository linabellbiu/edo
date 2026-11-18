<script setup lang="ts">
import { computed } from 'vue'
import { ArrowUpRight } from 'lucide-vue-next'
import { sessionPopoutURL, type SessionTarget } from '@/utils/sessionPopout'

const props = defineProps<SessionTarget & { disabled?: boolean }>()
const url = computed(() => sessionPopoutURL(props))
const available = computed(() => !props.disabled && Boolean(url.value))
const noun = computed(() => props.kind === 'terminal' ? '终端' : '日志')
const hint = computed(() => `Ctrl/⌘+点击在新标签页打开${noun.value}${props.title ? `：${props.title}` : ''}`)
const openLabel = computed(() => `在新标签页打开${noun.value}${props.title ? `：${props.title}` : ''}`)

const blank = () => window.open(url.value, '_blank', 'noopener,noreferrer')

// Ctrl / ⌘ + 点击按钮直接在新标签打开，普通点击保留原抽屉与当前页操作。
function intercept(event: MouseEvent) {
  if (!available.value || !(event.ctrlKey || event.metaKey) || event.altKey || event.shiftKey) return
  event.preventDefault()
  event.stopPropagation()
  blank()
}

// macOS 把 Ctrl+点击识别为右键，只拦截带 Ctrl 的 contextmenu，普通右键保持原样。
function interceptContextMenu(event: MouseEvent) {
  if (!available.value || !event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return
  event.preventDefault()
  event.stopPropagation()
  blank()
}
</script>

<template>
  <span class="session-action" :class="{ 'is-disabled': !available }" :title="available ? hint : undefined" @click.capture="intercept" @contextmenu.capture="interceptContextMenu">
    <slot />
    <a
      v-if="available"
      class="session-action-corner"
      :href="url"
      target="_blank"
      rel="noopener noreferrer"
      :aria-label="openLabel"
      :title="hint"
      @click.stop
    ><ArrowUpRight :size="10" aria-hidden="true" /></a>
  </span>
</template>

<style scoped>
.session-action { position: relative; display: inline-flex; align-items: center; }
/* 给右上角的箭头留出位置，文字不会与小图标重叠。 */
.session-action :deep(.ant-btn) { padding-inline-end: 18px; }
.session-action-corner { position: absolute; top: 2px; right: 4px; display: inline-flex; height: 11px; align-items: center; justify-content: center; border-radius: 2px; color: var(--edo-muted); line-height: 11px; text-decoration: none; }
.session-action-corner svg { width: 11px; height: 11px; }
.session-action:hover .session-action-corner { color: var(--edo-primary); }
.session-action-corner:hover { color: var(--edo-primary); }
.session-action-corner:focus-visible { outline: 1px solid var(--edo-primary); outline-offset: 1px; }
</style>

<style>
/* 主按钮底色较深，角标改用高对比的浅色；插槽按钮不在本组件作用域内，规则需全局生效。 */
.session-action:has(> .ant-btn-primary, > .ant-btn-dangerous) .session-action-corner { color: rgb(255 255 255 / 78%); }
.session-action:has(> .ant-btn-primary, > .ant-btn-dangerous) .session-action-corner:hover { color: #fff; }
</style>
