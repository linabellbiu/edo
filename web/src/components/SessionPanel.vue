<script setup lang="ts">
import { useAttrs, type CSSProperties } from 'vue'

defineOptions({ inheritAttrs: false })
defineProps<{ open: boolean; title: string; standalone?: boolean; bodyStyle?: CSSProperties }>()
const emit = defineEmits<{ close: []; 'after-open-change': [open: boolean] }>()
const attrs = useAttrs()
</script>

<template>
  <section v-if="standalone && open" class="session-page-panel" :class="[attrs.class, attrs['root-class-name']]" :aria-label="title">
    <header class="session-page-header">
      <h1>{{ title }}</h1>
      <div class="session-page-extra"><slot name="extra" /></div>
    </header>
    <div class="session-page-body" :style="bodyStyle"><slot /></div>
  </section>
  <a-drawer v-else-if="!standalone" v-bind="attrs" :open="open" :title="title" :body-style="bodyStyle" @close="emit('close')" @after-open-change="emit('after-open-change', $event)">
    <template #extra><slot name="extra" /></template>
    <slot />
  </a-drawer>
</template>

<style scoped>
.session-page-panel{display:flex;flex-direction:column;height:100%;min-height:0;min-width:0;background:var(--edo-surface);color:var(--edo-text)}
.session-page-header{display:flex;flex:none;align-items:center;justify-content:space-between;gap:12px;min-height:54px;padding:10px 16px;border-bottom:1px solid var(--edo-border)}
.session-page-header h1{margin:0;min-width:0;font-size:15px;font-weight:600;overflow-wrap:anywhere}
.session-page-extra{display:flex;flex:none;align-items:center;gap:8px}
.session-page-body{display:flex;flex:1;flex-direction:column;min-height:0;min-width:0;padding:16px;overflow:hidden}
@media(max-width:640px){.session-page-header{flex-wrap:wrap;padding:10px 12px}.session-page-body{padding:12px}}
</style>
