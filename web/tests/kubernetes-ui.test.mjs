import assert from 'node:assert/strict'
import {test} from 'node:test'
import {readFileSync} from 'node:fs'
import {parse,compileScript} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import {createRouter,createMemoryHistory} from 'vue-router'
function load(path,modules={}){
 const raw=readFileSync(new URL(path,import.meta.url),'utf8'),source=path.endsWith('.vue')?compileScript(parse(raw).descriptor,{id:'kube-ui'}).content:raw
 const output=ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports',output)(key=>modules[key]??{},module,module.exports);return module.exports
}
const api=load('../src/api/kubernetes.ts',{'./resources':{apiErrorMessage:()=> '连接暂不可用'}}),workspace=load('../src/utils/kubernetesWorkspace.ts'),presentation=load('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':api,'@/utils/kubernetesRoles':load('../src/utils/kubernetesRoles.ts')}),workload=load('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api})
const translate=key=>key,settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
test('列表地址参数拒绝异常值，默认项不污染地址；排序按真实时间和自然名称且不改原列表',()=>{
 assert.deepEqual(workspace.readKubeListState({q:['private'],sort:'injected',order:['desc'],page:'-1',limit:['20']}),{query:'',label:'',status:'',sort:'name',order:'asc',page:1,limit:10})
 assert.equal(workspace.readKubeListState({page:'9999999999',limit:'100'}).page,1)
 const state=workspace.readKubeListState({q:'api',page:'3',limit:'20',sort:'created',order:'desc'});assert.equal(workspace.kubeListQuery(state).q,'api');assert.equal(workspace.kubeListQuery(state).page,'3')
 const a={metadata:{name:'pod-10',uid:'1',creationTimestamp:'2026-10-23T01:00:00Z'}},b={metadata:{name:'pod-2',uid:'2',creationTimestamp:'2026-10-22T01:00:00Z'}},missing={metadata:{name:'pod-1',uid:'3'}};const rows=[a,b,missing]
 assert.deepEqual(workspace.sortKubeRows(rows,'created','desc'),[a,b,missing]);assert.deepEqual(workspace.sortKubeRows(rows,'created','asc'),[b,a,missing]);assert.deepEqual(workspace.sortKubeRows(rows,'name','asc'),[missing,b,a]);assert.deepEqual(rows,[a,b,missing])
 assert.deepEqual(workspace.withoutKubeListQuery({cluster:'c',namespace:['a','b'],from:'overview',q:'x',page:'2'}),{cluster:'c',namespace:['a','b'],from:'overview'})
})
async function workspaceHarness(path='/kubernetes?view=workloads&resource=pods&cluster=test&namespace=team',overrides={}){
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/kubernetes',component:{}}]});await router.push(path)
 const route=new Proxy({},{get:(_,key)=>router.currentRoute.value[key]}),scope=vue.effectScope()
 const component=load('../src/views/KubernetesView.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:translate})},'vue-router':{useRoute:()=>route,useRouter:()=>router},'@/api/kubernetes':{...api,listKubeResourceScopes:async()=>({items:[],cursor:{}}),...overrides},'@/utils/kubernetesWorkspace':workspace,'@/utils/kubernetesPresentation':presentation,'@/stores/auth':{useAuthStore:()=>({canAny:()=>true})},'@/api/client':{__esModule:true,default:{get:async url=>({data:url.endsWith('/namespaces')?{namespaces:['team','other']}:{counts:{},nodes:[],usage:{},phases:{}}})}}}).default
 let state;scope.run(()=>{state=component.setup({}, {expose(){}})});state.clusters.value=[{id:'test',name:'test',default_namespace:'team',is_active:true}];state.cluster.value='test';await settle()
 return {state,router,route,stop(){state.cancelListPersist();scope.stop()}}
}
test('切换资源与浏览器后退恢复筛选、排序和分页，不把 Pod 条件带到 Service 或其他命名空间',async()=>{
 const {state,router,route,stop}=await workspaceHarness()
 state.query.value='api';state.labelFilter.value='app=web';state.sortKey.value='created';state.sortOrder.value='desc';state.pageSize.value=20;state.currentPage.value=3;state.persistList();await settle()
 assert.equal(route.query.q,'api');assert.equal(route.query.page,'3');const original=route.fullPath
 state.selectResource('services');await settle();assert.equal(state.query.value,'');assert.equal(state.currentPage.value,1);assert.equal(route.query.q,undefined)
 await new Promise(resolve=>{const off=router.afterEach(()=>{off();resolve()});router.back()});await settle();assert.equal(route.fullPath,original);assert.equal(state.query.value,'api');assert.equal(state.sortKey.value,'created');assert.equal(state.currentPage.value,3)
 await router.push('/kubernetes?view=workloads&resource=pods&cluster=test&namespace=other');await settle();assert.equal(state.query.value,'');assert.equal(state.labelFilter.value,'')
 await router.push(original);await settle();assert.equal(state.query.value,'api');assert.equal(state.pageSize.value,20);assert.equal(state.currentPage.value,3)
 state.clearFilters();assert.equal(state.currentPage.value,1);state.persistList();await settle();assert.equal(route.query.q,undefined);stop()
})
test('刷新深链接恢复列表参数；隐藏详情时保留对象，编辑或日志结束后回到同一页签',async()=>{
 const {state,stop}=await workspaceHarness('/kubernetes?view=workloads&resource=pods&cluster=test&namespace=team&q=api&page=2&limit=20')
 assert.equal(state.query.value,'api');assert.equal(state.currentPage.value,2);assert.equal(state.pageSize.value,20)
 state.detail.value={kind:'Pod',metadata:{uid:'p1',name:'api',namespace:'team'}};state.detailOpen.value=true;state.detailTab.value='diagnostics';assert.equal(state.detailPresented.value,true)
 state.editorOpen.value=true;assert.equal(state.detailPresented.value,false);assert.equal(state.detail.value.metadata.uid,'p1')
 state.editorOpen.value=false;state.stream.value.open=true;assert.equal(state.detailPresented.value,false);state.stream.value.open=false;assert.equal(state.detailPresented.value,true);assert.equal(state.detailTab.value,'diagnostics')
 state.editorOpen.value=true;state.editorRef.value={confirmLeave:async()=>false};assert.equal(await state.leaveWorkspace(),false);assert.equal(state.editorOpen.value,true)
 state.editorRef.value={confirmLeave:async()=>true};assert.equal(await state.leaveWorkspace(),true);assert.equal(state.editorOpen.value,false);stop()
})
test('后台刷新暂时清空列表时不强制回到第一页，数据返回后才校正超出范围的页码',async()=>{
 const events=[],props=vue.reactive({rows:Array.from({length:45},(_,id)=>({id,name:String(id)})),columns:[{key:'name',label:'Name'}],currentPage:4,rowsPerPage:10,pagination:true,loading:false,rowKey:'id'}),scope=vue.effectScope()
 const component=load('../src/components/ResourceTable.vue',{vue,'vue-i18n':{useI18n:()=>({t:translate})}}).default;let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(event,value)=>{events.push([event,value]);if(event==='update:currentPage')props.currentPage=value}})})
 assert.equal(state.visibleRows.value[0].id,30);props.loading=true;props.rows=[];await settle();assert.equal(props.currentPage,4)
 props.rows=Array.from({length:25},(_,id)=>({id,name:String(id)}));props.loading=false;await settle();assert.equal(props.currentPage,3);assert.equal(state.visibleRows.value[0].id,20);scope.stop()
})
function editorHarness(){
 const dialogs=[],events=[],component=load('../src/components/KubeResourceEditor.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:translate})},'ant-design-vue':{Modal:{confirm:options=>{dialogs.push(options);return {destroy(){}}}}},'@/api/kubernetes':api,'@/utils/kubernetesWorkloadForm':workload}).default,scope=vue.effectScope()
 const props={open:true,cluster:'test',namespace:'team',type:api.kubeTypes.find(item=>item.kind==='Deployment'),object:{kind:'Deployment',metadata:{name:'web',namespace:'team',uid:'d1',resourceVersion:'2'},spec:{template:{metadata:{labels:{app:'web'}},spec:{containers:[{name:'app',image:'app:1'}]}}}}}
 let state;scope.run(()=>{state=component.setup(props,{expose(){},emit:(...args)=>events.push(args)})});return {state,dialogs,events,stop:()=>scope.stop()}
}
test('编辑默认进入容器页签，无修改直接离开；有草稿时取消保留、确认才关闭，保存中拒绝离开',async()=>{
 const {state,dialogs,events,stop}=editorHarness();assert.equal(state.editorTab.value,'containers');assert.equal(state.dirty.value,false);assert.equal(await state.confirmLeave(),true);assert.equal(dialogs.length,0)
 state.form.containers[0].image='app:2';assert.equal(state.dirty.value,true);const denied=state.closeEditor();dialogs[0].onCancel();dialogs[0].afterClose();await denied;assert.equal(events.length,0);assert.equal(state.form.containers[0].image,'app:2')
 const allowed=state.closeEditor();dialogs[1].onOk();dialogs[1].afterClose();await allowed;assert.deepEqual(events,[['update:open',false]])
 state.saving.value=true;assert.equal(await state.confirmLeave(),false);assert.equal(dialogs.length,2);stop()
})
test('跨页签校验失败自动定位到容器、存储或更新策略，不发送无效配置',async()=>{
 const {state,stop}=editorHarness();state.form.containers[0].image='';await state.save();assert.equal(state.editorTab.value,'containers');assert.equal(state.error.value,'k8s.imageRequired')
 state.form.containers[0].image='app:2';state.form.volumes=[workload.volumeForm({name:'',emptyDir:{}})];await state.save();assert.equal(state.editorTab.value,'pod')
 state.form.volumes=[];state.form.maxSurge='0';state.form.maxUnavailable='0';await state.save();assert.equal(state.editorTab.value,'strategy');assert.equal(state.error.value,'k8s.invalidStrategy');stop()
})
test('总览状态颜色按 Pod 阶段固定，不因结果顺序变化把失败状态标绿',()=>{
 const props=vue.reactive({data:{counts:{pods:{count:3,state:'ok'}},nodes:[],usage:{},phases:{Failed:2,Unknown:1}},loading:false,error:''})
 const component=load('../src/components/KubeOverview.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:translate,locale:vue.ref('zh-CN')})},'echarts/core':{use(){}},'@/api/kubernetes':api,'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})}}).default
 const state=component.setup(props,{expose(){},emit(){}});assert.equal(state.phases.value[0].color,'#ef5a6e');assert.equal(state.phases.value[1].color,'#708198')
 props.data.phases={Running:1,Unknown:1,Failed:1};assert.equal(state.phases.value.find(p=>p.phase==='Failed').color,'#ef5a6e');assert.equal(state.phases.value.find(p=>p.phase==='Running').color,'#22b978')
})
test('修改筛选后立即切换资源，后退仍恢复最后一次输入',async()=>{
 const {state,router,route,stop}=await workspaceHarness();state.query.value='old';state.persistList();await settle()
 state.query.value='latest';state.selectResource('services');await settle()
 await new Promise(resolve=>{const off=router.afterEach(()=>{off();resolve()});router.back()});await settle()
 assert.equal(state.query.value,'latest');assert.equal(route.query.q,'latest');stop()
})
test('自动同步与手动刷新保留用户加载的后续页窗口，失败不覆盖已加载记录',async()=>{
 let failing=false;const calls=[]
 const {state,stop}=await workspaceHarness(undefined,{listKubeResourceScopes:async(_id,_type,_selection,cursor)=>{if(failing)throw Error('offline');calls.push(cursor);const page=cursor?.team==='next'?1:0;return{items:Array.from({length:10},(_,i)=>({kind:'Pod',metadata:{uid:`pod-${page*10+i}`,name:`pod-${page*10+i}`,namespace:'team'}})),cursor:page?{}:{team:'next'}}}})
 assert.equal(state.rows.value.length,10);await state.refresh(false,true);assert.equal(state.rows.value.length,20);assert.equal(state.loadedPages.value,2)
 state.currentPage.value=2;await state.refresh(true);assert.equal(state.rows.value.length,20);assert.equal(state.currentPage.value,2);assert.deepEqual(calls.slice(-2),[undefined,{team:'next'}])
 await state.refresh();assert.equal(state.rows.value.length,20);failing=true;await state.refresh(true);assert.equal(state.rows.value.length,20)
 stop()
})
test('有真实数据的浏览历史返回恢复整个已加载窗口，不只恢复页码',async()=>{
 const {state,router,route,stop}=await workspaceHarness(undefined,{listKubeResourceScopes:async(_id,type,_selection,cursor)=>{
  const page=Number(cursor?.team??0)
  return{items:Array.from({length:10},(_,index)=>({kind:type.kind,metadata:{uid:`${type.resource}-${page*10+index}`,name:`item-${page*10+index}`,namespace:'team'}})),cursor:page<2?{team:String(page+1)}:{}}
 }})
 await state.refresh(false,true);await state.refresh(false,true);state.currentPage.value=2;state.persistList();await settle()
 assert.equal(state.rows.value.length,30);assert.equal(state.loadedPages.value,3)
 const original=route.fullPath,expected=state.rows.value.map(row=>row.metadata.uid)
 state.selectResource('services');await settle();assert.equal(state.loadedPages.value,1)
 await new Promise(resolve=>{const off=router.afterEach(()=>{off();resolve()});router.back()});await settle()
 assert.equal(route.fullPath,original);assert.equal(state.currentPage.value,2);assert.equal(state.loadedPages.value,3);assert.deepEqual(state.rows.value.map(row=>row.metadata.uid),expected)
 stop()
})
test('刷新深链接按实际批次大小有界补齐当前页，首批足够时不额外读取',async()=>{
 let smallCalls=0
 const small=await workspaceHarness('/kubernetes?view=workloads&resource=pods&cluster=test&namespace=team&page=3',{listKubeResourceScopes:async(_id,_type,_selection,cursor)=>{
  const page=Number(cursor?.team??0);smallCalls++
  return{items:Array.from({length:8},(_,index)=>({kind:'Pod',metadata:{uid:`pod-${page*8+index}`,name:`pod-${page*8+index}`,namespace:'team'}})),cursor:{team:String(page+1)}}
 }})
 assert.equal(smallCalls,4);assert.equal(small.state.rows.value.length,32);assert.equal(small.state.currentPage.value,3);assert.equal(small.state.loadedPages.value,4);assert.equal(small.state.hasContinuation.value,true);small.stop()
 let largeCalls=0
 const large=await workspaceHarness('/kubernetes?view=workloads&resource=pods&cluster=test&namespace=team&page=2',{listKubeResourceScopes:async()=>{
  largeCalls++;return{items:Array.from({length:500},(_,index)=>({kind:'Pod',metadata:{uid:`pod-${index}`,name:`pod-${index}`,namespace:'team'}})),cursor:{team:'next'}}
 }})
 assert.equal(largeCalls,1);assert.equal(large.state.rows.value.length,500);assert.equal(large.state.loadedPages.value,1);large.stop()
})
test('超大页码最多自动读取二十批并保留partial，已明确加载的更深窗口仍可恢复',async()=>{
 let calls=0
 const {state,stop}=await workspaceHarness('/kubernetes?view=workloads&resource=pods&cluster=test&namespace=team&page=99999&limit=100',{listKubeResourceScopes:async(_id,_type,_selection,cursor)=>{
  const page=Number(cursor?.team??0);calls++;return{items:[{kind:'Pod',metadata:{uid:`pod-${page}`,name:`pod-${page}`,namespace:'team'}}],cursor:{team:String(page+1)}}
 }})
 assert.equal(calls,20);assert.equal(state.loadedPages.value,20);assert.equal(state.rows.value.length,20);assert.equal(state.hasContinuation.value,true)
 await state.refresh(true);assert.equal(calls,40);assert.equal(state.loadedPages.value,20);assert.equal(state.rows.value.length,20)
 await state.refresh(false,true);assert.equal(calls,41);assert.equal(state.loadedPages.value,21);assert.equal(state.rows.value.length,21)
 await state.refresh(true);assert.equal(calls,62);assert.equal(state.loadedPages.value,21);assert.equal(state.rows.value.length,21);stop()
 let retainedCalls=0
 const retained=await workspace.reloadKubeWindow(async()=>({items:[{metadata:{name:`pod-${++retainedCalls}`}}],cursor:{team:String(retainedCalls)}}),21)
 assert.equal(retainedCalls,21);assert.equal(retained.pages,21);assert.equal(retained.items.length,21)
})
test('窗口续批失败保留整份旧窗口，旧上下文迟到响应不覆盖新资源',async()=>{
 let fail=false,pendingResolve,hold=false
 const {state,stop}=await workspaceHarness(undefined,{listKubeResourceScopes:async(_id,type,_selection,cursor)=>{
  if(hold&&type.resource==='pods')return new Promise(resolve=>{pendingResolve=resolve})
  const page=Number(cursor?.team??0);if(fail&&page===1)throw Error('offline')
  return{items:Array.from({length:10},(_,index)=>({kind:type.kind,metadata:{uid:`${type.resource}-${page*10+index}`,name:`item-${page*10+index}`,namespace:'team'}})),cursor:page?{}:{team:'1'}}
 }})
 await state.refresh(false,true);const expected=state.rows.value;fail=true;await state.refresh(true)
 assert.equal(state.rows.value,expected);assert.equal(state.loadedPages.value,2);assert.equal(state.error.value,'连接暂不可用')
 fail=false;hold=true;const old=state.refresh(true);state.selectResource('services');await settle()
 const current=state.rows.value;pendingResolve({items:[{kind:'Pod',metadata:{uid:'late',name:'late',namespace:'team'}}],cursor:{}});await old;await settle()
 assert.equal(state.rows.value,current);assert.equal(state.rows.value.every(row=>row.kind==='Service'),true);assert.equal(state.error.value,'');stop()
})
test('YAML编辑加入统一离开守卫，取消保留草稿，确认才离开',async()=>{
 const {state,stop}=await workspaceHarness();state.yamlOpen.value=true
 state.yamlRef.value={confirmLeave:async()=>false};assert.equal(await state.leaveWorkspace(),false);assert.equal(state.yamlOpen.value,true)
 state.yamlRef.value={confirmLeave:async()=>true};assert.equal(await state.leaveWorkspace(),true);assert.equal(state.yamlOpen.value,false);stop()
})
test('副本与重启按数值排序，缺失始终在最后，URL保存正确排序列',()=>{
 const rows=[0,10,2,undefined].map((value,index)=>({metadata:{name:`pod-${index}`},cells:{restarts:{sortValue:value}}}))
 assert.deepEqual(workspace.sortKubeRows(rows,'restarts','asc').map(row=>row.cells.restarts.sortValue),[0,2,10,undefined])
 assert.deepEqual(workspace.sortKubeRows(rows,'restarts','desc').map(row=>row.cells.restarts.sortValue),[10,2,0,undefined])
 assert.equal(workspace.readKubeListState({sort:'availableReplicas'}).sort,'availableReplicas')
})
test('日志可选择Init和临时容器，终端只允许当前正在运行的普通或临时容器',()=>{
 const pod={status:{phase:'Running',containerStatuses:[{name:'app',state:{running:{}}}],initContainerStatuses:[{name:'init',state:{running:{}}}],ephemeralContainerStatuses:[{name:'debug',state:{running:{}}}]},spec:{containers:[{name:'app'},{name:'sidecar'}],initContainers:[{name:'init'}],ephemeralContainers:[{name:'debug'}]}}
 assert.deepEqual(workspace.podContainerChoices(pod),[{name:'app',kind:'app',running:true},{name:'sidecar',kind:'app',running:false},{name:'init',kind:'init',running:false},{name:'debug',kind:'ephemeral',running:true}])
 assert.equal(workspace.podContainerChoices({...pod,status:{phase:'Succeeded'}}).every(item=>!item.running),true)
})
test('重读已加载窗口可取消，命名空间耗尽后不多发请求',async()=>{
 let calls=0;const controller=new AbortController()
 const result=await workspace.reloadKubeWindow(async()=>{calls++;return{items:[],cursor:{}}},10)
 assert.equal(calls,1);assert.deepEqual(result,{items:[],cursor:{},pages:1})
 await assert.rejects(workspace.reloadKubeWindow(async()=>{controller.abort();return{items:[],cursor:{team:'next'}}},3,controller.signal),{name:'AbortError'})
})
