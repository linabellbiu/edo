<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatKubeCPU, formatKubeMemory, nodeResourceUsage, type KubeObject, type KubeOverview, type NodeResourceUsage } from '@/api/kubernetes'
import { formatDateTime } from '@/utils/time'

const props=defineProps<{node:KubeObject;overview:KubeOverview|null;failed?:boolean}>()
const {t}=useI18n()
const meters=computed(()=>nodeResourceUsage(props.overview,props.node,props.failed))
const name=(key:string)=>({cpu:'CPU',memory:'Mem',pods:'Pod'}[key]??key)
const percent=(meter:NodeResourceUsage)=>meter.percent===undefined?'—':`${meter.percent.toFixed(1)}%`
function amount(value:number|undefined,key:string,exact=false){
  if(value===undefined)return '—'
  if(key==='memory')return `${formatKubeMemory(value,exact)}${exact?` (${value} B)`:''}`
  if(key==='cpu')return formatKubeCPU(value,exact)
  return String(value)
}
function numbers(meter:NodeResourceUsage,exact=false){return `${amount(meter.used,meter.key,exact)} / ${amount(meter.total,meter.key,exact)}`}
function hint(meter:NodeResourceUsage){
  if(meter.state==='ok')return t(meter.key==='cpu'?'k8s.cpuUsageHint':meter.key==='memory'?'k8s.memoryUsageHint':'k8s.nodePodsHint')
  return t(meter.state==='loading'?'k8s.loading':meter.state==='no_permission'?'k8s.nodePodsNoPermission':meter.state==='missing_capacity'?'k8s.capacityMissing':meter.key==='pods'?'k8s.nodePodsUnavailable':'k8s.metricsUnavailable')
}
const tone=(meter:NodeResourceUsage)=>meter.percent===undefined?'unknown':meter.percent>=90?'danger':meter.percent>=75?'warning':'normal'
const description=computed(()=>meters.value.map(meter=>`${name(meter.key)} ${percent(meter)} [${numbers(meter)}] ${hint(meter)}`).join('; '))
</script>

<template>
  <a-tooltip :mouse-enter-delay="0.1" :trigger="['hover','focus']" placement="top">
    <template #title><div class="usage-tooltip"><div v-for="meter in meters" :key="meter.key"><strong>{{ name(meter.key) }} {{ percent(meter) }} [{{ numbers(meter,true) }}]</strong><span>{{ hint(meter) }}</span></div><small v-if="overview">{{ t('k8s.statisticsAt') }} {{ formatDateTime(overview.collected_at) }}</small></div></template>
    <div class="node-usage" tabindex="0" :aria-label="description">
      <div v-for="meter in meters" :key="meter.key" class="usage-meter" :class="`tone-${tone(meter)}`" :role="meter.percent===undefined?undefined:'meter'" :aria-label="name(meter.key)" :aria-valuemin="meter.percent===undefined?undefined:0" :aria-valuemax="meter.percent===undefined?undefined:Math.max(100,meter.percent)" :aria-valuenow="meter.percent" :aria-valuetext="`${percent(meter)} [${numbers(meter)}]`">
        <span class="meter-name">{{ name(meter.key) }}</span>
        <span class="meter-track" aria-hidden="true"><span class="meter-fill" :style="{width:`${meter.width}%`}" /></span>
        <span class="meter-percent">{{ percent(meter) }}</span><span class="meter-values">[{{ numbers(meter) }}]</span>
      </div>
    </div>
  </a-tooltip>
</template>

<style scoped>
.node-usage{width:296px;display:grid;gap:2px;font-variant-numeric:tabular-nums;border-radius:6px;outline-offset:3px}
.node-usage:focus-visible{outline:2px solid var(--kube-focus)}
.usage-meter{--meter-color:var(--kube-meter-fill);display:grid;grid-template-columns:30px 70px 48px minmax(0,1fr);align-items:center;gap:8px;height:24px;color:var(--kube-text);font-size:12px;line-height:24px;white-space:nowrap}
.meter-name{color:var(--kube-muted);font-weight:500}
.meter-track{position:relative;height:6px;overflow:hidden;border-radius:3px;background:var(--kube-meter-track)}
.meter-fill{position:absolute;inset:0 auto 0 0;border-radius:3px;background:var(--meter-color)}
.meter-percent{text-align:right;font-weight:600;font-variant-numeric:tabular-nums}
.meter-values{padding-left:2px;overflow:hidden;text-align:right;color:var(--kube-muted);text-overflow:ellipsis}
.tone-warning{--meter-color:var(--kube-meter-warning)}.tone-warning .meter-percent{color:var(--kube-warning)}
.tone-danger{--meter-color:var(--kube-meter-danger)}.tone-danger .meter-percent{color:var(--kube-danger)}
.tone-unknown .meter-fill{display:none}
.usage-tooltip{display:grid;gap:8px;max-width:420px;font-size:12px;line-height:1.6;font-variant-numeric:tabular-nums}.usage-tooltip>div{display:grid;gap:2px}.usage-tooltip strong{font-weight:600}.usage-tooltip span,.usage-tooltip small{opacity:.9}.usage-tooltip small{border-top:1px solid currentColor;padding-top:6px;font-size:12px}
</style>
