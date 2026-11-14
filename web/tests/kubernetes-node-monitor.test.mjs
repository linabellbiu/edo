import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import ts from 'typescript'
const source=readFileSync(new URL('../src/utils/kubernetesNode.ts',import.meta.url),'utf8'),module={exports:{}}
new Function('require','module','exports',ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText)(()=>({}),module,module.exports)
const {nodeChartPoints,nodeMetricValue,nodeSampleFresh,nodeConditionState}=module.exports
const now=Date.now(),sample={collected_at:new Date(now).toISOString(),metrics_at:new Date(now).toISOString(),cpu:0,memory:1024,cpu_allocatable:4,memory_allocatable:2048,pods:2,pod_capacity:10,metrics_state:'ok',pods_state:'ok'}
test('节点图表保留真实零值与超配，不使用失效指标或无权限 Pod 数',()=>{
 assert.equal(nodeMetricValue(sample,'cpu'),0);assert.equal(nodeMetricValue({...sample,cpu:5},'cpu'),125)
 assert.equal(nodeMetricValue({...sample,metrics_at:'bad'},'cpu'),null)
 assert.equal(nodeMetricValue({...sample,cpu:null},'cpu'),null)
 assert.equal(nodeMetricValue({...sample,pods_state:'no_permission'},'pods'),null)
 assert.equal(nodeMetricValue({...sample,memory_allocatable:0},'memory'),null)
 assert.equal(nodeMetricValue({...sample,metrics_state:'forbidden'},'memory'),null)
 assert.equal(nodeSampleFresh(sample,now),true);assert.equal(nodeSampleFresh(sample,now+180000),false)
})
test('节点趋势断档插空点，窗口之外不补历史',()=>{
 const history={from:new Date(now-3600000).toISOString(),to:new Date(now).toISOString(),interval_seconds:60,points:[{...sample,collected_at:new Date(now-300000).toISOString(),metrics_at:new Date(now-300000).toISOString()},sample]}
 const points=nodeChartPoints(history,'cpu');assert.equal(points.length,3);assert.equal(points[1][1],null);assert.equal(points[2][1],0)
 history.points=[{...sample,collected_at:new Date(now-7200000).toISOString()}];assert.deepEqual(nodeChartPoints(history,'cpu'),[])
})
test('Ready 与压力状态采用各自健康方向，未知与非标准条件不标绿',()=>{
 assert.equal(nodeConditionState({type:'Ready',status:'True'}),'success');assert.equal(nodeConditionState({type:'Ready',status:'False'}),'error')
 for(const type of ['MemoryPressure','DiskPressure','PIDPressure','NetworkUnavailable']){assert.equal(nodeConditionState({type,status:'True'}),'error');assert.equal(nodeConditionState({type,status:'False'}),'success')}
 assert.equal(nodeConditionState({type:'Ready',status:'Unknown'}),'default');assert.equal(nodeConditionState({type:'Custom',status:'True'}),'default')
})

