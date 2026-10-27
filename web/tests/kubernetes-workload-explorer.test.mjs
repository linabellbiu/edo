import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {createRouter,createMemoryHistory} from 'vue-router'
import {loadFrontend} from './acceptanceHarness.mjs'

const api=loadFrontend('../src/api/kubernetes.ts')
const helpers=loadFrontend('../src/utils/kubernetesWorkloadExplorer.ts')
const workspace=loadFrontend('../src/utils/kubernetesWorkspace.ts')
const presentation=loadFrontend('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':api,'@/utils/kubernetesRoles':loadFrontend('../src/utils/kubernetesRoles.ts')})
const deployment={kind:'Deployment',metadata:{name:'web',namespace:'team',uid:'deployment-a'}}
const revision=(uid,current=false)=>({uid,name:uid,revision:uid==='rs-current'?'2':'1',current,created:'2026-10-27T00:00:00Z'})
function pod(uid,owner='rs-current',namespace='team'){
 return {kind:'Pod',metadata:{name:uid,namespace,uid,ownerReferences:[{kind:'ReplicaSet',uid:owner,controller:true}]},spec:{containers:[{name:'app',image:'web:2'},{name:'metrics',image:'metrics:1'}],initContainers:[{name:'prepare',image:'init:1'}]},status:{phase:'Running',conditions:[{type:'Ready',status:'True'}],containerStatuses:[{name:'app',state:{running:{}}},{name:'metrics',state:{running:{}}}],initContainerStatuses:[{name:'prepare',state:{terminated:{exitCode:0}}}]}}
}
const flush=async()=>{await Promise.resolve();await vue.nextTick();await Promise.resolve();await vue.nextTick()}
function harness(overrides={}){
 const requests=[],events=[],selections=[],permissions=vue.reactive(new Set(['kubernetes.workload.read','terminal.open']))
 const props=vue.reactive({cluster:'cluster-a',object:structuredClone(deployment),type:api.kubeTypes.find(type=>type.resource==='deployments'),pods:[pod('pod-a'),pod('pod-b'),pod('pod-old','rs-old')],podsLoading:false,podsError:'',podsPartial:false,events:[],eventsError:false,autoRefresh:false,refreshToken:0,can:permission=>permissions.has(permission),canFollow:()=>true,disabled:false,...overrides})
 let unmount
 const component=loadFrontend('../src/components/KubeWorkloadExplorer.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(fn){unmount=fn}},
  'vue-i18n':{useI18n:()=>({t:key=>key})},
  '@/api/kubernetes':{...api,kubeErrorMessage:error=>error.message},
  '@/api/client':{__esModule:true,default:{get:(url,options)=>new Promise((resolve,reject)=>requests.push({url,options,resolve,reject}))}},
  '@/utils/kubernetesPresentation':presentation,
  '@/utils/kubernetesWorkspace':workspace,
  '@/utils/kubernetesWorkloadExplorer':helpers,
 },{document:{removeEventListener(){}}}).default
 const scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(name,...args)=>name==='selection'?selections.push(args[0]):events.push({name,args})})})
 return {state,props,requests,events,selections,permissions,async history(index=0,revisions=[revision('rs-old'),revision('rs-current',true)],uid=props.object.metadata.uid){requests[index].resolve({data:{uid,state:'ok',revisions}});await flush()},stop(){unmount?.();scope.stop()}}
}

test('副本组使用控制器 UID 与命名空间关联；同标签、同名重建和非控制器引用不能混入',()=>{
 const mine=pod('mine'),wrongNamespace=pod('foreign','rs-current','other'),sameLabel=pod('same-label','different-rs'),notController=pod('observer')
 notController.metadata.ownerReferences[0].controller=false
 assert.deepEqual(helpers.revisionOwnedPods([mine,wrongNamespace,sameLabel,notController],'rs-current','team').map(item=>item.metadata.uid),['mine'])
 assert.equal(helpers.selectedWorkloadPod([mine],'old-uid','team'),undefined)
 assert.equal(helpers.selectedWorkloadPod([mine],'mine','other'),undefined)
 assert.deepEqual(helpers.revisionOwnedPods([mine],'','team'),[])
})

