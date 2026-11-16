<script setup lang="ts">
import { computed } from 'vue'
import { ArrowUpRight } from 'lucide-vue-next'
import { sessionPopoutURL, type SessionTarget } from '@/utils/sessionPopout'

const props = defineProps<SessionTarget & { disabled?: boolean }>()
const href = computed(() => sessionPopoutURL(props))
const label = computed(() => `${props.kind === 'terminal' ? '在新标签页打开终端（新会话）' : '在新标签页打开日志'}${props.title ? `：${props.title}` : ''}`)
</script>

<template>
  <a v-if="!disabled && href" class="session-popout-link" :href="href" target="_blank" rel="noopener noreferrer" :title="label" :aria-label="label" @click.stop><ArrowUpRight :size="14" aria-hidden="true" /></a>
  <span v-else class="session-popout-link is-disabled" role="link" aria-disabled="true" :title="label" :aria-label="label"><ArrowUpRight :size="14" aria-hidden="true" /></span>
</template>

<style scoped>
.session-popout-link{display:inline-flex;flex:none;align-items:center;justify-content:center;width:26px;height:28px;vertical-align:middle;border:1px solid var(--edo-border);border-radius:3px;color:var(--edo-primary);background:var(--edo-surface);text-decoration:none}
.session-popout-link:hover{background:var(--edo-primary-soft);border-color:var(--edo-primary)}
.session-popout-link:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
.session-popout-link.is-disabled{opacity:.4;cursor:not-allowed;color:var(--edo-muted)}
</style>