import {parse,compileScript} from '@vue/compiler-sfc'
import * as vue from 'vue'
function monitorState(){
 const requests=[],hooks={mounted:[],unmounted:[]},options=new Map(),disposed=[]
 const source=readFileSync(new URL('../src/components/KubeNodeMonitor.vue',import.meta.url),'utf8')
 const code=ts.transpileModule(compileScript(parse(source).descriptor,{id:'node-monitor'}).content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText
 const componentModule={exports:{}},modules={vue:{...vue,onMounted:fn=>hooks.mounted.push(fn),onBeforeUnmount:fn=>hooks.unmounted.push(fn)},'vue-i18n':{useI18n:()=>({t:key=>key,locale:vue.ref('zh-CN')})},'@/api/client':{get:(url,options)=>new Promise((resolve,reject)=>requests.push({url,options,resolve,reject}))},'@/api/kubernetes':{formatKubeCPU:value=>String(value??'—'),formatKubeMemory:value=>String(value??'—'),kubeErrorMessage:err=>err.message},'@/utils/kubernetesNode':module.exports,'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},'echarts/core':{use(){},init:element=>({setOption:option=>options.set(element.selector,option),resize(){},dispose:()=>disposed.push(element.selector)})}}
 new Function('require','module','exports','document','getComputedStyle',code)(key=>modules[key]??{},componentModule,componentModule.exports,{removeEventListener(){}},()=>({getPropertyValue:key=>key==='--edo-primary'?'#526aff':'#758195'}))
 const scope=vue.effectScope(),props=vue.reactive({cluster:'c1',node:{metadata:{name:'worker',uid:'n1'}},autoRefresh:true,workloads:true})
 const state=scope.run(()=>componentModule.exports.default.setup(props,{expose(){}}))
 return{state,requests,hooks,props,options,disposed,enableCharts(){state.root.value={querySelector:selector=>({selector,clientWidth:360})}},stop(){hooks.unmounted.forEach(fn=>fn());scope.stop()}}
}
test('节点监控切换窗口中止旧请求，迟到响应和关闭后响应不覆盖状态',async()=>{
 const ctx=monitorState(),first=ctx.state.load(),second=ctx.state.load()
 assert.equal(ctx.requests[0].options.signal.aborted,true)
 ctx.requests[1].resolve({data:{uid:'n1',points:[sample]}});await second
 ctx.requests[0].resolve({data:{uid:'old',points:[]}});await first
 assert.equal(ctx.state.history.value.uid,'n1')
 const third=ctx.state.load();ctx.stop();assert.equal(ctx.requests[2].options.signal.aborted,true)
 ctx.requests[2].resolve({data:{uid:'closed',points:[]}});await third
 assert.equal(ctx.state.history.value.uid,'n1')
})
test('节点监控失败不把旧样本当成当前，缺少容量保持空值',async()=>{
 const ctx=monitorState();let promise=ctx.state.load();ctx.requests[0].resolve({data:{uid:'n1',points:[sample]}});await promise
 assert.equal(ctx.state.cards.value[0].percent,0)
 promise=ctx.state.load();ctx.requests[1].reject(new Error('监控不可用'));await promise
 assert.equal(ctx.state.current.value,undefined);assert.equal(ctx.state.cards.value[0].percent,null)
 assert.equal(ctx.state.history.value.points.length,1);assert.equal(ctx.state.error.value,'监控不可用');ctx.stop()
})
test('监控饼图只比较使用与剩余，申请和限制单独保留真实数值，超配不截断',async()=>{
 const ctx=monitorState();ctx.enableCharts();const promise=ctx.state.load()
 ctx.requests[0].resolve({data:{uid:'n1',points:[{...sample,cpu:5,memory:1.5*1024**3,memory_allocatable:2*1024**3,allocation:{cpu_requests:2,cpu_limits:8,memory_requests:512*1024**2,memory_limits:4*1024**3}}]}});await promise;await ctx.state.render()
 assert.equal(ctx.options.size,6)
 const cpu=ctx.options.get('[data-comparison="cpu"]'),memory=ctx.options.get('[data-comparison="memory"]'),pods=ctx.options.get('[data-comparison="pods"]')
 assert.equal(cpu.series[0].type,'pie');assert.deepEqual(cpu.series[0].data.map(row=>row.value),[5,0])
 assert.deepEqual(cpu.series[0].data.map(row=>row.name),['k8s.nodeUsed','k8s.available'])
 assert.deepEqual(ctx.state.cards.value[0].rows.map(row=>row.value),[5,4,2,8])
 assert.equal(ctx.state.cards.value[0].percentText,'125%');assert.equal(ctx.state.cards.value[0].overCapacity,true)
 assert.match(ctx.state.cards.value[0].description,/125%.*k8s.nodeOverAllocatable/)
 assert.equal(cpu.tooltip.formatter({name:'使用量',value:5}),'使用量\n5')
 assert.deepEqual(memory.series[0].data.map(row=>row.value),[1.5*1024**3,.5*1024**3])
 assert.deepEqual(ctx.state.cards.value[1].rows.map(row=>row.value),[1.5*1024**3,2*1024**3,.5*1024**3,4*1024**3])
 assert.deepEqual(pods.series[0].data.map(row=>row.value),[2,8])
 assert.equal(ctx.options.get('[data-chart="cpu"]').series[0].type,'line')
 ctx.stop();assert.equal(ctx.disposed.length,6)
})
test('饼图保留真实零，缺失与过期数据留空；撤销权限时清除 Pod 及申请限制',async()=>{
 const ctx=monitorState();ctx.enableCharts();const promise=ctx.state.load()
 ctx.requests[0].resolve({data:{uid:'n1',points:[{...sample,allocation:null}]}});await promise;await ctx.state.render()
 assert.deepEqual(ctx.options.get('[data-comparison="cpu"]').series[0].data.map(row=>row.value),[0,4])
 assert.equal(ctx.state.cards.value[0].percentText,'0%');assert.equal(ctx.state.cards.value[0].parts[0].text,'0')
 assert.equal(ctx.state.cards.value[0].rows[2].text,'—')
 ctx.state.history.value.points[0].allocation={cpu_requests:2,cpu_limits:8,memory_requests:512,memory_limits:4096}
 ctx.props.workloads=false;await ctx.state.render()
 assert.deepEqual(ctx.state.cards.value[0].rows.map(row=>row.value),[0,4,null,null])
 assert.deepEqual(ctx.options.get('[data-comparison="pods"]').series[0].data,[]);assert.deepEqual(ctx.options.get('[data-chart="pods"]').series[0].data,[])
 assert.equal(ctx.state.cards.value[2].parts[1].value,null)
 ctx.state.history.value.points[0].cpu_allocatable=null;await ctx.state.render()
 assert.deepEqual(ctx.options.get('[data-comparison="cpu"]').series[0].data,[])
 assert.equal(ctx.state.cards.value[0].percentText,'—');assert.equal(ctx.state.cards.value[0].parts[1].value,null)
 ctx.state.now.value=now+180001;await ctx.state.render()
 assert.deepEqual(ctx.state.cards.value[0].rows.map(row=>row.value),[null,null,null,null])
 assert.deepEqual(ctx.options.get('[data-comparison="cpu"]').series[0].data,[])
 assert.equal(ctx.options.get('[data-comparison="cpu"]').series[0].showEmptyCircle,true)
 ctx.stop()
})