test('版本默认只使用真实 current 标记，版本排序不修改输入，事件按对象 UID 隔离',()=>{
 const revisions=[revision('rs-current',true),{...revision('rs-old'),revision:'99',created:'2026-10-26T00:00:00Z'}],snapshot=structuredClone(revisions)
 assert.equal(helpers.initialWorkloadRevisionUID(revisions),'rs-current')
 assert.equal(helpers.initialWorkloadRevisionUID([revision('rs-old')]),'')
 assert.deepEqual(helpers.sortWorkloadRevisions(revisions).map(item=>item.uid),['rs-current','rs-old'])
 assert.deepEqual(revisions,snapshot)
 assert.deepEqual(helpers.podExplorerEvents([{involvedObject:{uid:'pod-a'},reason:'Ready'},{regarding:{uid:'pod-a'},reason:'Started'},{involvedObject:{uid:'old-pod'},reason:'Failed'}],'pod-a').map(item=>item.reason),['Ready','Started'])
 assert.deepEqual(helpers.podExplorerEvents([{involvedObject:{uid:''}}],''),[])
})

test('失败和中断条件成立时显示警告，未知条件保持中性，Ready False不显示健康',()=>{
 for(const type of ['Failed','ReplicaFailure','DisruptionTarget']){
  assert.equal(helpers.workloadConditionTone({type,status:'True'}),'warning',type)
  assert.equal(helpers.workloadConditionTone({type,status:'False'}),'default',type)
 }
 for(const type of ['Ready','Available','ContainersReady','Complete']){
  assert.equal(helpers.workloadConditionTone({type,status:'True'}),'success',type)
  assert.equal(helpers.workloadConditionTone({type,status:'False'}),'warning',type)
 }
 for(const condition of [{type:'FutureCondition',status:'True'},{type:'Progressing',status:'True'},{type:'Ready',status:'Unknown'},{type:'Ready'}])assert.equal(helpers.workloadConditionTone(condition),'default')
})

test('当前版本、首个 Pod 和普通容器自动联动，非 Deployment 首屏也完成容器选择',async()=>{
 const h=harness();await h.history()
 assert.equal(h.state.revisionUID.value,'rs-current');assert.equal(h.state.podUID.value,'pod-a');assert.equal(h.state.containerName.value,'app')
 assert.deepEqual(h.state.visiblePods.value.map(item=>item.metadata.uid),['pod-a','pod-b'])
 h.stop()
 const standalone=harness({object:{...deployment,kind:'StatefulSet'},type:api.kubeTypes.find(type=>type.resource==='statefulsets')})
 assert.equal(standalone.requests.length,0);assert.equal(standalone.state.podUID.value,'pod-a');assert.equal(standalone.state.containerName.value,'app')
 standalone.stop()
})

test('初始加载及禁用期间不选中资源，解除后自动选择首个 Pod 和容器',async()=>{
 const h=harness({pods:[],podsLoading:true,disabled:true});await h.history()
 assert.equal(h.state.podUID.value,'')
 h.props.pods=[pod('pod-a'),pod('pod-b')];await flush();assert.equal(h.state.podUID.value,'')
 h.props.podsLoading=false;h.props.disabled=false;await flush()
 assert.equal(h.state.podUID.value,'pod-a');assert.equal(h.state.containerName.value,'app')
 h.stop()
})

test('显式切换副本组重置 Pod 与容器；刷新保留用户所选版本、Pod 和诊断页',async()=>{
 const h=harness();await h.history();h.state.choosePod(h.props.pods[1]);await flush();h.state.containerName.value='metrics';h.state.podTab.value='diagnostics'
 const pending=h.state.loadHistory();await h.history(1);await pending
 assert.equal(h.state.podUID.value,'pod-b');assert.equal(h.state.containerName.value,'metrics');assert.equal(h.state.podTab.value,'diagnostics')
 h.state.chooseRevision(h.state.revisions.value.find(item=>item.uid==='rs-old'));await flush()
 assert.equal(h.state.podUID.value,'pod-old');assert.equal(h.state.containerName.value,'app');assert.equal(h.state.podTab.value,'summary')
 h.stop()
})

