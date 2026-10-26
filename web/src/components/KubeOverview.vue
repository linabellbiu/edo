<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Server, Boxes, Box, Database, Network, FileKey2, Layers, Cpu, MemoryStick, Activity, Settings } from 'lucide-vue-next'
import { init, use, type EChartsType } from 'echarts/core'
import { PieChart } from 'echarts/charts'
import { TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { formatKubeCPU, formatKubeMemory, usageFor, type KubeOverview } from '@/api/kubernetes'
import { usePreferencesStore } from '@/stores/preferences'

use([PieChart,TooltipComponent,CanvasRenderer])
const props=defineProps<{data:KubeOverview|null;loading:boolean;error:string}>()
const emit=defineEmits<{select:[resource:string]}>()
const {t,locale}=useI18n(), preferences=usePreferencesStore()
const root=ref<HTMLElement>(), cpuChart=ref<HTMLElement>(),memoryChart=ref<HTMLElement>(),podChart=ref<HTMLElement>()
const charts=new Map<HTMLElement,EChartsType>()
let resize:ResizeObserver|undefined, disposed=false
const cards=computed(()=>[
  ['nodes','k8s.nodes',Server],['pods','Pod',Box],['deployments','Deployment',Boxes],['statefulsets','StatefulSet',Layers],['daemonsets','DaemonSet',Layers],['jobs','Job',Activity],['cronjobs','CronJob',Activity],['namespaces','Namespace',Boxes],['services','Service',Network],['ingresses','Ingress',Network],['configmaps','ConfigMap',Database],['secrets','Secret',FileKey2],['persistentvolumeclaims','PVC',Database],['persistentvolumes','PV',Database],['storageclasses','StorageClass',Database],['networkpolicies','NetworkPolicy',Network],
].filter(([key])=>Object.hasOwn(props.data?.counts??{},String(key))).map(([key,label,icon])=>({key:String(key),label:String(label).startsWith('k8s.')?t(String(label)):String(label),icon,count:props.data!.counts[String(key)]!})))
const categories=computed(()=>[
  {key:'nodes',icon:Server,resources:['nodes']},
  {key:'workloads',icon:Boxes,resources:['deployments','statefulsets','daemonsets','jobs','cronjobs','pods']},
  {key:'config',icon:Settings,resources:['configmaps','secrets','namespaces']},
  {key:'storage',icon:Database,resources:['storageclasses','persistentvolumes','persistentvolumeclaims']},
  {key:'network',icon:Network,resources:['services','ingresses','networkpolicies']},
].map(group=>({...group,cards:cards.value.filter(card=>group.resources.includes(card.key))})).filter(group=>group.cards.length))
const overviewColumns=computed(()=>[['nodes','network'],['workloads'],['config','storage']].map(keys=>categories.value.filter(group=>keys.includes(group.key))).filter(groups=>groups.length))
const totals=computed(()=>{
  const nodes=props.data?.nodes??[], all=props.data?.counts.nodes?.state==='ok'&&Array.isArray(props.data?.nodes), usages=Object.fromEntries(nodes.map(node=>[node.name,usageFor(props.data,{kind:'Node',metadata:{name:node.name}})]))
  const complete=!props.error&&all&&nodes.length>0&&nodes.every(node=>usages[node.name])
  const sum=(values:unknown[])=>values.every((value):value is number=>typeof value==='number'&&Number.isFinite(value)&&value>=0)?values.reduce((total,value)=>total+value,0):undefined
  return {cpu:all?sum(nodes.map(node=>node.cpu)):undefined,memory:all?sum(nodes.map(node=>node.memory)):undefined,cpuUsed:complete?sum(nodes.map(node=>usages[node.name]!.cpu)):undefined,memoryUsed:complete?sum(nodes.map(node=>usages[node.name]!.memory)):undefined,ready:nodes.filter(node=>node.ready).length,nodes:nodes.length}
})
const phaseKeys:Record<string,string>={Running:'running',Pending:'pending',Failed:'failed',Succeeded:'completed',Unknown:'unknown',Terminating:'terminating'}
const phaseColors:Record<string,string>={Running:'#22b978',Pending:'#e5a11c',Failed:'#ef5a6e',Succeeded:'#526aff',Unknown:'#708198',Terminating:'#a66b00'}
const phases=computed(()=>Object.entries(props.data?.phases??{}).map(([phase,value])=>({phase,name:phaseKeys[phase]?t(`k8s.state_${phaseKeys[phase]}`):phase,value,color:phaseColors[phase]??'#708198'})))
const percentage=(used:number|undefined,total:number|undefined)=>used===undefined||total===undefined||total<=0?'—':`${(used/total*100).toFixed(1)}%`
async function render(){
  await nextTick();if(disposed||!root.value)return
  const elements=[cpuChart.value,memoryChart.value,podChart.value]
  // 数据重载会重建容器，不能继续向已经移出页面的旧画布绘制。
  for(const [element,chart] of charts){
    if(!elements.includes(element)){chart.dispose();charts.delete(element)}
  }
  const style=getComputedStyle(root.value), track=style.getPropertyValue('--edo-border').trim(), text=style.getPropertyValue('--edo-text').trim()
  for(const [i,element] of elements.entries()){
    if(!element||!element.clientWidth||!element.clientHeight)continue
    let chart=charts.get(element)
    if(!chart){chart=init(element,undefined,{renderer:'canvas'});charts.set(element,chart)}
    const used=i===0?totals.value.cpuUsed:totals.value.memoryUsed,total=i===0?totals.value.cpu:totals.value.memory
    const data=i===2?phases.value:(used===undefined||!total?[]:[{name:t('k8s.used'),value:used},{name:t('k8s.available'),value:Math.max(0,total-used)}])
    chart.setOption({animation:false,aria:{enabled:true},tooltip:{trigger:'item',renderMode:'richText',valueFormatter:(value:unknown)=>i===0?formatKubeCPU(value):i===1?formatKubeMemory(value):String(value),textStyle:{fontSize:12,color:text},backgroundColor:style.getPropertyValue('--edo-surface').trim(),borderColor:track},series:[{type:'pie',radius:['72%','92%'],center:['50%','50%'],silent:false,label:{show:false},emphasis:{scale:false},data:data.length?data.map((entry,index)=>({...entry,itemStyle:{color:i===2?phaseColors[phases.value[index]?.phase??'']??'#708198':index===0?'#526aff':track}})):[{value:1,itemStyle:{color:track},tooltip:{show:false}}]}]},{notMerge:true})
    chart.resize()
  }
}
watch([()=>props.data,()=>preferences.theme,locale,cpuChart,memoryChart,podChart],()=>void render(),{deep:true,flush:'post'})
onMounted(()=>{void render();resize=new ResizeObserver(()=>void render());if(root.value)resize.observe(root.value)})
onBeforeUnmount(()=>{disposed=true;resize?.disconnect();charts.forEach(chart=>chart.dispose());charts.clear()})
</script>

<template>
  <section ref="root" class="cluster-overview" :aria-busy="loading">
    <a-alert v-if="error" type="warning" show-icon :message="error" />
    <a-alert v-if="!error&&!loading&&data?.nodes?.length&&(totals.cpuUsed===undefined||totals.memoryUsed===undefined)" type="warning" show-icon :message="t('k8s.metricsMissing')" />
    <a-skeleton v-if="!data&&loading" active :paragraph="{rows:4}" />
    <template v-else-if="data">
      <div class="cluster-charts">
        <article v-if="data.counts.nodes" class="chart-panel"><h4><Cpu :size="16" /> CPU <span>{{ t('k8s.clusterTotal') }}</span></h4><div class="chart-content"><div class="ring"><div ref="cpuChart" class="chart" role="img" :aria-label="`CPU ${percentage(totals.cpuUsed,totals.cpu)}`" /><strong>{{ percentage(totals.cpuUsed,totals.cpu) }}</strong></div><dl><dt>{{ t('k8s.used') }}</dt><dd>{{ formatKubeCPU(totals.cpuUsed) }}</dd><dt>{{ t('k8s.cpu') }}</dt><dd>{{ formatKubeCPU(totals.cpu) }}</dd></dl></div></article>
        <article v-if="data.counts.nodes" class="chart-panel"><h4><MemoryStick :size="16" /> {{ t('k8s.memoryUsage') }} <span>{{ t('k8s.clusterTotal') }}</span></h4><div class="chart-content"><div class="ring"><div ref="memoryChart" class="chart" role="img" :aria-label="`${t('k8s.memoryUsage')} ${percentage(totals.memoryUsed,totals.memory)}`" /><strong>{{ percentage(totals.memoryUsed,totals.memory) }}</strong></div><dl><dt>{{ t('k8s.used') }}</dt><dd>{{ formatKubeMemory(totals.memoryUsed) }}</dd><dt>{{ t('k8s.memory') }}</dt><dd>{{ formatKubeMemory(totals.memory) }}</dd></dl></div></article>
        <article v-if="data.counts.pods" class="chart-panel"><h4><Box :size="16" /> {{ t('k8s.podStatus') }}</h4><div class="chart-content"><div class="ring"><div ref="podChart" class="chart" role="img" :aria-label="phases.map(item=>`${item.name}: ${item.value}`).join(', ')" /><strong>{{ data.counts.pods.state==='unavailable'?'—':data.counts.pods.count }}{{ data.counts.pods.state==='partial'?'+':'' }}</strong></div><div class="phase-list"><span v-for="(phase,index) in phases" :key="phase.name"><i :style="{background:phase.color}" />{{ phase.name }}<b>{{ phase.value }}</b></span><span v-if="!phases.length">{{ t(data.counts.pods.state==='unavailable'?'k8s.unavailable':'k8s.noPods') }}</span></div></div></article>
      </div>
      <div class="overview-resource-groups" :style="{gridTemplateColumns:`repeat(${overviewColumns.length},minmax(0,1fr))`}"><div v-for="(groups,index) in overviewColumns" :key="index" class="overview-column"><section v-for="category in groups" :key="category.key" class="overview-category"><h4><component :is="category.icon" :size="16" />{{ t(`k8s.${category.key}`) }}</h4><button v-for="card in category.cards" :key="card.key" class="overview-resource-row" @click="emit('select',card.key)"><component :is="card.icon" :size="15" /><span>{{ t(`k8s.type_${card.key}`) }}<small>{{ card.label }}</small></span><strong>{{ card.count.state==='unavailable'?'—':card.count.count }}{{ card.count.state==='partial'?'+':'' }}</strong><span v-if="card.count.state!=='ok'" class="count-hint">{{ t(card.count.state==='partial'?'k8s.partial':'k8s.unavailable') }}</span><span aria-hidden="true" class="resource-arrow">›</span></button></section></div></div>
      <div class="overview-footer"><span v-if="data.counts.nodes&&data.counts.nodes.state!=='unavailable'"><Server :size="14" />{{ t('k8s.readyNodes',{ready:totals.ready,total:totals.nodes}) }}{{ data.counts.nodes.state==='partial'?'+':'' }}</span><span>{{ t('k8s.statisticsAt') }} {{ new Date(data.collected_at).toLocaleTimeString(locale,{hour12:false}) }}</span></div>
    </template>
  </section>
</template>

<style scoped>
.cluster-overview{padding:14px;display:grid;gap:14px}.cluster-charts{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px}.chart-panel{border:1px solid var(--edo-border);border-radius:6px;padding:12px 14px;background:var(--edo-surface)}h4{font-size:13px;font-weight:600;margin:0;display:flex;align-items:center;gap:8px}h4 svg{color:var(--edo-primary)}h4>span{margin-left:auto;font-size:11px;font-weight:400;color:var(--edo-muted)}.chart-content{display:flex;align-items:center;gap:18px;min-height:130px}.ring{width:112px;height:112px;position:relative;flex-shrink:0}.ring strong{position:absolute;inset:0;display:grid;place-items:center;font-size:20px;pointer-events:none}.chart{width:100%;height:100%}dl{margin:0;display:grid;gap:4px}dt{font-size:12px;color:var(--edo-muted)}dd{margin:0 0 8px;font-size:15px;font-weight:600}.phase-list{display:grid;gap:7px;font-size:12px;flex:1}.phase-list span{display:flex;align-items:center;gap:6px}.phase-list i{width:7px;height:7px;border-radius:50%}.phase-list b{margin-left:auto}.overview-footer{display:flex;justify-content:space-between;font-size:11px;color:var(--edo-muted);gap:12px}.overview-footer>span{display:flex;gap:6px;align-items:center}.overview-resource-groups{display:grid;gap:12px;align-items:start}.overview-column{display:flex;flex-direction:column;gap:12px;min-width:0}.overview-category{border:1px solid var(--edo-border);border-radius:6px;overflow:hidden;flex-shrink:0}.overview-category h4{padding:11px 14px;background:var(--edo-bg);border-bottom:1px solid var(--edo-border)}.overview-resource-row{display:flex;align-items:center;gap:8px;width:100%;min-height:46px;padding:8px 14px;border:0;border-bottom:1px solid var(--edo-border);background:var(--edo-surface);color:var(--edo-text);cursor:pointer;text-align:left;font-size:13px}.overview-resource-row:last-child{border-bottom:0}.overview-resource-row:hover,.overview-resource-row:focus-visible{background:var(--edo-primary-soft)}.overview-resource-row:focus-visible{outline:2px solid var(--edo-primary);outline-offset:-2px}.overview-resource-row>svg{color:var(--edo-muted);flex-shrink:0}.overview-resource-row>span:not(.count-hint):not(.resource-arrow){display:grid;gap:2px;min-width:0}.overview-resource-row small{font-size:11px;color:var(--edo-muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.overview-resource-row strong{margin-left:auto;font-size:17px;font-variant-numeric:tabular-nums}.resource-arrow{color:var(--edo-muted);margin-left:5px}.count-hint{font-size:11px;color:var(--edo-muted)}
@media(max-width:1350px){.chart-content{gap:10px}.ring{width:94px;height:94px}}
@media(max-width:1100px){.overview-resource-groups{grid-template-columns:1fr!important}.overview-column{display:contents}}
@media(max-width:900px){.cluster-charts{grid-template-columns:1fr}.chart-content{min-height:110px}}
</style>
