<script setup lang="ts">
withDefaults(defineProps<{ open: boolean; title?: string; width?: string | number; compactHeader?: boolean }>(), { title: '', width: 'min(1280px, 94vw)', compactHeader: false })
const emit = defineEmits<{ 'update:open': [value: boolean]; close: [] }>()
</script>

<template>
  <a-drawer :open="open" :title="compactHeader || $slots.title ? undefined : title" :width="width" class="resource-detail-drawer" :body-style="{ padding: 0, background: 'var(--edo-bg)' }" @update:open="emit('update:open', $event)" @close="emit('close')">
    <template v-if="compactHeader || $slots.title" #title><div v-if="compactHeader" class="resource-detail-compact-header"><strong class="resource-detail-compact-title" :title="title">{{ title }}</strong><div class="resource-detail-compact-actions"><slot name="actions" /></div></div><slot v-else name="title" /></template>
    <div v-if="$slots.actions && !compactHeader" class="resource-detail-toolbar"><slot name="actions" /></div>
    <div class="resource-detail-content"><slot /></div>
    <template v-if="$slots.extra" #extra><slot name="extra" /></template>
  </a-drawer>
</template>

<style scoped>
.resource-detail-compact-header{display:flex;align-items:center;gap:12px;min-width:0}
.resource-detail-compact-title{min-width:0;max-width:32%;font-size:14px;line-height:22px;overflow-wrap:anywhere}
.resource-detail-compact-actions{display:flex;align-items:center;flex-wrap:wrap;gap:6px;min-width:0;flex:1;font-size:12px;font-weight:400}
@media(max-width:680px){.resource-detail-compact-header{flex-wrap:wrap;gap:6px}.resource-detail-compact-title{max-width:100%}.resource-detail-compact-actions{flex-basis:100%}}
</style>
