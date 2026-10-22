<script setup lang="ts">
withDefaults(defineProps<{ open: boolean; title?: string; width?: string | number }>(), { title: '', width: 'min(1280px, 94vw)' })
const emit = defineEmits<{ 'update:open': [value: boolean]; close: [] }>()
</script>

<template>
  <a-drawer :open="open" :title="title" :width="width" class="resource-detail-drawer" :body-style="{ padding: 0, background: 'var(--edo-bg)' }" @update:open="emit('update:open', $event)" @close="emit('close')">
    <template v-if="$slots.title" #title><slot name="title" /></template>
    <div v-if="$slots.actions" class="resource-detail-toolbar"><slot name="actions" /></div>
    <div class="resource-detail-content"><slot /></div>
    <template v-if="$slots.extra" #extra><slot name="extra" /></template>
  </a-drawer>
</template>
