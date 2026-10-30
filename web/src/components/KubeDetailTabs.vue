<script setup lang="ts">
import { nextTick, onBeforeUnmount } from 'vue'
import { Tabs } from 'ant-design-vue'

defineOptions({ inheritAttrs: false })
type TabScrollEvent = { direction: 'left' | 'right' | 'top' | 'bottom' }
const emit = defineEmits<{ tabScroll: [event: TabScrollEvent] }>()
let focusRoot: HTMLElement | undefined
onBeforeUnmount(() => { focusRoot = undefined })

async function revealFocusedTab(root: HTMLElement | undefined) {
  const target = root?.ownerDocument.activeElement as HTMLElement | null
  if (!root || target?.getAttribute('role') !== 'tab' || target.closest('.ant-tabs') !== root) return

  // Ant 在焦点、尺寸及选项重算后都可能位移，等本次更新完成再揭示仍持焦的页签。
  await nextTick()
  if (root !== focusRoot || !root.isConnected || !target.isConnected || target.ownerDocument.activeElement !== target || target.closest('.ant-tabs') !== root) return
  const viewport = target.closest<HTMLElement>('.ant-tabs-nav-wrap')
  if (!viewport || !root.contains(viewport)) return
  const bounds = viewport.getBoundingClientRect(), focused = target.getBoundingClientRect()
  const delta = focused.left < bounds.left + 2 ? -Math.ceil(bounds.left + 2 - focused.left)
    : focused.right > bounds.right - 2 ? Math.ceil(focused.right - bounds.right + 2) : 0
  if (delta) viewport.scrollBy({ left: delta, behavior: 'instant' })
}

function focusEntered(event: FocusEvent) {
  focusRoot = event.currentTarget as HTMLElement
  return revealFocusedTab(focusRoot)
}

function tabScrolled(event: TabScrollEvent) {
  emit('tabScroll', event)
  return revealFocusedTab(focusRoot)
}
</script>

<template>
  <Tabs v-bind="$attrs" @focusin="focusEntered" @tab-scroll="tabScrolled"><slot /></Tabs>
</template>
