<script setup lang="ts">
import { nextTick } from 'vue'
import { Tabs } from 'ant-design-vue'

defineOptions({ inheritAttrs: false })

async function revealFocusedTab(event: FocusEvent) {
  const root = event.currentTarget as HTMLElement | null
  const target = event.target as HTMLElement | null
  if (!root || target?.getAttribute('role') !== 'tab' || target.closest('.ant-tabs') !== root) return

  // 等待 Ant 完成本次位移，避免其 focus 处理清零原生滚动后再次遮住焦点。
  await nextTick()
  if (!root.isConnected || !target.isConnected || target.ownerDocument.activeElement !== target) return
  const viewport = target.closest<HTMLElement>('.ant-tabs-nav-wrap')
  if (!viewport || !root.contains(viewport)) return
  const bounds = viewport.getBoundingClientRect(), focused = target.getBoundingClientRect()
  if (focused.left < bounds.left || focused.right > bounds.right || focused.top < bounds.top || focused.bottom > bounds.bottom) {
    target.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'instant' })
  }
}
</script>

<template>
  <Tabs v-bind="$attrs" @focusin="revealFocusedTab"><slot /></Tabs>
</template>
