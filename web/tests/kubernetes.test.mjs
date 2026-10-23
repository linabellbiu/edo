import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse,compileScript} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function load(path,modules){
 const source=readFileSync(new URL(path,import.meta.url),'utf8')
 const code=path.endsWith('.vue')?compileScript(parse(source).descriptor,{id:'kubernetes-test'}).content:source
 const output=ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText
 const module={exports:{}}
 new Function('require','module','exports',output)(key=>modules[key]||{},module,module.exports)
 return module.exports
}
const api=load('../src/api/kubernetes.ts',{})
test('独立 Pod 直接创建 spec，支持重启策略和节点标签，不套工作负载模板',()=>{
 const state=editor('Pod');state.form.name='standalone';state.form.containers[0].image='nginx:stable-alpine';state.restartPolicy.value='Never';state.nodeSelector.value=[{key:'pool',value:'test'}]
 const object=state.buildObject()
 assert.equal(object.kind,'Pod');assert.equal(object.spec.containers[0].image,'nginx:stable-alpine');assert.equal(object.spec.template,undefined)
 assert.equal(object.spec.restartPolicy,'Never');assert.deepEqual(object.spec.nodeSelector,{pool:'test'})
 assert.equal(api.kubeTypes.find(type=>type.kind==='Pod').create,true)
 assert.equal(api.kubeTypes.find(type=>type.kind==='Node').create,false)
})
test('资源指标不向同名新 Pod 套用旧采样，缺失指标不显示为零',()=>{
 const object={kind:'Pod',metadata:{name:'app',namespace:'team',creationTimestamp:'2026-10-23T02:00:00Z'}}
 const data={usage:{pods:{'team/app':{cpu:.02,memory:1024,timestamp:'2026-10-23T01:00:00Z'}}}}
 assert.equal(api.usageFor(data,object),undefined);assert.equal(api.formatKubeCPU(undefined),'—');assert.equal(api.formatKubeMemory(undefined),'—')
 data.usage.pods['team/app'].timestamp=new Date().toISOString();assert.equal(api.usageFor(data,object).cpu,.02)
})
test('总览只展示获准资源，缺少一个节点的用量时不把部分用量当成全局',()=>{
 const component=load('../src/components/KubeOverview.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},'vue-i18n':{useI18n:()=>({t:key=>key,locale:vue.ref('zh-CN')})},'echarts/core':{use(){}},'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},'@/api/kubernetes':api}).default
 const props={data:{counts:{nodes:{count:2,state:'ok'},pods:{count:0,state:'unavailable'}},nodes:[{name:'a',ready:true,cpu:2,memory:1024},{name:'b',ready:false,cpu:2,memory:1024}],phases:{},usage:{nodes:{a:{cpu:1,memory:100,timestamp:new Date().toISOString()}}}},loading:false,error:''}
 const state=component.setup(props,{expose(){},emit(){}})
 assert.equal(state.totals.value.cpu,4);assert.equal(state.totals.value.cpuUsed,undefined)
 assert.equal(state.cards.value.some(card=>card.key==='secrets'),false)
 props.data.usage.nodes.b={cpu:.5,memory:200,timestamp:new Date().toISOString()}
 // 替换 props.data 使该纯函数测试触发 Vue 计算属性重新求值。
 const next=component.setup({...props,data:{...props.data}},{expose(){},emit(){}})
 assert.equal(next.totals.value.cpuUsed,1.5);assert.equal(next.totals.value.memoryUsed,300)
})
function editor(kind,object){
 const watchers=[]
 const component=load('../src/components/KubeResourceEditor.vue',{
  vue:{...vue,watch:(source,callback)=>watchers.push(callback)},
  'vue-i18n':{useI18n:()=>({t:key=>key})},
  '@/api/kubernetes':api,
 }).default
 const props={open:true,cluster:'cluster-a',namespace:'team',type:api.kubeTypes.find(item=>item.kind===kind),object}
 const state=component.setup(props,{expose(){},emit(){}})
 watchers[0](true)
 return state
}
test('工作负载编辑保留探针、资源限制、Secret 引用和其他容器字段，不提交服务端状态',()=>{
 const state=editor('Deployment',{apiVersion:'apps/v1',kind:'Deployment',metadata:{name:'web',namespace:'team',uid:'1',resourceVersion:'2',labels:{old:'remove'}},spec:{replicas:2,template:{metadata:{labels:{app:'web'}},spec:{containers:[{name:'app',image:'old:1',resources:{limits:{cpu:'1'}},livenessProbe:{httpGet:{path:'/ready',port:80}},env:[{name:'PASSWORD',valueFrom:{secretKeyRef:{name:'private',key:'password'}}}],ports:[{name:'http',containerPort:80}]}]}}},status:{readyReplicas:2}})
 state.form.containers[0].image='new:2';state.form.labels=[]
 const object=state.buildObject(),container=object.spec.template.spec.containers[0]
 assert.equal(container.image,'new:2');assert.deepEqual(container.resources,{limits:{cpu:'1'}})
 assert.deepEqual(container.env[0].valueFrom,{secretKeyRef:{name:'private',key:'password'}})
 assert.equal(container.livenessProbe.httpGet.path,'/ready');assert.equal(container.ports[0].name,'http')
 assert.equal(object.metadata.labels.old,null);assert.equal(object.status,undefined)
 assert.equal(object.metadata.uid,'1');assert.equal(object.metadata.resourceVersion,'2')
})
test('Secret 盲改只提交新值和明确删除的键，空白已有值不会清空密钥',()=>{
 const state=editor('Secret',{metadata:{name:'private',namespace:'team',uid:'1',resourceVersion:'2'},type:'Opaque',keys:['keep','remove']})
 state.form.data=[{key:'keep',value:''},{key:'new',value:'changed'}]
 const object=state.buildObject()
 assert.deepEqual(object.stringData,{new:'changed'});assert.deepEqual(object.data,{remove:null})
 assert.equal(object.metadata.resourceVersion,'2')
})
test('网络和存储表单校验端口、容量和键名，不能提交空白或重复键',()=>{
 const service=editor('Service');service.form.name='web';service.form.port=65536
 assert.throws(()=>service.buildObject(),/invalidPort/)
 service.form.port=80;service.form.selector=[{key:'app',value:'web'},{key:'app',value:'other'}]
 assert.throws(()=>service.buildObject(),/invalidKeys/)
 const pvc=editor('PersistentVolumeClaim');pvc.form.name='data';pvc.form.storage='not-a-size'
 assert.throws(()=>pvc.buildObject(),/invalidStorage/)
})
test('集群页面关闭详情或切换记录后丢弃旧响应，不重新打开抽屉',async()=>{
 const pending=[]
 const component=load('../src/views/KubernetesView.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},
  'vue-i18n':{useI18n:()=>({t:key=>key})},
  'vue-router':{useRoute:()=>({query:{view:'nodes'}}),useRouter:()=>({})},
  '@/stores/auth':{useAuthStore:()=>({canAny:()=>true})},
  '@/api/kubernetes':api,
  '@/api/client':{__esModule:true,default:{get:()=>new Promise(resolve=>pending.push(resolve))}},
 }).default
 const state=component.setup({}, {expose(){}})
 state.cluster.value='test';state.clusters.value=[{id:'test',is_active:true}]
 const object=name=>({kind:'Node',metadata:{name,uid:name,resourceVersion:'1'}})
 const first=state.openDetails(object('a'));state.closeDetails();pending.shift()({data:{object:object('a')}});await first
 assert.equal(state.detailOpen.value,false);assert.equal(state.detail.value,undefined)
 const a=state.openDetails(object('a')),b=state.openDetails(object('b'))
 pending[1]({data:{object:object('b')}});await b
 pending[0]({data:{object:object('a')}});await a
 assert.equal(state.detail.value.metadata.name,'b')
})
test('Pod 摘要优先显示容器故障原因，不把未就绪的 Running 当成功',()=>{
 const summary=api.kubeSummary({kind:'Pod',metadata:{},spec:{},status:{phase:'Running',containerStatuses:[{ready:false,state:{waiting:{reason:'CrashLoopBackOff'}}}]}})
 assert.equal(summary.status,'CrashLoopBackOff');assert.notEqual(summary.tone,'success')
})