test('所选 Pod 被删除或同名重建后保留失效提示，不把日志目标自动替换为其他 Pod',async()=>{
 const h=harness();await h.history();h.state.choosePod(h.props.pods[1]);await flush()
 h.props.pods=[pod('pod-a'),{...pod('replacement'),metadata:{...pod('replacement').metadata,name:'pod-b'}}];await flush()
 assert.equal(h.state.podUID.value,'pod-b');assert.equal(h.state.selectedPod.value,undefined);assert.equal(h.state.podMissing.value,true)
 h.state.openContainer();h.state.openContainer(true);assert.equal(h.events.length,0)
 h.state.choosePod(h.props.pods[0]);await flush();assert.equal(h.state.podUID.value,'pod-a');assert.equal(h.state.podMissing.value,false)
 h.stop()
})

test('关联返回恢复所选版本、Pod、容器和诊断页；失效快照不回退到其他目标',async()=>{
 const selection={revisionUID:'rs-current',podUID:'pod-b',containerName:'metrics',podTab:'diagnostics'}
 const restored=harness({selection});await restored.history()
 assert.equal(restored.state.selectedPod.value.metadata.uid,'pod-b');assert.equal(restored.state.selectedContainer.value.name,'metrics');assert.equal(restored.state.podTab.value,'diagnostics')
 restored.state.containerName.value='app';await flush();assert.deepEqual(restored.selections.at(-1),{...selection,containerName:'app'});restored.stop()
 const missing=harness({selection:{...selection,podUID:'deleted-pod'}});await missing.history()
 assert.equal(missing.state.podUID.value,'deleted-pod');assert.equal(missing.state.podMissing.value,true);assert.equal(missing.state.selectedPod.value,undefined);missing.stop()
 const removedRevision=harness({selection:{...selection,revisionUID:'removed-rs'}});await removedRevision.history()
 assert.equal(removedRevision.state.revisionUID.value,'removed-rs');assert.equal(removedRevision.state.selectedRevision.value,undefined);assert.deepEqual(removedRevision.state.visiblePods.value,[]);removedRevision.stop()
})

test('日志允许已结束 Init；终端重新核对权限、运行状态与正在删除标记',async()=>{
 const h=harness();await h.history();h.state.containerName.value='prepare';h.state.openContainer();h.state.openContainer(true)
 assert.equal(h.events.length,1);assert.equal(h.events[0].name,'logs');assert.equal(h.events[0].args[1],'prepare')
 h.state.containerName.value='app';h.permissions.delete('terminal.open');h.state.openContainer(true);assert.equal(h.events.length,1)
 h.state.openContainer();assert.equal(h.events[1].name,'logs')
 h.permissions.add('terminal.open');h.props.pods[0].metadata.deletionTimestamp='2026-10-27T01:00:00Z';h.state.openContainer(true);assert.equal(h.events.length,2)
 delete h.props.pods[0].metadata.deletionTimestamp;h.state.openContainer(true);assert.equal(h.events[2].name,'terminal');assert.equal(h.events[2].args[0].metadata.uid,'pod-a')
 h.props.podsLoading=true;h.state.openContainer();assert.equal(h.events.length,3)
 h.stop()
})

test('运行面板迟到的旧Pod目标不能选择或打开新Pod同名容器',async()=>{
 const h=harness();await h.history()
 const previous={cluster:'cluster-a',namespace:'team',podUID:'pod-a',container:'metrics'}
 h.state.choosePod(h.props.pods[1]);await flush()
 assert.equal(h.state.podUID.value,'pod-b');assert.equal(h.state.containerName.value,'app')
 h.state.chooseRuntimeContainer(previous);h.state.openRuntimeContainer(previous);h.state.openRuntimeContainer(previous,true)
 assert.equal(h.state.containerName.value,'app');assert.deepEqual(h.events,[])
 const current={...previous,podUID:'pod-b'}
 h.state.chooseRuntimeContainer(current);assert.equal(h.state.containerName.value,'metrics')
 h.state.openRuntimeContainer(current);assert.equal(h.events[0].name,'logs');assert.equal(h.events[0].args[0].metadata.uid,'pod-b');assert.equal(h.events[0].args[1],'metrics')
 h.state.openRuntimeContainer(current,true);assert.equal(h.events[1].name,'terminal');assert.equal(h.events[1].args[0].metadata.uid,'pod-b');h.stop()
})

