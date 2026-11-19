<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getKubeServiceEndpoints, type KubeServicePortSource } from '@/api/kubernetes'
import { formatDateTime } from '@/utils/time'
import { vCopyFullText } from '@/directives/copyFullText'
import { servicePortTooltipLines, type KubeCell, type KubeReference } from '@/utils/kubernetesPresentation'
const props=defineProps<{cell?:KubeCell;date?:boolean;canFollow?:boolean;servicePortSource?:KubeServicePortSource}>()
const emit=defineEmits<{follow:[reference:KubeReference]}>()
const {t}=useI18n()
const value=(text:string|undefined)=>!text||text==='—'?'—':props.date?formatDateTime(text):text

const mappings = computed(() => props.cell?.portMappings ?? [])
const lookupKey = computed(() => {
  const source = props.servicePortSource
  if (!source || !mappings.value.some(port => port.needsResolution)) return ''
  return JSON.stringify([source.scope, source.cluster, source.namespace, source.name, source.uid, source.resourceVersion, mappings.value])
})
const tooltipOpen = ref(false)
const lookupState = ref<'idle' | 'loading' | 'failed'>('idle')
const lookup = shallowRef<{lines: ReturnType<typeof servicePortTooltipLines>; partial: boolean; expiresAt: number}>()
const tooltipLines = computed(() => lookup.value?.lines ?? servicePortTooltipLines(mappings.value))
const tooltipNote = computed(() => {
  if (lookupState.value === 'loading') return t('k8s.servicePortsResolving')
  if (lookupState.value === 'failed') return t('k8s.servicePortsUnavailable')
  if (lookup.value?.partial) return t('k8s.servicePortsPartial')
  return ''
})
let request: AbortController | undefined

function cancelLookup() {
  request?.abort()
  request = undefined
  lookupState.value = 'idle'
}

async function readServicePorts() {
  const key = lookupKey.value, source = props.servicePortSource
  if (!source || !key || request || (lookup.value && Date.now() < lookup.value.expiresAt)) return
  lookup.value = undefined
  const controller = new AbortController()
  request = controller
  lookupState.value = 'loading'
  const isCurrent = () => tooltipOpen.value && request === controller && key === lookupKey.value && !controller.signal.aborted
  try {
    const result = await getKubeServiceEndpoints(source, controller.signal)
    if (!isCurrent()) return
    if (result.uid !== source.uid || !['ok', 'partial'].includes(result.state)) {
      lookupState.value = 'failed'
      return
    }
    lookup.value = {
      lines: servicePortTooltipLines(mappings.value, result.endpoints ?? []),
      partial: result.state === 'partial',
      expiresAt: Date.now() + 15_000,
    }
    lookupState.value = 'idle'
  } catch {
    if (isCurrent()) lookupState.value = 'failed'
  } finally {
    if (request === controller) request = undefined
  }
}

function onTooltipOpen(open: boolean) {
  tooltipOpen.value = open
  if (open) void readServicePorts()
  else cancelLookup()
}

watch(lookupKey, () => {
  cancelLookup()
  lookup.value = undefined
  if (tooltipOpen.value) void readServicePorts()
}, {flush: 'sync'})
onBeforeUnmount(cancelLookup)
</script>
<template>
  <a-tooltip :mouse-enter-delay="0.1" :trigger="['hover','focus']" :overlay-style="mappings.length?{maxWidth:'calc(100vw - 24px)'}:undefined" @update:open="onTooltipOpen">
    <template #title>
      <div v-if="mappings.length" class="port-tooltip">
        <div v-for="(line, index) in tooltipLines" :key="index" class="port-tooltip-line">
          {{ line.text }}<span v-if="!line.resolved&&lookupState!=='loading'" class="port-tooltip-note">{{ ` · ${t('k8s.servicePortUnresolved')}` }}</span>
        </div>
        <div v-if="tooltipNote" class="port-tooltip-note" role="status">{{ tooltipNote }}</div>
      </div>
      <template v-else><span>{{ value(cell?.text) }}</span><br v-if="cell?.secondary" /><span v-if="cell?.secondary">{{ value(cell.secondary) }}</span><template v-if="cell?.raw"><br /><span>{{ t('k8s.quantityOriginal') }}: {{ cell.raw }}</span></template></template>
    </template>
    <div class="kube-cell" :class="{numeric:cell?.progress!==undefined||cell?.badge,'with-port-mappings':cell?.portMappings?.length}">
      <div v-if="cell?.portMappings?.length" class="port-mapping-list" role="list">
        <span v-for="({mapping, nodePort}, index) in cell.portMappings" :key="`${index}/${mapping}`"
          v-copy-full-text tabindex="0" class="kube-namespace-badge kube-port-badge" role="listitem">
          <span class="port-mapping-value">{{ mapping }}</span><span v-if="nodePort" class="port-mapping-node">{{ ` (NodePort ${nodePort})` }}</span>
        </span>
      </div>
      <button v-else-if="cell?.reference&&canFollow" type="button" class="cell-link" @click="emit('follow',cell.reference)">{{ value(cell.text) }}</button><span v-else v-copy-full-text tabindex="0" class="cell-primary" :class="cell?.badge?['kube-ready-badge',`is-${cell.badge}`]:undefined">{{ value(cell?.text) }}</span>
      <span v-if="!cell?.badge&&cell?.secondary&&cell.secondary!=='—'" v-copy-full-text tabindex="0" class="cell-secondary">{{ value(cell.secondary) }}</span>
      <div v-if="!cell?.badge&&cell?.progress!==undefined" class="replica-track" role="meter" :aria-label="t('k8s.readyReplicas')" :aria-valuenow="cell.progress" aria-valuemin="0" aria-valuemax="100" :aria-valuetext="cell.text"><i :style="{width:`${cell.progress}%`}" /></div>
    </div>
  </a-tooltip>
</template>
<style scoped>
.kube-cell{display:flex;flex-direction:column;align-items:flex-start;gap:5px;max-width:300px;min-width:70px;font-size:13px;outline-offset:3px}.cell-primary,.cell-secondary,.cell-link{max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.cell-secondary{color:var(--kube-muted);font-size:12px}.cell-link{border:0;padding:0;min-height:24px;border-radius:2px;background:none;font:inherit;color:var(--kube-link);cursor:pointer;text-align:left}.cell-link:hover{text-decoration:underline}.cell-link:focus-visible,.kube-cell:focus-visible{outline:2px solid var(--kube-focus);outline-offset:3px}.numeric{font-variant-numeric:tabular-nums}.replica-track{height:4px;width:84px;background:var(--kube-border);border-radius:2px;overflow:hidden;margin-top:1px}.replica-track i{display:block;height:100%;background:var(--kube-link)}

.with-port-mappings { max-width: 100%; min-width: 0; }
.port-mapping-list { display: grid; justify-items: start; gap: 4px; max-width: 100%; }
.kube-port-badge { max-width: 100%; overflow-wrap: break-word; font-variant-numeric: tabular-nums; }
.port-mapping-value {
  color: var(--kube-text);
  font-size: var(--kube-primary-value-size);
  font-weight: var(--kube-primary-value-weight);
}
.port-mapping-node { white-space: nowrap; }
.port-tooltip { max-width: calc(100vw - 48px); max-height: 60dvh; overflow: auto; font-variant-numeric: tabular-nums; }
.port-tooltip-line { white-space: nowrap; }
.port-tooltip-note { font-size: 12px; opacity: .85; }
.copy-full-text{cursor:text;user-select:text}.copy-full-text:focus-visible{outline:2px solid var(--kube-focus);outline-offset:3px}
</style>
