<script setup lang="ts">
import {computed,nextTick,onBeforeUnmount,onMounted,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {init,use,type EChartsType} from 'echarts/core'
import {LineChart} from 'echarts/charts'
import {GridComponent,TooltipComponent} from 'echarts/components'
import {CanvasRenderer} from 'echarts/renderers'
import {RefreshCw} from 'lucide-vue-next'
import client from '@/api/client'
import {formatKubeCPU,formatKubeMemory,kubeErrorMessage,type KubeObject} from '@/api/kubernetes'
import {nodeChartPoints,nodeMetricValue,nodeSampleFresh,type NodeHistory} from '@/utils/kubernetesNode'
import {usePreferencesStore} from '@/stores/preferences'
import {formatDateTime} from '@/utils/time'
const props=defineProps<{cluster:string;node:KubeObject;autoRefresh:boolean;workloads:boolean}>()
const {t,locale}=useI18n(),preferences=usePreferencesStore()
use([LineChart,GridComponent,TooltipComponent,CanvasRenderer])
const root=ref<HTMLElement>(),history=ref<NodeHistory|null>(null),windowSeconds=ref(900),error=ref(''),loading=ref(false)
const charts=new Map<string,EChartsType>();let disposed=false,request:AbortController|undefined,revision=0,observer:ResizeObserver|undefined,timer:ReturnType<typeof setInterval>|undefined
const ranges=computed(()=>[900,3600,21600,86400,259200].map(value=>({value,label:t(`k8s.nodeWindow${value}`)})))
const now=ref(Date.now())
const latest=computed(()=>history.value?.points.at(-1))
const current=computed(()=>!error.value&&nodeSampleFresh(latest.value,now.value)?latest.value:undefined)
const cards=computed(()=>['cpu','memory','pods'].map(key=>{
 const sample=current.value,kind=key as 'cpu'|'memory'|'pods',percent=sample?nodeMetricValue(sample,kind):null
 const formatter=kind==='cpu'?formatKubeCPU:kind==='memory'?formatKubeMemory:(value:number|undefined)=>value===undefined?'—':String(value)
 const value=percent===null?undefined:sample?.[kind]??undefined,capacity=sample?.[kind==='cpu'?'cpu_allocatable':kind==='memory'?'memory_allocatable':'pod_capacity']??undefined
 return {key:kind,label:kind==='cpu'?'CPU':kind==='memory'?t('k8s.memoryUsage'):t('k8s.nodePodCount'),percent,value:formatter(value),capacity:formatter(capacity),requests:kind==='pods'?undefined:sample?.allocation?.[kind==='cpu'?'cpu_requests':'memory_requests'],limits:kind==='pods'?undefined:sample?.allocation?.[kind==='cpu'?'cpu_limits':'memory_limits'],formatter}
}))
async function load(){
 request?.abort();const controller=new AbortController();request=controller;const currentRevision=++revision;loading.value=true
 try{
  const {data}=await client.get<NodeHistory>(`/kubernetes/clusters/${encodeURIComponent(props.cluster)}/nodes/${encodeURIComponent(props.node.metadata.name)}/metrics`,{params:{uid:props.node.metadata.uid,window:windowSeconds.value},signal:controller.signal})
  if(!disposed&&currentRevision===revision){history.value=data;error.value=''}
 }catch(err){if(!disposed&&currentRevision===revision&&!controller.signal.aborted)error.value=kubeErrorMessage(err)}
 finally{if(!disposed&&currentRevision===revision){loading.value=false;void render()}}
}
async function render(){
 await nextTick();if(disposed||!root.value)return
 const style=getComputedStyle(root.value),text=style.getPropertyValue('--edo-muted').trim(),border=style.getPropertyValue('--edo-border').trim(),primary=style.getPropertyValue('--edo-primary').trim()
 for(const card of cards.value){
  const element=root.value.querySelector<HTMLElement>(`[data-chart="${card.key}"]`);if(!element?.clientWidth)continue
  let chart=charts.get(card.key);if(!chart){chart=init(element,undefined,{renderer:'canvas'});charts.set(card.key,chart)}
  chart.setOption({animation:false,grid:{left:45,right:14,top:18,bottom:30},tooltip:{trigger:'axis',renderMode:'richText',valueFormatter:(value:unknown)=>typeof value==='number'?`${value.toFixed(1)}%`:'—'},xAxis:{type:'time',min:history.value?Date.parse(history.value.from):undefined,max:history.value?Date.parse(history.value.to):undefined,axisLabel:{color:text,hideOverlap:true},axisLine:{lineStyle:{color:border}}},yAxis:{type:'value',min:0,max:(extent:{max:number})=>Math.max(100,Math.ceil(extent.max/10)*10),axisLabel:{formatter:'{value}%',color:text},splitLine:{lineStyle:{color:border}}},series:[{name:card.label,type:'line',smooth:false,connectNulls:false,showSymbol:true,symbolSize:3,itemStyle:{color:primary},lineStyle:{width:2},areaStyle:{opacity:.06},data:history.value?nodeChartPoints(history.value,card.key):[]}]},{notMerge:true});chart.resize()
 }
}
function refreshVisible(){now.value=Date.now();if(!document.hidden&&props.autoRefresh&&!loading.value)void load()}
watch(windowSeconds,()=>{history.value=null;void load()})
watch([()=>props.cluster,()=>props.node.metadata.uid],()=>{history.value=null;void load()})
watch([history,()=>preferences.theme,locale],()=>void render())
onMounted(()=>{void load();observer=new ResizeObserver(()=>void render());if(root.value)observer.observe(root.value);timer=setInterval(refreshVisible,30000);document.addEventListener('visibilitychange',refreshVisible)})
onBeforeUnmount(()=>{disposed=true;revision++;request?.abort();if(timer)clearInterval(timer);document.removeEventListener('visibilitychange',refreshVisible);observer?.disconnect();charts.forEach(chart=>chart.dispose())})
</script>
<template>
 <section ref="root" class="node-monitor resource-detail-section">
  <div class="monitor-toolbar"><div><h4>{{ t('k8s.nodeMonitoring') }}</h4><p>{{ t('k8s.nodeMonitoringHint') }}</p></div><label>{{ t('k8s.nodeTimeRange') }}<a-select v-model:value="windowSeconds" :options="ranges" /></label><a-button size="small" :loading="loading" @click="load"><RefreshCw :size="13" />{{ t('k8s.refresh') }}</a-button></div>
  <a-alert v-if="error" type="warning" show-icon :message="error" />
  <a-alert v-else-if="!loading&&!current" type="info" show-icon :message="t(latest?'k8s.nodeSampleStale':'k8s.nodeWaitingSamples')" />
  <a-alert v-else-if="current&&current.metrics_state!=='ok'" type="warning" show-icon :message="t(current.metrics_state==='forbidden'?'k8s.nodeMetricsForbidden':'k8s.metricsMissing')" />
  <a-alert v-if="!workloads||current&&current.pods_state!=='ok'" type="info" show-icon :message="t(!workloads?'k8s.nodePodsNoPermission':'k8s.nodePodsUnavailable')" />
  <div class="monitor-grid"><article v-for="card in cards" :key="card.key" class="monitor-card"><h4>{{ card.label }}<span>{{ card.percent===null?'—':`${card.percent.toFixed(1)}%` }}</span></h4><div class="monitor-value"><strong>{{ card.value }}</strong><span>/ {{ card.capacity }}</span></div><div class="monitor-meter" role="img" :aria-label="`${card.label} ${card.value} / ${card.capacity}`"><i :class="{high:card.percent!==null&&card.percent>=90}" :style="{width:`${Math.min(100,card.percent??0)}%`}" /></div><dl v-if="card.key!=='pods'"><div><dt>{{ t('k8s.resourceRequests') }}</dt><dd>{{ card.formatter(card.requests) }}</dd></div><div><dt>{{ t('k8s.nodeDeclaredLimits') }}</dt><dd>{{ card.formatter(card.limits) }}</dd></div></dl><p v-else>{{ t('k8s.nodePodsHint') }}</p><div :data-chart="card.key" class="node-trend" role="img" :aria-label="t('k8s.nodeTrendLabel',{name:card.label})" /></article></div>
  <p class="monitor-note">{{ t('k8s.nodeAllocationHint') }}</p><p class="monitor-note">{{ t('k8s.nodeMetricsScope') }}</p><p class="monitor-note">{{ t('k8s.statisticsAt') }}：{{ latest?formatDateTime(latest.collected_at):'—' }} · {{ t('k8s.nodeMetricsAt') }}：{{ current?.metrics_at?formatDateTime(current.metrics_at):'—' }} · {{ t(autoRefresh?'k8s.autoRefresh':'k8s.paused') }}</p>
 </section>
</template>
<style scoped>
h4{margin:0;font-size:14px}.monitor-toolbar{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-bottom:16px}.monitor-toolbar>div{flex:1;min-width:220px}.monitor-toolbar p,.monitor-note{font-size:12px;color:var(--edo-muted);margin:8px 0}.monitor-toolbar label{display:flex;align-items:center;gap:8px;font-size:12px}.monitor-toolbar :deep(.ant-select){width:130px}.monitor-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px;margin:16px 0}.monitor-card{min-width:0;border:1px solid var(--edo-border);border-radius:8px;background:var(--edo-surface);padding:16px}.monitor-card h4{display:flex;align-items:center;justify-content:space-between}.monitor-card h4 span{color:var(--edo-muted);font-size:13px}.monitor-value{display:flex;gap:8px;align-items:baseline;margin:14px 0 10px;flex-wrap:wrap}.monitor-value strong{font-size:22px}.monitor-value>span{color:var(--edo-muted);font-size:12px}.monitor-meter{height:6px;border-radius:4px;background:var(--edo-border);overflow:hidden}.monitor-meter i{display:block;height:100%;background:var(--edo-primary)}.monitor-meter i.high{background:#e5a11c}dl{display:flex;gap:18px;margin:12px 0;flex-wrap:wrap}dt{color:var(--edo-muted);font-size:12px}dd{margin:5px 0 0;font-size:13px}.monitor-card p{min-height:46px;font-size:12px;color:var(--edo-muted);line-height:1.8;margin:12px 0}.node-trend{height:190px;min-width:0}.monitor-note{line-height:1.8}.node-monitor :deep(.ant-alert){margin-bottom:10px}@media(max-width:950px){.monitor-grid{grid-template-columns:1fr}.monitor-card p{min-height:0}}
</style>