test('切换集群或命名空间后即使PodUID和容器名相同也拒绝旧运行目标',async()=>{
 const h=harness();await h.history()
 const clusterA={cluster:'cluster-a',namespace:'team',podUID:'pod-a',container:'metrics'}
 h.props.cluster='cluster-b';await flush();await h.history(1)
 assert.equal(h.state.selectedPod.value.metadata.uid,'pod-a');assert.equal(h.state.containerName.value,'app')
 h.state.chooseRuntimeContainer(clusterA);h.state.openRuntimeContainer(clusterA);h.state.openRuntimeContainer(clusterA,true)
 assert.equal(h.state.containerName.value,'app');assert.deepEqual(h.events,[])
 const namespaceTeam={...clusterA,cluster:'cluster-b'}
 h.props.object={...deployment,metadata:{...deployment.metadata,namespace:'other'}}
 h.props.pods=[pod('pod-a','rs-current','other')];await flush();await h.history(2)
 assert.equal(h.state.selectedPod.value.metadata.namespace,'other');assert.equal(h.state.containerName.value,'app')
 h.state.chooseRuntimeContainer(namespaceTeam);h.state.openRuntimeContainer(namespaceTeam);h.state.openRuntimeContainer(namespaceTeam,true)
 assert.equal(h.state.containerName.value,'app');assert.deepEqual(h.events,[])
 const current={...namespaceTeam,namespace:'other'}
 h.state.openRuntimeContainer(current);assert.equal(h.events[0].args[0].metadata.namespace,'other');assert.equal(h.events[0].args[1],'metrics');h.stop()
})

test('身份匹配的运行目标仍须逐次检查容器存在、权限、禁用及终端运行状态',async()=>{
 const h=harness();await h.history()
 const target={cluster:'cluster-a',namespace:'team',podUID:'pod-a',container:'metrics'}
 h.props.disabled=true;h.state.chooseRuntimeContainer(target);h.state.openRuntimeContainer(target)
 assert.equal(h.state.containerName.value,'app');assert.deepEqual(h.events,[])
 h.props.disabled=false;h.permissions.delete('terminal.open');h.state.openRuntimeContainer(target,true);assert.deepEqual(h.events,[])
 h.permissions.add('terminal.open');h.props.pods[0].status.containerStatuses[1].state={terminated:{exitCode:0}}
 h.state.openRuntimeContainer(target,true);assert.deepEqual(h.events,[])
 h.state.openRuntimeContainer(target);assert.equal(h.events[0].name,'logs')
 h.props.pods[0].spec.containers=h.props.pods[0].spec.containers.filter(item=>item.name!=='metrics');await flush()
 h.state.openRuntimeContainer(target);assert.equal(h.events.length,1)
 h.permissions.delete('kubernetes.workload.read');h.state.openRuntimeContainer({...target,container:'app'});assert.equal(h.events.length,1);h.stop()
})

test('切换工作负载会取消旧请求，迟到响应与 UID 错配不能污染新详情',async()=>{
 const h=harness();h.props.object={...deployment,metadata:{...deployment.metadata,uid:'deployment-b',name:'other'}};await flush()
 assert.equal(h.requests[0].options.signal.aborted,true)
 await h.history(1,[revision('rs-current',true)],'deployment-b')
 await h.history(0,[revision('foreign-rs',true)],'deployment-a')
 assert.deepEqual(h.state.revisions.value.map(item=>item.uid),['rs-current'])
 const pending=h.state.loadHistory();await h.history(2,[revision('foreign-rs',true)],'wrong-uid');await pending
 assert.match(h.state.historyError.value,/工作负载已发生变化/);assert.equal(h.state.actionDisabled.value,true)
 h.stop()
})

test('撤销读取权限立即清理选择和未完成请求，恢复后重新加载并选择当前版本',async()=>{
 const h=harness();h.permissions.delete('kubernetes.workload.read');await flush()
 assert.equal(h.requests[0].options.signal.aborted,true);assert.equal(h.state.revisionUID.value,'');assert.equal(h.state.selectedPod.value,undefined)
 await h.history(0);assert.deepEqual(h.state.revisions.value,[]);h.state.openContainer();assert.equal(h.events.length,0)
 h.permissions.add('kubernetes.workload.read');await flush();await h.history(1)
 assert.equal(h.state.revisionUID.value,'rs-current');assert.equal(h.state.selectedPod.value.metadata.uid,'pod-a')
 h.stop()
})

