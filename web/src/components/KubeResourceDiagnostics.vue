<script setup lang="ts">
import {computed,nextTick,onMounted,onBeforeUnmount,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {init,use,type EChartsType} from 'echarts/core'
import {BarChart} from 'echarts/charts'
import {GridComponent,TooltipComponent,LegendComponent} from 'echarts/components'
import {CanvasRenderer} from 'echarts/renderers'
import client from '@/api/client'
import {resourceURL,kubeErrorMessage,formatKubeCPU,formatKubeMemory,type KubeObject,type KubeType} from '@/api/kubernetes'
import {podRuntimeContainers,podLifecycle,podMetricRows} from '@/utils/kubernetesDiagnostics'
import {usePreferencesStore} from '@/stores/preferences'
import {formatDateTime} from '@/utils/time'
import ResourceTable from './ResourceTable.vue'
import type {KubeReference} from '@/utils/kubernetesPresentation'
const props=defineProps<{cluster:string;object:KubeObject;type:KubeType;autoRefresh:boolean;canFollow:(reference:KubeReference)=>boolean}>()
const emit=defineEmits<{follow:[reference:KubeReference]}>()
const {t,te,locale}=useI18n(),preferences=usePreferencesStore()
use([BarChart,GridComponent,TooltipComponent,LegendComponent,CanvasRenderer])
const data=ref<KubeObject|null>(null),error=ref(''),loading=ref(false),root=ref<HTMLElement>(),now=ref(Date.now())
let revision=0,request:AbortController|undefined,disposed=false,timer:ReturnType<typeof setInterval>|undefined,observer:ResizeObserver|undefined
const charts=new Map<string,EChartsType>()
const endpoints=computed<KubeObject[]>(()=>(data.value?.endpoints??[]).map((e:KubeObject,i:number)=>({...e,id:i})))
const revisions=computed<KubeObject[]>(()=>data.value?.revisions??[])
const tableRows=computed<KubeObject[]>(()=>props.object.kind==='Service'?endpoints.value:revisions.value)
const containers=computed<KubeObject[]>(()=>podRuntimeContainers(props.object).map(c=>({...c,id:c.name})))
const lifecycle=computed(()=>podLifecycle(props.object))
const rows=computed(()=>podMetricRows(props.object,error.value?null:data.value,now.value))
const title=computed(()=>t(props.object.kind==='Service'?'k8s.serviceEndpoints':props.object.kind==='Deployment'?'k8s.deploymentHistory':'k8s.podDiagnostics'))
const columns=computed(()=>props.object.kind==='Service'?[{key:'addresses',label:t('k8s.addresses')},{key:'ports',label:t('k8s.servicePort')},{key:'ready',label:'Ready'},{key:'serving',label:'Serving'},{key:'terminating',label:'Terminating'},{key:'target',label:t('k8s.backend')},{key:'slice',label:'EndpointSlice'}]:[{key:'revision',label:t('k8s.revision')},{key:'name',label:'ReplicaSet'},{key:'replicas',label:t('k8s.replicas')},{key:'images',label:t('k8s.image')},{key:'pods',label:'Pod'},{key:'created',label:t('k8s.age')}])
function targetReference(e:KubeObject):KubeReference|undefined{const r=e.target;return r?.kind==='Pod'&&r.name&&r.uid&&(!r.namespace||r.namespace===props.object.metadata.namespace)?{resource:'pods',name:r.name,uid:r.uid,namespace:props.object.metadata.namespace}:undefined}
function followEndpoint(e:KubeObject){const r=targetReference(e);if(r&&props.canFollow(r))emit('follow',r)}
function podReference(p:KubeObject):KubeReference{return {resource:'pods',name:p.name,uid:p.uid,namespace:props.object.metadata.namespace}}
function condition(value:unknown){return value===true?t('k8s.stateReady'):value===false?t('k8s.stateNotReady'):t('k8s.stateUnknown')}
async function load(){
 request?.abort();const controller=new AbortController();request=controller;const current=++revision;loading.value=true
 try{const result=await client.get(`${resourceURL(props.cluster,props.type,props.object.metadata.name)}/diagnostics`,{params:{namespace:props.object.metadata.namespace,uid:props.object.metadata.uid},signal:controller.signal})
  if(disposed||current!==revision)return
  if(result.data.uid!==props.object.metadata.uid)throw new Error(t('k8s.resourceChanged'))
  data.value=result.data;error.value=''
 }catch(err){if(!disposed&&current===revision&&!controller.signal.aborted){data.value=null;error.value=kubeErrorMessage(err)}}
 finally{if(!disposed&&current===revision){now.value=Date.now();loading.value=false;void render()}}
}
async function render(){
 await nextTick();if(disposed||props.object.kind!=='Pod'||!root.value)return
 const style=getComputedStyle(root.value),text=style.getPropertyValue('--edo-muted').trim(),border=style.getPropertyValue('--edo-border').trim(),primary=style.getPropertyValue('--edo-primary').trim()
 for(const kind of ['cpu','memory'] as const){
  const element=root.value.querySelector<HTMLElement>(`[data-metric="${kind}"]`);if(!element?.clientWidth)continue
  const format=kind==='cpu'?formatKubeCPU:formatKubeMemory,values=rows.value
  const scale=kind==='memory'?(Math.max(...values.flatMap(r=>Object.values(r.memory).map(v=>v??0)))>=1024**3?1024**3:1024**2):1
  let chart=charts.get(kind);if(!chart){chart=init(element,undefined,{renderer:'canvas'});charts.set(kind,chart)}
  chart.setOption({animation:false,grid:{left:90,right:65,top:36,bottom:30},legend:{textStyle:{color:text,fontSize:11}},tooltip:{trigger:'axis',renderMode:'richText',valueFormatter:(v:unknown)=>format(typeof v==='number'?v*scale:undefined)},xAxis:{type:'value',min:0,splitNumber:3,axisLabel:{color:text,hideOverlap:true,formatter:(v:number)=>kind==='memory'?`${Number(v.toFixed(2))} ${scale===1024**3?'GiB':'MiB'}`:format(v)},splitLine:{lineStyle:{color:border}}},yAxis:{type:'category',inverse:true,data:values.map(r=>r.name),axisLabel:{color:text,width:75,overflow:'truncate'},axisLine:{show:false},axisTick:{show:false}},series:([{key:'used',label:t('k8s.nodeUsed'),color:primary},{key:'requests',label:t('k8s.resourceRequests'),color:'#a66b00'},{key:'limits',label:t('k8s.nodeDeclaredLimits'),color:'#9a64ce'}] as const).map(s=>({name:s.label,type:'bar',barMaxWidth:12,label:{show:true,position:'right',fontSize:10,color:text,formatter:(p:{data:unknown})=>format(typeof p.data==='number'?p.data*scale:undefined)},itemStyle:{color:s.color},data:values.map(r=>r[kind][s.key]===null?null:r[kind][s.key]!/scale)}))},{notMerge:true});chart.resize()
 }
}
function sync(){now.value=Date.now();if(!document.hidden&&props.autoRefresh&&!loading.value)void load()}
watch(()=>`${props.cluster}:${props.object.metadata.uid}`,()=>{data.value=null;error.value='';void load()})
watch([rows,()=>preferences.theme,locale],()=>void render())
onMounted(()=>{void load();observer=new ResizeObserver(()=>void render());if(root.value)observer.observe(root.value);timer=setInterval(sync,30000);document.addEventListener('visibilitychange',sync)})
onBeforeUnmount(()=>{disposed=true;revision++;request?.abort();if(timer)clearInterval(timer);document.removeEventListener('visibilitychange',sync);observer?.disconnect();charts.forEach(c=>c.dispose())})
</script>
<template>
 <section ref="root" class="resource-detail-section diagnostics">
  <div class="section-heading"><h4>{{ title }}</h4><a-button size="small" :loading="loading" @click="load">{{ t('k8s.refresh') }}</a-button></div>
  <a-alert v-if="error" type="error" show-icon :message="error" />
  <a-alert v-else-if="data?.state==='partial'||data?.state==='pods_unavailable'" type="warning" show-icon :message="t(data.state==='partial'?'k8s.relatedPartial':'k8s.revisionPodsUnavailable')" />
  <template v-if="object.kind==='Pod'">
   <a-alert v-if="!error&&data&&data.state!=='ok'" type="warning" :message="t(data.state==='forbidden'?'k8s.nodeMetricsForbidden':'k8s.podMetricsMissing')" />
   <div class="diagnostic-charts"><article v-for="kind in ['cpu','memory']" :key="kind"><h4>{{ kind==='cpu'?'CPU':t('k8s.nodeMemory') }}</h4><div :data-metric="kind" :style="{height:`${Math.max(190,rows.length*90+70)}px`}" role="img" :aria-label="rows.map(r=>`${r.name}: ${Object.entries(r[kind as 'cpu'|'memory']).map(([k,v])=>`${t(k==='used'?'k8s.nodeUsed':k==='requests'?'k8s.resourceRequests':'k8s.nodeDeclaredLimits')} ${kind==='cpu'?formatKubeCPU(v??undefined):formatKubeMemory(v??undefined)}`).join('; ')}`).join('; ')" /></article></div>
   <h4>{{ t('k8s.containerRuntime') }}</h4><ResourceTable class="kube-resource-table" :rows="containers" :columns="[{key:'name',label:t('k8s.container')},{key:'state',label:t('k8s.status')},{key:'ready',label:'Ready / Started'},{key:'restarts',label:t('k8s.restarts')},{key:'previous',label:t('k8s.lastTermination')}]" :details="false">
    <template #cell-name="{row}"><strong>{{ row.name }}</strong><div class="secondary">{{ row.containerKind }}{{ row.restartPolicy==='Always'?' · Sidecar':'' }}</div></template>
    <template #cell-state="{row}"><template v-if="row.runtime?.state?.waiting"><a-tag color="warning">Waiting</a-tag>{{ row.runtime.state.waiting.reason }}<div class="secondary">{{ row.runtime.state.waiting.message }}</div></template><template v-else-if="row.runtime?.state?.terminated"><a-tag :color="row.runtime.state.terminated.exitCode===0?'default':'error'">Terminated</a-tag>{{ row.runtime.state.terminated.reason }} · exit {{ row.runtime.state.terminated.exitCode }}<div class="secondary">{{ formatDateTime(row.runtime.state.terminated.finishedAt) }}</div></template><template v-else-if="row.runtime?.state?.running"><a-tag color="success">Running</a-tag><div class="secondary">{{ formatDateTime(row.runtime.state.running.startedAt) }}</div></template><span v-else>—</span></template>
    <template #cell-ready="{row}">{{ condition(row.runtime?.ready) }} / {{ condition(row.runtime?.started) }}</template><template #cell-restarts="{row}">{{ row.runtime?.restartCount??'—' }}</template>
    <template #cell-previous="{row}"><template v-if="row.runtime?.lastState?.terminated">{{ row.runtime.lastState.terminated.reason||'—' }} · exit {{ row.runtime.lastState.terminated.exitCode??'—' }} · signal {{ row.runtime.lastState.terminated.signal??'—' }}<div class="secondary">{{ formatDateTime(row.runtime.lastState.terminated.finishedAt) }}</div></template><span v-else>—</span></template>
   </ResourceTable>
   <h4 class="lifecycle-heading">{{ t('k8s.podLifecycle') }}</h4><a-timeline><a-timeline-item v-for="(point,index) in lifecycle" :key="index" :color="point.status==='True'?'green':point.status==='False'?'red':'gray'"><strong>{{ te(`k8s.lifecycle_${point.key}`)?t(`k8s.lifecycle_${point.key}`):point.key }}</strong> <span class="secondary">{{ formatDateTime(point.at) }}</span><div>{{ point.reason }}</div></a-timeline-item></a-timeline>
  </template>
  <ResourceTable v-else class="kube-resource-table" :rows="tableRows" :row-key="object.kind==='Service'?'id':'uid'" :columns="columns" :details="false" :loading="loading" :empty-text="error?t('k8s.unavailable'):t(object.kind==='Service'?'k8s.noEndpoints':'k8s.noRevisions')">
   <template #cell-addresses="{row}"><div v-for="address in row.addresses" :key="address"><code>{{ address }}</code></div><span class="secondary">{{ row.address_type }} · {{ row.node_name||'—' }}</span></template>
   <template #cell-ports="{row}"><div v-for="(port,index) in row.ports??[]" :key="index">{{ port.name||'—' }} · {{ port.port??'—' }}/{{ port.protocol||'TCP' }}</div></template>
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
.section-heading{display:flex;align-items:center;justify-content:space-between;margin-bottom:16px}h4{font-size:14px;margin:0}.diagnostic-charts{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin:16px 0}.diagnostic-charts article{min-width:0;border:1px solid var(--edo-border);border-radius:8px;padding:12px;background:var(--edo-surface)}.diagnostics :deep(.ant-alert){margin-bottom:12px}.diagnostics :deep(td){overflow-wrap:anywhere}.lifecycle-heading{margin:24px 0 16px}.secondary{color:var(--edo-muted);font-size:12px}@media(max-width:800px){.diagnostic-charts{grid-template-columns:1fr}}
</style>
