<script setup lang="ts">
import {computed,nextTick,onBeforeUnmount,onMounted,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {init,use,type EChartsType} from 'echarts/core'
import {PieChart,LineChart} from 'echarts/charts'
import {GridComponent,TooltipComponent} from 'echarts/components'
import {CanvasRenderer} from 'echarts/renderers'
import {RefreshCw} from 'lucide-vue-next'
import client from '@/api/client'
import {formatKubeCPU,formatKubeMemory,kubeErrorMessage,type KubeObject} from '@/api/kubernetes'
import {nodeChartPoints,nodeMetricValue,nodeSampleFresh,type NodeHistory} from '@/utils/kubernetesNode'
import {usePreferencesStore} from '@/stores/preferences'
import KubeSelect from './KubeSelect.vue'
const props=defineProps<{cluster:string;node:KubeObject;autoRefresh:boolean;workloads:boolean;embedded?:boolean;refreshToken?:number}>()
const {t,locale}=useI18n(),preferences=usePreferencesStore()
use([PieChart,LineChart,GridComponent,TooltipComponent,CanvasRenderer])
const root=ref<HTMLElement>(),history=ref<NodeHistory|null>(null),windowSeconds=ref(900),error=ref(''),loading=ref(false)
const charts=new Map<string,EChartsType>();let disposed=false,request:AbortController|undefined,revision=0,observer:ResizeObserver|undefined,timer:ReturnType<typeof setInterval>|undefined,resizeFrame:number|undefined
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
 const used=rows[0].value,allocatable=amount(capacity),remaining=percent!==null&&used!==null&&allocatable!==null?Math.max(0,allocatable-used):null
 const parts=[rows[0],{key:'remaining',label:t('k8s.available'),value:remaining,text:formatter(remaining??undefined)}]
 const percentText=percent===null?'—':`${percent>0&&percent<.1?'<0.1':Number(percent.toFixed(1))}%`,overCapacity=percent!==null&&percent>100
 return {key:kind,label,percent,percentText,overCapacity,rows,parts,formatter,description:`${label}: ${percentText}; ${[...rows,parts[1]].map(row=>`${row.label} ${row.text}`).join('; ')}${overCapacity?`; ${t('k8s.nodeOverAllocatable')}`:''}`}
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
 const style=getComputedStyle(root.value),text=style.getPropertyValue('--kube-muted').trim(),border=style.getPropertyValue('--kube-border').trim(),primary=style.getPropertyValue('--kube-chart-usage').trim(),remaining=style.getPropertyValue('--kube-chart-remaining').trim(),surface=style.getPropertyValue('--kube-surface').trim(),danger=style.getPropertyValue('--kube-danger').trim()
 const tooltipStyle={textStyle:{fontSize:12,color:text},backgroundColor:style.getPropertyValue('--kube-surface').trim(),borderColor:border}
 for(const card of cards.value){
  const comparison=root.value.querySelector<HTMLElement>(`[data-comparison="${card.key}"]`)
  if(comparison?.clientWidth){
   const key=`${card.key}-comparison`;let chart=charts.get(key);if(!chart){chart=init(comparison,undefined,{renderer:'canvas'});charts.set(key,chart)}
   chart.setOption({animation:false,tooltip:{trigger:'item',renderMode:'richText',...tooltipStyle,formatter:(item:{name:string;value:number})=>`${item.name}\n${card.formatter(item.value)}`},series:[{type:'pie',radius:'88%',center:['50%','50%'],silent:card.percent===null,selectedMode:false,stillShowZeroSum:false,label:{show:false},labelLine:{show:false},emphasis:{scale:false},showEmptyCircle:true,emptyCircleStyle:{color:style.getPropertyValue('--kube-surface-soft').trim(),borderColor:border,borderWidth:1},itemStyle:{borderColor:surface,borderWidth:2},data:card.percent===null?[]:card.parts.map(part=>({name:part.label,value:part.value,itemStyle:{color:part.key==='used'?(card.overCapacity?danger:primary):remaining}}))}]},{notMerge:true});chart.resize()
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
onMounted(()=>{
 void load();let lastWidth:number|undefined
 observer=new ResizeObserver(entries=>{
  const width=entries[0]?.contentRect.width;if(width===lastWidth)return;lastWidth=width
  if(resizeFrame===undefined)resizeFrame=requestAnimationFrame(()=>{resizeFrame=undefined;void render()})
 })
 if(root.value)observer.observe(root.value);timer=setInterval(refreshVisible,30000);document.addEventListener('visibilitychange',refreshVisible)
})
onBeforeUnmount(()=>{disposed=true;revision++;request?.abort();if(timer)clearInterval(timer);document.removeEventListener('visibilitychange',refreshVisible);observer?.disconnect();if(resizeFrame!==undefined)cancelAnimationFrame(resizeFrame);charts.forEach(chart=>chart.dispose())})
</script>
<template>
 <section ref="root" class="node-monitor resource-detail-section" :aria-busy="loading">
  <div class="monitor-toolbar"><h4 v-if="!embedded">{{ t('k8s.nodeMonitoring') }}</h4><label>{{ t('k8s.nodeTimeRange') }}<KubeSelect v-model:value="windowSeconds" :aria-label="t('k8s.nodeTimeRange')" :options="ranges" /></label><a-button v-if="!embedded" size="small" :loading="loading" @click="load"><RefreshCw aria-hidden="true" :size="14" />{{ t('k8s.refresh') }}</a-button></div>
  <a-alert v-if="error" type="warning" show-icon :message="error" />
  <a-alert v-else-if="!loading&&!current" type="info" show-icon :message="t(latest?'k8s.nodeSampleStale':'k8s.nodeWaitingSamples')" />
  <a-alert v-else-if="current&&current.metrics_state!=='ok'" type="warning" show-icon :message="t(current.metrics_state==='forbidden'?'k8s.nodeMetricsForbidden':'k8s.metricsMissing')" />
  <a-alert v-if="!workloads||current&&current.pods_state!=='ok'" type="info" show-icon :message="t(!workloads?'k8s.nodePodsNoPermission':'k8s.nodePodsUnavailable')" />
  <div class="monitor-grid">
   <article v-for="card in cards" :key="card.key" class="monitor-card">
    <header class="monitor-card-heading"><h4>{{ card.label }}</h4><div class="usage-ratio"><strong :class="{ 'is-over-capacity':card.overCapacity }">{{ card.percentText }}</strong><span v-if="card.overCapacity" class="is-over-capacity">{{ t('k8s.nodeOverAllocatable') }}</span></div></header>
    <div class="usage-summary">
     <div class="usage-pie"><div :data-comparison="card.key" class="node-comparison" role="img" :aria-label="card.description" /><span v-if="card.percent===null" class="pie-empty" aria-hidden="true">—</span></div>
     <dl class="usage-legend"><div v-for="part in card.parts" :key="part.key"><dt><i :class="['usage-dot',`usage-dot-${part.key}`,{ 'is-over-capacity':card.overCapacity&&part.key==='used' }]" aria-hidden="true" />{{ part.label }}</dt><dd>{{ part.text }}</dd></div></dl>
    </div>
    <dl class="node-values"><div v-for="row in card.rows.slice(1)" :key="row.key"><dt>{{ row.label }}</dt><dd>{{ row.text }}</dd></div></dl>
    <h5>{{ t('k8s.nodeUtilizationTrend') }}</h5><div :data-chart="card.key" class="node-trend" role="img" :aria-label="t('k8s.nodeTrendLabel',{name:card.label})" />
   </article>
  </div>
 </section>
</template>
<style scoped>
.node-monitor{container-type:inline-size;color:var(--kube-text)}
h4{margin:0;font-size:14px;font-weight:600;line-height:1.5}
.monitor-toolbar{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:12px}.monitor-toolbar>h4{flex:1;min-width:140px}.monitor-toolbar label{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--kube-muted)}.monitor-toolbar :deep(.kube-select){width:130px}
.monitor-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:12px}
.monitor-card{min-width:0;border:1px solid var(--kube-border);border-radius:6px;background:var(--kube-surface);padding:12px}.monitor-card-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:12px;min-height:34px}.monitor-card-heading h4{font-size:13px}.usage-ratio{text-align:right;font-variant-numeric:tabular-nums}.usage-ratio strong{display:block;font-size:22px;line-height:1.2;font-weight:600}.usage-ratio>span{display:block;font-size:12px;margin-top:4px}.is-over-capacity{color:var(--kube-danger)}.monitor-card h5{font-size:12px;font-weight:500;color:var(--kube-muted);margin:8px 0 0;padding-top:10px;border-top:1px solid var(--kube-border)}
.usage-summary{display:grid;grid-template-columns:128px minmax(0,1fr);align-items:center;gap:14px;margin:6px 0 10px}.usage-pie{position:relative;min-width:0}.node-comparison{height:140px;min-width:0}.pie-empty{position:absolute;inset:0;display:grid;place-items:center;color:var(--kube-muted);font-size:20px;pointer-events:none}
dl{margin:0;font-size:12px;font-variant-numeric:tabular-nums}dt{color:var(--kube-muted)}dd{margin:0;color:var(--kube-text);overflow-wrap:anywhere}.usage-legend{display:grid;gap:14px}.usage-legend dt{display:flex;align-items:center;gap:6px}.usage-legend dd{margin:3px 0 0 14px;font-size:14px;font-weight:500}.usage-dot{width:8px;height:8px;border-radius:50%;flex-shrink:0;background:var(--kube-chart-usage)}.usage-dot-remaining{background:var(--kube-chart-remaining);border:1px solid var(--kube-muted)}.usage-dot-used.is-over-capacity{background:var(--kube-danger)}.node-values{min-height:96px}.node-values>div{display:flex;justify-content:space-between;gap:12px;padding:6px 0}.node-values dd{text-align:right}.node-trend{height:184px;min-width:0}.node-monitor :deep(.ant-alert){margin-bottom:10px}
@container(max-width:960px){.monitor-grid{grid-template-columns:1fr}}
@media(max-width:950px){.monitor-grid{grid-template-columns:1fr}}
</style>
