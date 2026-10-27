import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse} from '@vue/compiler-sfc'
import * as vue from 'vue'
import {loadFrontend} from './acceptanceHarness.mjs'

const api=loadFrontend('../src/api/kubernetes.ts')
const helpers=loadFrontend('../src/utils/kubernetesWorkloadExplorer.ts')
const diagnostics=loadFrontend('../src/utils/kubernetesDiagnostics.ts',{'@/api/kubernetes':api})
const workspace=loadFrontend('../src/utils/kubernetesWorkspace.ts')
const presentation=loadFrontend('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':api,'@/utils/kubernetesRoles':loadFrontend('../src/utils/kubernetesRoles.ts')})
const time=loadFrontend('../src/utils/time.ts')
const flush=async()=>{await Promise.resolve();await vue.nextTick();await Promise.resolve();await vue.nextTick()}

function pod(uid='pod-a'){
 return {apiVersion:'v1',kind:'Pod',metadata:{name:'web',uid,namespace:'team',creationTimestamp:'2026-10-27T01:00:00Z'},spec:{nodeName:'worker-a',restartPolicy:'Always',containers:[{name:'app',image:'web:1'},{name:'metrics',image:'metrics:1'}],initContainers:[{name:'prepare',image:'prepare:1'}],ephemeralContainers:[{name:'debug',image:'debug:1'}]},status:{phase:'Running',podIP:'10.42.0.10',conditions:[{type:'Ready',status:'True'}],containerStatuses:[{name:'app',ready:true,started:true,restartCount:0,state:{running:{startedAt:'2026-10-27T01:01:00Z'}}},{name:'metrics',ready:true,state:{running:{}}}],initContainerStatuses:[{name:'prepare',state:{terminated:{exitCode:0,reason:'Completed'}}}],ephemeralContainerStatuses:[{name:'debug',state:{waiting:{reason:'ContainerCreating'}}}]}}
}
function event(uid,reason='Started',extra={}){
 return {metadata:{uid:`event-${uid}-${reason}`},involvedObject:{uid},reason,count:1,message:`${uid} ${reason}`,...extra}
}
const containerTarget=(container,podUID='pod-a',cluster='cluster-a',namespace='team')=>({cluster,namespace,podUID,container})

function runtimeHarness(overrides={}){
 const requests=[],emitted=[],mounts=[],unmounts=[],intervals=[],cleared=[],listeners=new Map(),removed=[]
 const permissions=vue.reactive(new Set(['kubernetes.workload.read','terminal.open']))
 const props=vue.reactive({cluster:'cluster-a',pod:pod(),container:'app',autoRefresh:false,refreshToken:0,disabled:false,can:permission=>permissions.has(permission),canFollow:()=>true,...overrides})
 const document={hidden:false,addEventListener:(name,listener)=>listeners.set(name,listener),removeEventListener:(name,listener)=>{removed.push({name,listener});if(listeners.get(name)===listener)listeners.delete(name)}}
 const component=loadFrontend('../src/components/KubeWorkloadPodRuntime.vue',{
  vue:{...vue,onMounted:callback=>mounts.push(callback),onBeforeUnmount:callback=>unmounts.push(callback)},
  'vue-i18n':{useI18n:()=>({t:key=>key})},
  '@/api/kubernetes':{...api,kubeErrorMessage:error=>error.message},
  '@/api/client':{__esModule:true,default:{get:(url,options)=>new Promise((resolve,reject)=>requests.push({url,options,resolve,reject}))}},
  '@/utils/kubernetesPresentation':presentation,
  '@/utils/kubernetesDiagnostics':diagnostics,
  '@/utils/kubernetesWorkspace':workspace,
  '@/utils/kubernetesWorkloadExplorer':helpers,
  '@/utils/time':time,
 },{document,setInterval:callback=>{intervals.push(callback);return intervals.length},clearInterval:id=>cleared.push(id)}).default
 const scope=vue.effectScope();let state,stopped=false
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(name,...args)=>emitted.push({name,args})})})
 return {state,props,permissions,requests,emitted,document,listeners,removed,intervals,cleared,mount(){mounts.forEach(callback=>callback())},async respond(index,items){requests[index].resolve({data:{items}});await flush()},async reject(index,error=new Error('事件暂不可用')){requests[index].reject(error);await flush()},actions(){return emitted.filter(item=>['logs','terminal'].includes(item.name))},stop(){if(stopped)return;stopped=true;unmounts.forEach(callback=>callback());scope.stop()}}
}

