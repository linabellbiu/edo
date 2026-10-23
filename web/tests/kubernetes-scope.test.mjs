import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import {parse,compileScript} from '@vue/compiler-sfc'
function load(path,modules={}) {
 const source=readFileSync(new URL(path,import.meta.url),'utf8')
 const code=path.endsWith('.vue')?compileScript(parse(source).descriptor,{id:'namespace-scope'}).content:source
 const output=ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText
 const module={exports:{}}
 new Function('require','module','exports',output)(key=>modules[key]??{},module,module.exports)
 return module.exports
}
const api=load('../src/api/kubernetes.ts'),presentation=load('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':api})
const translate=key=>key
const overview=(namespace,count)=>({counts:{nodes:{count:3,state:'ok'},namespaces:{count:10,state:'ok'},pods:{count,state:'ok'}},nodes:[{name:'worker',pods:99,pod_state:'ok'}],phases:{Running:count},usage:{nodes:{worker:{cpu:2}},pods:{[`${namespace}/api`]:{cpu:1}}},metrics_state:{pods:'ok'},collected_at:'2026-10-23T04:00:00Z'})
test('多选统计只累加选中命名空间，节点及全局资源不重复计算',()=>{
 const a=overview('prod',2),b=overview('test',3),merged=api.mergeNamespaceOverviews([a,b])
 assert.equal(merged.counts.pods.count,5);assert.equal(merged.counts.nodes.count,3);assert.equal(merged.counts.namespaces.count,10)
 assert.equal(merged.nodes[0].pods,99);assert.equal(merged.usage.nodes.worker.cpu,2)
 assert.deepEqual(Object.keys(merged.usage.pods),['prod/api','test/api']);assert.equal(merged.phases.Running,5)
 b.counts.pods.state='unavailable';b.phases=null
 const missing=api.mergeNamespaceOverviews([a,b]);assert.equal(missing.counts.pods.state,'unavailable');assert.equal(missing.phases,null)
 b.counts.pods.state='partial';assert.equal(api.mergeNamespaceOverviews([a,b]).counts.pods.state,'partial')
 assert.equal(api.mergeNamespaceOverviews([]),null)
})
test('各命名空间分页游标独立保留，加载更多不重复请求已完成命名空间',async()=>{
 const calls=[]
 const api=load('../src/api/kubernetes.ts',{'./client':{__esModule:true,default:{get:async(_,options)=>{
  const {namespace,continue:cursor}=options.params;calls.push({namespace,cursor})
  return {data:{items:[{metadata:{namespace,uid:namespace+(cursor||'first')}}],continue:namespace==='prod'&&!cursor?'prod-page2':''}}
 }}}})
 const type=api.kubeTypes.find(type=>type.resource==='deployments')
 const first=await api.listKubeResourceScopes('cluster',type,['prod','test'])
 assert.equal(first.items.length,2);assert.deepEqual(first.cursor,{prod:'prod-page2'})
 const second=await api.listKubeResourceScopes('cluster',type,['prod','test'],first.cursor)
 assert.deepEqual(calls,[{namespace:'prod',cursor:''},{namespace:'test',cursor:''},{namespace:'prod',cursor:'prod-page2'}]);assert.deepEqual(second.cursor,{})
 await api.listKubeResourceScopes('cluster',api.kubeTypes[0],['prod','test'])
 assert.equal(calls.at(-1).namespace,'')
 const before=calls.length;await api.listKubeResourceScopes('cluster',type,[]);assert.equal(calls.length,before)
})
test('多命名空间请求限制并发，切换范围后不再发送排队请求',async()=>{
 const controller=new AbortController(),pending=[];let active=0,maximum=0,started=0
 const work=api.mapNamespaceRequests(['a','b','c','d','e','f'],async()=>{started++;active++;maximum=Math.max(maximum,active);await new Promise(resolve=>pending.push(resolve));active--},controller.signal)
 assert.equal(started,4);assert.equal(maximum,4)
 controller.abort();pending.splice(0).forEach(resolve=>resolve())
 await assert.rejects(work,{name:'AbortError'});assert.equal(started,4)
})
test('命名空间树筛选不改变选择，父节点支持全选、半选与清空',async()=>{
 const events=[],scope=vue.effectScope(),props=vue.reactive({clusters:[{id:'c',name:'k3s',is_active:true}],cluster:'c',namespaces:['prod','test','cicd'],selected:['prod']})
 const component=load('../src/components/KubeNamespaceTree.vue',{vue,'vue-i18n':{useI18n:()=>({t:translate})}}).default
 let state;scope.run(()=>{state=component.setup(props,{expose(){},emit:(...args)=>events.push(args)})})
 state.search.value='test';assert.deepEqual(state.visibleNamespaces.value,['test']);assert.deepEqual(props.selected,['prod'])
 state.toggleNamespace('test',true);assert.deepEqual(events.pop(),['selection',['prod','test']])
 props.selected=['*'];state.toggleNamespace('test',false);assert.deepEqual(events.pop(),['selection',['cicd','prod']])
 state.toggleAll(props.clusters[0]);assert.deepEqual(events.pop(),['selection',[]])
 state.toggleAll({id:'other',name:'other',is_active:true});assert.deepEqual(events.pop(),['cluster','other',true])
 scope.stop()
})
test('就绪徽标区分健康、更新、故障、未知和缩至零，不把缺失数值标绿',()=>{
 const object={kind:'Deployment',metadata:{generation:3},spec:{replicas:3},status:{observedGeneration:3,readyReplicas:3,updatedReplicas:3}}
 const cell=()=>presentation.resourceCells(object,translate).replicas
 assert.equal(cell().text,'3/3');assert.equal(cell().badge,'success')
 object.status.observedGeneration=2;assert.equal(cell().badge,'warning')
 object.status.conditions=[{type:'ReplicaFailure',status:'True'}];assert.equal(cell().badge,'danger')
 object.status={};assert.equal(cell().text,'—/3');assert.equal(cell().badge,'neutral')
 object.spec.replicas=0;object.status={readyReplicas:0};assert.equal(cell().text,'0/0');assert.equal(cell().badge,'neutral')
 const pod={kind:'Pod',metadata:{},spec:{containers:[{name:'web'}]},status:{phase:'Running',conditions:[{type:'Ready',status:'True'}],containerStatuses:[{ready:true}]}}
 assert.equal(presentation.resourceCells(pod,translate).replicas.badge,'success')
})
test('多选范围在分类、浏览历史和集群切换后保留',async()=>{
 const route=vue.reactive({query:{view:'workloads',cluster:'a',namespace:['prod','test']}}),replacements=[],scope=vue.effectScope()
 const localAPI={...api,listKubeResourceScopes:async()=>({items:[],cursor:{}})}
 const component=load('../src/views/KubernetesView.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:translate})},'@/stores/auth':{useAuthStore:()=>({canAny:()=>true})},'@/api/kubernetes':localAPI,'@/utils/kubernetesPresentation':presentation,
  'vue-router':{useRoute:()=>route,useRouter:()=>({replace:value=>{replacements.push(value);route.query=value.query}})},
  '@/api/client':{__esModule:true,default:{get:async path=>({data:path.endsWith('/namespaces')?{namespaces:['prod','test']} : overview('prod',2)})}},
 }).default
 let state;scope.run(()=>{state=component.setup({}, {expose(){}})})
 state.clusters.value=[{id:'a',is_active:true,default_namespace:'default'},{id:'b',is_active:true,default_namespace:'system'}];state.cluster.value='a';await vue.nextTick()
 assert.deepEqual(state.selectedNamespaces.value,['prod','test'])
 route.query={...route.query,view:'network'};await vue.nextTick();assert.deepEqual(state.selectedNamespaces.value,['prod','test'])
 state.selectCluster('b',true);await vue.nextTick();assert.deepEqual(state.selectedNamespaces.value,['*'])
 state.selectCluster('a');await vue.nextTick();assert.deepEqual(state.selectedNamespaces.value,['prod','test'])
 route.query={...route.query,namespace:'prod'};await vue.nextTick();assert.deepEqual(state.selectedNamespaces.value,['prod'])
 assert.ok(replacements.length>0);scope.stop()
})
