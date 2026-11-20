import assert from 'node:assert/strict'
import {test} from 'node:test'
import {readFileSync} from 'node:fs'
import {parse,compileScript} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import {createRouter,createMemoryHistory} from 'vue-router'

function load(path,modules={},globals={}){
 const source=readFileSync(new URL(path,import.meta.url),'utf8'),code=path.endsWith('.vue')?compileScript(parse(source).descriptor,{id:'kube-watch'}).content:source
 const output=ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports',...Object.keys(globals),output)(name=>modules[name]??(name==='./sse'?load('../src/api/sse.ts'):{}),module,module.exports,...Object.values(globals));return module.exports
}
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const sample={kind:'Pod',metadata:{name:'web',namespace:'team',uid:'p1',resourceVersion:'1'}}

test('共享SSE分帧支持跨块CRLF、多行数据、注释、命名事件与EOF，拒绝无限事件',()=>{
 const {createSSEParser}=load('../src/api/sse.ts'),events=[],parser=createSSEParser((event,data)=>events.push({event,data}))
 parser.push(': heartbeat\r');parser.push('\nevent: change\r\ndata: {"type":\r\ndata: "reset"}\r');parser.push('\n\r\nevent: ping\rdata: {}\r\revent: ready\ndata: {}');parser.finish()
 assert.deepEqual(events,[{event:'change',data:'{"type":\n"reset"}'},{event:'ping',data:'{}'},{event:'ready',data:'{}'}])
 assert.throws(()=>createSSEParser(()=>{}).push('data: '+'x'.repeat(1_048_577)+'\n'),/too large/)
})

test('K8s SSE固定项目和命名空间、使用Cookie，忽略取消后的同块迟到事件',async()=>{
 let input,url
 const source='event: ready\ndata: {}\n\nevent: change\ndata: {"type":"MODIFIED","resource":"pods","namespace":"team","name":"web"}\n\nevent: change\ndata: {"type":"reset"}\n\n'
 const api=load('../src/api/kubernetes.ts',{}, {fetch:async(target,options)=>{url=target;input=options;return new Response(source,{headers:{'Content-Type':'text/event-stream'}})}})
 const controller=new AbortController(),events=[]
 await api.streamKubeEvents('cluster/a',['team','prod'],'project-1',controller.signal,event=>{events.push(event);if(event.type==='MODIFIED')controller.abort()})
 assert.equal(url,'/api/v1/kubernetes/clusters/cluster%2Fa/watch?namespace=team&namespace=prod')
 assert.equal(input.credentials,'include');assert.equal(input.headers['X-EDO-Project-ID'],'project-1');assert.equal(input.signal,controller.signal)
 assert.deepEqual(events,[{type:'ready'},{type:'MODIFIED',resource:'pods',namespace:'team',name:'web'}])
})

test('K8s SSE认证失败保留HTTP状态，异常类型与断流进入降级',async()=>{
 for(const response of [new Response('{}',{status:403}),new Response('{}',{headers:{'Content-Type':'application/json'}}),new Response('event: ready\ndata: {}\n\n',{headers:{'Content-Type':'text/event-stream'}})]){
  const api=load('../src/api/kubernetes.ts',{}, {fetch:async()=>response})
  await assert.rejects(api.streamKubeEvents('c',['*'],'p',new AbortController().signal,()=>{}),error=>error instanceof api.KubeWatchError&&(response.status!==403||error.status===403))
 }
})

