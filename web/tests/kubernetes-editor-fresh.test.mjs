import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse,compileScript} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import {createRouter,createMemoryHistory} from 'vue-router'

function load(path,modules={},globals={}){
 const raw=readFileSync(new URL(path,import.meta.url),'utf8')
 const source=path.endsWith('.vue')?compileScript(parse(raw).descriptor,{id:'kube-editor-fresh'}).content:raw
 const output=ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports',...Object.keys(globals),output)(name=>modules[name]??{},module,module.exports,...Object.values(globals))
 return module.exports
}
const api=load('../src/api/kubernetes.ts',{'./resources':{apiErrorMessage:()=> '资源读取失败，请重试'}})
const workspace=load('../src/utils/kubernetesWorkspace.ts')
const presentation=load('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':api,'@/utils/kubernetesRoles':load('../src/utils/kubernetesRoles.ts')})
const clone=value=>JSON.parse(JSON.stringify(value))
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const statefulType=api.kubeTypes.find(type=>type.kind==='StatefulSet')
function object({kind='StatefulSet',name='web',namespace='team',uid='workload-uid',version='12',partition=2}={}){
 return {apiVersion:kind==='CronJob'?'batch/v1':'apps/v1',kind,metadata:{name,namespace,uid,resourceVersion:version},spec:{replicas:2,updateStrategy:{type:'RollingUpdate',rollingUpdate:{partition}},template:{metadata:{labels:{app:name}},spec:{containers:[{name:'app',image:'app:1'}]}}}}
}
function resourceObject(resource,{name='settings',namespace='team',uid='config-uid',version='12',value='FIRST'}={}){
 const type=api.kubeTypes.find(item=>item.resource===resource)
 return {apiVersion:type.apiVersion,kind:type.kind,metadata:{name,namespace:type.namespaced?namespace:'',uid,resourceVersion:version},...(resource==='configmaps'?{data:{setting:value}}:{})}
}

async function harness({denied=[],resource='statefulsets'}={}){
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/kubernetes',component:{}}]})
 const type=api.kubeTypes.find(type=>type.resource===resource)
 await router.push(`/kubernetes?view=${type.group}&resource=${resource}&cluster=test&namespace=team`)
 const route=new Proxy({},{get:(_target,key)=>router.currentRoute.value[key]}),gets=[],writes=[],messages=[],unmounts=[],blocked=new Set(denied)
 const client={get:(url,options={})=>{
  if(/\/resources\/[^/]+\/[^/]+$/.test(url))return new Promise((resolve,reject)=>gets.push({url,options,resolve,reject}))
  if(url.endsWith('/namespaces'))return Promise.resolve({data:{namespaces:['team','other']}})
  if(url.endsWith('/events'))return Promise.resolve({data:{items:[]}})
  return Promise.resolve({data:{counts:{},nodes:[],usage:{},phases:{}}})
 }}
 for(const method of ['post','patch','put','delete','request'])client[method]=async(...args)=>{writes.push({method,args});throw new Error('编辑入口不应执行写操作')}
 const component=load('../src/views/KubernetesView.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn)},'vue-i18n':{useI18n:()=>({t:key=>key})},
  'vue-router':{useRoute:()=>route,useRouter:()=>router,onBeforeRouteLeave(){},onBeforeRouteUpdate(){}},
  'ant-design-vue':{Modal:{confirm:()=>({destroy(){}})},message:Object.fromEntries(['error','warning','success'].map(kind=>[kind,value=>messages.push({kind,value})]))},
  '@/api/kubernetes':{...api,listKubeResourceScopes:async()=>({items:[],cursor:{}})},'@/utils/kubernetesWorkspace':workspace,'@/utils/kubernetesPresentation':presentation,
  '@/stores/auth':{useAuthStore:()=>({canAny:permissions=>permissions.some(permission=>!blocked.has(permission))})},'@/api/client':{__esModule:true,default:client},
 },{document:{hidden:false,removeEventListener(){}},window:{removeEventListener(){}}}).default
 const scope=vue.effectScope();let state,stopped=false
 scope.run(()=>{state=component.setup({}, {expose(){}})})
 state.clusters.value=[{id:'test',name:'test',default_namespace:'team',is_active:true},{id:'other-cluster',name:'other',default_namespace:'team',is_active:true}]
 state.cluster.value='test';await settle()
 const stop=()=>{if(stopped)return;stopped=true;unmounts.forEach(fn=>fn());state.cancelListPersist();scope.stop()}
 return {state,router,route,gets,writes,messages,blocked,unmount:stop,stop}
}

