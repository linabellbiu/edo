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
   <details v-if="settings.length||fields.length||object.spec?.selector||object.status?.conditions?.length" class="workload-strategy"><summary>状态、发布策略与选择器</summary><div class="workload-counts"><div v-for="field in fields" :key="field.key"><span>{{ field.label }}</span><KubeResourceCell :cell="cells[field.key]" /></div></div><div v-if="object.status?.conditions?.length" class="workload-conditions"><a-tooltip v-for="condition in object.status.conditions" :key="condition.type" :trigger="['hover','focus','click']" :title="[condition.reason,condition.message].filter(Boolean).join(' · ')"><span tabindex="0" :class="['condition',`condition-${workloadConditionTone(condition)}`]"><i />{{ condition.type }}<span>{{ condition.status||'—' }}</span></span></a-tooltip></div><dl class="strategy-facts"><div v-for="field in settings" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></div><div v-if="object.spec?.selector" class="workload-selector"><dt>{{ t('k8s.selectorShort') }}</dt><dd>{{ selectorText(object.spec.selector,t('k8s.allPods')) }}</dd></div></dl></details>
  </section>
  <KubeWorkloadEvents :events="events" :error="eventsError" :loading="eventsLoading" compact><template #actions><a-button size="small" type="link" @click="emit('events')">查看全部</a-button></template></KubeWorkloadEvents>
 </div>
</template>

<style scoped>
.workload-summary { display: grid; grid-template-columns: minmax(0,3fr) minmax(0,2fr); align-items: start; gap: 12px; margin-bottom: 12px; }
.workload-identity { min-width: 0; padding: 10px 12px; border: 1px solid var(--workload-border); border-radius: 4px; background: var(--workload-column); color: var(--workload-text); }
.identity-facts { display: flex; align-items: baseline; flex-wrap: wrap; gap: 8px 18px; margin: 0; padding: 0 0 9px; border-bottom: 1px solid var(--workload-border); }
.identity-facts>div { display: flex; align-items: baseline; gap: 7px; max-width: 100%; min-width: 0; }
dt { margin: 0; color: var(--workload-muted); font-size: 12px; white-space: nowrap; }
dd { margin: 0; font-size: 13px; line-height: 1.6; overflow-wrap: anywhere; }
.identity-facts dd { max-width: 360px; font-weight: 600; font-variant-numeric: tabular-nums; }
.workload-metadata { display: grid; grid-template-columns: minmax(0,1fr) minmax(0,1fr); gap: 12px; margin-top: 9px; }
.metadata-group { display: grid; grid-template-columns: 30px minmax(0,1fr); align-items: start; gap: 7px; min-width: 0; }
.metadata-label,.metadata-empty { color: var(--workload-muted); font-size: 12px; line-height: 22px; }
.metadata-chips { display: flex; flex-wrap: wrap; align-content: flex-start; gap: 4px; min-width: 0; max-height: 46px; overflow: hidden; }
.metadata-chip { display: block; max-width: 100%; padding: 1px 5px; overflow: hidden; border: 1px solid var(--workload-idle-border); border-radius: 2px; background: var(--workload-stripe); color: var(--workload-text); font-size: 12px; line-height: 18px; text-overflow: ellipsis; white-space: nowrap; }
.identity-compact .metadata-chips { max-height: 22px; }
.expanded .metadata-chips,.identity-compact .expanded .metadata-chips { max-height: none; overflow: visible; }
.expanded .metadata-chip { white-space: pre-wrap; overflow-wrap: anywhere; }
.metadata-controls { display: flex; justify-content: flex-end; min-height: 18px; margin-top: 3px; }
.metadata-controls button { min-height: 24px; padding: 0; border: 0; background: none; color: var(--workload-link); font: inherit; font-size: 12px; line-height: 20px; cursor: pointer; }
.metadata-controls button:hover { text-decoration: underline; text-underline-offset: 3px; }
.metadata-controls button:focus-visible,.workload-strategy summary:focus-visible,.condition:focus-visible { outline: 2px solid var(--workload-focus); outline-offset: 2px; }
.workload-strategy { margin-top: 5px; padding-top: 6px; border-top: 1px solid var(--workload-border); font-size: 12px; }
.workload-strategy summary { min-height: 24px; width: fit-content; color: var(--workload-link); cursor: pointer; }
.workload-counts { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 18px; margin-top: 10px; }
.workload-counts>div { display: flex; align-items: center; gap: 7px; color: var(--workload-muted); font-size: 12px; }
.workload-counts :deep(.kube-ready-badge) { min-width: 36px; padding: 1px 6px; font-size: 12px; line-height: 18px; }
.workload-conditions { display: flex; flex-wrap: wrap; gap: 6px 12px; margin-top: 10px; }
.condition { display: inline-flex; align-items: center; gap: 5px; min-height: 24px; font-size: 12px; }
.condition>span { color: var(--workload-muted); }
.condition i { width: 6px; height: 6px; border-radius: 50%; background: var(--workload-muted); }
.condition-success i { background: var(--workload-ready-text); }
.condition-warning i { background: var(--workload-warning-text); }
.condition-error i { background: var(--workload-error-text); }
.strategy-facts { display: flex; flex-wrap: wrap; gap: 10px 20px; margin: 10px 0 0; }
.strategy-facts>div { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.strategy-facts dd { font-size: 12px; }
.workload-selector { flex-basis: 100%; }
@media(max-width:1150px) { .workload-summary { grid-template-columns: minmax(0,1fr); } }
@media(max-width:640px) { .workload-summary { gap: 10px; } .workload-identity { padding: 10px; } .identity-facts { display: grid; grid-template-columns: minmax(0,1fr) minmax(0,1fr); gap: 8px 14px; } .identity-facts>div { display: block; } .identity-facts dd { max-width: none; white-space: normal; overflow-wrap: anywhere; } .workload-metadata { grid-template-columns: minmax(0,1fr); } .strategy-facts>div { display: block; } }
</style>
