<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/time'
import type { KubeCell, KubeReference } from '@/utils/kubernetesPresentation'
const props=defineProps<{cell?:KubeCell;date?:boolean;canFollow?:boolean}>()
const emit=defineEmits<{follow:[reference:KubeReference]}>()
const {t}=useI18n()
const value=(text:string|undefined)=>!text||text==='—'?'—':props.date?formatDateTime(text):text
</script>
<template>
  <a-tooltip :mouse-enter-delay="0.1" :trigger="['hover','focus']">
    <template #title><span>{{ value(cell?.text) }}</span><br v-if="cell?.secondary" /><span v-if="cell?.secondary">{{ value(cell.secondary) }}</span></template>
    <div class="kube-cell" :class="{numeric:cell?.progress!==undefined}" :tabindex="cell?.reference&&canFollow?undefined:0">
      <button v-if="cell?.reference&&canFollow" class="cell-link" @click="emit('follow',cell.reference)">{{ value(cell.text) }}</button><span v-else class="cell-primary">{{ value(cell?.text) }}</span>
      <span v-if="cell?.secondary&&cell.secondary!=='—'" class="cell-secondary">{{ value(cell.secondary) }}</span>
      <div v-if="cell?.progress!==undefined" class="replica-track" role="meter" :aria-label="t('k8s.readyReplicas')" :aria-valuenow="cell.progress" aria-valuemin="0" aria-valuemax="100" :aria-valuetext="cell.text"><i :style="{width:`${cell.progress}%`}" /></div>
    </div>
  </a-tooltip>
</template>
<style scoped>
.kube-cell{display:flex;flex-direction:column;align-items:flex-start;gap:5px;max-width:300px;min-width:70px;font-size:13px;outline-offset:3px}.cell-primary,.cell-secondary,.cell-link{max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.cell-secondary{color:var(--edo-muted);font-size:12px}.cell-link{border:0;padding:0;background:none;font:inherit;color:var(--edo-primary);cursor:pointer;text-align:left}.cell-link:hover{text-decoration:underline}.numeric{font-variant-numeric:tabular-nums}.replica-track{height:4px;width:84px;background:var(--edo-border);border-radius:2px;overflow:hidden;margin-top:1px}.replica-track i{display:block;height:100%;background:var(--edo-primary)}
</style>