const workloadForm=loadFrontend('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api})
function editorHarness(){
 const dialogs=[],events=[]
 const object={...structuredClone(deployment),apiVersion:'apps/v1',metadata:{...deployment.metadata,resourceVersion:'7'},spec:{replicas:2,selector:{matchLabels:{app:'web'}},template:{spec:pod('template').spec}}}
 const props=vue.reactive({open:true,cluster:'cluster-a',type:api.kubeTypes.find(type=>type.resource==='deployments'),namespace:'team',object})
 const component=loadFrontend('../src/components/KubeResourceEditor.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){}},
  'vue-i18n':{useI18n:()=>({t:key=>key})},
  'ant-design-vue':{Modal:{confirm:options=>{dialogs.push(options);return {destroy(){},update(){}}}},message:{success(){}}},
  '@/api/kubernetes':api,'@/utils/kubernetesWorkloadForm':workloadForm,
 }).default
 const scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(name,...args)=>events.push({name,args})})})
 return {state,props,dialogs,events,stop:()=>scope.stop()}
}

test('编辑三级导航和容器选择不产生草稿变更，回到模板保留上次子页',()=>{
 const h=editorHarness(),before=JSON.parse(JSON.stringify(h.props.object))
 assert.equal(h.state.editorSection.value,'template');assert.equal(h.state.templateTab.value,'containers');assert.equal(h.state.dirty.value,false)
 h.state.selectContainer('initContainers',0);assert.equal(h.state.selectedContainer.value.name,'prepare')
 h.state.templateTab.value='scheduling';h.state.editorSection.value='basic';h.state.editorSection.value='template'
 assert.equal(h.state.templateTab.value,'scheduling');assert.equal(h.state.dirty.value,false);assert.deepEqual(h.props.object,before)
 h.stop()
})

test('新增容器自动定位，删除后选择有效容器且禁止删除最后一个普通容器',()=>{
 const h=editorHarness();h.state.addContainer('initContainers')
 assert.equal(h.state.activeContainerGroup.value,'initContainers');assert.equal(h.state.activeContainerIndex.value,1);assert.equal(h.state.selectedContainer.value.name,'');assert.equal(h.state.dirty.value,true)
 h.state.removeContainer();assert.equal(h.state.activeContainerIndex.value,0);assert.equal(h.state.selectedContainer.value.name,'prepare')
 h.state.selectContainer('containers',1);h.state.removeContainer();assert.equal(h.state.form.containers.length,1);assert.equal(h.state.selectedContainer.value.name,'app')
 h.state.removeContainer();assert.equal(h.state.form.containers.length,1)
 h.stop()
})

test('第二个容器无效时切回模板并定位对应容器，脏草稿取消退出后继续保留',async()=>{
 const h=editorHarness();h.state.form.containers[1].image='';h.state.editorSection.value='basic';await h.state.save()
 assert.equal(h.state.editorSection.value,'template');assert.equal(h.state.templateTab.value,'containers');assert.equal(h.state.activeContainerIndex.value,1);assert.equal(h.state.error.value,'k8s.imageRequired')
 const leaving=h.state.closeEditor();assert.equal(h.dialogs.length,1);h.dialogs[0].onCancel();h.dialogs[0].afterClose();await leaving
 assert.equal(h.state.dirty.value,true);assert.deepEqual(h.events,[]);assert.equal(h.state.form.containers[1].image,'')
 const discard=h.state.closeEditor();h.dialogs[1].onOk();h.dialogs[1].afterClose();await discard
 assert.deepEqual(h.events,[{name:'update:open',args:[false]}]);h.stop()
})