test('工作负载编辑独立读取新版本，旧列表分区不直接进入表单',async t=>{
 const h=await harness();t.after(h.stop);const stale=object(),before=JSON.stringify(stale)
 h.state.rows.value=[stale]
 const pending=h.state.edit(stale)
 assert.equal(h.gets.length,1);assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.editorLoading.value,true);assert.equal(h.state.busy.value,api.kubeKey(stale))
 const request=h.gets[0]
 assert.equal(request.url,api.resourceURL('test',statefulType,'web'));assert.equal(request.options.params.namespace,'team');assert.equal(request.options.timeout,30000);assert.ok(request.options.signal instanceof AbortSignal)
 const fresh=object({version:'14',partition:1});request.resolve({data:{object:fresh}});await pending
 assert.equal(h.state.editorOpen.value,true);assert.equal(h.state.editorLoading.value,false);assert.equal(h.state.busy.value,'')
 assert.equal(h.state.editorContext.value.cluster,'test');assert.equal(h.state.editorContext.value.namespace,'team');assert.equal(h.state.editorContext.value.type.resource,'statefulsets')
 assert.equal(h.state.editorContext.value.object.metadata.resourceVersion,'14');assert.equal(h.state.editorContext.value.object.spec.updateStrategy.rollingUpdate.partition,1)
 assert.equal(JSON.stringify(stale),before);assert.equal(h.state.rows.value[0].metadata.resourceVersion,'12');assert.deepEqual(h.writes,[])
 fresh.spec.updateStrategy.rollingUpdate.partition=99;assert.equal(h.state.editorContext.value.object.spec.updateStrategy.rollingUpdate.partition,1)
})

test('工作负载编辑核对 UID、名称和命名空间，不把同名新对象交给编辑器',async t=>{
 for(const [field,value] of [['uid','replacement'],['name','other-name'],['namespace','other']]){
  const h=await harness();t.after(h.stop);const pending=h.state.edit(object()),fresh=object({version:'14',partition:1})
  fresh.metadata[field]=value;h.gets[0].resolve({data:{object:fresh}});await pending
  assert.equal(h.state.editorOpen.value,false,field);assert.equal(h.state.editorContext.value,undefined);assert.equal(h.state.editorLoading.value,false);assert.equal(h.state.busy.value,'')
  assert.deepEqual(h.messages,[{kind:'error',value:'k8s.resourceChanged'}]);assert.deepEqual(h.writes,[])
 }
})

test('工作负载编辑读取失败保留原页面并提供安全错误，允许重新请求',async t=>{
 const h=await harness();t.after(h.stop);const pending=h.state.edit(object())
 h.gets[0].reject(new Error('private infrastructure address'));await pending
 assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.editorLoading.value,false);assert.equal(h.state.busy.value,'');assert.deepEqual(h.messages,[{kind:'error',value:'资源读取失败，请重试'}])
 const retry=h.state.edit(object());assert.equal(h.gets.length,2);h.gets[1].resolve({data:{object:object({version:'14',partition:1})}});await retry
 assert.equal(h.state.editorOpen.value,true);assert.equal(h.state.editorContext.value.object.metadata.resourceVersion,'14')
})

test('工作负载编辑固定所点对象的命名空间，双击不会重复请求',async t=>{
 const h=await harness();t.after(h.stop);const target=object({namespace:'other'})
 const first=h.state.edit(target);await h.state.edit(target)
 assert.equal(h.gets.length,1);assert.equal(h.gets[0].options.params.namespace,'other')
 h.gets[0].resolve({data:{object:object({namespace:'other',version:'14',partition:1})}});await first
 assert.equal(h.state.editorContext.value.namespace,'other');assert.equal(h.state.editorContext.value.object.metadata.namespace,'other')
})

test('缺少工作负载读写权限、已有操作或编辑窗口时不读取新对象',async t=>{
 for(const denied of ['kubernetes.workload.read','kubernetes.workload.update']){
  const h=await harness({denied:[denied]});t.after(h.stop);await h.state.edit(object(),statefulType)
  assert.equal(h.gets.length,0,denied);assert.equal(h.state.editorOpen.value,false)
 }
 for(const locked of ['busy','yamlOpen','editorOpen']){
  const h=await harness();t.after(h.stop)
  if(locked==='busy')h.state.busy.value='another-operation'
  else h.state[locked].value=true
  await h.state.edit(object())
  assert.equal(h.gets.length,0,locked)
  if(locked==='busy')assert.equal(h.state.busy.value,'another-operation')
 }
})

