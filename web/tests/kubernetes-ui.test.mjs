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
const api=load('../src/api/kubernetes.ts'),workspace=load('../src/utils/kubernetesWorkspace.ts'),presentation=load('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':api}),workload=load('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api})
const translate=key=>key,settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
test('列表地址参数拒绝异常值，默认项不污染地址；排序按真实时间和自然名称且不改原列表',()=>{
 assert.deepEqual(workspace.readKubeListState({q:['private'],sort:'injected',order:['desc'],page:'-1',limit:['20']}),{query:'',label:'',status:'',sort:'name',order:'asc',page:1,limit:10})
 assert.equal(workspace.readKubeListState({page:'9999999999',limit:'100'}).page,1)
 const state=workspace.readKubeListState({q:'api',page:'3',limit:'20',sort:'created',order:'desc'});assert.equal(workspace.kubeListQuery(state).q,'api');assert.equal(workspace.kubeListQuery(state).page,'3')
 const a={metadata:{name:'pod-10',uid:'1',creationTimestamp:'2026-10-23T01:00:00Z'}},b={metadata:{name:'pod-2',uid:'2',creationTimestamp:'2026-10-22T01:00:00Z'}},missing={metadata:{name:'pod-1',uid:'3'}};const rows=[a,b,missing]
 assert.deepEqual(workspace.sortKubeRows(rows,'created','desc'),[a,b,missing]);assert.deepEqual(workspace.sortKubeRows(rows,'created','asc'),[b,a,missing]);assert.deepEqual(workspace.sortKubeRows(rows,'name','asc'),[missing,b,a]);assert.deepEqual(rows,[a,b,missing])
 assert.deepEqual(workspace.withoutKubeListQuery({cluster:'c',namespace:['a','b'],from:'overview',q:'x',page:'2'}),{cluster:'c',namespace:['a','b'],from:'overview'})
})
async function workspaceHarness(path='/kubernetes?view=workloads&resource=pods&cluster=test&namespace=team'){
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/kubernetes',component:{}}]});await router.push(path)
 const route=new Proxy({},{get:(_,key)=>router.currentRoute.value[key]}),scope=vue.effectScope()
 const component=load('../src/views/KubernetesView.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:translate})},'vue-router':{useRoute:()=>route,useRouter:()=>router},'@/api/kubernetes':{...api,listKubeResourceScopes:async()=>({items:[],cursor:{}})},'@/utils/kubernetesWorkspace':workspace,'@/utils/kubernetesPresentation':presentation,'@/stores/auth':{useAuthStore:()=>({canAny:()=>true})},'@/api/client':{__esModule:true,default:{get:async url=>({data:url.endsWith('/namespaces')?{namespaces:['team','other']}:{counts:{},nodes:[],usage:{},phases:{}}})}}}).default
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
