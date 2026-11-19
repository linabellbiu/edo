<script setup lang="ts">
import {computed,nextTick,onBeforeUnmount,onMounted,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {init,use,type EChartsType} from 'echarts/core'
import {LineChart} from 'echarts/charts'
import {GridComponent,TooltipComponent} from 'echarts/components'
import {CanvasRenderer} from 'echarts/renderers'
import {RefreshCw} from 'lucide-vue-next'
import client from '@/api/client'
import {formatKubeCPU,formatKubeMemory,kubeErrorMessage,kubeSnapshotPending,readKubeSnapshotMeta,type KubeObject,type KubeSnapshotMeta} from '@/api/kubernetes'
import {nodeChartPoints,nodeMetricValue,nodeResourceValue,nodeSampleFresh,type NodeHistory,type NodeMeasure} from '@/utils/kubernetesNode'
import {usePreferencesStore} from '@/stores/preferences'
import {formatDateTime} from '@/utils/time'
import KubeSelect from './KubeSelect.vue'
const props=defineProps<{cluster:string;node:KubeObject;autoRefresh:boolean;workloads:boolean;embedded?:boolean;refreshToken?:number}>()
const {t,locale}=useI18n(),preferences=usePreferencesStore()
use([LineChart,GridComponent,TooltipComponent,CanvasRenderer])
const root=ref<HTMLElement>(),history=ref<NodeHistory|null>(null),windowSeconds=ref(900),error=ref(''),loading=ref(false)
const snapshot=ref<KubeSnapshotMeta>({}),snapshotPending=ref(false)
const charts=new Map<string,EChartsType>();let disposed=false,request:AbortController|undefined,revision=0,observer:ResizeObserver|undefined,timer:ReturnType<typeof setInterval>|undefined,resizeFrame:number|undefined
const ranges=computed(()=>[900,3600,21600,86400,259200].map(value=>({value,label:t(`k8s.nodeWindow${value}`)})))
const now=ref(Date.now())
const latest=computed(()=>history.value?.points.at(-1))
const current=computed(()=>!error.value&&nodeSampleFresh(latest.value,now.value)?latest.value:undefined)
const cards=computed(()=>['cpu','memory','pods'].map(key=>{
 const sample=current.value,kind=key as 'cpu'|'memory'|'pods',percent=sample&&(kind!=='pods'||props.workloads)?nodeMetricValue(sample,kind):null
 const formatter=kind==='cpu'?formatKubeCPU:kind==='memory'?formatKubeMemory:(value:number|undefined)=>value===undefined?'—':String(value)
 const measures:NodeMeasure[]=kind==='pods'?['used','capacity']:['used','capacity','limits','requests']
 const labels={used:'nodeUsed',capacity:'nodeAllocatable',limits:'nodeDeclaredLimits',requests:'resourceRequests'}
 const rows=measures.map(measure=>{
  const allowed=measure==='capacity'||props.workloads||kind!=='pods'&&measure==='used'
  const value=sample&&allowed?nodeResourceValue(sample,kind,measure):null
  return {key:measure,label:t(`k8s.${labels[measure]}`),value,text:formatter(value??undefined)}
 })
 const label=kind==='cpu'?'CPU':kind==='memory'?t('k8s.nodeMemory'):t('k8s.nodePodCount')
 const percentText=percent===null?'—':`${percent>0&&percent<.1?'<0.1':Number(percent.toFixed(1))}%`,overCapacity=percent!==null&&percent>100
 return {key:kind,label,percent,percentText,overCapacity,rows,formatter,description:`${label}: ${percentText}; ${rows.map(row=>`${row.label} ${row.text}`).join('; ')}${overCapacity?`; ${t('k8s.nodeOverAllocatable')}`:''}`}
}))
async function load(){
 request?.abort();const controller=new AbortController();request=controller;const currentRevision=++revision;loading.value=true
 try{
  const {data,headers}=await client.get<NodeHistory>(`/kubernetes/clusters/${encodeURIComponent(props.cluster)}/nodes/${encodeURIComponent(props.node.metadata.name)}/metrics`,{params:{uid:props.node.metadata.uid,window:windowSeconds.value},signal:controller.signal})
  if(!disposed&&currentRevision===revision){history.value=data;error.value='';snapshot.value=readKubeSnapshotMeta(headers);snapshotPending.value=false}
 }catch(err){if(!disposed&&currentRevision===revision&&!controller.signal.aborted){
  if(kubeSnapshotPending(err)){
   error.value='';snapshotPending.value=!history.value
   if(history.value)snapshot.value={...snapshot.value,stale:true}
  }else{snapshotPending.value=false;error.value=kubeErrorMessage(err)}
 }}
 finally{if(!disposed&&currentRevision===revision){loading.value=false;void render()}}
}
async function render(){
 await nextTick();if(disposed||!root.value)return
 const style=getComputedStyle(root.value),text=style.getPropertyValue('--kube-muted').trim(),border=style.getPropertyValue('--kube-border').trim()
 const colors={used:style.getPropertyValue('--kube-chart-used').trim(),capacity:style.getPropertyValue('--kube-chart-capacity').trim(),limits:style.getPropertyValue('--kube-chart-limits').trim()}
 const lineTypes={used:'solid',capacity:'dashed',limits:'dotted'} as const
 for(const card of cards.value){
  const element=root.value.querySelector<HTMLElement>(`[data-chart="${card.key}"]`);if(!element?.clientWidth)continue
  let chart=charts.get(card.key);if(!chart){chart=init(element,undefined,{renderer:'canvas'});charts.set(card.key,chart)}
  const series=card.rows.filter(row=>row.key!=='requests').map(row=>{
   const measure=row.key as keyof typeof colors,allowed=measure==='capacity'||props.workloads||measure==='used'&&card.key!=='pods'
   const data=history.value&&allowed?nodeChartPoints(history.value,card.key,measure):[]
   return {name:row.label,type:'line',smooth:false,connectNulls:false,showSymbol:data.filter(point=>point[1]!==null).length===1,symbolSize:4,z:measure==='used'?3:2,itemStyle:{color:colors[measure]},lineStyle:{color:colors[measure],width:2,type:lineTypes[measure]},data}
  })
  const largest=series.reduce((max,line)=>line.data.reduce((max,[,value])=>Math.max(max,value??0),max),0),scale=card.key==='memory'?(largest>=1024**3?1024**3:1024**2):1
  const axisValue=(value:number)=>value===0?'0':card.key==='memory'?`${value>0&&value<.01?'<0.01':Number(value.toFixed(2))} ${scale===1024**3?'GiB':'MiB'}`:card.formatter(value)
  chart.setOption({animation:false,grid:{left:8,right:16,top:18,bottom:12,containLabel:true},tooltip:{trigger:'axis',renderMode:'richText',textStyle:{fontSize:12,color:text},backgroundColor:style.getPropertyValue('--kube-surface').trim(),borderColor:border,formatter:(items:Array<{seriesName:string;value:[number,number|null]}>)=>{
   const at=items[0]?.value[0]
   return [at===undefined?'':new Date(at).toLocaleString(locale.value,{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}),...items.map(item=>`${item.seriesName}  ${card.formatter(item.value[1]===null?undefined:item.value[1]*scale)}`)].join('\n')
  }},xAxis:{type:'time',min:history.value?Date.parse(history.value.from):undefined,max:history.value?Date.parse(history.value.to):undefined,axisLabel:{color:text,fontSize:12,hideOverlap:true},axisLine:{lineStyle:{color:border}}},yAxis:{type:'value',min:0,minInterval:card.key==='pods'?1:undefined,boundaryGap:[0,.1],axisLabel:{formatter:axisValue,color:text,fontSize:12},splitLine:{lineStyle:{color:border}}},series:series.map(line=>({...line,data:line.data.map(([at,value])=>[at,value===null?null:value/scale])}))},{notMerge:true});chart.resize()
 }
}
function refreshVisible(){now.value=Date.now();if(!document.hidden&&props.autoRefresh&&!loading.value)void load()}
watch([windowSeconds,()=>props.cluster,()=>props.node.metadata.uid],()=>{history.value=null;snapshot.value={};snapshotPending.value=false;error.value='';void load()})
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
  <p v-else-if="snapshotPending" class="secondary" role="status">{{ t('k8s.snapshotPending') }}</p>
  <a-alert v-else-if="!loading&&!current" type="info" show-icon :message="t(latest?'k8s.nodeSampleStale':'k8s.nodeWaitingSamples')" />
  <a-alert v-else-if="current&&current.metrics_state!=='ok'" type="warning" show-icon :message="t(current.metrics_state==='forbidden'?'k8s.nodeMetricsForbidden':'k8s.metricsMissing')" />
  <p v-if="snapshot.stale" class="secondary" role="status">{{ t('k8s.snapshotStale') }}<template v-if="snapshot.collectedAt"> · {{ formatDateTime(snapshot.collectedAt) }}</template></p>
  <a-alert v-if="!workloads||current&&current.pods_state!=='ok'" type="info" show-icon :message="t(!workloads?'k8s.nodePodsNoPermission':'k8s.nodePodsUnavailable')" />
  <div class="monitor-grid">
   <article v-for="card in cards" :key="card.key" class="monitor-card">
    <header class="monitor-card-heading"><h4>{{ card.label }}</h4><div class="usage-ratio"><strong :class="{ 'is-over-capacity':card.overCapacity }">{{ card.percentText }}</strong><span v-if="card.overCapacity" class="is-over-capacity">{{ t('k8s.nodeOverAllocatable') }}</span></div></header>
    <div :data-chart="card.key" class="node-trend" role="img" :aria-label="`${t('k8s.nodeTrendLabel',{name:card.label})}; ${card.description}`" />
    <dl class="node-values"><div v-for="row in card.rows" :key="row.key" :class="['metric-row',`metric-${row.key}`]"><dt><i v-if="row.key!=='requests'" class="series-swatch" aria-hidden="true" />{{ row.label }}</dt><dd>{{ row.text }}</dd></div></dl>
   </article>
  </div>
 </section>
</template>
<style scoped>
.node-monitor{container-type:inline-size;color:var(--kube-text)}
h4{margin:0;font-size:14px;font-weight:600;line-height:1.5}
.monitor-toolbar{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:12px}.monitor-toolbar>h4{flex:1;min-width:140px}.monitor-toolbar label{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--kube-muted)}.monitor-toolbar :deep(.kube-select){width:130px}
.monitor-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:12px}
.monitor-card{min-width:0;border:1px solid var(--kube-border);border-radius:6px;background:var(--kube-surface);padding:12px}.monitor-card-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:12px;min-height:30px}.monitor-card-heading h4{font-size:14px}.usage-ratio{text-align:right;font-variant-numeric:tabular-nums}.usage-ratio strong{display:block;font-size:18px;line-height:1.2;font-weight:600}.usage-ratio>span{display:block;font-size:12px;margin-top:4px}.is-over-capacity{color:var(--kube-danger)}
.node-trend{height:230px;min-width:0;margin:8px 0 10px}.node-values{margin:0;padding-top:8px;border-top:1px solid var(--kube-border);font-size:12px;font-variant-numeric:tabular-nums}.metric-row{--series-color:var(--kube-chart-used);display:flex;justify-content:space-between;gap:12px;padding:5px 0}.metric-row dt{display:flex;align-items:center;gap:7px;color:var(--kube-muted)}.metric-row dd{margin:0;text-align:right;overflow-wrap:anywhere;color:var(--kube-text)}.series-swatch{width:16px;flex-shrink:0;border-top:2px solid var(--series-color)}.metric-capacity{--series-color:var(--kube-chart-capacity)}.metric-capacity .series-swatch{border-top-style:dashed}.metric-limits{--series-color:var(--kube-chart-limits)}.metric-limits .series-swatch{border-top-style:dotted}.metric-requests dt{padding-left:23px}.node-monitor :deep(.ant-alert){margin-bottom:10px}
@container(max-width:960px){.monitor-grid{grid-template-columns:1fr}}
@media(max-width:950px){.monitor-grid{grid-template-columns:1fr}}
</style>