test('不可编辑 Job 和 Pod 不产生编辑读取，工作负载及配置新建直接打开',async t=>{
 for(const kind of ['Job','Pod']){
  const h=await harness();t.after(h.stop);await h.state.edit(object({kind}),api.kubeTypes.find(type=>type.kind===kind))
  assert.equal(h.gets.length,0,kind);assert.equal(h.state.editorOpen.value,false,kind)
 }
 const create=await harness();t.after(create.stop);await create.state.edit()
 assert.equal(create.gets.length,0);assert.equal(create.state.editorOpen.value,true);assert.equal(create.state.editorContext.value.object,undefined);assert.equal(create.state.editorContext.value.namespace,'team')
 const forbidden=await harness({denied:['kubernetes.workload.create']});t.after(forbidden.stop);await forbidden.state.edit();assert.equal(forbidden.gets.length,0);assert.equal(forbidden.state.editorOpen.value,false)
 const config=await harness({resource:'configmaps'});t.after(config.stop);await config.state.edit()
 assert.equal(config.gets.length,0);assert.equal(config.state.editorOpen.value,true);assert.equal(config.state.editorContext.value.object,undefined)
})

test('切换资源范围或卸载中止编辑读取，迟到结果不弹窗或清理新操作状态',async t=>{
 for(const change of ['cluster','namespace','resource','unmount']){
  const h=await harness();t.after(h.stop);const pending=h.state.edit(object()),signal=h.gets[0].options.signal
  if(change==='unmount')h.unmount()
  else await h.router.push({query:{...h.route.query,...(change==='cluster'?{cluster:'other-cluster'}:change==='namespace'?{namespace:'other'}:{resource:'daemonsets'})}})
  await settle();assert.equal(signal.aborted,true,change);assert.equal(h.state.editorOpen.value,false)
  h.state.busy.value='new-operation';h.state.editorLoading.value=true
  h.gets[0].resolve({data:{object:object({version:'14',partition:1})}});await pending;await settle()
  assert.equal(h.state.editorOpen.value,false,change);assert.equal(h.state.editorContext.value,undefined);assert.equal(h.state.busy.value,'new-operation');assert.equal(h.state.editorLoading.value,true);assert.deepEqual(h.messages,[]);assert.deepEqual(h.writes,[])
 }
})

test('关闭或切换详情会取消待打开编辑器，迟到编辑对象不替代当前详情',async t=>{
 for(const change of ['close','other-detail']){
  const h=await harness();t.after(h.stop);h.state.detail.value=object();h.state.detailType.value=statefulType;h.state.detailOpen.value=true
  const pending=h.state.edit(object()),signal=h.gets[0].options.signal
  let detailPending
  if(change==='close')h.state.closeDetails()
  else detailPending=h.state.openDetails(object({name:'other-web',uid:'other-uid'}),'basic',statefulType)
  assert.equal(signal.aborted,true,change);h.gets[0].resolve({data:{object:object({version:'14',partition:1})}});await pending
  assert.equal(h.state.editorOpen.value,false);assert.deepEqual(h.messages,[])
  if(detailPending){h.gets[1].resolve({data:{object:object({name:'other-web',uid:'other-uid',version:'15'})}});await detailPending;assert.equal(h.state.detail.value.metadata.name,'other-web');assert.equal(h.state.detailOpen.value,true)}
  else assert.equal(h.state.detailOpen.value,false)
 }
})

test('从详情打开新版本编辑器保留父详情与列表上下文，关闭后仍在原详情',async t=>{
 const h=await harness();t.after(h.stop);const parent=object()
 h.state.detail.value=parent;h.state.detailType.value=statefulType;h.state.detailOpen.value=true;h.state.detailTab.value='events';h.state.query.value='keep-filter';h.state.currentPage.value=2
 const pending=h.state.edit(parent,statefulType)
 assert.equal(h.state.detailOpen.value,true);assert.equal(h.state.detailTab.value,'events');assert.equal(h.state.detail.value.metadata.resourceVersion,'12')
 h.gets[0].resolve({data:{object:object({version:'14',partition:1})}});await pending
 assert.equal(h.state.detailOpen.value,true);assert.equal(h.state.detailPresented.value,false);assert.equal(h.state.detail.value.metadata.resourceVersion,'12');assert.equal(h.state.query.value,'keep-filter');assert.equal(h.state.currentPage.value,2)
 h.state.editorOpen.value=false
 assert.equal(h.state.detailPresented.value,true);assert.equal(h.state.detailTab.value,'events');assert.equal(h.state.detail.value.metadata.uid,'workload-uid');assert.equal(h.state.query.value,'keep-filter');assert.equal(h.state.currentPage.value,2)
})