test('Pod事件按当前UID隔离，保留两种事件引用，不能混入同名重建或无UID事件',async()=>{
 const h=runtimeHarness()
 assert.match(h.requests[0].url,/\/clusters\/cluster-a\/resources\/pods\/web\/events$/)
 assert.deepEqual(h.requests[0].options.params,{namespace:'team',uid:'pod-a'})
 await h.respond(0,[event('pod-a'),event('pod-old','Failed',{involvedObject:{uid:'pod-old',name:'web'}}),event('pod-a','Scheduled',{involvedObject:undefined,regarding:{uid:'pod-a'}}),event('','Unknown'),{metadata:{uid:'no-target'},reason:'NoTarget'}])
 assert.deepEqual(h.state.events.value.map(item=>item.reason),['Started','Scheduled'])
 assert.equal(h.state.eventsLoading.value,false);assert.equal(h.state.eventsError.value,'')
 h.stop()
})

test('切换同名Pod会中止旧事件请求，迟到响应不覆盖新UID的事件或加载状态',async()=>{
 const h=runtimeHarness();h.props.pod=pod('pod-b');await flush()
 assert.equal(h.requests[0].options.signal.aborted,true)
 assert.equal(h.requests[1].options.params.uid,'pod-b');assert.deepEqual(h.state.events.value,[])
 await h.respond(0,[event('pod-a','Old')])
 assert.equal(h.state.eventsLoading.value,true);assert.deepEqual(h.state.events.value,[])
 await h.respond(1,[event('pod-b','New'),event('pod-a','Foreign')])
 assert.deepEqual(h.state.events.value.map(item=>item.reason),['New']);assert.equal(h.state.eventsLoading.value,false)
 h.stop()
})