const settleView=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
async function viewHarness(){
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/kubernetes',component:{}}]})
 await router.push('/kubernetes?view=workloads&resource=deployments&cluster=test&namespace=team')
 const route=new Proxy({},{get:(_target,key)=>router.currentRoute.value[key]}),calls=[],lists=[],unmounts=[],messages=[],blocked=new Set()
 const workload={...structuredClone(deployment),apiVersion:'apps/v1',spec:{replicas:2,selector:{matchLabels:{app:'web'}},template:{spec:pod('template').spec}},metadata:{...deployment.metadata,resourceVersion:'7'}}
 const podA=pod('pod-a');podA.apiVersion='v1';podA.metadata.labels={app:'web'}
 const podB=pod('pod-b');podB.apiVersion='v1';podB.metadata.labels={app:'web'}
 const pvc={apiVersion:'v1',kind:'PersistentVolumeClaim',metadata:{name:'data',namespace:'team',uid:'pvc-a'}}
 const objects=new Map([['web',workload],['pod-a',podA],['pod-b',podB],['data',pvc]])
 let now=Date.now()
 class FixtureDate extends Date{static now(){return now}}
 const client={get:async(url,options={})=>{
  calls.push({url,options})
  if(url.endsWith('/namespaces'))return {data:{namespaces:['team']}}
  if(url.endsWith('/events'))return {data:{items:[{metadata:{uid:'event-a'},involvedObject:{uid:options.params.uid},reason:'Ready',lastTimestamp:'2026-10-27T00:00:00Z'}]}}
  if(/\/resources\/[^/]+\/[^/]+$/.test(url)){const object=objects.get(decodeURIComponent(url.split('/').at(-1)));if(!object)throw new Error('示例对象不存在');return {data:{object:JSON.parse(JSON.stringify(object))}}}
  return {data:{counts:{},nodes:[],usage:{},phases:{}}}
 }}
 const scope=vue.effectScope()
 const component=loadFrontend('../src/views/KubernetesView.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn)},'vue-i18n':{useI18n:()=>({t:key=>key})},
  'vue-router':{useRoute:()=>route,useRouter:()=>router,onBeforeRouteLeave(){},onBeforeRouteUpdate(){}},
  'ant-design-vue':{Modal:{confirm:()=>({destroy(){}})},message:Object.fromEntries(['error','warning','success'].map(kind=>[kind,value=>messages.push({kind,value})]))},
  '@/api/kubernetes':{...api,listKubeResourceScopes:async()=>({items:[],cursor:{}}),listKubeResources:async(cluster,type,namespace,continuation,signal)=>{lists.push({cluster,type:type.resource,namespace,continuation,signal});return {items:type.resource==='pods'?[podA,podB]:[],continue:''}}},
  '@/utils/kubernetesWorkspace':workspace,'@/utils/kubernetesPresentation':presentation,
  '@/stores/auth':{useAuthStore:()=>({canAny:permissions=>permissions.some(permission=>!blocked.has(permission))})},
  '@/api/client':{__esModule:true,default:client},
 },{document:{hidden:false,removeEventListener(){}},window:{removeEventListener(){}},localStorage:{getItem:()=> 'fixture-project'},Date:FixtureDate}).default
 let state;scope.run(()=>{state=component.setup({}, {expose(){}})})
 state.clusters.value=[{id:'test',name:'test',default_namespace:'team',is_active:true}];state.cluster.value='test';await settleView()
 return {state,workload,podA,podB,pvc,calls,lists,messages,blocked,advance:milliseconds=>{now+=milliseconds},stop(){unmounts.forEach(fn=>fn());state.cancelListPersist();scope.stop()}}
}

test('工作负载概览默认加载关联 Pod 和事件，后台同步保留概览并刷新两者',async()=>{
 const h=await viewHarness();await h.state.openDetails(h.workload);await settleView()
 assert.equal(h.state.detailTab.value,'basic');assert.equal(h.state.workloadExplorer.value,true);assert.equal(h.state.relatedLoaded.value,true)
 assert.deepEqual(h.state.relatedPods.value.map(item=>item.metadata.uid),['pod-a','pod-b'])
 const eventsBefore=h.calls.filter(call=>call.url.endsWith('/events')).length,podsBefore=h.lists.length
 h.advance(11000);await h.state.syncDetails();await settleView()
 assert.equal(h.calls.filter(call=>call.url.endsWith('/events')).length,eventsBefore+1);assert.equal(h.lists.length,podsBefore+1);assert.equal(h.state.detailTab.value,'basic')
 h.stop()
})