test('已打开草稿不会被再次编辑或迟到的打开请求覆盖',async t=>{
 const h=await harness();t.after(h.stop);const pending=h.state.edit(object())
 const draft={cluster:'test',namespace:'team',type:statefulType,object:object({name:'draft',uid:'draft-uid',version:'20'})}
 draft.object.spec.template.spec.containers[0].image='unsaved:1';h.state.editorContext.value=draft;h.state.editorOpen.value=true
 h.gets[0].resolve({data:{object:object({version:'14',partition:1})}});await pending
 assert.equal(h.state.editorContext.value.object.metadata.uid,'draft-uid');assert.equal(h.state.editorContext.value.object.spec.template.spec.containers[0].image,'unsaved:1')
 await h.state.edit(object({name:'another'}));assert.equal(h.gets.length,1);assert.equal(h.state.editorContext.value.object.metadata.uid,'draft-uid');assert.equal(h.state.editorContext.value.object.spec.template.spec.containers[0].image,'unsaved:1')
})

test('编辑读取期间撤销修改权限不会打开编辑器',async t=>{
 const h=await harness();t.after(h.stop);const pending=h.state.edit(object())
 h.blocked.add('kubernetes.workload.update');h.gets[0].resolve({data:{object:object({version:'14',partition:1})}});await pending
 assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.editorLoading.value,false);assert.equal(h.state.busy.value,'');assert.deepEqual(h.writes,[])
})

test('ConfigMap 旧列表 FIRST 不进入编辑器，独立读取 A 后才打开且不改旧行',async t=>{
 const h=await harness({resource:'configmaps'});t.after(h.stop)
 const stale=resourceObject('configmaps'),before=clone(stale);h.state.rows.value=[stale]
 const pending=h.state.edit(stale)
 assert.equal(h.gets.length,1);assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.editorLoading.value,true)
 assert.equal(h.gets[0].options.params.namespace,'team');assert.ok(h.gets[0].options.signal instanceof AbortSignal)
 const fresh=resourceObject('configmaps',{version:'14',value:'A'});h.gets[0].resolve({data:{object:fresh}});await pending
 assert.equal(h.state.editorContext.value.object.data.setting,'A');assert.equal(h.state.editorContext.value.object.metadata.resourceVersion,'14')
 assert.deepEqual(stale,before);assert.equal(h.state.rows.value[0].data.setting,'FIRST');assert.deepEqual(h.writes,[])
 fresh.data.setting='later';assert.equal(h.state.editorContext.value.object.data.setting,'A')
})

test('所有已有可编辑资源含 Service 均核新快照，保持所点对象的真实范围',async t=>{
 for(const type of api.kubeTypes.filter(item=>item.update)){
  const h=await harness({resource:type.resource});t.after(h.stop)
  const stale=resourceObject(type.resource,{namespace:'other'}),pending=h.state.edit(stale)
  assert.equal(h.gets.length,1,type.resource);assert.equal(h.state.editorOpen.value,false,type.resource)
  assert.equal(h.gets[0].options.params.namespace,type.namespaced?'other':'',type.resource)
  const fresh=resourceObject(type.resource,{namespace:'other',version:'19',value:'A'});h.gets[0].resolve({data:{object:fresh}});await pending
  assert.equal(h.state.editorContext.value.object.metadata.resourceVersion,'19',type.resource)
  assert.equal(h.state.editorContext.value.namespace,type.namespaced?'other':'',type.resource);assert.deepEqual(h.writes,[])
 }
})

test('ConfigMap 与 Service 不接受同 UID 异名称或命名空间的新响应',async t=>{
 for(const resource of ['configmaps','services'])for(const change of [{name:'other-name'},{namespace:'other'}]){
  const h=await harness({resource});t.after(h.stop);const pending=h.state.edit(resourceObject(resource))
  assert.equal(h.gets.length,1);h.gets[0].resolve({data:{object:resourceObject(resource,{...change,version:'14'})}});await pending
  assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.editorContext.value,undefined)
  assert.deepEqual(h.messages,[{kind:'error',value:'k8s.resourceChanged'}]);assert.deepEqual(h.writes,[])
 }
})

