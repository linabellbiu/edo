<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/time'
import { vCopyFullText } from '@/directives/copyFullText'
import type { KubeCell, KubeReference } from '@/utils/kubernetesPresentation'
const props=defineProps<{cell?:KubeCell;date?:boolean;canFollow?:boolean}>()
const emit=defineEmits<{follow:[reference:KubeReference]}>()
const {t}=useI18n()
const value=(text:string|undefined)=>!text||text==='—'?'—':props.date?formatDateTime(text):text
</script>
<template>
  <a-tooltip :mouse-enter-delay="0.1" :trigger="['hover','focus']">
    <template #title><span>{{ value(cell?.text) }}</span><br v-if="cell?.secondary" /><span v-if="cell?.secondary">{{ value(cell.secondary) }}</span><template v-if="cell?.raw"><br /><span>{{ t('k8s.quantityOriginal') }}: {{ cell.raw }}</span></template></template>
    <div class="kube-cell" :class="{numeric:cell?.progress!==undefined||cell?.badge,tokenized:cell?.tokens?.length}">
      <div v-if="cell?.tokens?.length" class="cell-tokens" role="list"><span v-for="(token,index) in cell.tokens" :key="`${index}/${token}`" v-copy-full-text class="kube-namespace-badge kube-port-badge" role="listitem">{{ token }}</span></div>
      <button v-else-if="cell?.reference&&canFollow" type="button" class="cell-link" @click="emit('follow',cell.reference)">{{ value(cell.text) }}</button><span v-else v-copy-full-text class="cell-primary" :class="cell?.badge?['kube-ready-badge',`is-${cell.badge}`]:undefined">{{ value(cell?.text) }}</span>
      <span v-if="!cell?.badge&&cell?.secondary&&cell.secondary!=='—'" v-copy-full-text class="cell-secondary">{{ value(cell.secondary) }}</span>
      <div v-if="!cell?.badge&&cell?.progress!==undefined" class="replica-track" role="meter" :aria-label="t('k8s.readyReplicas')" :aria-valuenow="cell.progress" aria-valuemin="0" aria-valuemax="100" :aria-valuetext="cell.text"><i :style="{width:`${cell.progress}%`}" /></div>
    </div>
  </a-tooltip>
</template>
<style scoped>
.kube-cell{display:flex;flex-direction:column;align-items:flex-start;gap:5px;max-width:300px;min-width:70px;font-size:13px;outline-offset:3px}.cell-primary,.cell-secondary,.cell-link{max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.cell-secondary{color:var(--kube-muted);font-size:12px}.cell-link{border:0;padding:0;min-height:24px;border-radius:2px;background:none;font:inherit;color:var(--kube-link);cursor:pointer;text-align:left}.cell-link:hover{text-decoration:underline}.cell-link:focus-visible,.kube-cell:focus-visible{outline:2px solid var(--kube-focus);outline-offset:3px}.numeric{font-variant-numeric:tabular-nums}.replica-track{height:4px;width:84px;background:var(--kube-border);border-radius:2px;overflow:hidden;margin-top:1px}.replica-track i{display:block;height:100%;background:var(--kube-link)}

.tokenized{max-width:100%;min-width:0}.cell-tokens{display:flex;align-items:center;flex-wrap:wrap;gap:6px;max-width:100%}.kube-port-badge{white-space:nowrap;font-variant-numeric:tabular-nums}
.copy-full-text{cursor:text;user-select:text}.copy-full-text:focus-visible{outline:2px solid var(--kube-focus);outline-offset:3px}
</style>
