<script setup lang="ts">
import { CircleAlert, Info } from 'lucide-vue-next'

defineProps<{ message: string; failed?: boolean }>()
</script>

<template>
  <span class="sync-status">
    <span class="sync-announcement" role="status">{{ message }}</span>
    <a-tooltip v-if="message" :title="message" :trigger="['hover', 'focus', 'click']">
      <button type="button" class="sync-icon" :class="{ failed }" :aria-label="message">
        <CircleAlert v-if="failed" aria-hidden="true" :size="15" />
        <Info v-else aria-hidden="true" :size="15" />
      </button>
    </a-tooltip>
  </span>
</template>

<style scoped>
.sync-status{position:relative;display:inline-grid;place-items:center;flex:none;width:24px;height:24px}
.sync-icon{display:grid;place-items:center;width:24px;height:24px;padding:0;border:0;border-radius:4px;background:none;color:var(--edo-muted);cursor:pointer}
.sync-icon:hover,.sync-icon:focus-visible{background:var(--edo-surface-soft)}
.sync-icon:focus-visible{outline:2px solid var(--edo-primary);outline-offset:1px}
.sync-icon.failed{color:var(--kube-danger,var(--edo-muted))}
.sync-announcement{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%);white-space:nowrap}
</style>