test('ConfigMap 关闭后立即从同一旧行重开仍重新读取，不等待列表或 Watch 校准',async t=>{
 const h=await harness({resource:'configmaps'});t.after(h.stop);const stale=resourceObject('configmaps')
 const first=h.state.edit(stale);assert.equal(h.gets.length,1);h.gets[0].resolve({data:{object:resourceObject('configmaps',{version:'14',value:'A'})}});await first
 h.state.editorOpen.value=false
 const reopen=h.state.edit(stale);assert.equal(h.gets.length,2);assert.equal(h.state.editorOpen.value,false)
 h.gets[1].resolve({data:{object:resourceObject('configmaps',{version:'15',value:'A-latest'})}});await reopen
 assert.equal(h.state.editorContext.value.object.data.setting,'A-latest');assert.equal(stale.data.setting,'FIRST');assert.deepEqual(h.writes,[])
})

test('ConfigMap 在途读取受切换范围、关闭详情和撤权约束，不覆盖已打开 B 草稿',async t=>{
 for(const change of ['cluster','namespace','close','permission','draft']){
  const h=await harness({resource:'configmaps'});t.after(h.stop);const pending=h.state.edit(resourceObject('configmaps'))
  assert.equal(h.gets.length,1);const signal=h.gets[0].options.signal
  if(change==='cluster'||change==='namespace')await h.router.push({query:{...h.route.query,...(change==='cluster'?{cluster:'other-cluster'}:{namespace:'other'})}})
  else if(change==='close')h.state.closeDetails()
  else if(change==='permission')h.blocked.add('kubernetes.config.update')
  else {h.state.editorContext.value={cluster:'test',namespace:'team',type:api.kubeTypes.find(item=>item.resource==='configmaps'),object:resourceObject('configmaps',{value:'DRAFT_B'})};h.state.editorOpen.value=true}
  await settle();if(['cluster','namespace','close'].includes(change))assert.equal(signal.aborted,true,change)
  h.gets[0].resolve({data:{object:resourceObject('configmaps',{version:'14',value:'A'})}});await pending
  if(change==='draft'){assert.equal(h.state.editorContext.value.object.data.setting,'DRAFT_B');await h.state.edit(resourceObject('configmaps'));assert.equal(h.gets.length,1)}
  else assert.equal(h.state.editorOpen.value,false,change)
  assert.deepEqual(h.writes,[]);assert.deepEqual(h.messages,[])
 }
})

test('ConfigMap 编辑器仍以原快照预检阻断并发 A 到 C，保留 B 草稿且无校验或写入',async t=>{
 const original=resourceObject('configmaps',{version:'14',value:'A'}),calls=[],events=[]
 const client={get:async()=>{calls.push('GET');return {data:{object:resourceObject('configmaps',{version:'15',value:'C'})}}},post:async()=>{calls.push('POST');return {}},request:async()=>{calls.push('PATCH');return {}}}
 const localApi=load('../src/api/kubernetes.ts',{'./client':{__esModule:true,default:client},'./resources':{apiErrorMessage:()=> '安全错误'}})
 const workload=load('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':localApi})
 const component=load('../src/components/KubeResourceEditor.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},'ant-design-vue':{message:{success:()=>events.push('success')}},
  '@/api/client':{__esModule:true,default:client},'@/api/kubernetes':localApi,'@/utils/kubernetesWorkloadForm':workload,
 },{window:{addEventListener(){},removeEventListener(){}}}).default
 const props=vue.reactive({open:true,cluster:'test',namespace:'team',type:localApi.kubeTypes.find(item=>item.resource==='configmaps'),object:clone(original)})
 const scope=vue.effectScope();t.after(()=>scope.stop());let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(...args)=>events.push(args)})});state.form.data[0].value='DRAFT_B'
 await assert.rejects(state.submit(localApi.resourceURL('test',props.type,'settings'),'team','PATCH',{...clone(original),data:{setting:'DRAFT_B'}}),/k8s.resourceChanged/)
 assert.deepEqual(calls,['GET']);assert.equal(state.form.data[0].value,'DRAFT_B');assert.equal(state.error.value,'k8s.resourceChanged');assert.equal(props.open,true);assert.deepEqual(events,[])
})