async function harness({holdOverview=false,holdClusters=false,readPods,readNamespaces}={}){
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/kubernetes',component:{}}]});await router.push('/kubernetes?view=workloads&resource=pods&cluster=test&namespace=team')
 const route=new Proxy({},{get:(_,key)=>router.currentRoute.value[key]}),calls=[],sessions=[],pendingClusters=[],pendingOverviews=[],timers=new Map(),mounts=[],unmounts=[],listeners=new Map(),clock={now:100_000},storage={project:'project-1'},scope=vue.effectScope()
 let timerID=0
 const doc={hidden:false,addEventListener:(name,callback)=>listeners.set(name,callback),removeEventListener:name=>listeners.delete(name)}
 const win={addEventListener:(name,callback)=>listeners.set(name,callback),removeEventListener:name=>listeners.delete(name)}
 const client={get:async(url,options={})=>{calls.push({url,options});if(url==='/kubernetes/clusters'){if(holdClusters)return new Promise(resolve=>pendingClusters.push({options,resolve}));return {data:{clusters:[{id:'test',name:'test',default_namespace:'team',is_active:true},{id:'other',name:'other',default_namespace:'team',is_active:true}]}}}if(url.endsWith('/namespaces'))return readNamespaces?.(options)??{data:{namespaces:['team','prod']}};if(url.endsWith('/overview')){if(holdOverview)return new Promise(resolve=>pendingOverviews.push({options,resolve}));return {data:{counts:{},nodes:[],usage:{},phases:{},collected_at:new Date().toISOString()}}}if(url.endsWith('/events'))return {data:{items:[]}};if(url.endsWith('/resources/pods/web'))return {data:{object:sample}};if(readPods&&url.endsWith('/resources/pods'))return await readPods();return {data:{items:[sample],continue:''}}}}
 const api=load('../src/api/kubernetes.ts',{'./client':{__esModule:true,default:client}}),workspace=load('../src/utils/kubernetesWorkspace.ts'),auth=vue.reactive({user:{id:'reader',project_id:'project-1',permissions:['kubernetes.workload.read','kubernetes.config.read'],is_superuser:false},canAny(permissions){return !!this.user&&permissions.some(value=>this.user.permissions.includes(value))}})
 const component=load('../src/views/KubernetesView.vue',{
  vue:{...vue,onMounted:fn=>mounts.push(fn),onBeforeUnmount:fn=>unmounts.push(fn)},'vue-i18n':{useI18n:()=>({t:key=>key})},'vue-router':{useRoute:()=>route,useRouter:()=>router,onBeforeRouteLeave(){},onBeforeRouteUpdate(){}},
  '@/api/client':{__esModule:true,default:client},'@/api/kubernetes':{...api,streamKubeEvents:(cluster,namespaces,project,signal,onEvent)=>new Promise((resolve,reject)=>sessions.push({cluster,namespaces,project,signal,onEvent,resolve,reject}))},'@/utils/kubernetesWorkspace':workspace,'@/stores/auth':{useAuthStore:()=>auth},
 },{document:doc,window:win,localStorage:{getItem:()=>storage.project},setInterval:()=>++timerID,clearInterval(){},setTimeout:(callback,delay)=>{const id=++timerID;timers.set(id,{callback,delay});return id},clearTimeout:id=>timers.delete(id),Date:class extends Date{static now(){return clock.now}}}).default
 let state;scope.run(()=>{state=component.setup({}, {expose(){}})});mounts.forEach(fn=>fn());await settle()
 return {state,router,calls,sessions,pendingClusters,pendingOverviews,timers,auth,doc,listeners,clock,storage,api,async run(delay){for(const [id,timer]of [...timers])if(timer.delay===delay){timers.delete(id);timer.callback()}await settle()},stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}
const changes=h=>h.calls.filter(call=>call.url.endsWith('/resources/pods')).length
const podEvent={type:'MODIFIED',resource:'pods',namespace:'team',name:'web'}

test('Watch合并资源事件，只刷新相关范围；正常连接30秒校准，断开恢复5秒轮询',async t=>{
 const h=await harness();t.after(h.stop);const session=h.sessions.at(-1),before=changes(h)
 session.onEvent({type:'synced'});await h.run(500);const synced=changes(h);h.clock.now+=5000;h.state.syncVisible();await settle();assert.equal(changes(h),synced)
 session.onEvent(podEvent);session.onEvent(podEvent);await h.run(500);assert.equal(changes(h),synced+1)
 session.onEvent({...podEvent,namespace:'other'});await h.run(500);assert.equal(changes(h),synced+1)
 h.clock.now+=25_000;h.state.syncVisible();await settle();assert.equal(changes(h),synced+2)
 session.reject(new h.api.KubeWatchError());await settle();assert.equal(h.state.watchHealthy.value,false)
 h.clock.now+=5000;h.state.syncVisible();await settle();assert.equal(changes(h),synced+3)
 await h.run(1000);assert.equal(h.sessions.length,2)
})

test('EDO心跳存活不掩盖上游Watch降级，其他kind事件不能清除降级，synced后才恢复30秒校准',async t=>{
 const h=await harness();t.after(h.stop);const session=h.sessions.at(-1)
 session.onEvent({type:'ready'});assert.equal(h.state.watchHealthy.value,false)
 session.onEvent({type:'synced'});await h.run(500);assert.equal(h.state.watchHealthy.value,true)
 session.onEvent({type:'degraded'});await h.run(500);assert.equal(h.state.watchHealthy.value,false)
 for(const event of [{type:'ping'},{type:'ready'},{type:'reset'},{...podEvent,resource:'configmaps'}])session.onEvent(event)
 await h.run(500);assert.equal(h.state.watchHealthy.value,false)
 const before=changes(h);h.clock.now+=5000;h.state.syncVisible();await settle();assert.equal(changes(h),before+1)
 h.clock.now+=5000;session.onEvent({type:'ping'});h.state.syncVisible();await settle();assert.equal(changes(h),before+2)
 session.onEvent({type:'synced'});await h.run(500);const recovered=changes(h)
 h.clock.now+=5000;session.onEvent({type:'ping'});h.state.syncVisible();await settle();assert.equal(changes(h),recovered)
})

test('持续事件每500ms刷新列表而不饥饿，总览至少间隔5秒且保留最后一次dirty',async t=>{
 const h=await harness();t.after(h.stop);const session=h.sessions.at(-1)
 session.onEvent({type:'synced'});await h.run(500)
 const beforeList=changes(h),beforeOverview=h.calls.filter(call=>call.url.endsWith('/overview')).length
 for(let i=0;i<12;i++){h.clock.now+=500;session.onEvent(podEvent);await h.run(500)}
 assert.equal(changes(h),beforeList+12)
 assert.equal(h.calls.filter(call=>call.url.endsWith('/overview')).length,beforeOverview+1)
 assert.equal(h.state.watchDirty.has('overview'),true)
 h.clock.now+=4000;h.state.syncVisible();await settle()
 assert.equal(h.calls.filter(call=>call.url.endsWith('/overview')).length,beforeOverview+2)
 assert.equal(h.state.watchDirty.has('overview'),false)
})

test('快照通知在编辑时积累，结束编辑后重读并保留表单，手动刷新也只读快照',async t=>{
 const h=await harness();t.after(h.stop);const before=changes(h),context={object:{spec:{draft:true}}}
 h.state.editorOpen.value=true;h.state.editorContext.value=context;h.sessions.at(-1).onEvent({type:'reset'});await h.run(500)
 assert.equal(changes(h),before);assert.deepEqual(h.state.editorContext.value,context);assert.equal(h.state.editorOpen.value,true)
 h.state.editorOpen.value=false;await settle();await h.run(500);assert.equal(changes(h),before+1)
 h.calls.length=0;h.state.refreshAll();await settle()
 for(const suffix of ['/resources/pods','/overview','/namespaces'])assert.equal(h.calls.find(call=>call.url.endsWith(suffix)).options.params?.fresh,undefined)
})

test('手动重读总览中止旧请求，迟到旧快照不会覆盖新结果',async t=>{
 const h=await harness({holdOverview:true});t.after(h.stop);const previous=h.pendingOverviews.at(-1)
 const refresh=h.state.refreshOverview(false,true),fresh=h.pendingOverviews.at(-1)
 assert.notEqual(previous,fresh);assert.equal(previous.options.signal.aborted,true);assert.equal(fresh.options.params.fresh,undefined)
 const collectedAt='2026-11-19T01:00:00Z'
 fresh.resolve({data:{counts:{pods:{count:2,state:'ok'}}},headers:{'x-edo-snapshot-state':'stale','x-edo-snapshot-collected-at':collectedAt}});await refresh
 previous.resolve({data:{counts:{pods:{count:1,state:'ok'}}}});await settle()
 assert.equal(h.state.overview.value.counts.pods.count,2)
 assert.deepEqual(h.state.overviewSnapshot.value,{stale:true,collectedAt})
})

test('资源初次缺少快照时显示等待，采样时间由后台返回且过期结果明确标记',async t=>{
 let state='pending'
 const h=await harness({readPods(){
  if(state==='pending')throw {response:{status:503,data:{code:'kubernetes_snapshot_pending'}}}
  return {data:{items:[sample],continue:''},headers:{'x-edo-snapshot-collected-at':'2026-11-19T01:00:00Z','x-edo-snapshot-state':state}}
 }});t.after(h.stop)
 assert.equal(h.state.listSnapshotState.value,'pending')
 assert.equal(h.state.error.value,'')
 assert.equal(h.state.lastRefreshed.value,'')
 state='stale';await h.state.refresh(true)
 assert.deepEqual(h.state.rows.value,[sample])
 assert.equal(h.state.lastRefreshed.value,'2026-11-19T01:00:00Z')
 assert.equal(h.state.listSnapshotState.value,'stale')
 state='ok';await h.state.refresh(true)
 assert.equal(h.state.listSnapshotState.value,'')
})

test('集群、权限、项目和窗口可见性切换取消旧流，迟到事件不更新新范围',async t=>{
 const h=await harness();t.after(h.stop);const first=h.sessions.at(-1)
 h.state.cluster.value='other';await settle();assert.equal(first.signal.aborted,true);const before=changes(h);first.onEvent(podEvent);await h.run(500);assert.equal(changes(h),before)
 const current=h.sessions.at(-1);h.auth.user.permissions=[];await settle();assert.equal(current.signal.aborted,true);assert.equal(h.state.watchHealthy.value,false)
 h.auth.user.permissions=['kubernetes.workload.read'];await settle();const active=h.sessions.at(-1)
 h.doc.hidden=true;h.listeners.get('visibilitychange')();assert.equal(active.signal.aborted,true)
 h.doc.hidden=false;h.listeners.get('visibilitychange')();await settle();const visible=h.sessions.at(-1)
 h.storage.project='project-2';h.auth.user.project_id='project-2';h.listeners.get('storage')({key:'edo.project'});await settle();assert.equal(visible.signal.aborted,true);assert.equal(h.sessions.at(-1).project,'project-2')
 h.auth.user=null;await settle();assert.equal(h.sessions.at(-1).signal.aborted,true)
})

test('项目切换取消旧集群目录读取，迟到旧响应不能替换新项目集群或Watch',async t=>{
 const h=await harness({holdClusters:true});t.after(h.stop);const previous=h.pendingClusters[0]
 h.storage.project='project-2';h.auth.user.project_id='project-2';h.listeners.get('storage')({key:'edo.project'});await settle()
 assert.equal(previous.options.signal.aborted,true)
 h.pendingClusters.at(-1).resolve({data:{clusters:[{id:'new',name:'new-project-cluster',default_namespace:'team',is_active:true}]}});await settle()
 previous.resolve({data:{clusters:[{id:'test',name:'old-project-private-name',default_namespace:'team',is_active:true}]}});await settle()
 assert.equal(h.state.clusters.value[0].name,'new-project-cluster');assert.equal(h.state.cluster.value,'new')
 assert.equal(h.sessions.at(-1).cluster,'new');assert.equal(h.sessions.at(-1).project,'project-2')
})

test('只有workload.read且无Namespace事件时，健康与降级状态均每30秒校准选择器，降级5秒轮询不增加Namespace查询',async t=>{
 const h=await harness();t.after(h.stop);h.auth.user.permissions=['kubernetes.workload.read'];await settle()
 assert.equal(h.auth.canAny(['kubernetes.config.read']),false)
 const session=h.sessions.at(-1),namespaceReads=()=>h.calls.filter(call=>call.url.endsWith('/namespaces')).length
 session.onEvent({type:'synced'});await h.run(500);const healthy=namespaceReads()
 for(let i=0;i<5;i++){h.clock.now+=5000;h.state.syncVisible();await settle()}
 assert.equal(namespaceReads(),healthy)
 h.clock.now+=5000;h.state.syncVisible();await settle();assert.equal(namespaceReads(),healthy+1)
 session.onEvent({type:'degraded'});await h.run(500);const degraded=namespaceReads()
 for(let i=0;i<5;i++){h.clock.now+=5000;h.state.syncVisible();await settle()}
 assert.equal(namespaceReads(),degraded)
 h.clock.now+=5000;h.state.syncVisible();await settle();assert.equal(namespaceReads(),degraded+1)
 assert.equal(h.state.watchHealthy.value,false)
})

test('命名空间后台校准保留列表和错误，手动刷新反馈忙碌并忽略迟到响应',async t=>{
 let hold=false
 const requests=[]
 const h=await harness({readNamespaces:({signal})=>hold?new Promise((resolve,reject)=>requests.push({signal,resolve,reject})):undefined})
 t.after(h.stop)
 hold=true
 h.clock.now+=30_000
 h.state.syncVisible()
 assert.equal(h.state.namespaceLoading.value,false)
 assert.deepEqual(h.state.namespaces.value,['team','prod'])
 requests[0].reject(new h.api.KubeResourceChanged('读取失败'))
 await settle()
 assert.equal(h.state.namespaceError.value,'读取失败')

 h.clock.now+=30_000
 h.state.syncVisible()
 assert.equal(h.state.namespaceLoading.value,false)
 assert.equal(h.state.namespaceError.value,'读取失败')
 const refresh=h.state.loadNamespaces(true)
 assert.equal(requests[1].signal.aborted,true)
 assert.equal(h.state.namespaceLoading.value,true)
 requests[1].resolve({data:{namespaces:['迟到数据']}})
 await settle()
 assert.deepEqual(h.state.namespaces.value,['team','prod'])
 assert.equal(h.state.namespaceError.value,'读取失败')

 requests[2].resolve({data:{namespaces:['team','next']}})
 await refresh
 assert.equal(h.state.namespaceLoading.value,false)
 assert.equal(h.state.namespaceError.value,'')
 assert.deepEqual(h.state.namespaces.value,['team','next'])
 assert.deepEqual(h.state.selectedNamespaces.value,['team'])
})
