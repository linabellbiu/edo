import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {afterEach,test} from 'node:test'
import {parse,compileScript} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function load(path,modules){
 const source=readFileSync(new URL(path,import.meta.url),'utf8')
 const code=path.endsWith('.vue')?compileScript(parse(source).descriptor,{id:'kubernetes-test'}).content:source
 const output=ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText
 const module={exports:{}}
 new Function('require','module','exports',output)(key=>modules[key]||(key==='@/utils/kubernetesWorkspace'?load('../src/utils/kubernetesWorkspace.ts',{}):{}),module,module.exports)
 return module.exports
}
const api=load('../src/api/kubernetes.ts',{})
const workloadForm=load('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api})
const presentation=load('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':api,'@/utils/kubernetesRoles':load('../src/utils/kubernetesRoles.ts')})
const details=load('../src/utils/kubernetesDetails.ts',{'@/api/kubernetes':api})
test('资源数量按原始 SI、二进制及指数含义换算，截图中的 Ki 和无单位字节统一可读',()=>{
 for(const [input,bytes] of [['2147483Ki',2147483*1024],['955574586848',955574586848],['982292956Ki',982292956*1024],['1.5Gi',1.5*1024**3],['.5Mi',.5*1024**2],['128Mi',128*1024**2],['1G',1e9],['1M',1e6],['1e6',1e6],['1E+6',1e6],['1E',1e18],['1Ti',1024**4],['400m',.4],['10u',.00001],['100n',.0000001]])assert.ok(Math.abs(api.parseKubeQuantity(input)-bytes)<=Number.EPSILON*bytes,input)
 assert.equal(api.formatKubeQuantity('memory','2147483Ki'),'2.05 GiB')
 assert.equal(api.formatKubeQuantity('ephemeral-storage','955574586848'),'889.95 GiB')
 assert.equal(api.formatKubeQuantity('storage','982292956Ki'),'936.79 GiB')
 assert.equal(api.formatKubeQuantity('storage','1G'),'953.67 MiB')
 assert.equal(api.formatKubeQuantity('memory','1.5Gi'),'1.5 GiB')
 assert.equal(api.formatKubeQuantity('memory','1M'),'0.95 MiB')
})
test('容量阈值、微小非零值和缺失值有明确区别，CPU 与计数不使用存储单位',()=>{
 for(const bad of [undefined,null,'',' ','bad','1GB','1MiB','1e3Mi','1ki','NaN',NaN,Infinity,-1,'-1Gi','1e999','1e-999']){
  assert.equal(api.parseKubeQuantity(bad),undefined,String(bad));assert.equal(api.formatKubeQuantity('memory',bad),'—',String(bad))
 }
 assert.equal(api.formatKubeMemory(0),'0 MiB');assert.equal(api.formatKubeMemory(1024**3),'1 GiB')
 assert.equal(api.formatKubeMemory(128*1024**2),'128 MiB');assert.equal(api.formatKubeMemory(300*1024),'0.29 MiB');assert.equal(api.formatKubeMemory(1),'<0.01 MiB')
 for(const bad of [null,NaN,Infinity,-1]){assert.equal(api.formatKubeMemory(bad),'—');assert.equal(api.formatKubeCPU(bad),'—')}
 assert.equal(api.formatKubeCPU(.0000001),'<0.01 m');assert.equal(api.formatKubeQuantity('cpu','250m'),'250 m');assert.equal(api.formatKubeQuantity('cpu','16'),'16 CPU')
 assert.equal(api.formatKubeQuantity('pods','110'),'110');assert.equal(api.formatKubeQuantity('nvidia.com/gpu','2'),'2');assert.equal(api.formatKubeQuantity('nvidia.com/gpu','0'),'0')
 assert.equal(api.formatKubeQuantity('nvidia.com/gpu','1.5'),'—');assert.equal(api.formatKubeQuantity('hugepages-2Mi','0'),'0 MiB');assert.equal(api.formatKubeQuantity('hugepages-1Gi','2Gi'),'2 GiB')
})
test('Pod 与工作负载详情统一请求和限制，并展示 GPU、大页内存，原始配置不变',()=>{
 const object={kind:'Pod',metadata:{name:'worker'},spec:{containers:[{name:'app',resources:{requests:{cpu:'100m',memory:'128Mi','ephemeral-storage':'1G','nvidia.com/gpu':'2'},limits:{memory:'1.5Gi','hugepages-2Mi':'80Mi'}}}]}}
 const before=JSON.stringify(object)
 const component=load('../src/components/KubeResourceDetails.vue',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})},'@/api/kubernetes':api,'@/utils/kubernetesPresentation':presentation,'@/utils/kubernetesDetails':details}).default
 const state=component.setup({object,type:api.kubeTypes.find(item=>item.kind==='Pod'),canFollow:()=>false},{expose(){},emit(){}})
 assert.equal(state.resources(state.containers.value[0].container,'requests'),'cpu: 100 m · memory: 128 MiB · ephemeral-storage: 953.67 MiB · nvidia.com/gpu: 2')
 assert.equal(state.resources(state.containers.value[0].container,'limits'),'memory: 1.5 GiB · hugepages-2Mi: 80 MiB')
 assert.equal(state.resources({},'requests'),'—');assert.match(state.rawResources(state.containers.value[0].container,'limits'),/1.5Gi/)
 assert.equal(JSON.stringify(object),before)
 const pvc={kind:'PersistentVolumeClaim',metadata:{},spec:{resources:{requests:{storage:'1536Mi'}}},status:{capacity:{storage:'2Gi'}}}
 assert.equal(presentation.resourceCells(pvc,key=>key).capacity.text,'1.5 GiB');assert.match(presentation.resourceCells(pvc,key=>key).capacity.secondary,/2 GiB/)
 assert.match(api.kubeSummary(pvc).primary,/2 GiB/)
 assert.equal(presentation.resourceCells({...pvc,status:{}},key=>key).capacity.secondary,'k8s.allocated: —')
 assert.match(api.kubeSummary({kind:'PersistentVolume',spec:{capacity:{storage:'512Mi'}},metadata:{}}).primary,/512 MiB/)
})
test('节点三条用量条显示已用与可分配量，0 值有效，超量只截取条宽不截取数值',()=>{
 const now=Date.now(),node={kind:'Node',metadata:{name:'worker',uid:'node-1',creationTimestamp:new Date(now-3600_000).toISOString()}}
 const data={nodes:[{name:'worker',uid:'node-1',cpu:4,memory:8*1073741824,pod_capacity:110,pods:26,pod_state:'ok'}],usage:{nodes:{worker:{cpu:.229,memory:3.7*1073741824,timestamp:new Date(now).toISOString()}}},collected_at:new Date(now).toISOString()}
 let meters=api.nodeResourceUsage(data,node,false,now)
 assert.equal(meters.length,3);assert.ok(Math.abs(meters[0].percent-5.725)<1e-10);assert.equal(meters[1].percent,46.25);assert.equal(meters[2].percent,26/110*100)
 data.usage.nodes.worker.cpu=0;data.nodes[0].pods=0;meters=api.nodeResourceUsage(data,node,false,now)
 assert.equal(meters[0].percent,0);assert.equal(meters[0].state,'ok');assert.equal(meters[2].state,'ok')
 data.usage.nodes.worker.cpu=5;meters=api.nodeResourceUsage(data,node,false,now)
 assert.equal(meters[0].percent,125);assert.equal(meters[0].width,100);assert.equal(meters[0].used,5)
})
test('节点用量遇到旧 UID、过期采样、读取错误或无权限时显示空缺，不伪造零',()=>{
 const now=Date.now(),node={kind:'Node',metadata:{name:'worker',uid:'node-1',creationTimestamp:new Date(now-3600_000).toISOString()}}
 const data={nodes:[{name:'worker',uid:'node-1',cpu:4,memory:8*1073741824,pod_capacity:110,pods:null,pod_state:'no_permission'}],usage:{nodes:{worker:{cpu:1,memory:1024,timestamp:new Date(now-180_000).toISOString()}}},collected_at:new Date(now).toISOString()}
 let meters=api.nodeResourceUsage(data,node,false,now)
 assert.equal(meters[0].used,undefined);assert.equal(meters[2].used,undefined);assert.equal(meters[2].state,'no_permission')
 data.usage.nodes.worker.timestamp=new Date(now).toISOString()
 assert.equal(api.nodeResourceUsage(data,node,true,now)[0].used,undefined)
 data.nodes[0].uid='old-node';assert.equal(api.nodeResourceUsage(data,node,false,now)[0].total,undefined)
 data.nodes[0].uid='node-1';data.collected_at=new Date(now-180_000).toISOString();assert.equal(api.nodeResourceUsage(data,node,false,now)[0].used,undefined)
 data.collected_at=new Date(now).toISOString();data.nodes[0].cpu=0
 meters=api.nodeResourceUsage(data,node,false,now);assert.equal(meters[0].percent,undefined);assert.equal(meters[0].state,'missing_capacity')
})
test('资源条的悬浮提示保留完整数值和统计口径，键盘聚焦可查看',()=>{
 const component=load('../src/components/KubeNodeUsage.vue',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})},'@/api/kubernetes':api}).default
 const now=Date.now(),node={kind:'Node',metadata:{name:'worker',uid:'1'}},overview={collected_at:new Date(now).toISOString(),nodes:[{name:'worker',uid:'1',cpu:4,memory:8*1073741824,pod_capacity:110,pods:26,pod_state:'ok'}],usage:{nodes:{worker:{cpu:.229,memory:3.7*1073741824,timestamp:new Date(now).toISOString()}}}}
 const state=component.setup({node,overview},{expose(){}})
 assert.equal(state.numbers(state.meters.value[0]),'229 m / 4 CPU');assert.equal(state.numbers(state.meters.value[1]),'3.7 GiB / 8 GiB')
 assert.match(state.description.value,/CPU 5.7%/);assert.match(state.description.value,/26 \/ 110/)
 assert.equal(state.numbers({...state.meters.value[1],used:300*1024,total:1024**3}),'0.29 MiB / 1 GiB')
 assert.match(state.numbers({...state.meters.value[1],used:300*1024},true),/307200 B/)
 const source=readFileSync(new URL('../src/components/KubeNodeUsage.vue',import.meta.url),'utf8')
 assert.match(source,/tabindex="0"/);assert.match(source,/\['hover','focus'\]/);assert.match(source,/:mouse-enter-delay="0.1"/)
})
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
 props.data.usage.nodes.b.memory=null
 const incomplete=component.setup({...props,data:{...props.data}},{expose(){},emit(){}})
 assert.equal(incomplete.totals.value.memoryUsed,undefined);assert.equal(incomplete.totals.value.cpuUsed,1.5)
 const missing=component.setup({...props,data:{...props.data,nodes:null}},{expose(){},emit(){}})
 assert.equal(missing.totals.value.memory,undefined);assert.equal(missing.percentage(undefined,undefined),'—')
})
const editorScopes=[]
afterEach(()=>{for(const scope of editorScopes.splice(0))scope.stop()})
function editor(kind,object){
 const component=load('../src/components/KubeResourceEditor.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){}},
  'vue-i18n':{useI18n:()=>({t:key=>key})},
  '@/api/kubernetes':api,
  '@/utils/kubernetesWorkloadForm':workloadForm,
  '@/utils/kubernetesPresentation':presentation,
 }).default
 const props=vue.reactive({open:true,cluster:'cluster-a',namespace:'team',type:api.kubeTypes.find(item=>item.kind===kind),object})
 const scope=vue.effectScope();editorScopes.push(scope)
 let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit(){}})})
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
  '@/utils/kubernetesPresentation':presentation,
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