test('相同PodUID切换集群或命名空间后取消旧请求，旧失败不会污染新上下文',async()=>{
 const h=runtimeHarness();h.props.cluster='cluster-b';await flush()
 assert.equal(h.requests[0].options.signal.aborted,true);assert.match(h.requests[1].url,/\/clusters\/cluster-b\//)
 await h.respond(1,[event('pod-a','ClusterB')]);await h.reject(0,new Error('旧集群读取失败'))
 assert.deepEqual(h.state.events.value.map(item=>item.reason),['ClusterB']);assert.equal(h.state.eventsError.value,'')
 h.props.pod={...pod(),metadata:{...pod().metadata,namespace:'other'}};await flush()
 assert.equal(h.requests[1].options.signal.aborted,true);assert.equal(h.requests[2].options.params.namespace,'other');assert.deepEqual(h.state.events.value,[])
 await h.respond(2,[event('pod-a','OtherNamespace')])
 assert.deepEqual(h.state.events.value.map(item=>item.reason),['OtherNamespace']);h.stop()
})

test('撤销读取权限清理数据并取消请求，恢复权限后的迟到结果仍不能回填',async()=>{
 const h=runtimeHarness();await h.respond(0,[event('pod-a')])
 const pending=h.state.loadEvents();assert.equal(h.requests.length,2)
 h.permissions.delete('kubernetes.workload.read');await flush()
 assert.equal(h.requests[1].options.signal.aborted,true);assert.deepEqual(h.state.events.value,[]);assert.deepEqual(h.state.containers.value,[]);assert.equal(h.state.eventsLoading.value,false)
 h.state.openLogs('app');h.state.openLogs('app',true);assert.deepEqual(h.actions(),[])
 h.permissions.add('kubernetes.workload.read');await flush();assert.equal(h.requests.length,3)
 await h.respond(2,[event('pod-a','AfterRestore')]);await h.respond(1,[event('pod-a','BeforeRevoke')]);await pending
 assert.deepEqual(h.state.events.value.map(item=>item.reason),['AfterRestore']);assert.equal(h.state.eventsError.value,'');h.stop()
})

test('刷新令牌替换旧请求，读取失败明确报错并清除过期事件',async()=>{
 const h=runtimeHarness();await h.respond(0,[event('pod-a')])
 h.props.refreshToken++;await flush();h.props.refreshToken++;await flush()
 assert.equal(h.requests[1].options.signal.aborted,true)
 await h.respond(1,[event('pod-a','LateRefresh')]);assert.equal(h.state.eventsLoading.value,true)
 await h.reject(2)
 assert.deepEqual(h.state.events.value,[]);assert.equal(h.state.eventsError.value,'事件暂不可用');assert.equal(h.state.eventsLoading.value,false);h.stop()
})

test('卸载取消事件请求、定时器和可见性监听，迟到成功或失败不更新已卸载状态',async()=>{
 for(const rejected of [false,true]){
  const h=runtimeHarness();h.mount()
  assert.equal(h.intervals.length,1);assert.equal(h.listeners.has('visibilitychange'),true)
  h.stop();assert.equal(h.requests[0].options.signal.aborted,true);assert.deepEqual(h.cleared,[1]);assert.equal(h.listeners.has('visibilitychange'),false);assert.equal(h.removed.length,1)
  if(rejected)await h.reject(0,new Error('卸载后失败'));else await h.respond(0,[event('pod-a','AfterUnmount')])
  assert.deepEqual(h.state.events.value,[]);assert.equal(h.state.eventsError.value,'')
 }
})

test('后台事件更新遵守自动刷新、页面可见性、禁用及读取权限',async()=>{
 const h=runtimeHarness();h.mount();await h.respond(0,[])
 h.intervals[0]();assert.equal(h.requests.length,1)
 h.props.autoRefresh=true;h.document.hidden=true;h.intervals[0]();assert.equal(h.requests.length,1)
 h.document.hidden=false;h.props.disabled=true;h.intervals[0]();assert.equal(h.requests.length,1)
 h.props.disabled=false;h.listeners.get('visibilitychange')();assert.equal(h.requests.length,2)
 h.intervals[0]();assert.equal(h.requests.length,2)
 await h.respond(1,[]);h.permissions.delete('kubernetes.workload.read');await flush();h.intervals[0]();assert.equal(h.requests.length,2);h.stop()
})

test('日志和终端每次操作重核权限、禁用状态、容器存在及运行状态，Init退出仍能看日志',()=>{
 const h=runtimeHarness()
 h.state.openLogs('prepare');h.state.openLogs('prepare',true)
 assert.deepEqual(h.actions(),[{name:'logs',args:[containerTarget('prepare')]}])
 h.state.openLogs('debug',true);h.state.openLogs('not-found');assert.equal(h.actions().length,1)
 h.state.openLogs('app',true);assert.deepEqual(h.emitted.slice(-2),[{name:'container',args:[containerTarget('app')]},{name:'terminal',args:[containerTarget('app')]}])
 h.permissions.delete('terminal.open');h.state.openLogs('app',true);assert.equal(h.actions().length,2)
 h.state.openLogs('app');assert.deepEqual(h.actions().at(-1),{name:'logs',args:[containerTarget('app')]})
 h.props.disabled=true;h.state.openLogs('app');assert.equal(h.actions().length,3);h.props.disabled=false
 h.permissions.delete('kubernetes.workload.read');h.state.openLogs('app');h.state.openLogs('app',true);assert.equal(h.actions().length,3)
 h.permissions.add('kubernetes.workload.read');h.permissions.add('terminal.open')
 h.props.pod.metadata.deletionTimestamp='2026-10-27T02:00:00Z';h.state.openLogs('app',true);assert.equal(h.actions().length,3)
 h.state.openLogs('app');assert.equal(h.actions().length,4)
 delete h.props.pod.metadata.deletionTimestamp;h.props.pod.status.containerStatuses[0].state={terminated:{reason:'Error',exitCode:1}}
 h.state.openLogs('app',true);assert.equal(h.actions().length,4)
 h.state.openLogs('app');assert.equal(h.actions().length,5);h.stop()
})

test('运行中Init和原生sidecar禁用终端但可查看日志，普通与临时容器复用父层资格判断',()=>{
 const h=runtimeHarness()
 h.props.pod.status.initContainerStatuses[0].state={running:{startedAt:'2026-10-27T02:00:00Z'}}
 h.props.pod.spec.initContainers.push({name:'native-sidecar',image:'sidecar:1',restartPolicy:'Always'})
 h.props.pod.status.initContainerStatuses.push({name:'native-sidecar',ready:true,state:{running:{}}})
 h.props.pod.status.ephemeralContainerStatuses[0].state={running:{}}
 for(const name of ['prepare','native-sidecar']){
  assert.equal(h.state.allowedTerminal(name),false,name);h.state.openLogs(name,true)
 }
 assert.deepEqual(h.actions(),[])
 h.state.openLogs('prepare');h.state.openLogs('native-sidecar')
 assert.deepEqual(h.actions().map(item=>item.args[0].container),['prepare','native-sidecar'])
 for(const name of ['app','debug']){
  assert.equal(h.state.allowedTerminal(name),true,name);h.state.openLogs(name,true)
 }
 assert.deepEqual(h.actions().filter(item=>item.name==='terminal').map(item=>item.args[0].container),['app','debug'])
 for(const choice of workspace.podContainerChoices(h.props.pod))assert.equal(h.state.allowedTerminal(choice.name),choice.running,choice.name)
 h.props.pod.status.phase='Pending'
 for(const name of ['app','debug']){assert.equal(h.state.allowedTerminal(name),false);h.state.openLogs(name,true)}
 assert.equal(h.actions().length,4)
 const template=parse(readFileSync(new URL('../src/components/KubeWorkloadPodRuntime.vue',import.meta.url),'utf8')).descriptor.template.content
 assert.match(template,/:disabled="!allowedTerminal\(container\)" @click="openLogs\(container,true\)"/)
 assert.match(template,/:value="container"[^>]*@update:value="chooseContainer\(String\(\$event\)\)"/)
 h.stop()
})

test('所选容器消失后保留失效提示，不自动切换日志或终端到其他有效容器',async()=>{
 const h=runtimeHarness({container:'metrics'})
 h.props.pod.spec.containers=h.props.pod.spec.containers.filter(item=>item.name!=='metrics');await flush()
 assert.equal(h.props.container,'metrics');assert.equal(h.state.missingContainer.value,true)
 assert.equal(h.state.allowedContainer('app'),false);h.state.openLogs('app');h.state.openLogs('app',true);assert.deepEqual(h.emitted,[])
 h.props.container='app';await flush();assert.equal(h.state.missingContainer.value,false)
 h.state.openLogs('app');assert.deepEqual(h.actions(),[{name:'logs',args:[containerTarget('app')]}]);h.stop()
})

test('容器选择与日志事件携带不可变目标快照，后续切换Pod或集群不改写旧事件身份',async()=>{
 const h=runtimeHarness();h.state.chooseContainer('metrics')
 const previous=h.emitted[0].args[0]
 assert.deepEqual(previous,containerTarget('metrics'))
 h.props.cluster='cluster-b';h.props.pod={...pod('pod-b'),metadata:{...pod('pod-b').metadata,namespace:'other'}};await flush()
 h.state.openLogs('app')
 assert.deepEqual(h.emitted.slice(-2),[{name:'container',args:[containerTarget('app','pod-b','cluster-b','other')]},{name:'logs',args:[containerTarget('app','pod-b','cluster-b','other')]}])
 assert.deepEqual(previous,containerTarget('metrics'))
 h.props.disabled=true;h.state.chooseContainer('metrics');assert.equal(h.emitted.length,3)
 h.props.disabled=false;h.permissions.delete('kubernetes.workload.read');h.state.chooseContainer('app');assert.equal(h.emitted.length,3);h.stop()
})

test('关联导航每次检查读取权限、目标权限与禁用状态',()=>{
 const h=runtimeHarness(),reference={resource:'nodes',name:'worker-a'}
 h.state.follow(reference);assert.deepEqual(h.emitted,[{name:'follow',args:[reference,containerTarget('app')]}])
 h.props.canFollow=()=>false;h.state.follow(reference);assert.equal(h.emitted.length,1)
 h.props.canFollow=()=>true;h.props.disabled=true;h.state.follow(reference);assert.equal(h.emitted.length,1)
 h.props.disabled=false;h.permissions.delete('kubernetes.workload.read');h.state.follow(reference);assert.equal(h.emitted.length,1);h.stop()
})

function eventsHarness(events){
 const props=vue.reactive({events,error:false,loading:false,compact:false,emptyText:'暂无事件',title:'事件'})
 const component=loadFrontend('../src/components/KubeWorkloadEvents.vue',{vue,'@/utils/time':time}).default
 const scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup(props,{expose(){}})})
 return {state,props,stop:()=>scope.stop()}
}

