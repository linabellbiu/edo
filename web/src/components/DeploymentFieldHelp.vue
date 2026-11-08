<script setup lang="ts">
defineProps<{ text: string; label: string }>()
</script>
<template>
  <a-tooltip :title="text" :trigger="['hover', 'focus', 'click']">
    <button type="button" class="deployment-field-help" :aria-label="label" @click.stop>?</button>
  </a-tooltip>
</template>
<style scoped>
.deployment-field-help{display:inline-flex;align-items:center;justify-content:center;flex-shrink:0;width:22px;height:22px;margin-left:6px;padding:0;border:1px solid var(--edo-border);border-radius:50%;background:transparent;color:var(--edo-muted);font-size:13px;line-height:1;cursor:help;vertical-align:middle}
.deployment-field-help:hover{color:var(--edo-primary);border-color:var(--edo-primary)}
.deployment-field-help:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
</style>