const translate=(key,values={})=>key+Object.entries(values).map(([key,value])=>` ${key}=${value}`).join('')
test('后台总览同步不闪加载，不重复请求；首屏仍有加载反馈',async()=>{
 const pending=[]
 const component=load('../src/views/KubernetesView.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},'vue-i18n':{useI18n:()=>({t:translate})},
  'vue-router':{useRoute:()=>({query:{view:'overview'}}),useRouter:()=>({})},'@/stores/auth':{useAuthStore:()=>({canAny:()=>true})},
  '@/api/kubernetes':api,'@/utils/kubernetesPresentation':presentation,
  '@/api/client':{__esModule:true,default:{get:()=>new Promise(resolve=>pending.push(resolve))}},
 }).default
 const state=component.setup({}, {expose(){}})
 state.cluster.value='a';state.clusters.value=[{id:'a',is_active:true}];state.overview.value={counts:{}}
 const sync=state.refreshOverview(true);assert.equal(state.overviewLoading.value,false)
 await state.refreshOverview(true);assert.equal(pending.length,1)
 pending.shift()({data:{counts:{pods:{count:2,state:'ok'}}}});await sync;assert.equal(state.overview.value.counts.pods.count,2)
 state.overview.value=null;const initial=state.refreshOverview(true);assert.equal(state.overviewLoading.value,true)
 pending.shift()({data:{counts:{}}});await initial;assert.equal(state.overviewLoading.value,false)
})
test('所有资源使用各自的关键列，不再用通用配置列代替',()=>{
 for(const type of api.kubeTypes){
  const columns=presentation.resourceColumns(type,translate),keys=columns.map(column=>column.key)
  assert.equal(keys[0],'name');assert.equal(keys.at(-1),'created');assert.equal(new Set(keys).size,keys.length)
  assert.equal(keys.includes('configuration'),false);assert.equal(keys.includes('namespace'),type.namespaced)
 }
 const columns=resource=>presentation.resourceColumns(api.kubeTypes.find(type=>type.resource===resource),translate).map(column=>column.key)
 assert.ok(columns('deployments').includes('replicas'));assert.ok(columns('pods').includes('restarts'))
 assert.ok(columns('persistentvolumeclaims').includes('volume'));assert.ok(columns('storageclasses').includes('binding'))
 assert.ok(columns('services').includes('servicePorts'));assert.ok(columns('ingresses').includes('routes'));assert.ok(columns('networkpolicies').includes('egressRules'))
})
test('工作负载状态等待控制器确认版本，Running 但未就绪不显示成功',()=>{
 const object={kind:'Deployment',metadata:{generation:3},spec:{replicas:2},status:{observedGeneration:2,readyReplicas:2,updatedReplicas:2}}
 assert.equal(presentation.resourceStatus(object,translate).key,'updating')
 object.status.observedGeneration=3;assert.equal(presentation.resourceStatus(object,translate).key,'ready')
 object.status.updatedReplicas=1;assert.equal(presentation.resourceStatus(object,translate).key,'updating')
 object.spec.replicas=0;assert.equal(presentation.resourceStatus(object,translate).key,'scaledDown')
 const pod={kind:'Pod',metadata:{},spec:{containers:[{name:'app'}]},status:{phase:'Running',conditions:[{type:'Ready',status:'False'}],containerStatuses:[{name:'app',ready:false,state:{waiting:{reason:'ImagePullBackOff'}}}]}}
 assert.equal(presentation.resourceStatus(pod,translate).reason,'ImagePullBackOff');assert.notEqual(presentation.resourceStatus(pod,translate).tone,'success')
 assert.equal(presentation.resourceCells({kind:'Pod',metadata:{},spec:{containers:[{name:'app'}]}},translate).replicas.text,'—/1')
 pod.metadata.deletionTimestamp='2026-10-23T00:00:00Z';assert.equal(presentation.resourceStatus(pod,translate).key,'terminating')
 for(const kind of ['Service','Ingress','NetworkPolicy'])assert.equal(presentation.resourceStatus({kind,metadata:{},spec:{}},translate).key,'configured')
})
test('存储和网络单元格完整展示绑定、命名端口、多条路径，不把无状态当健康',()=>{
 const pvc={kind:'PersistentVolumeClaim',metadata:{name:'data',namespace:'team'},spec:{resources:{requests:{storage:'5Gi'}},storageClassName:'fast',volumeName:'pv-1',accessModes:['ReadWriteOnce']},status:{phase:'Bound',capacity:{storage:'8Gi'}}}
 const cells=presentation.resourceCells(pvc,translate)
 assert.equal(cells.capacity.text,'5 GiB');assert.match(cells.capacity.secondary,/8 GiB/);assert.equal(cells.capacity.raw,'5Gi / 8Gi');assert.equal(cells.volume.reference.resource,'persistentvolumes')
 assert.deepEqual(cells.storageClass.reference,{resource:'storageclasses',name:'fast',namespace:undefined})
 const service={kind:'Service',metadata:{},spec:{type:'NodePort',clusterIPs:['10.0.0.1','fd00::1'],ports:[{port:80,targetPort:'http',nodePort:30080},{port:53,targetPort:53,protocol:'UDP'}]}}
 const ports=presentation.resourceCells(service,translate).servicePorts.text;assert.match(ports,/80 → http\/TCP \(:30080\)/);assert.match(ports,/53 → 53\/UDP/)
 const ingress={spec:{rules:[{host:'a.test',http:{paths:[{path:'/api',pathType:'Prefix',backend:{service:{name:'api',port:{name:'http'}}}}]}},{host:'b.test',http:{paths:[{path:'/',backend:{service:{name:'web',port:{number:80}}}}]}}],defaultBackend:{service:{name:'fallback',port:{number:8080}}}}}
 assert.equal(presentation.ingressRoutes(ingress).length,3);assert.equal(presentation.ingressRoutes(ingress)[0].port,'http');assert.equal(presentation.ingressRoutes(ingress)[2].service,'fallback')
})
test('搜索只使用资源摘要，Secret 和 ConfigMap 的数据原文不进入索引',()=>{
 for(const kind of ['ConfigMap','Secret']){
  const object={kind,metadata:{name:'settings',labels:{app:'web'}},keys:['password'],data:{password:'do-not-index-this'},stringData:{password:'nor-this'},binaryData:{cert:'or-this'}}
  const text=presentation.resourceSearchText(object,translate)
  assert.match(text,/settings/);assert.match(text,/app=web/);assert.doesNotMatch(text,/do-not-index-this|nor-this|or-this/)
  assert.ok(presentation.matchesLabelFilter(object,'app=web, app'));assert.equal(presentation.matchesLabelFilter(object,'app=api'),false)
 }
})
test('关联 Pod 严格使用选择器、命名空间、实际引用和任务归属，不用节点选择器冒充 Pod 标签',()=>{
 const pod={kind:'Pod',metadata:{name:'web',namespace:'team',labels:{app:'web',tier:'api'},ownerReferences:[{kind:'Job',uid:'job-1'}]},spec:{nodeName:'worker',containers:[{name:'app',env:[{name:'TOKEN',valueFrom:{secretKeyRef:{name:'token',key:'value'}}}],envFrom:[{configMapRef:{name:'env'}}]}],volumes:[{persistentVolumeClaim:{claimName:'data'}},{projected:{sources:[{secret:{name:'projected'}},{configMap:{name:'files'}}]}}],imagePullSecrets:[{name:'registry'}]}}
 assert.equal(presentation.relatedPodMatch({kind:'Node',metadata:{name:'worker'}},pod),true)
 assert.equal(presentation.relatedPodMatch({kind:'Service',metadata:{namespace:'team'},spec:{}},pod),false)
 assert.equal(presentation.relatedPodMatch({kind:'NetworkPolicy',metadata:{namespace:'team'},spec:{podSelector:{}}},pod),true)
 assert.equal(presentation.relatedPodMatch({kind:'Deployment',metadata:{namespace:'team'},spec:{selector:{matchLabels:{app:'web'},matchExpressions:[{key:'tier',operator:'NotIn',values:['frontend']}]}}},pod),true)
 assert.equal(presentation.relatedPodMatch({kind:'Deployment',metadata:{namespace:'team'},spec:{template:{spec:{nodeSelector:{app:'web'}}}}},pod),false)
 for(const [kind,name] of [['Secret','token'],['Secret','registry'],['Secret','projected'],['ConfigMap','env'],['ConfigMap','files'],['PersistentVolumeClaim','data']]){
  assert.equal(presentation.relatedPodMatch({kind,metadata:{name,namespace:'team'}},pod),true)
  assert.equal(presentation.relatedPodMatch({kind,metadata:{name,namespace:'other'}},pod),false)
 }
 assert.equal(presentation.relatedPodMatch({kind:'CronJob',metadata:{namespace:'team'}},pod,['job-1']),true)
 assert.equal(presentation.relatedPodMatch({kind:'CronJob',metadata:{namespace:'team'}},pod,['another-job']),false)
 assert.equal(presentation.matchesSelector({}, {matchExpressions:[{key:'tier',operator:'NotIn',values:['api']}]}),true)
 assert.equal(presentation.matchesSelector({},undefined),false)
})
test('跨类型详情使用自己的资源路径和权限，返回时保留原列表和筛选',async()=>{
 const paths=[]
 const component=load('../src/views/KubernetesView.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},'vue-i18n':{useI18n:()=>({t:translate})},
  'vue-router':{useRoute:()=>({query:{view:'network',resource:'ingresses'}}),useRouter:()=>({})},'@/stores/auth':{useAuthStore:()=>({canAny:permissions=>!permissions.includes('kubernetes.config.read')})},
  '@/api/kubernetes':api,'@/utils/kubernetesPresentation':presentation,
  '@/api/client':{__esModule:true,default:{get:async(path,options)=>{paths.push(path);if(path.endsWith('/events'))return {data:{items:[]}};const name=path.split('/').at(-1);return {data:{object:{kind:path.includes('/services/')?'Service':'Ingress',metadata:{name,namespace:options.params.namespace,uid:name}}}}}}},
 }).default
 const state=component.setup({}, {expose(){}})
 state.cluster.value='cluster-a';state.clusters.value=[{id:'cluster-a',is_active:true}];state.query.value='keep-filter'
 const ingress={kind:'Ingress',metadata:{name:'public',namespace:'team',uid:'public'}}
 await state.openDetails(ingress)
 await state.followReference({resource:'services',name:'api',namespace:'team'})
 assert.equal(state.detailType.value.resource,'services');assert.equal(state.activeType.value.resource,'ingresses');assert.equal(state.query.value,'keep-filter');assert.equal(state.detailOrigin.value,'public')
 assert.equal(state.detailHistory.value.length,1);assert.ok(paths.some(path=>path.endsWith('/resources/services/api')))
 const before=paths.length;await state.followReference({resource:'secrets',name:'token',namespace:'team'});assert.equal(paths.length,before)
 state.backDetail();await new Promise(resolve=>setImmediate(resolve));assert.equal(state.detail.value.metadata.name,'public');assert.equal(state.detailHistory.value.length,0)
 state.closeDetails();assert.equal(state.detailHistory.value.length,0);assert.equal(state.detailOrigin.value,'');assert.equal(state.query.value,'keep-filter')
})
test('表单提交失败向确认窗口返回拒绝，并保留错误和编辑内容',async()=>{
 const component=load('../src/components/KubeResourceEditor.vue',{
  vue:{...vue,watch(){}},'vue-i18n':{useI18n:()=>({t:translate})},'ant-design-vue':{message:{success(){}}},
  '@/api/kubernetes':{...api,kubeErrorMessage:()=> '保存失败，请重试'},
  '@/utils/kubernetesWorkloadForm':workloadForm,
  '@/api/client':{__esModule:true,default:{post:async()=>({data:{valid:true}}),request:async()=>{throw Error('private infrastructure error')}}},
 }).default
 const state=component.setup({open:true,cluster:'a',namespace:'team',type:api.kubeTypes.find(type=>type.kind==='Service')},{expose(){},emit(){throw Error('失败时不应关闭抽屉')}})
 state.form.name='keep-name'
 await assert.rejects(state.submit('/url','team','POST',{}))
 assert.equal(state.error.value,'保存失败，请重试');assert.equal(state.saving.value,false);assert.equal(state.form.name,'keep-name')
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

test('无事件的 null 返回显示空状态，读取失败后再次成功可恢复',async()=>{
 let result={items:null},failed=false
 const component=load('../src/views/KubernetesView.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},
  'vue-router':{useRoute:()=>({query:{view:'nodes'}}),useRouter:()=>({})},'@/stores/auth':{useAuthStore:()=>({canAny:()=>true})},
  '@/api/kubernetes':api,'@/utils/kubernetesPresentation':presentation,
  '@/api/client':{__esModule:true,default:{get:async()=>{if(failed)throw new Error('读取失败');return{data:result}}}},
 }).default
 const state=component.setup({}, {expose(){}}),node={kind:'Node',metadata:{name:'worker',uid:'uid-1'}}
 const loadEvents=()=>state.loadEvents('cluster',api.kubeTypes[0],node,0,new AbortController().signal)
 await loadEvents();assert.equal(state.eventsError.value,false);assert.deepEqual(state.events.value,[])
 result={items:[{metadata:{uid:'old'},lastTimestamp:'2026-10-23T00:00:00Z'},{metadata:{uid:'new'},lastTimestamp:'2026-10-23T01:00:00Z'}]}
 await loadEvents();assert.equal(state.events.value[0].metadata.uid,'new')
 failed=true;await loadEvents();assert.equal(state.eventsError.value,true)
 failed=false;result={items:[]};await loadEvents();assert.equal(state.eventsError.value,false);assert.deepEqual(state.events.value,[])
})
test('Deployment 表单组合 Init、卷挂载和更新策略，Recreate 清理旧 rollingUpdate，保留模板其他配置',()=>{
 const original={kind:'Deployment',metadata:{name:'web',namespace:'team',uid:'d1',resourceVersion:'2'},spec:{replicas:2,strategy:{type:'RollingUpdate',rollingUpdate:{maxSurge:'50%',maxUnavailable:0}},minReadySeconds:5,progressDeadlineSeconds:60,revisionHistoryLimit:3,template:{metadata:{labels:{app:'web'}},spec:{containers:[{name:'app',image:'app:1',volumeMounts:[{name:'config',mountPath:'/config'}]}],initContainers:[{name:'init',image:'busybox:1',command:['true']}],volumes:[{name:'config',configMap:{name:'config',items:[{key:'app.yaml',path:'app.yaml'}]}}],imagePullSecrets:[{name:'registry'}]}}}}
 const state=editor('Deployment',original);state.form.strategy='Recreate';state.form.containers[0].memoryRequest='128Mi';state.form.containers[0].memoryLimit='512Mi'
 const object=state.buildObject();assert.deepEqual(object.spec.strategy,{type:'Recreate',rollingUpdate:null});assert.equal(object.spec.template.spec.initContainers[0].name,'init');assert.equal(object.spec.template.spec.volumes[0].configMap.items[0].key,'app.yaml');assert.equal(object.spec.template.spec.imagePullSecrets[0].name,'registry');assert.equal(object.spec.minReadySeconds,5);assert.equal(object.spec.revisionHistoryLimit,3);assert.equal(object.spec.template.spec.containers[0].resources.requests.memory,'128Mi')
 state.form.strategy='RollingUpdate';state.form.maxSurge='0';state.form.maxUnavailable='0';assert.throws(()=>state.buildObject(),/invalidStrategy/)
 state.form.maxSurge='25%';state.form.initContainers[0].name='app';assert.throws(()=>state.buildObject(),/invalidKeys/)
})