test('事件计数保留显式零与series计数，未知和无效值不伪装为零',()=>{
 const h=eventsHarness([{count:0},{series:{count:4},count:1},{},{count:null},{count:-1},{count:1.5},{count:'2'},{count:NaN},{count:Infinity}])
 assert.deepEqual(h.state.rows.value.map(row=>row.count),['0','4','—','—','—','—','—','—','—'])
 h.props.events=[];assert.deepEqual(h.state.rows.value,[]);h.stop()
})

test('事件支持两种消息和真实时间来源，缺失字段与未知状态保持空值',()=>{
 const h=eventsHarness([event('pod-a','Failed',{type:'Warning',series:{lastObservedTime:'2026-10-27T03:00:00Z'},lastTimestamp:'2026-10-26T03:00:00Z'}),{metadata:{uid:'note'},note:'新版事件消息',eventTime:'2026-10-27T04:00:00Z'},{reason:'',count:0,lastTimestamp:'not-a-time'}])
 assert.equal(h.state.rows.value[0].warning,true);assert.equal(h.state.rows.value[0].time,time.formatDateTime('2026-10-27T03:00:00Z'))
 assert.equal(h.state.rows.value[1].message,'新版事件消息');assert.equal(h.state.rows.value[1].time,time.formatDateTime('2026-10-27T04:00:00Z'))
 assert.equal(h.state.rows.value[2].reason,'—');assert.equal(h.state.rows.value[2].message,'—');assert.equal(h.state.rows.value[2].time,'—');assert.equal(h.state.rows.value[2].warning,false);h.stop()
})

