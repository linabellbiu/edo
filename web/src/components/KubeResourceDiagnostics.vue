<script setup lang="ts">
import {computed,nextTick,onMounted,onBeforeUnmount,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {init,use,type EChartsType} from 'echarts/core'
import {BarChart,LineChart} from 'echarts/charts'
import {GridComponent,TooltipComponent,LegendComponent} from 'echarts/components'
import {CanvasRenderer} from 'echarts/renderers'
import client from '@/api/client'
import {resourceURL,kubeErrorMessage,formatKubeCPU,formatKubeMemory,type KubeObject,type KubeType} from '@/api/kubernetes'
import {podRuntimeContainers,podLifecycle,podMetricRows,podMetricsFresh,appendPodMetricSample,interruptPodMetricHistory,podMetricTrendSegments,podMetricTrendRange,type PodMetricHistory} from '@/utils/kubernetesDiagnostics'
import {usePreferencesStore} from '@/stores/preferences'
import {formatDateTime} from '@/utils/time'
import ResourceTable from './ResourceTable.vue'
import type {KubeReference} from '@/utils/kubernetesPresentation'
const props=defineProps<{cluster:string;object:KubeObject;type:KubeType;autoRefresh:boolean;canFollow:(reference:KubeReference)=>boolean;embedded?:boolean;compact?:boolean;trend?:boolean;refreshToken?:number}>()
const emit=defineEmits<{follow:[reference:KubeReference]}>()
const {t,te,locale}=useI18n(),preferences=usePreferencesStore()
use([BarChart,LineChart,GridComponent,TooltipComponent,LegendComponent,CanvasRenderer])
const data=ref<KubeObject|null>(null),error=ref(''),loading=ref(false),root=ref<HTMLElement>(),now=ref(Date.now()),history=ref<PodMetricHistory|null>(null)
let revision=0,request:AbortController|undefined,disposed=false,timer:ReturnType<typeof setInterval>|undefined,observer:ResizeObserver|undefined
const charts=new Map<string,EChartsType>()
let observedWidth=0
const context=computed(()=>JSON.stringify([props.cluster,props.type.resource,props.type.apiVersion,props.object.metadata.namespace??'',props.object.metadata.name,props.object.metadata.uid]))
const endpoints=computed<KubeObject[]>(()=>(data.value?.endpoints??[]).map((e:KubeObject,i:number)=>({...e,id:i})))
const revisions=computed<KubeObject[]>(()=>data.value?.revisions??[])
const tableRows=computed<KubeObject[]>(()=>props.object.kind==='Service'?endpoints.value:revisions.value)
const containers=computed<KubeObject[]>(()=>podRuntimeContainers(props.object).map(c=>({...c,id:c.name})))
const lifecycle=computed(()=>podLifecycle(props.object))
const rows=computed(()=>podMetricRows(props.object,error.value?null:data.value,now.value))
const chartHeight=computed(()=>props.trend?(props.compact?125:180):props.compact?Math.max(180,rows.value.length*46+70):Math.max(190,rows.value.length*90+70))
const trendCurrent=computed(()=>!error.value&&podMetricsFresh(props.object,data.value,now.value)&&rows.value.some(row=>row.cpu.used!==null||row.memory.used!==null))
const trendHint=computed(()=>{
 if(data.value?.state==='forbidden')return t('k8s.podTrendForbidden')
 if(error.value||data.value&&data.value.state!=='ok'||data.value&&podMetricsFresh(props.object,data.value,now.value)&&!trendCurrent.value)return t('k8s.podTrendUnavailable')
 if(data.value&&!podMetricsFresh(props.object,data.value,now.value))return t('k8s.podTrendStale')
 return history.value?.points.length===1?t('k8s.podTrendSingle'):!history.value?.points.length?t('k8s.podTrendWaiting'):''
})
const title=computed(()=>t(props.object.kind==='Service'?'k8s.serviceEndpoints':props.object.kind==='Deployment'?'k8s.deploymentHistory':'k8s.podDiagnostics'))
const columns=computed(()=>props.object.kind==='Service'?[{key:'addresses',label:t('k8s.addresses')},{key:'ports',label:t('k8s.endpointPort')},{key:'ready',label:t('k8s.endpointReady')},{key:'serving',label:t('k8s.endpointServing')},{key:'terminating',label:t('k8s.endpointTerminating')},{key:'target',label:t('k8s.endpointTarget')},{key:'slice',label:'EndpointSlice'}]:[{key:'revision',label:t('k8s.revision')},{key:'name',label:'ReplicaSet'},{key:'replicas',label:t('k8s.replicas')},{key:'images',label:t('k8s.image')},{key:'pods',label:'Pod'},{key:'created',label:t('k8s.age')}])
function targetReference(e:KubeObject):KubeReference|undefined{const r=e.target;return r?.kind==='Pod'&&r.name&&r.uid&&(!r.namespace||r.namespace===props.object.metadata.namespace)?{resource:'pods',name:r.name,uid:r.uid,namespace:props.object.metadata.namespace}:undefined}
function followEndpoint(e:KubeObject){const r=targetReference(e);if(r&&props.canFollow(r))emit('follow',r)}
function podReference(p:KubeObject):KubeReference{return {resource:'pods',name:p.name,uid:p.uid,namespace:props.object.metadata.namespace}}
function lifecycleLabel(key:string){
 if(te(`k8s.lifecycle_${key}`))return t(`k8s.lifecycle_${key}`)
 const match=/^(.+) \/ (current|previous) \/ (started|startedAt|finishedAt)$/.exec(key)
 return match?`${match[1]} · ${t(match[2]==='current'?'k8s.currentContainerInstance':'k8s.previousContainerInstance')} · ${t(match[3]==='finishedAt'?'k8s.containerFinished':'k8s.containerStarted')}`:key
}
function condition(value:unknown){return value===true?t('k8s.stateReady'):value===false?t('k8s.stateNotReady'):t('k8s.stateUnknown')}
function metricValue(kind:'cpu'|'memory',value:number|null){
 if(!props.trend||kind==='memory')return (kind==='cpu'?formatKubeCPU:formatKubeMemory)(value??undefined)
 if(value===null||!Number.isFinite(value)||value<0)return '—'
 const milli=value*1000;return `${milli>0&&milli<.01?'<0.01':Number(milli.toFixed(2))} m`
}
function metricDescription(kind:'cpu'|'memory'){
 return rows.value.map(row=>`${row.name}: ${(['used','requests','limits'] as const).map(key=>`${t(key==='used'?'k8s.nodeUsed':key==='requests'?'k8s.resourceRequests':'k8s.nodeDeclaredLimits')} ${metricValue(kind,row[kind][key])}`).join('; ')}`).join('; ')
}
function trendSeries(kind:'cpu'|'memory',scale:number,primary:string){
 const colors=[primary,'#20a38e','#a66b00','#9a64ce','#bf5b74','#3a88b9']
 return rows.value.flatMap((row,index)=>{
  return podMetricTrendSegments(history.value,row.name,kind).map((points,segment)=>({id:`${row.name}:${segment}`,name:row.name,type:'line',smooth:false,connectNulls:false,showSymbol:true,symbol:'circle',symbolSize:points.length===1?6:3,itemStyle:{color:colors[index%colors.length]},lineStyle:{width:1.8},areaStyle:{opacity:.05},data:points.map(point=>[point[0],point[1]===null?null:point[1]/scale])}))
 })
}
function trendTooltip(kind:'cpu'|'memory',scale:number,raw:unknown){
 const points=(Array.isArray(raw)?raw:[raw]) as Array<{seriesName?:string;value?:unknown}>,first=points.find(point=>Array.isArray(point?.value))?.value as [number,unknown]|undefined
 return [first?formatDateTime(first[0]):'',...points.filter(point=>Array.isArray(point?.value)).map(point=>{const value=(point.value as [number,unknown])[1];return `${point.seriesName??''}  ${metricValue(kind,typeof value==='number'?value*scale:null)}`})].filter(Boolean).join('\n')
}
async function load(){
 request?.abort();const controller=new AbortController();request=controller;const current=++revision,target={cluster:props.cluster,type:{...props.type},namespace:props.object.metadata.namespace,name:props.object.metadata.name,uid:props.object.metadata.uid,context:context.value};loading.value=true
 const isCurrent=()=>!disposed&&current===revision&&!controller.signal.aborted&&target.context===context.value
 try{const result=await client.get(`${resourceURL(target.cluster,target.type,target.name)}/diagnostics`,{params:{namespace:target.namespace,uid:target.uid},signal:controller.signal})
  if(!isCurrent())return
  if(result.data.uid!==target.uid)throw new Error(t('k8s.resourceChanged'))
  data.value=result.data;error.value='';now.value=Date.now()
  if(props.trend&&props.object.kind==='Pod')history.value=appendPodMetricSample(history.value,props.object,result.data,now.value)
 }catch(err){if(isCurrent()){data.value=null;error.value=kubeErrorMessage(err);if(props.trend&&props.object.kind==='Pod')history.value=interruptPodMetricHistory(history.value,props.object)}}
 finally{if(isCurrent()){now.value=Date.now();loading.value=false;void render()}}
}
async function render(){
 await nextTick();if(disposed||props.object.kind!=='Pod'||!root.value)return
 const style=getComputedStyle(root.value),text=style.getPropertyValue('--edo-muted').trim(),border=style.getPropertyValue('--edo-border').trim(),primary=style.getPropertyValue('--edo-primary').trim()
 for(const kind of ['cpu','memory'] as const){
  const element=root.value.querySelector<HTMLElement>(`[data-metric="${kind}"]`);if(!element?.clientWidth)continue
  const format=kind==='cpu'?formatKubeCPU:formatKubeMemory,values=rows.value
  const scale=kind==='memory'?(Math.max(...values.flatMap(r=>Object.values(r.memory).map(v=>v??0)))>=1024**3?1024**3:1024**2):1
  let chart=charts.get(kind);if(!chart){chart=init(element,undefined,{renderer:'canvas'});charts.set(kind,chart)}
  if(props.trend){
   const amounts=(history.value?.points??[]).flatMap(point=>point.containers.map(container=>container[kind]??0)),trendScale=kind==='cpu'?.001:Math.max(...amounts,0)>=1024**3?1024**3:1024**2
   const range=podMetricTrendRange(history.value,kind),series=trendSeries(kind,trendScale,primary)
   chart.setOption({animation:false,grid:{left:58,right:16,top:30,bottom:26},legend:{type:'scroll',top:0,itemWidth:12,itemGap:10,data:[...new Set(series.map(line=>line.name))],textStyle:{color:text,fontSize:11}},tooltip:{trigger:'axis',renderMode:'richText',formatter:(raw:unknown)=>trendTooltip(kind,trendScale,raw)},xAxis:{type:'time',show:Boolean(range),min:range?.min,max:range?.max,splitNumber:3,axisLabel:{color:text,fontSize:10,hideOverlap:true,formatter:(value:number)=>new Date(value).toLocaleTimeString(locale.value,{timeZone:'Asia/Shanghai',hour12:false,hour:'2-digit',minute:'2-digit',second:'2-digit'})},axisLine:{lineStyle:{color:border}},axisTick:{show:false}},yAxis:{type:'value',min:0,splitNumber:3,axisLabel:{color:text,fontSize:10,formatter:(value:number)=>metricValue(kind,value*trendScale)},splitLine:{lineStyle:{color:border,type:'dashed'}}},series},{notMerge:true});chart.resize();continue
  }
  chart.setOption({animation:false,grid:{left:90,right:65,top:36,bottom:30},legend:{top:0,bottom:'auto',itemWidth:14,itemGap:10,textStyle:{color:text,fontSize:11}},tooltip:{trigger:'axis',renderMode:'richText',valueFormatter:(v:unknown)=>format(typeof v==='number'?v*scale:undefined)},xAxis:{type:'value',min:0,splitNumber:3,axisLabel:{color:text,hideOverlap:true,formatter:(v:number)=>kind==='memory'?`${Number(v.toFixed(2))} ${scale===1024**3?'GiB':'MiB'}`:format(v)},splitLine:{lineStyle:{color:border}}},yAxis:{type:'category',inverse:true,data:values.map(r=>r.name),axisLabel:{color:text,width:75,overflow:'truncate'},axisLine:{show:false},axisTick:{show:false}},series:([{key:'used',label:t('k8s.nodeUsed'),color:primary},{key:'requests',label:t('k8s.resourceRequests'),color:'#a66b00'},{key:'limits',label:t('k8s.nodeDeclaredLimits'),color:'#9a64ce'}] as const).map(s=>({name:s.label,type:'bar',barMaxWidth:12,label:{show:true,position:'right',fontSize:10,color:text,formatter:(p:{data:unknown})=>format(typeof p.data==='number'?p.data*scale:undefined)},itemStyle:{color:s.color},data:values.map(r=>r[kind][s.key]===null?null:r[kind][s.key]!/scale)}))},{notMerge:true});chart.resize()
 }
}
function sync(){now.value=Date.now();if(!document.hidden&&props.autoRefresh&&!loading.value)void load()}
watch(()=>context.value,()=>{data.value=null;history.value=null;error.value='';void load()})
watch([rows,history,()=>props.trend,()=>preferences.theme,locale],()=>void render())
watch(()=>props.trend,enabled=>{history.value=null;if(enabled&&props.object.kind==='Pod')history.value=appendPodMetricSample(null,props.object,data.value,now.value)})
watch(()=>props.refreshToken,()=>void load())
watch(()=>props.autoRefresh,(enabled,previous)=>{if(enabled&&!previous)void load()})
onMounted(()=>{void load();observer=new ResizeObserver(entries=>{const width=entries[0]?.contentRect.width??0;if(width>0&&Math.abs(width-observedWidth)>.5){observedWidth=width;void render()}});if(root.value)observer.observe(root.value);timer=setInterval(sync,30000);document.addEventListener('visibilitychange',sync)})
onBeforeUnmount(()=>{disposed=true;revision++;request?.abort();history.value=null;if(timer)clearInterval(timer);document.removeEventListener('visibilitychange',sync);observer?.disconnect();charts.forEach(c=>c.dispose());charts.clear()})
</script>
<template>
 <section ref="root" class="resource-detail-section diagnostics" :aria-busy="loading">
  <div v-if="!embedded" class="section-heading"><h4>{{ title }}</h4><a-button size="small" :loading="loading" @click="load">{{ t('k8s.refresh') }}</a-button></div>
  <a-alert v-if="error" type="error" show-icon :message="error" />
  <a-alert v-else-if="data?.state==='partial'||data?.state==='pods_unavailable'" type="warning" show-icon :message="t(data.state==='partial'?'k8s.relatedPartial':'k8s.revisionPodsUnavailable')" />
  <template v-if="object.kind==='Pod'">
   <a-alert v-if="!trend&&!error&&data&&data.state!=='ok'" type="warning" :message="t(data.state==='forbidden'?'k8s.nodeMetricsForbidden':'k8s.podMetricsMissing')" />
   <div v-if="trend" class="trend-caption" :class="{'compact-caption':compact}"><span>{{ t('k8s.podTrendHistory') }}</span><span>{{ t('k8s.podTrendSampleCount',{count:history?.points.length??0}) }}</span></div>
   <p v-if="trend&&trendHint" class="trend-state" :class="{'is-unavailable':!trendCurrent&&!!data}" role="status">{{ trendHint }}</p>
   <div class="diagnostic-charts" :class="{'trend-charts':trend}"><article v-for="kind in (['cpu','memory'] as const)" :key="kind">
    <h4>{{ kind==='cpu'?'CPU':t('k8s.nodeMemory') }}</h4>
    <div :data-metric="kind" :style="{height:`${chartHeight}px`}" role="img" :aria-label="`${trend?t('k8s.podTrendLabel',{name:kind==='cpu'?'CPU':t('k8s.nodeMemory')}):''} ${metricDescription(kind)}`" />
   </article></div>
   <details v-if="trend" class="trend-resources-details" :open="!compact">
    <summary>{{ t('k8s.podTrendResources') }}</summary>
    <div class="trend-resource-grid"><section v-for="kind in (['cpu','memory'] as const)" :key="kind"><h4>{{ kind==='cpu'?'CPU':t('k8s.nodeMemory') }}</h4><dl class="trend-resources"><div v-for="row in rows" :key="row.name"><dt :title="row.name">{{ row.name }}</dt><dd><span>{{ t('k8s.nodeUsed') }} <strong>{{ metricValue(kind,row[kind].used) }}</strong></span><span>{{ t('k8s.resourceRequests') }} <strong>{{ metricValue(kind,row[kind].requests) }}</strong></span><span>{{ t('k8s.nodeDeclaredLimits') }} <strong>{{ metricValue(kind,row[kind].limits) }}</strong></span></dd></div></dl></section></div>
   </details>
   <template v-if="!compact"><h4>{{ t('k8s.containerRuntime') }}</h4><ResourceTable class="kube-resource-table" :rows="containers" :columns="[{key:'name',label:t('k8s.container')},{key:'state',label:t('k8s.status')},{key:'ready',label:t('k8s.readyAndStarted')},{key:'restarts',label:t('k8s.restarts')},{key:'previous',label:t('k8s.lastTermination')}]" :details="false" :pagination="containers.length>10">
    <template #cell-name="{row}"><strong>{{ row.name }}</strong><div class="secondary">{{ t(`k8s.containerKind_${row.containerKind}`) }}{{ row.restartPolicy==='Always'?' · Sidecar':'' }}</div></template>
    <template #cell-state="{row}"><template v-if="row.runtime?.state?.waiting"><a-tag color="warning">{{ t('k8s.containerWaiting') }}</a-tag>{{ row.runtime.state.waiting.reason }}<div class="secondary">{{ row.runtime.state.waiting.message }}</div></template><template v-else-if="row.runtime?.state?.terminated"><a-tag :color="row.runtime.state.terminated.exitCode===0?'default':'error'">{{ t('k8s.containerTerminated') }}</a-tag>{{ row.runtime.state.terminated.reason }} · exit {{ row.runtime.state.terminated.exitCode }}<div class="secondary">{{ formatDateTime(row.runtime.state.terminated.finishedAt) }}</div></template><template v-else-if="row.runtime?.state?.running"><a-tag color="success">{{ t('k8s.containerRunning') }}</a-tag><div class="secondary">{{ formatDateTime(row.runtime.state.running.startedAt) }}</div></template><span v-else>—</span></template>
    <template #cell-ready="{row}">{{ condition(row.runtime?.ready) }} / {{ condition(row.runtime?.started) }}</template><template #cell-restarts="{row}">{{ row.runtime?.restartCount??'—' }}</template>
    <template #cell-previous="{row}"><template v-if="row.runtime?.lastState?.terminated">{{ row.runtime.lastState.terminated.reason||'—' }} · exit {{ row.runtime.lastState.terminated.exitCode??'—' }} · signal {{ row.runtime.lastState.terminated.signal??'—' }}<div class="secondary">{{ formatDateTime(row.runtime.lastState.terminated.finishedAt) }}</div></template><span v-else>—</span></template>
   </ResourceTable>
   <h4 class="lifecycle-heading">{{ t('k8s.podLifecycle') }}</h4><a-timeline><a-timeline-item v-for="(point,index) in lifecycle" :key="index" :color="point.status==='True'?'green':point.status==='False'?'red':'gray'"><strong>{{ lifecycleLabel(point.key) }}</strong> <span class="secondary">{{ formatDateTime(point.at) }}</span><div>{{ point.reason }}</div></a-timeline-item></a-timeline></template>
  </template>
  <ResourceTable v-else class="kube-resource-table" :rows="tableRows" :row-key="object.kind==='Service'?'id':'uid'" :columns="columns" :details="false" keep-header :pagination="tableRows.length>10" :loading="loading" :empty-text="loading?t('k8s.loading'):error?t('k8s.unavailable'):t(object.kind==='Service'?'k8s.noEndpoints':'k8s.noRevisions')">
   <template #cell-addresses="{row}"><div v-for="address in row.addresses" :key="address"><code>{{ address }}</code></div><span class="secondary">{{ row.address_type }} · {{ row.node_name||'—' }}</span></template>
   <template #cell-ports="{row}"><div v-for="(port,index) in row.ports??[]" :key="index">{{ port.name?`${port.name} · `:'' }}{{ port.port??'—' }}/{{ port.protocol||'TCP' }}</div></template>
   <template v-for="key in ['ready','serving','terminating']" :key="key" #[`cell-${key}`]="{row}"><a-tag :color="row[key]===true?(key==='terminating'?'warning':'success'):row[key]===false?(key==='ready'?'warning':'default'):'default'">{{ condition(row[key]) }}</a-tag></template>
   <template #cell-target="{row}"><button v-if="targetReference(row)&&canFollow(targetReference(row)!)" class="resource-name" @click="followEndpoint(row)">{{ row.target.name }}</button><span v-else>{{ row.target?.name||'—' }}</span></template>
   <template #cell-revision="{row}"><strong>{{ row.revision||'—' }}</strong><a-tag v-if="row.current" color="blue">{{ t('k8s.currentTemplate') }}</a-tag></template>
   <template #cell-replicas="{row}">{{ t('k8s.revisionReplicaCounts',{ready:row.ready,available:row.available,current:row.replicas,desired:row.desired??'—'}) }}</template>
   <template #cell-images="{row}"><div v-for="image in row.images" :key="image">{{ image }}</div></template>
   <template #cell-pods="{row}"><div v-for="pod in row.pods" :key="pod.uid"><button v-if="canFollow(podReference(pod))" class="resource-name" @click="emit('follow',podReference(pod))">{{ pod.name }}</button><span v-else>{{ pod.name }}</span><span class="secondary"> · {{ pod.phase||'—' }}</span></div><span v-if="!row.pods.length">{{ data?.state==='pods_unavailable'?'—':t('k8s.noPods') }}</span></template>
   <template #cell-created="{row}">{{ formatDateTime(row.created) }}</template>
  </ResourceTable>
 </section>
</template>
<style scoped>
.section-heading{display:flex;align-items:center;justify-content:space-between;margin-bottom:16px}h4{font-size:14px;margin:0}.diagnostic-charts{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin:16px 0}.diagnostic-charts article{min-width:0;border:1px solid var(--edo-border);border-radius:8px;padding:12px;background:var(--edo-surface)}.diagnostics :deep(.ant-alert){margin-bottom:12px}.diagnostics :deep(td){overflow-wrap:anywhere}.lifecycle-heading{margin:24px 0 16px}.secondary{color:var(--edo-muted);font-size:12px}.trend-caption{display:flex;align-items:center;justify-content:space-between;gap:12px;color:var(--edo-muted);font-size:11px}.trend-state{margin:8px 0 0;color:var(--edo-muted);font-size:12px}.trend-state.is-unavailable{color:var(--edo-warning,#a66b00)}.trend-charts{gap:12px;margin:10px 0 0}.trend-charts article{padding:12px 10px 8px}.trend-resources-details{margin:8px 0 10px;border:1px solid var(--edo-border);border-radius:4px;font-size:11px;background:var(--edo-surface)}.trend-resources-details>summary{padding:6px 9px;color:var(--edo-muted);cursor:pointer;user-select:none}.trend-resources-details>summary:focus-visible{outline:2px solid var(--edo-primary);outline-offset:1px}.trend-resource-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px;padding:4px 10px 10px}.trend-resource-grid>section{min-width:0}.trend-resource-grid h4{font-size:11px}.trend-resources{margin:8px 0 0;font-size:11px}.trend-resources>div+div{margin-top:8px}.trend-resources dt{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:600;max-width:100%}.trend-resources dd{display:flex;flex-wrap:wrap;column-gap:12px;row-gap:4px;margin:4px 0 0;color:var(--edo-muted)}.trend-resources strong{font-weight:500;color:var(--edo-text);font-variant-numeric:tabular-nums}@media(max-width:800px){.diagnostic-charts,.trend-resource-grid{grid-template-columns:1fr}}
.compact-caption{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
</style>