test('仅状态更新时重读版本；配置改变、资源重建或 Secret 变动时不覆盖',async()=>{
 let current,mutations=0,conflictOnce=false
 const sdk=load('../src/api/kubernetes.ts',{
  './client':{__esModule:true,default:{get:async()=>({data:{object:current}})}},
  axios:{__esModule:true,default:{isAxiosError:error=>Boolean(error.response)}},
 })
 const type=api.kubeTypes.find(item=>item.resource==='deployments')
 const original={metadata:{name:'web',namespace:'team',uid:'1',resourceVersion:'1',labels:{app:'web'}},spec:{replicas:1}}
 current={...original,metadata:{...original.metadata,resourceVersion:'2'},status:{readyReplicas:1}}
 const mutate=async object=>{mutations++;if(conflictOnce){conflictOnce=false;throw {response:{status:409}}}return object.metadata.resourceVersion}
 assert.equal(await sdk.withFreshKubeResource('test',type,original,'changed',mutate),'2')
 conflictOnce=true
 assert.equal(await sdk.withFreshKubeResource('test',type,original,'changed',mutate),'2')
 assert.equal(mutations,3)
 current={...current,spec:{replicas:2}}
 await assert.rejects(sdk.withFreshKubeResource('test',type,original,'changed',mutate),/changed/)
 current={...original,metadata:{...original.metadata,uid:'new'}}
 await assert.rejects(sdk.withFreshKubeResource('test',type,original,'changed',mutate),/changed/)
 current={...original,metadata:{...original.metadata,resourceVersion:'2'}}
 const secretType=api.kubeTypes.find(item=>item.resource==='secrets')
 await assert.rejects(sdk.withFreshKubeResource('test',secretType,original,'changed',mutate),/changed/)
 assert.equal(mutations,3)
})
