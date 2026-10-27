<script setup lang="ts">
import {computed,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {type KubeObject,type KubeType} from '@/api/kubernetes'
import {resourceCells,resourceColumns,selectorText} from '@/utils/kubernetesPresentation'
import {workloadConditionTone} from '@/utils/kubernetesWorkloadExplorer'
import {formatDateTime,formatRelativeTime} from '@/utils/time'
import KubeResourceCell from './KubeResourceCell.vue'
import KubeWorkloadEvents from './KubeWorkloadEvents.vue'

const props=defineProps<{object:KubeObject;type:KubeType;events:KubeObject[];eventsError:boolean;eventsLoading?:boolean}>()
const emit=defineEmits<{events:[]}>()
const {t}=useI18n()
const cells=computed(()=>resourceCells(props.object,t))
const fields=computed(()=>resourceColumns(props.type,t).filter(field=>['replicas','updatedReplicas','availableReplicas','currentReplicas','completions','activeJobs','restarts'].includes(field.key)))
const annotations=computed(()=>Object.entries(props.object.metadata.annotations??{}).filter(([key])=>key!=='kubectl.kubernetes.io/last-applied-configuration'))
const labels=computed(()=>Object.entries(props.object.metadata.labels??{}))
const metadataExpanded=ref(false)
watch(()=>props.object.metadata.uid,()=>{metadataExpanded.value=false})
const settings=computed(()=>{
 const spec=props.object.spec??{},rows:Array<{label:string;value:unknown}>=[]
 const add=(label:string,value:unknown)=>{if(value!==undefined&&value!==null)rows.push({label,value})}
 if(['Deployment','StatefulSet','DaemonSet'].includes(props.object.kind)){
  const strategy=props.object.kind==='Deployment'?spec.strategy:spec.updateStrategy
  add(t('k8s.deploymentStrategy'),strategy?.type)
  for(const [key,label] of [['maxSurge','maxSurge'],['maxUnavailable','maxUnavailable'],['partition','statefulPartition']] as const)add(t(`k8s.${label}`),strategy?.rollingUpdate?.[key])
  add(t('k8s.minReadySeconds'),spec.minReadySeconds===undefined?undefined:String(spec.minReadySeconds))
  add(t('k8s.progressDeadlineSeconds'),spec.progressDeadlineSeconds===undefined?undefined:String(spec.progressDeadlineSeconds))
  add(t('k8s.revisionHistoryLimit'),spec.revisionHistoryLimit)
 }
 if(props.object.kind==='CronJob'){
  add(t('k8s.schedule'),spec.schedule);add(t('k8s.cronTimeZone'),spec.timeZone);add(t('k8s.cronConcurrencyPolicy'),spec.concurrencyPolicy)
 }
 if(props.object.kind==='Job'){
  add(t('k8s.jobParallelism'),spec.parallelism);add(t('k8s.jobBackoffLimit'),spec.backoffLimit)
 }
 return rows
})
</script>

<template>
 <div class="workload-summary">
  <section class="workload-identity identity-compact">
   <dl class="identity-facts identity-inline"><div><dt>{{ t('k8s.namespace') }}</dt><dd :title="object.metadata.namespace">{{ object.metadata.namespace||'—' }}</dd></div><div><dt>{{ t('k8s.name') }}</dt><dd :title="object.metadata.name">{{ object.metadata.name||'—' }}</dd></div><div><dt>ResourceVersion</dt><dd>{{ object.metadata.resourceVersion||'—' }}</dd></div><div><dt>创建时间</dt><dd :title="formatDateTime(object.metadata.creationTimestamp)">{{ formatRelativeTime(object.metadata.creationTimestamp) }}</dd></div></dl>
   <div class="workload-metadata" :class="{expanded:metadataExpanded}">
    <div class="metadata-group"><span class="metadata-label">标签</span><div class="metadata-chips"><span v-for="[key,value] in labels" :key="key" class="metadata-chip" :title="`${key}=${value}`">{{ key }}={{ value }}</span><span v-if="!labels.length" class="metadata-empty">—</span></div></div>
    <div class="metadata-group"><span class="metadata-label">注解</span><div class="metadata-chips"><span v-for="[key,value] in annotations" :key="key" class="metadata-chip annotation-chip" :title="`${key}=${value}`">{{ key }}={{ value }}</span><span v-if="!annotations.length" class="metadata-empty">—</span></div></div>
   </div>
   <div class="metadata-controls"><button v-if="labels.length||annotations.length" type="button" :aria-expanded="metadataExpanded" @click="metadataExpanded=!metadataExpanded">{{ metadataExpanded?'收起标签与注解':'展开标签与注解' }}</button></div>
   <details v-if="settings.length||fields.length||object.spec?.selector||object.status?.conditions?.length" class="workload-strategy"><summary>状态、发布策略与选择器</summary><div class="workload-counts"><div v-for="field in fields" :key="field.key"><span>{{ field.label }}</span><KubeResourceCell :cell="cells[field.key]" /></div></div><div v-if="object.status?.conditions?.length" class="workload-conditions"><a-tooltip v-for="condition in object.status.conditions" :key="condition.type" :title="[condition.reason,condition.message].filter(Boolean).join(' · ')"><span :class="['condition',`condition-${workloadConditionTone(condition)}`]"><i />{{ condition.type }}<span>{{ condition.status||'—' }}</span></span></a-tooltip></div><dl class="strategy-facts"><div v-for="field in settings" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></div><div v-if="object.spec?.selector" class="workload-selector"><dt>{{ t('k8s.selectorShort') }}</dt><dd>{{ selectorText(object.spec.selector,t('k8s.allPods')) }}</dd></div></dl></details>
  </section>
  <KubeWorkloadEvents :events="events" :error="eventsError" :loading="eventsLoading" compact><template #actions><a-button size="small" type="link" @click="emit('events')">查看全部</a-button></template></KubeWorkloadEvents>
 </div>
</template>

<style scoped>
.workload-summary{display:grid;grid-template-columns:minmax(0,3fr) minmax(0,2fr);align-items:start;gap:14px;margin-bottom:14px}.workload-identity{min-width:0;padding:12px 14px;border:1px solid var(--edo-border);border-radius:5px;background:var(--edo-surface);color:var(--edo-text)}.workload-state{display:flex;align-items:center;gap:8px;min-height:24px;flex-wrap:wrap}.workload-state :deep(.ant-tag){margin-inline-end:0;font-size:11px}.state-reason{font-size:12px;overflow-wrap:anywhere;color:var(--edo-muted)}.resource-kind{margin-left:auto;color:var(--edo-muted);font-size:11px}.identity-facts{display:grid;grid-template-columns:minmax(65px,.7fr) minmax(100px,1.25fr) minmax(95px,.8fr) minmax(140px,1fr);gap:10px 14px;margin:10px 0 0}.identity-facts>div{min-width:0}dt{margin-bottom:4px;color:var(--edo-muted);font-size:11px}dd{margin:0;font-size:12px;line-height:1.6;overflow-wrap:anywhere}.identity-facts dd{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-variant-numeric:tabular-nums}.workload-counts{display:flex;flex-wrap:wrap;gap:8px 18px;margin-top:12px}.workload-counts>div{display:flex;gap:8px;align-items:center;color:var(--edo-muted);font-size:11px}.workload-counts :deep(.kube-ready-badge){padding:1px 6px;min-width:36px;font-size:12px;line-height:18px}.workload-conditions{display:flex;flex-wrap:wrap;gap:6px 12px;margin-top:10px}.condition{display:inline-flex;align-items:center;gap:5px;font-size:11px}.condition>span{color:var(--edo-muted)}.condition i{width:6px;height:6px;border-radius:50%;background:var(--edo-muted)}.condition-success i{background:var(--kube-success,#008566)}.condition-warning i{background:var(--kube-warning,#a96008)}.condition-error i{background:var(--kube-danger,#c83c52)}.workload-metadata{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:12px;margin-top:12px;padding-top:10px;border-top:1px solid var(--edo-border)}.metadata-group{display:grid;grid-template-columns:30px minmax(0,1fr);align-items:start;gap:7px;min-width:0}.metadata-label,.metadata-empty{color:var(--edo-muted);font-size:11px;line-height:22px}.metadata-chips{display:flex;flex-wrap:wrap;align-content:flex-start;gap:4px;min-width:0;max-height:48px;overflow:hidden}.metadata-chip{display:block;max-width:100%;padding:2px 6px;border:1px solid color-mix(in srgb,var(--edo-primary) 18%,var(--edo-border));border-radius:3px;color:var(--edo-text);background:color-mix(in srgb,var(--edo-primary) 5%,var(--edo-surface));font-size:11px;line-height:17px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.annotation-chip{border-color:var(--edo-border);background:var(--edo-surface-soft)}.expanded .metadata-chips{max-height:none;overflow:visible}.expanded .metadata-chip{white-space:pre-wrap;overflow-wrap:anywhere}.metadata-controls{display:flex;justify-content:flex-end;min-height:20px;margin-top:2px}.metadata-controls button{padding:0;border:0;background:none;cursor:pointer;color:var(--edo-primary);font:inherit;font-size:11px;line-height:20px}.metadata-controls button:focus-visible,.workload-strategy summary:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.workload-strategy{border-top:1px solid var(--edo-border);margin-top:6px;padding-top:8px;font-size:11px}.workload-strategy summary{color:var(--edo-muted);cursor:pointer;width:fit-content}.strategy-facts{display:flex;flex-wrap:wrap;gap:10px 20px;margin:12px 0 0}.strategy-facts>div{min-width:0;display:flex;gap:8px;align-items:baseline}.strategy-facts dt{margin:0}.strategy-facts dd{font-size:11px}.workload-selector{flex-basis:100%}@media(max-width:1150px){.workload-summary{grid-template-columns:minmax(0,1fr)}}@media(max-width:640px){.workload-summary{gap:10px}.workload-identity{padding:10px}.identity-facts{grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:8px 14px}.identity-facts dd{white-space:normal;overflow-wrap:anywhere}.workload-metadata{grid-template-columns:minmax(0,1fr)}.resource-kind{margin-left:0}.workload-counts{gap:8px 14px}.strategy-facts>div{display:block}}
.identity-compact{min-height:160px}.identity-inline{display:flex;flex-wrap:wrap;gap:8px 18px;margin:0}.identity-inline>div{display:flex;align-items:baseline;gap:8px;max-width:100%;min-width:0}.identity-inline dt{margin:0;white-space:nowrap}.identity-inline dd{font-weight:600;max-width:240px}.identity-compact .metadata-chips{max-height:23px}.identity-compact .expanded .metadata-chips{max-height:none}.identity-compact .workload-metadata{margin-top:10px;padding-top:9px}.identity-compact .metadata-controls{min-height:18px}.identity-compact .workload-strategy{margin-top:4px;padding-top:6px}.workload-summary :deep(.compact .event-table-scroll){height:110px}
@media(max-width:640px){.identity-compact{min-height:0}.identity-inline{display:grid;grid-template-columns:1fr 1fr}.identity-inline>div{display:block}.identity-inline dd{max-width:none}}
</style>
