<script setup lang="ts">
import {computed} from 'vue'
import {useI18n} from 'vue-i18n'
import {type KubeObject,type KubeType} from '@/api/kubernetes'
import {resourceCells,resourceColumns,resourceStatus,selectorText} from '@/utils/kubernetesPresentation'
import {workloadConditionTone} from '@/utils/kubernetesWorkloadExplorer'
import {formatDateTime} from '@/utils/time'
import KubeResourceCell from './KubeResourceCell.vue'

const props=defineProps<{object:KubeObject;type:KubeType;events:KubeObject[];eventsError:boolean}>()
const emit=defineEmits<{events:[]}>()
const {t}=useI18n()
const cells=computed(()=>resourceCells(props.object,t))
const status=computed(()=>resourceStatus(props.object,t))
const fields=computed(()=>resourceColumns(props.type,t).filter(field=>['replicas','updatedReplicas','availableReplicas','currentReplicas','completions','activeJobs','restarts'].includes(field.key)))
const annotations=computed(()=>Object.entries(props.object.metadata.annotations??{}).filter(([key])=>key!=='kubectl.kubernetes.io/last-applied-configuration'))
const warnings=computed(()=>props.events.filter(event=>event.type==='Warning').length)
const latest=computed(()=>props.events[0])
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
  <section class="workload-card workload-identity">
   <div class="workload-state"><a-tag :color="status.tone">{{ status.text }}</a-tag><span v-if="status.reason" class="muted">{{ status.reason }}</span><span class="muted">{{ object.kind }} · {{ object.apiVersion }}</span></div>
   <dl class="identity-facts"><div><dt>{{ t('k8s.namespace') }}</dt><dd>{{ object.metadata.namespace||'—' }}</dd></div><div><dt>{{ t('k8s.age') }}</dt><dd>{{ formatDateTime(object.metadata.creationTimestamp) }}</dd></div><div><dt>{{ t('k8s.resourceVersion') }}</dt><dd>{{ object.metadata.resourceVersion||'—' }}</dd></div></dl>
   <div class="workload-counts"><div v-for="field in fields" :key="field.key"><span>{{ field.label }}</span><KubeResourceCell :cell="cells[field.key]" /></div></div>
   <details v-if="Object.keys(object.metadata.labels??{}).length||annotations.length" class="workload-metadata"><summary>{{ t('k8s.labels') }} / {{ t('k8s.annotations') }} <span class="muted">{{ Object.keys(object.metadata.labels??{}).length }} / {{ annotations.length }}</span></summary><div class="workload-labels"><a-tag v-for="(value,key) in object.metadata.labels??{}" :key="key">{{ key }}={{ value }}</a-tag></div><dl class="annotation-facts"><div v-for="[key,value] in annotations" :key="key"><dt>{{ key }}</dt><dd>{{ value }}</dd></div></dl></details>
  </section>
  <section class="workload-card workload-health">
   <div class="workload-section-heading"><h4>{{ t('k8s.events') }}</h4><a-button size="small" type="link" @click="emit('events')">{{ t('k8s.workloadAllEvents') }}<a-tag v-if="warnings" color="warning">{{ warnings }}</a-tag></a-button></div>
   <a-alert v-if="eventsError" type="warning" :message="t('k8s.eventsUnavailable')" />
   <template v-else-if="latest"><div class="latest-event"><a-tag :color="latest.type==='Warning'?'warning':'default'">{{ latest.reason }}</a-tag><span class="muted">{{ formatDateTime(latest.lastTimestamp??latest.eventTime??latest.metadata?.creationTimestamp) }}</span></div><p class="event-message">{{ latest.message }}</p></template>
   <p v-else class="muted quiet-empty">{{ t('k8s.noEvents') }}</p>
   <div v-if="object.status?.conditions?.length" class="workload-conditions"><a-tooltip v-for="condition in object.status.conditions" :key="condition.type" :title="[condition.reason,condition.message].filter(Boolean).join(' · ')"><span :class="['condition',`condition-${workloadConditionTone(condition)}`]"><i />{{ condition.type }}<span class="muted">{{ condition.status }}</span></span></a-tooltip></div>
  </section>
 </div>
 <section v-if="settings.length||object.spec?.selector" class="workload-card workload-strategy">
  <dl class="strategy-facts"><div v-for="field in settings" :key="field.label"><dt>{{ field.label }}</dt><dd>{{ field.value }}</dd></div><div v-if="object.spec?.selector" class="workload-selector"><dt>{{ t('k8s.selectorShort') }}</dt><dd>{{ selectorText(object.spec.selector,t('k8s.allPods')) }}</dd></div></dl>
 </section>
</template>

<style scoped>
.workload-summary{display:grid;grid-template-columns:minmax(0,1.45fr) minmax(0,1fr);gap:14px;margin-bottom:14px}.workload-card{min-width:0;padding:16px;background:var(--edo-surface);border:1px solid var(--edo-border);border-radius:8px;color:var(--edo-text)}.workload-state{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.workload-state>.muted:last-child{margin-left:auto}.muted,dt{color:var(--edo-muted);font-size:12px}.identity-facts,.strategy-facts{display:flex;gap:16px 28px;flex-wrap:wrap;margin:14px 0 0}.identity-facts>div,.strategy-facts>div{min-width:0}dt{margin-bottom:4px}dd{margin:0;font-size:13px;overflow-wrap:anywhere}.workload-counts{display:flex;flex-wrap:wrap;gap:10px 24px;margin-top:14px}.workload-counts>div{display:flex;gap:10px;align-items:center;font-size:12px;color:var(--edo-muted)}.workload-metadata{border-top:1px solid var(--edo-border);margin-top:14px;padding-top:10px;font-size:12px}.workload-metadata summary{cursor:pointer}.workload-labels{display:flex;flex-wrap:wrap;gap:6px;margin-top:10px}.workload-labels :deep(.ant-tag){white-space:normal;overflow-wrap:anywhere;margin:0}.annotation-facts{display:grid;gap:8px;margin:10px 0 0}.annotation-facts>div{display:grid;grid-template-columns:minmax(160px,1fr) minmax(0,2fr);gap:10px}.annotation-facts dt{overflow-wrap:anywhere}.workload-section-heading{display:flex;align-items:center;justify-content:space-between;gap:8px}.workload-section-heading h4{font-size:13px;margin:0}.latest-event{display:flex;align-items:center;flex-wrap:wrap;gap:8px;margin-top:12px}.event-message{font-size:13px;line-height:1.6;overflow-wrap:anywhere;margin:10px 0}.quiet-empty{padding:10px 0}.workload-conditions{display:flex;flex-wrap:wrap;gap:8px;border-top:1px solid var(--edo-border);padding-top:12px;margin-top:12px}.condition{display:inline-flex;align-items:center;gap:6px;font-size:12px}.condition i{width:7px;height:7px;border-radius:50%;background:var(--edo-muted)}.condition-success i{background:#059b75}.condition-warning i{background:#d69222}.workload-strategy{margin-bottom:14px;padding:12px 16px}.strategy-facts{margin:0;gap:10px 24px}.strategy-facts>div{display:flex;gap:8px;align-items:baseline}.strategy-facts dt{margin:0}.workload-selector{flex:1;min-width:200px!important}.workload-metadata summary:focus-visible{outline:2px solid var(--edo-primary);outline-offset:4px}@media(max-width:900px){.workload-summary{grid-template-columns:1fr}.annotation-facts>div{grid-template-columns:1fr}.workload-state>.muted:last-child{margin-left:0}}
</style>