test('工作负载查看 Pod 与历史诊断快捷入口归到概览，普通 Pod 保留自身诊断页',async()=>{
 const h=await viewHarness()
 for(const tab of ['pods','diagnostics']){await h.state.openDetails(h.workload,tab);await settleView();assert.equal(h.state.detailTab.value,'basic');assert.equal(h.state.needsRelatedPods.value,true)}
 await h.state.openDetails(h.podA,'diagnostics',api.kubeTypes.find(type=>type.resource==='pods'));await settleView()
 assert.equal(h.state.detailTab.value,'diagnostics');assert.equal(h.state.workloadExplorer.value,false);h.stop()
})

test('工作负载日志桥接使用当前 Pod 精确容器，UID、命名空间或容器失效时拒绝打开',async()=>{
 const h=await viewHarness();await h.state.openDetails(h.workload);await settleView()
 h.state.selectedContainer.value['pod-a']='app';h.state.openWorkloadStream(h.podA,'metrics')
 assert.equal(h.state.stream.value.open,true);assert.equal(h.state.stream.value.title,'pod-a / metrics');assert.match(h.state.stream.value.path,/\/pods\/pod-a\/containers\/metrics\/logs\/ws\?project_id=fixture-project$/)
 for(const target of [{...h.podA,metadata:{...h.podA.metadata,uid:'replacement'}},{...h.podA,metadata:{...h.podA.metadata,namespace:'other'}}]){
  h.state.stream.value.open=false;h.state.openWorkloadStream(target,'app');assert.equal(h.state.stream.value.open,false)
 }
 h.state.openWorkloadStream(h.podA,'removed-container');assert.equal(h.state.stream.value.open,false)
 h.state.relatedPods.value[0].spec.containers=h.state.relatedPods.value[0].spec.containers.filter(item=>item.name!=='metrics')
 h.state.openWorkloadStream(h.podA,'metrics');assert.equal(h.state.stream.value.open,false)
 h.stop()
})

test('工作负载日志桥接独立检查父层忙碌、错误、读取权限和终端就绪状态',async()=>{
 const h=await viewHarness();await h.state.openDetails(h.workload);await settleView()
 for(const field of ['detailLoading','relatedLoading','relatedError','detailSyncError','detailError','busy']){
  h.state[field].value=['relatedError','detailSyncError','detailError','busy'].includes(field)?'暂不可用':true
  h.state.openWorkloadStream(h.podA,'app',true);assert.equal(h.state.stream.value.open,false,field)
  h.state[field].value=['relatedError','detailSyncError','detailError','busy'].includes(field)?'':false
 }
 h.blocked.add('kubernetes.workload.read');h.state.openWorkloadStream(h.podA,'app',true);assert.equal(h.state.stream.value.open,false);h.blocked.delete('kubernetes.workload.read')
 h.blocked.add('terminal.open');h.state.openWorkloadStream(h.podA,'app',true);assert.equal(h.state.stream.value.open,false);h.blocked.delete('terminal.open')
 h.state.openWorkloadStream(h.podA,'prepare',true);assert.equal(h.state.stream.value.open,false)
 h.state.openWorkloadStream(h.podA,'metrics',true);assert.equal(h.state.stream.value.open,true);assert.equal(h.state.stream.value.terminal,true)
 h.state.stream.value.open=false;h.state.closeDetails();h.state.openWorkloadStream(h.podA,'app');assert.equal(h.state.stream.value.open,false);h.stop()
})

