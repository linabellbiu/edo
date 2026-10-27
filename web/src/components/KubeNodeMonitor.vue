<script setup lang="ts">
import {computed,nextTick,onBeforeUnmount,onMounted,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {init,use,type EChartsType} from 'echarts/core'
import {BarChart,LineChart} from 'echarts/charts'
import {GridComponent,MarkLineComponent,TooltipComponent} from 'echarts/components'
import {CanvasRenderer} from 'echarts/renderers'
import {RefreshCw} from 'lucide-vue-next'
import client from '@/api/client'
import {formatKubeCPU,formatKubeMemory,kubeErrorMessage,type KubeObject} from '@/api/kubernetes'
import {nodeChartPoints,nodeMetricValue,nodeSampleFresh,type NodeHistory} from '@/utils/kubernetesNode'
import {usePreferencesStore} from '@/stores/preferences'
import KubeSelect from './KubeSelect.vue'
const props=defineProps<{cluster:string;node:KubeObject;autoRefresh:boolean;workloads:boolean;embedded?:boolean;refreshToken?:number}>()
const {t,locale}=useI18n(),preferences=usePreferencesStore()
use([BarChart,LineChart,GridComponent,MarkLineComponent,TooltipComponent,CanvasRenderer])
const root=ref<HTMLElement>(),history=ref<NodeHistory|null>(null),windowSeconds=ref(900),error=ref(''),loading=ref(false)
const charts=new Map<string,EChartsType>();let disposed=false,request:AbortController|undefined,revision=0,observer:ResizeObserver|undefined,timer:ReturnType<typeof setInterval>|undefined
const ranges=computed(()=>[900,3600,21600,86400,259200].map(value=>({value,label:t(`k8s.nodeWindow${value}`)})))
const now=ref(Date.now())
const latest=computed(()=>history.value?.points.at(-1))
const current=computed(()=>!error.value&&nodeSampleFresh(latest.value,now.value)?latest.value:undefined)
const amount=(value:unknown):number|null=>typeof value==='number'&&Number.isFinite(value)&&value>=0?value:null
const cards=computed(()=>['cpu','memory','pods'].map(key=>{
 const sample=current.value,kind=key as 'cpu'|'memory'|'pods',percent=sample&&(kind!=='pods'||props.workloads)?nodeMetricValue(sample,kind):null
 const formatter=kind==='cpu'?formatKubeCPU:kind==='memory'?formatKubeMemory:(value:number|undefined)=>value===undefined?'—':String(value)
 const value=percent===null?undefined:sample?.[kind]??undefined,capacity=sample?.[kind==='cpu'?'cpu_allocatable':kind==='memory'?'memory_allocatable':'pod_capacity']??undefined
 const rows=[{key:'used',label:t('k8s.nodeUsed'),value:amount(value)},{key:'capacity',label:t('k8s.nodeAllocatable'),value:amount(capacity)},...(kind==='pods'?[]:[{key:'requests',label:t('k8s.resourceRequests'),value:props.workloads?amount(sample?.allocation?.[kind==='cpu'?'cpu_requests':'memory_requests']):null},{key:'limits',label:t('k8s.nodeDeclaredLimits'),value:props.workloads?amount(sample?.allocation?.[kind==='cpu'?'cpu_limits':'memory_limits']):null}])].map(row=>({...row,text:formatter(row.value??undefined)}))
 const label=kind==='cpu'?'CPU':kind==='memory'?t('k8s.nodeMemory'):t('k8s.nodePodCount')
 const scale=kind==='memory'?(Math.max(...rows.map(row=>row.value??0))>=1024**3?1024**3:1024**2):1
 return {key:kind,label,percent,capacity:amount(capacity),scale,rows,formatter,description:`${label}: ${rows.map(row=>`${row.label} ${row.text}`).join('; ')}`}
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
 const style=getComputedStyle(root.value),text=style.getPropertyValue('--kube-muted').trim(),border=style.getPropertyValue('--kube-border').trim(),primary=style.getPropertyValue('--kube-link').trim()
 const tones:Record<string,string>={used:primary,capacity:text,requests:style.getPropertyValue('--kube-chart-requests').trim(),limits:style.getPropertyValue('--kube-chart-limits').trim()}
 const tooltipStyle={textStyle:{fontSize:12,color:text},backgroundColor:style.getPropertyValue('--kube-surface').trim(),borderColor:border}
 for(const card of cards.value){
  const comparison=root.value.querySelector<HTMLElement>(`[data-comparison="${card.key}"]`)
  if(comparison?.clientWidth){
   const key=`${card.key}-comparison`;let chart=charts.get(key);if(!chart){chart=init(comparison,undefined,{renderer:'canvas'});charts.set(key,chart)}
   chart.setOption({animation:false,grid:{left:96,right:84,top:14,bottom:36},tooltip:{trigger:'axis',axisPointer:{type:'shadow'},renderMode:'richText',...tooltipStyle,valueFormatter:(value:unknown)=>card.formatter(typeof value==='number'?value*card.scale:undefined)},xAxis:{type:'value',min:0,minInterval:card.key==='pods'?1:undefined,splitNumber:2,axisLabel:{color:text,fontSize:12,hideOverlap:true,formatter:(value:number)=>value===0?'0':card.key==='memory'?`${value<.01?'<0.01':Number(value.toFixed(2))} ${card.scale===1024**3?'GiB':'MiB'}`:card.formatter(value)},splitLine:{show:false}},yAxis:{type:'category',inverse:true,data:card.rows.map(row=>row.value===null?`${row.label} —`:row.label),axisLabel:{color:text,fontSize:12,width:80,overflow:'truncate'},axisLine:{show:false},axisTick:{show:false}},series:[{type:'bar',barWidth:12,label:{show:true,position:'right',color:text,fontSize:12,distance:6},itemStyle:{borderRadius:[0,3,3,0]},data:card.rows.map(row=>({value:row.value===null?null:row.value/card.scale,itemStyle:{color:tones[row.key]},label:{formatter:row.text}})),markLine:card.capacity!==null&&card.capacity>0?{silent:true,symbol:'none',label:{show:false},lineStyle:{color:text,opacity:.35,type:'dashed'},data:[{xAxis:card.capacity/card.scale}]}:undefined}]},{notMerge:true});chart.resize()
  }
  const element=root.value.querySelector<HTMLElement>(`[data-chart="${card.key}"]`);if(!element?.clientWidth)continue
  let chart=charts.get(card.key);if(!chart){chart=init(element,undefined,{renderer:'canvas'});charts.set(card.key,chart)}
  chart.setOption({animation:false,grid:{left:54,right:18,top:18,bottom:36},tooltip:{trigger:'axis',renderMode:'richText',...tooltipStyle,valueFormatter:(value:unknown)=>typeof value==='number'?`${value.toFixed(1)}%`:'—'},xAxis:{type:'time',min:history.value?Date.parse(history.value.from):undefined,max:history.value?Date.parse(history.value.to):undefined,axisLabel:{color:text,fontSize:12,hideOverlap:true},axisLine:{lineStyle:{color:border}}},yAxis:{type:'value',min:0,max:(extent:{max:number})=>Math.max(100,Math.ceil(extent.max/10)*10),axisLabel:{formatter:'{value}%',color:text,fontSize:12},splitLine:{lineStyle:{color:border}}},series:[{name:card.label,type:'line',smooth:false,connectNulls:false,showSymbol:true,symbolSize:3,itemStyle:{color:primary},lineStyle:{width:2},areaStyle:{opacity:.06},data:history.value&&(card.key!=='pods'||props.workloads)?nodeChartPoints(history.value,card.key):[]}]},{notMerge:true});chart.resize()
 }
}
function refreshVisible(){now.value=Date.now();if(!document.hidden&&props.autoRefresh&&!loading.value)void load()}
watch(windowSeconds,()=>{history.value=null;void load()})
watch([()=>props.cluster,()=>props.node.metadata.uid],()=>{history.value=null;void load()})
watch([history,cards,()=>preferences.theme,locale],()=>void render())
watch(()=>props.refreshToken,()=>void load())
watch(()=>props.autoRefresh,(enabled,previous)=>{if(enabled&&!previous)void load()})
onMounted(()=>{void load();observer=new ResizeObserver(()=>void render());if(root.value)observer.observe(root.value);timer=setInterval(refreshVisible,30000);document.addEventListener('visibilitychange',refreshVisible)})
onBeforeUnmount(()=>{disposed=true;revision++;request?.abort();if(timer)clearInterval(timer);document.removeEventListener('visibilitychange',refreshVisible);observer?.disconnect();charts.forEach(chart=>chart.dispose())})
</script>
<template>
 <section ref="root" class="node-monitor resource-detail-section" :aria-busy="loading">
  <div class="monitor-toolbar"><h4 v-if="!embedded">{{ t('k8s.nodeMonitoring') }}</h4><label>{{ t('k8s.nodeTimeRange') }}<KubeSelect v-model:value="windowSeconds" :aria-label="t('k8s.nodeTimeRange')" :options="ranges" /></label><a-button v-if="!embedded" size="small" :loading="loading" @click="load"><RefreshCw aria-hidden="true" :size="14" />{{ t('k8s.refresh') }}</a-button></div>
  <a-alert v-if="error" type="warning" show-icon :message="error" />
  <a-alert v-else-if="!loading&&!current" type="info" show-icon :message="t(latest?'k8s.nodeSampleStale':'k8s.nodeWaitingSamples')" />
  <a-alert v-else-if="current&&current.metrics_state!=='ok'" type="warning" show-icon :message="t(current.metrics_state==='forbidden'?'k8s.nodeMetricsForbidden':'k8s.metricsMissing')" />
  <a-alert v-if="!workloads||current&&current.pods_state!=='ok'" type="info" show-icon :message="t(!workloads?'k8s.nodePodsNoPermission':'k8s.nodePodsUnavailable')" />
  <div class="monitor-grid"><article v-for="card in cards" :key="card.key" class="monitor-card"><h4>{{ card.label }}</h4><div :data-comparison="card.key" class="node-comparison" role="img" :aria-label="card.description" /><h5>{{ t('k8s.nodeUtilizationTrend') }}</h5><div :data-chart="card.key" class="node-trend" role="img" :aria-label="t('k8s.nodeTrendLabel',{name:card.label})" /></article></div>
 </section>
</template>
<style scoped>
.node-monitor{container-type:inline-size;color:var(--kube-text)}
h4{margin:0;font-size:14px;font-weight:600;line-height:1.5}
.monitor-toolbar{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:12px}.monitor-toolbar>h4{flex:1;min-width:140px}.monitor-toolbar label{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--kube-muted)}.monitor-toolbar :deep(.kube-select){width:130px}
.monitor-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:12px}
.monitor-card{min-width:0;border:1px solid var(--kube-border);border-radius:6px;background:var(--kube-surface);padding:12px}.monitor-card>h4{font-size:13px}.monitor-card h5{font-size:12px;font-weight:500;color:var(--kube-muted);margin:8px 0 0;padding-top:10px;border-top:1px solid var(--kube-border)}
.node-comparison{height:200px;min-width:0}.node-trend{height:184px;min-width:0}.node-monitor :deep(.ant-alert){margin-bottom:10px}
@container(max-width:960px){.monitor-grid{grid-template-columns:1fr}}
@media(max-width:950px){.monitor-grid{grid-template-columns:1fr}}
</style>