test('紧凑事件使用相对时间但保留完整时间和消息，不裁剪底层数据或伪造缺失时间',()=>{
 const now=Date.now(),at=new Date(now-150_000).toISOString(),message='完整事件消息'.repeat(50)
 const h=eventsHarness([{eventTime:at,message},{}]);h.props.compact=true
 assert.equal(h.state.rows.value[0].relativeTime,time.formatRelativeTime(at,now));assert.equal(h.state.rows.value[0].time,time.formatDateTime(at));assert.equal(h.state.rows.value[0].message,message)
 assert.equal(h.state.rows.value[1].time,'—');assert.equal(h.state.rows.value[1].relativeTime,'—')
 const source=readFileSync(new URL('../src/components/KubeWorkloadEvents.vue',import.meta.url),'utf8'),template=parse(source).descriptor.template.content
 assert.match(template,/:title="event\.time"/);assert.match(template,/compact\s*\?\s*event\.relativeTime\s*:\s*event\.time/);assert.match(template,/:title="event\.message"/)
 h.stop()
})

test('事件与运行信息只使用Vue安全文本声明，不把消息、原因或容器数据交给HTML解释器',()=>{
 const literal='<img src=x onerror=alert(1)><script>alert(1)</script>',h=eventsHarness([{reason:literal,message:literal}])
 assert.equal(h.state.rows.value[0].reason,literal);assert.equal(h.state.rows.value[0].message,literal);h.stop()
 const eventSource=readFileSync(new URL('../src/components/KubeWorkloadEvents.vue',import.meta.url),'utf8'),runtimeSource=readFileSync(new URL('../src/components/KubeWorkloadPodRuntime.vue',import.meta.url),'utf8')
 const eventTemplate=parse(eventSource).descriptor.template.content,runtimeTemplate=parse(runtimeSource).descriptor.template.content
 assert.match(eventTemplate,/\{\{\s*event\.reason\s*\}\}/);assert.match(eventTemplate,/\{\{\s*event\.message\s*\}\}/)
 assert.match(runtimeTemplate,/\{\{\s*containerState\(item\)\.message\s*\}\}/)
 assert.doesNotMatch(eventTemplate+runtimeTemplate,/v-html|innerHTML|outerHTML/)
 assert.match(eventTemplate,/事件条数暂不可用/);assert.match(eventTemplate,/上次读取的事件/)
 assert.doesNotMatch(eventSource,/client\.(get|post|put|patch|delete)\(|\bfetch\(/)
})