test('关联详情返回保留工作负载选择与模板分区，关闭后清理全部工作负载选择',async()=>{
 const h=await viewHarness();await h.state.openDetails(h.workload);await settleView()
 const selection={revisionUID:'rs-current',podUID:'pod-b',containerName:'metrics',podTab:'diagnostics'}
 h.state.workloadSelections.value['testdeployment-a']=selection
 await h.state.followReference({resource:'pods',name:'pod-a',namespace:'team',uid:'pod-a'});await settleView()
 assert.equal(h.state.detail.value.kind,'Pod');assert.deepEqual(h.state.workloadSelections.value['testdeployment-a'],selection)
 await h.state.backDetail();await settleView();assert.equal(h.state.detail.value.metadata.uid,'deployment-a');assert.deepEqual(h.state.workloadSelections.value['testdeployment-a'],selection)
 h.state.detailTab.value='template';h.state.templateSection.value='storage'
 h.state.rememberTemplateContainer('app/metrics')
 assert.equal(h.state.templateIdentity.value,'test/deployment-a');assert.equal(h.state.templateContainerSelections.value['test/deployment-a'],'app/metrics')
 await h.state.followReference({resource:'persistentvolumeclaims',name:'data',namespace:'team',uid:'pvc-a'});await settleView()
 assert.equal(h.state.detail.value.metadata.uid,'pvc-a')
 assert.equal(h.state.detailHistory.value.at(-1).templateIdentity,'test/deployment-a');assert.equal(h.state.detailHistory.value.at(-1).templateContainerKey,'app/metrics')
 h.state.templateContainerSelections.value['test/deployment-a']='app/app'
 await h.state.backDetail();await settleView();assert.equal(h.state.detailTab.value,'template');assert.equal(h.state.templateSection.value,'storage')
 assert.equal(h.state.templateContainerSelections.value['test/deployment-a'],'app/metrics')
 h.state.closeDetails();assert.deepEqual(h.state.workloadSelections.value,{});assert.deepEqual(h.state.templateContainerSelections.value,{});assert.deepEqual(h.state.detailHistory.value,[]);h.stop()
})

test('模板容器选择只接受当前资源的真实容器和读取权限，刷新删除目标仍保留失效标识',async()=>{
 const h=await viewHarness();await h.state.openDetails(h.workload);await settleView()
 h.state.rememberTemplateContainer('init/prepare');assert.equal(h.state.templateContainerSelections.value['test/deployment-a'],'init/prepare')
 for(const key of ['app/not-found','init/app','app/metrics/other'])h.state.rememberTemplateContainer(key)
 h.state.rememberTemplateContainer('app/metrics','other-cluster/deployment-a')
 h.state.rememberTemplateContainer('app/metrics','test/other-workload')
 assert.equal(h.state.templateContainerSelections.value['test/deployment-a'],'init/prepare')
 h.blocked.add('kubernetes.workload.read');h.state.rememberTemplateContainer('app/metrics');assert.equal(h.state.templateContainerSelections.value['test/deployment-a'],'init/prepare');h.blocked.delete('kubernetes.workload.read')
 h.state.detailTab.value='template';h.state.templateSection.value='containers'
 await h.state.followReference({resource:'persistentvolumeclaims',name:'data',namespace:'team',uid:'pvc-a'});await settleView()
 h.workload.spec.template.spec.initContainers=[]
 await h.state.backDetail();await settleView()
 assert.equal(h.state.templateContainerSelections.value['test/deployment-a'],'init/prepare');assert.equal(h.state.detail.value.spec.template.spec.initContainers.length,0)
 assert.equal(h.state.templateContainerSelections.value['test/pvc-a'],undefined)
 h.stop()
})

test('工作负载摘要保留显式零策略和中性缩容状态，缺失副本数不显示成功',()=>{
 const component=loadFrontend('../src/components/KubeWorkloadSummary.vue',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})},'@/utils/kubernetesPresentation':presentation,'@/utils/kubernetesWorkloadExplorer':helpers}).default
 const props=vue.reactive({object:{...structuredClone(deployment),spec:{replicas:0,minReadySeconds:0,progressDeadlineSeconds:0,revisionHistoryLimit:0,strategy:{type:'RollingUpdate',rollingUpdate:{maxSurge:0,maxUnavailable:'0%'}}},status:{replicas:0,readyReplicas:0,updatedReplicas:0}},type:api.kubeTypes.find(type=>type.resource==='deployments'),events:[],eventsError:false})
 const scope=vue.effectScope();let state;scope.run(()=>{state=component.setup(props,{expose(){},emit(){}})})
 assert.equal(state.cells.value.replicas.text,'0/0');assert.equal(state.cells.value.replicas.badge,'neutral')
 assert.deepEqual(state.settings.value.map(row=>row.value),['RollingUpdate',0,'0%','0','0',0])
 props.object={...structuredClone(deployment),spec:{replicas:2}};assert.equal(state.cells.value.replicas.text,'—/2');assert.equal(state.cells.value.replicas.badge,'neutral');assert.deepEqual(state.settings.value,[])
 scope.stop()
})
