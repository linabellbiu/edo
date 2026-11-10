import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const permissions = ['workflow.read','workflow.update','workflow.create','workflow.execute','application.read','build_plan.read','deployment_plan.read','notification.read','notification.create','notification.execute','delivery_variable.read','application_database.read','deployment.read']
const graph = (project = 'project-a', patch = {}) => ({ id: `flow-${project}`, project_id: project, application_id: `app-${project}`, schema_version: 1, name: `流水线 ${project}`, revision: 3, is_active: false, source: { id:'source', type:'trigger', name:'代码源', config:{branch:'main',events:['manual']} }, stages:[{id:'stage',name:'检查',tasks:[{id:'shell',type:'shell',name:'脚本',config:{script:'echo ok',environment_variables:{MODE:'test'}}}]}], ...patch })

async function harness({ codes = permissions, project = 'project-a', selected = project, query, get, put, post, mount = true, task = false } = {}) {
  const requests=[],notifications=[],confirmations=[],mounted=[],unmounts=[],routeUpdates=[],routeLeaves=[],events=[],navigations=[],timers=new Map()
  let timerID=0,stopped=false
  const auth=vue.reactive({user:{id:'operator-a',department_id:'department-a',project_id:project,is_superuser:false,permissions:[...codes]},canAny(required){return !!this.user&&(this.user.is_superuser||required.some(code=>this.user.permissions.includes(code)))}})
  const storage={selected},localStorage={getItem:key=>key==='edo.project'?storage.selected:null}
  const window=new EventTarget();window.setTimeout=callback=>{const id=++timerID;timers.set(id,callback);return id};window.clearTimeout=id=>timers.delete(id)
  const document={body:{classList:{add(){},remove(){},toggle(){}}}},route=vue.reactive({query:query||{application:'app-project-a',workflow:'flow-project-a'},fullPath:'/pipeline-plans/editor'})
  const router={async replace(value){if(value.query)route.query={...value.query}},async push(value){navigations.push(value)},resolve:()=>({href:'/test'})}
  const client=Object.fromEntries(['get','put','post'].map(method=>[method,async(path,bodyOrOptions,options)=>{
    const request={method,path,body:method==='get'?undefined:JSON.parse(JSON.stringify(bodyOrOptions??null)),options:method==='get'?bodyOrOptions:options};requests.push(request)
    const handler={get,put,post}[method];if(handler){const value=await handler(request);if(value)return value}
    const projectID=request.options?.headers['X-EDO-Project-ID']||project,workflow=graph(projectID)
    const data=path==='/workflows'?{workflows:[{...workflow,kind:'application',application_name:`应用 ${projectID}`}]}:path==='/settings/builtin-variables'?{variables:[{id:'builtin',kind:'environment',name:'EDO_COMMIT',scopes:['pipeline_script'],syntax:'$EDO_COMMIT',label:'提交'}]}:path==='/applications'?{applications:[{id:`app-${projectID}`,name:`应用 ${projectID}`,workflows:[workflow]}]}:path==='/workflow-templates'?{workflow_templates:[{...workflow,application_id:''}]}:path==='/build-plans'?{build_plans:[{id:`build-${projectID}`,name:'构建',is_active:true}]}:path==='/deployment-plans'?{deployment_plans:[{id:`deploy-${projectID}`,name:'部署',is_active:true}]}:path==='/notification-channels'?{channels:[{id:`channel-${projectID}`,name:'渠道',is_active:true,type:'webhook'}]}:path==='/delivery-variables'?{variables:[{project_id:projectID,name:`KEY_${projectID}`,is_secret:true,value:'不得保留'}]}:path==='/application-databases'?{databases:[{id:`db-${projectID}`,project_id:projectID,name:'数据库'}]}:path==='/environments'?{environments:[{id:`env-${projectID}`,project_id:projectID,name:'环境'}]}:{workflow,workflow_template:workflow,issues:[],valid:true}
    return {data}
  }]))
  const props=vue.reactive({node:{id:'shell',type:'shell',name:'检查',config:{script:'echo ok'}},disabled:false,buildNodes:[]})
  const component=loadFrontend(task?'../src/components/PipelineTaskControls.vue':'../src/views/PipelineEditorView.vue',{
    vue:{...vue,onMounted:fn=>mounted.push(fn),onBeforeUnmount:fn=>unmounts.push(fn)},
    'vue-router':{useRoute:()=>route,useRouter:()=>router,onBeforeRouteUpdate:fn=>routeUpdates.push(fn),onBeforeRouteLeave:fn=>routeLeaves.push(fn)},
    '@/utils/deploymentExecution':loadFrontend('../src/utils/deploymentExecution.ts'),
    '@/stores/auth':{useAuthStore:()=>auth},'@/api/client':{__esModule:true,default:client},'@/api/resources':{apiErrorMessage:()=> '当前项目操作失败'},
    'ant-design-vue':{Modal:{confirm(config){const value={...config,destroyed:false,destroy(){this.destroyed=true;config.afterClose?.()}};confirmations.push(value);return value}},message:Object.fromEntries(['success','warning','error'].map(kind=>[kind,text=>notifications.push({kind,text})]))},
  },{localStorage,window,document}).default
  const scope=vue.effectScope();let state
  scope.run(()=>{state=component.setup(props,{expose(){},emit:(event,...values)=>events.push({event,values})})})
  if(mount)mounted.forEach(fn=>fn());await settle()
  const stop=()=>{if(stopped)return;stopped=true;unmounts.forEach(fn=>fn());scope.stop();timers.clear()}
  return {state,auth,storage,window,route,router,props,requests,notifications,confirmations,routeUpdates,routeLeaves,events,navigations,timers,stop}
}
const writes=h=>h.requests.filter(item=>item.method!=='get')
const changeProject=async(h,id)=>{h.storage.selected=id;h.window.dispatchEvent(new Event('edo:projects-changed'));h.auth.user.project_id=id;h.route.query={application:`app-${id}`,workflow:`flow-${id}`};h.state.resetScope?.();await settle()}

test('部署来源在流水线自定义和选择已有方案之间切换，保留本次自定义草稿且不新建库记录',async t=>{
 const custom={name:'流水线自定义',kind:'docker',timeout_seconds:600,deployment_target:{platform:'docker',environment_id:'env-project-a'}}
 const h=await harness();t.after(h.stop)
 h.state.stages.value[0].tasks=[{id:'deploy',type:'deploy',name:'部署',config:{deployment_plan_id:'existing'}}]
 h.state.selectNode('deploy');await settle();assert.equal(h.state.deploymentSource(h.state.selectedNode.value),'plan')
 h.state.changeDeploymentSource('inline');h.state.editInlineDeployment();await settle();h.state.applyInlineDeployment(custom)
 assert.equal(h.state.selectedNode.value.config.deployment_plan_id,undefined);assert.equal(h.state.selectedNode.value.config.inline_deployment_plan.name,custom.name)
 h.state.changeDeploymentSource('plan');assert.equal(h.state.selectedNode.value.config.inline_deployment_plan,undefined)
 assert.equal(h.state.selectedNode.value.config.deployment_plan_id,'existing')
 h.state.changeDeploymentSource('inline');assert.equal(h.state.selectedNode.value.config.inline_deployment_plan.name,custom.name)
 assert.equal(writes(h).length,0)
 await changeProject(h,'project-b');assert.equal(h.state.inlineDeploymentOpen.value,false)
})

const linkedPlan={id:'existing',name:'已关联方案',kind:'docker',is_active:true,timeout_seconds:120,deployment_target_id:'target',deployment_target:{id:'target',platform:'docker',environment_id:'env-project-a'}}
async function linkedHarness(options={}){
 const workflow=graph('project-a',{stages:[{id:'stage',name:'部署',tasks:[{id:'deploy',type:'deploy',name:'部署应用',config:{deployment_plan_id:'existing'}}]}]})
 const h=await harness({...options,get:request=>options.get?.(request)||(request.path==='/deployment-plans'?{data:{deployment_plans:[linkedPlan]}}:request.path==='/applications/app-project-a/workflows/flow-project-a'?{data:{workflow,issues:[]}}:undefined)})
 h.state.selectNode('deploy');await settle();return h
}

test('正常读取后自动回显已有库方案，重复来源事件和重新打开均不清空关联',async t=>{
 const h=await linkedHarness({codes:[...permissions,'deployment_plan.update']});t.after(h.stop)
 assert.equal(h.state.deploymentSource(h.state.selectedNode.value),'plan')
 assert.equal(h.state.selectedLibraryDeploymentPlan.value.name,'已关联方案')
 assert.equal(h.state.changingDeploymentPlanNodeID.value,'')
 h.state.changeDeploymentSource('plan');assert.equal(h.state.dirty.value,false)
 h.state.selectNode('source');h.state.selectNode('deploy');await settle()
 assert.equal(h.state.selectedNode.value.config.deployment_plan_id,'existing')
 h.state.modifyLinkedDeploymentPlan()
 assert.deepEqual(h.navigations.map(({path,query})=>({path,query})),[{path:'/deployment-plans',query:{plan:'existing',edit:'1'}}])
 assert.deepEqual(h.navigations[0].state.deployment_execution_handoff,{plan_id:'existing',user_id:'operator-a',project_id:'project-a',config_json:'{}'})
 assert.deepEqual(writes(h),[])
})

test('已有选择必须先更换，再选有效方案；切回选择方案恢复原关联',async t=>{
 const h=await linkedHarness();t.after(h.stop)
 h.state.deploymentPlans.value.push({...linkedPlan,id:'other',name:'另一方案'})
 h.state.changeLibraryDeploymentPlan('other');assert.equal(h.state.dirty.value,false)
 h.state.unlockDeploymentPlanSelection();assert.equal(h.state.changingDeploymentPlanNodeID.value,'deploy')
 h.state.closePanel();h.state.selectNode('deploy');await settle()
 assert.equal(h.state.changingDeploymentPlanNodeID.value,'')
 h.state.unlockDeploymentPlanSelection()
 h.state.changeLibraryDeploymentPlan('missing');assert.equal(h.state.selectedNode.value.config.deployment_plan_id,'existing')
 h.state.changeLibraryDeploymentPlan('other');assert.equal(h.state.selectedNode.value.config.deployment_plan_id,'other')
 assert.equal(h.state.changingDeploymentPlanNodeID.value,'')
 h.state.changeDeploymentSource('inline');h.state.changeDeploymentSource('plan')
 assert.equal(h.state.selectedNode.value.config.deployment_plan_id,'other')
})

test('停用或丢失方案可安全回显原选择，无查看或修改权限不允许跳转修改',async t=>{
 const inactive=await linkedHarness({get:r=>r.path==='/deployment-plans'?{data:{deployment_plans:[{...linkedPlan,is_active:false}]}}:undefined});t.after(inactive.stop)
 assert.deepEqual(inactive.state.deploymentPlanOptions('deploy').map(({label,disabled})=>({label,disabled})),[{label:'已关联方案',disabled:true}])
 assert.equal(inactive.state.selectedNode.value.config.deployment_plan_id,'existing')
 inactive.state.modifyLinkedDeploymentPlan();assert.deepEqual(inactive.navigations,[])
 const missing=await linkedHarness({get:r=>r.path==='/deployment-plans'?{data:{deployment_plans:[]}}:undefined});t.after(missing.stop)
 assert.equal(missing.state.deploymentPlanOptions('deploy')[0].label,'原部署方案不可用')
 assert.equal(missing.state.selectedNode.value.config.deployment_plan_id,'existing')
 const denied=await linkedHarness({codes:permissions.filter(code=>code!=='deployment_plan.read').concat('deployment_plan.update')});t.after(denied.stop)
 assert.equal(denied.state.selectedLibraryDeploymentPlan.value,undefined)
 assert.equal(denied.state.deploymentPlanOptions('deploy')[0].label,'无权查看原部署方案')
 denied.state.modifyLinkedDeploymentPlan();assert.deepEqual(denied.navigations,[])
})

// 真实用户与项目初始化通过页面正常的资源读取完成；不直接赋值绕过权限上下文。
test('流水线编辑按当前项目加载应用图及选项，所有请求固定范围',async t=>{
  const h=await harness();t.after(h.stop)
  assert.equal(h.state.workflow.value.id,'flow-project-a');assert.equal(h.state.canEdit.value,true)
  assert.ok(h.requests.every(request=>request.options.headers['X-EDO-Project-ID']==='project-a'));assert.deepEqual(writes(h),[])
})

test('未选项目、范围与已加载角色不匹配、没有read权限均不加载编辑器',async t=>{
  for(const options of [{selected:''},{selected:'project-b'},{codes:['workflow.update']}]){
    const h=await harness(options);t.after(h.stop);await h.state.loadResources();await h.state.save(true);h.state.requestSave();await h.state.validateGraph()
    assert.deepEqual(h.requests,[]);assert.equal(h.state.workflow.value,null);assert.equal(h.state.saveConfirmOpen.value,false);assert.ok(h.state.scopeError.value)
  }
})


test('只读角色可检查当前图，但不能保存启用或新增通知渠道',async t=>{
  const h=await harness({codes:['workflow.read','application.read']});t.after(h.stop)
  await h.state.validateGraph();assert.equal(h.requests.filter(item=>item.method==='post'&&item.path.endsWith('/validate')).length,1)
  assert.equal(await h.state.save(true),false);h.state.requestSave();h.state.openNotificationChannelModal();await h.state.createNotificationChannel()
  assert.equal(h.requests.filter(item=>item.method==='put').length,0);assert.equal(h.state.saveConfirmOpen.value,false);assert.equal(h.state.notificationChannelModalOpen.value,false);assert.ok(h.notifications.some(item=>item.text==='结构检查通过。'))
})

test('旧项目保存请求被冻结并中止，迟到成功不恢复图或抹掉新草稿',async t=>{
  const pending=deferred(),h=await harness({put:()=>pending.promise});t.after(h.stop)
  h.state.selectNode('source');h.state.updateSelectedNode({name:'旧草稿'});const saving=h.state.save(false,true)
  const request=writes(h)[0];assert.equal(request.options.headers['X-EDO-Project-ID'],'project-a')
  await changeProject(h,'project-b');assert.equal(request.options.signal.aborted,true)
  h.state.selectNode('source');h.state.updateSelectedNode({name:'新项目草稿'})
  pending.resolve({data:{workflow:graph('project-a',{name:'旧成功'}),issues:[]}});assert.equal(await saving,false)
  assert.equal(h.state.workflow.value.id,'flow-project-b');assert.equal(h.state.sources.value[0].name,'新项目草稿');assert.deepEqual(h.notifications,[])
})

test('旧启用确认与已排程自动保存回调在撤销角色后均不能写入',async t=>{
  const h=await harness();t.after(h.stop)
  h.state.workflow.value.is_active=true;h.state.requestSave();assert.equal(h.state.saveConfirmOpen.value,true)
  h.state.workflow.value.is_active=false;h.state.selectNode('source');h.state.updateSelectedNode({name:'待保存'});const callback=[...h.timers.values()][0]
  h.auth.user.permissions=['workflow.read'];await settle();await h.state.confirmSaveUpdate();await callback?.();await h.state.save(false)
  assert.equal(h.state.saveConfirmOpen.value,false);assert.deepEqual(writes(h),[]);assert.equal(h.state.canEdit.value,false)
})

test('项目选择变化但事件未派发，也不能以旧图向新项目修改',async t=>{
  const h=await harness();t.after(h.stop);h.storage.selected='project-b'
  await h.state.save(true);await h.state.validateGraph();h.state.requestSave();assert.deepEqual(writes(h),[])
})

test('迟到资源列表和详情不能覆盖新项目图、构建方案或渠道',async t=>{
  const pending=deferred(),h=await harness({get:request=>request.path==='/applications'&&request.options.headers['X-EDO-Project-ID']==='project-a'?pending.promise:undefined});t.after(h.stop)
  const old=h.requests.find(item=>item.path==='/applications');await changeProject(h,'project-b')
  pending.resolve({data:{applications:[{id:'app-project-a',name:'旧应用',workflows:[graph()]}]}});await settle()
  assert.equal(old.options.signal.aborted,true);assert.equal(h.state.workflow.value.id,'flow-project-b');assert.equal(h.state.buildPlans.value[0].id,'build-project-b');assert.equal(h.state.notificationChannels.value[0].id,'channel-project-b')
})

test('项目变更清空通知密钥草稿和确认，旧创建结果不会加入新任务',async t=>{
  const pending=deferred(),h=await harness({post:request=>request.path==='/notification-channels'?pending.promise:undefined});t.after(h.stop)
  h.state.selectNode('shell');h.state.openNotificationChannelModal();Object.assign(h.state.notificationChannelForm,{name:'旧渠道',endpoint:'https://example.test/hook',token:'旧密钥'})
  const creating=h.state.createNotificationChannel();await changeProject(h,'project-b')
  assert.equal(h.state.notificationChannelForm.token,'');assert.equal(h.state.notificationChannelModalOpen.value,false)
  pending.resolve({data:{channel:{id:'old-channel',name:'旧',is_active:true}}});await creating
  assert.ok(!h.state.notificationChannels.value.some(item=>item.id==='old-channel'));assert.deepEqual(h.notifications,[])
})

test('确认删除阶段后切图或撤权不会删除新图同名ID阶段',async t=>{
  const h=await harness();t.after(h.stop);h.state.removeStage(h.state.stages.value[0]);const confirmation=h.confirmations[0]
  await changeProject(h,'project-b');assert.equal(confirmation.destroyed,true);confirmation.onOk()
  assert.equal(h.state.stages.value.length,1);assert.equal(h.state.dirty.value,false)
})

test('路由切换等待期间切项目，旧详情响应不能更新图与路由身份',async t=>{
  const pending=deferred(),h=await harness({get:request=>request.path==='/workflow-templates/template-other'?pending.promise:undefined});t.after(h.stop)
  h.state.templates.value.push({...graph(),id:'template-other',application_id:''})
  const navigation=h.routeUpdates[0]({query:{template:'template-other'}});await settle();const request=h.requests.find(item=>item.path==='/workflow-templates/template-other')
  await changeProject(h,'project-b');pending.resolve({data:{workflow_template:{...graph(),id:'template-other'},issues:[]}})
  assert.equal(await navigation,false);assert.equal(request.options.signal.aborted,true);assert.equal(h.state.workflow.value.id,'flow-project-b');assert.equal(h.state.applicationID.value,'app-project-b')
})

test('403或卸载中止所有编辑请求，不保留受限图/脚本/内联输入',async t=>{
  const h=await harness({put:()=>{throw {response:{status:403}}}});t.after(h.stop)
  h.state.selectNode('source');h.state.updateSelectedNode({name:'不能保留'});h.state.environmentVariableTexts.shell='TOKEN=不应保留';await h.state.save(false)
  assert.equal(h.state.workflow.value,null);assert.deepEqual(h.state.sources.value,[]);assert.deepEqual({...h.state.environmentVariableTexts},{});assert.match(h.state.scopeError.value,/当前项目操作失败/)
  const late=deferred(),unmounted=await harness({put:()=>late.promise});t.after(unmounted.stop);const saving=unmounted.state.save(false);const request=writes(unmounted)[0];unmounted.stop();assert.equal(request.options.signal.aborted,true)
  late.resolve({data:{workflow:graph(),issues:[]}});assert.equal(await saving,false);assert.deepEqual(unmounted.notifications,[])
})

test('任务引用只保留当前项目名称，不在内存中保留变量内容',async t=>{
  const h=await harness({task:true});t.after(h.stop)
  assert.deepEqual(h.state.variables.value,[{name:'KEY_project-a',is_secret:true}]);assert.equal(h.state.variableOptions.value[0].label,'KEY_project-a · 密钥')
  assert.ok(h.requests.every(request=>request.options.headers['X-EDO-Project-ID']==='project-a'))
})

test('部署任务只额外选择共享引用，自动配置不重复出现，已有显式引用仍可编辑',async t=>{
  const h=await harness({task:true,get:request=>request.path==='/delivery-variables'?{data:{variables:[{project_id:'project-a',name:'APP_ENV',usage:'deploy',kind:'env_text',auto_deploy:true,is_secret:true,value:'不保留内容'},{project_id:'project-a',name:'SHARED',usage:'deploy',kind:'env',is_secret:false}]}}:undefined});t.after(h.stop)
  h.props.node.type='deploy';await settle()
  assert.deepEqual(h.state.variableOptions.value.map(item=>item.value),['SHARED'])
  assert.equal(JSON.stringify(h.state.variables.value).includes('不保留内容'),false)
  h.props.node.config.variable_names=['APP_ENV'];await settle()
  assert.deepEqual(h.state.variableOptions.value.map(item=>item.value),['APP_ENV','SHARED'])
})

test('任务引用角色撤销与切项目立即清理选项，旧响应不能回填',async t=>{
  const pending=deferred(),h=await harness({task:true,get:request=>request.path==='/delivery-variables'&&request.options.headers['X-EDO-Project-ID']==='project-a'?pending.promise:undefined});t.after(h.stop)
  h.storage.selected='project-b';h.window.dispatchEvent(new Event('edo:projects-changed'));h.auth.user.project_id='project-b';await settle()
  pending.resolve({data:{variables:[{project_id:'project-a',name:'OLD',is_secret:true}]}});await settle();assert.equal(h.state.variables.value[0].name,'KEY_project-b')
  h.auth.user.permissions=['workflow.read','workflow.update'];await settle();assert.deepEqual(h.state.variables.value,[]);assert.deepEqual(h.state.databases.value,[]);assert.deepEqual(h.state.environments.value,[])
})

test('禁用或没有workflow.update时任务配置回调不能发出本地修改',async t=>{
  const h=await harness({task:true,codes:['workflow.read','delivery_variable.read']});t.after(h.stop)
  h.state.chooseTool('migration');h.state.updateCache({paths:['.cache']});assert.deepEqual(h.events,[])
  h.auth.user.permissions=permissions;await settle();h.props.disabled=true;h.state.chooseTool('script');assert.deepEqual(h.events,[])
  h.props.disabled=false;h.state.chooseTool('script');assert.equal(h.events.length,1)
})

test('只有流水线查看权限时可从统一目录打开应用流水线，不额外要求应用查看权限',async t=>{
 const h=await harness({codes:['workflow.read']});t.after(h.stop)
 assert.equal(h.state.workflow.value.id,'flow-project-a');assert.ok(h.requests.some(r=>r.path==='/workflows'));assert.equal(h.requests.some(r=>r.path==='/applications'),false)
 assert.equal(h.state.canEdit.value,false)
})

test('构建任务不为已移除的变量表单读取资源，也不清空已有引用',async t=>{
 const h=await harness({task:true});t.after(h.stop)
 h.props.node={id:'build',type:'build',name:'镜像构建',config:{variable_names:['KEEP_ME'],variable_environment_id:'saved-group',image_tag:'old-tag'}}
 await settle();h.requests.length=0
 await h.state.load()
 assert.deepEqual(h.requests,[])
 assert.deepEqual(h.state.variables.value,[])
 assert.deepEqual(h.state.environments.value,[])
 assert.deepEqual(h.props.node.config.variable_names,['KEEP_ME'])
 assert.equal(h.props.node.config.variable_environment_id,'saved-group')
})

test('从常规配置修改镜像 Tag 保存时保留原项目变量与主机组引用',async t=>{
 for(const inline of [false,true]){
  const config={image_name:'saved-image',image_tag:'old-tag',variable_names:['KEEP_ME'],variable_environment_id:'saved-group',...(inline?{inline_build:{kind:'dockerfile',name:'内联构建',dockerfile_path:'Dockerfile',context_path:'.'}}:{build_plan_id:'build-project-a'})}
  const workflow=graph('project-a',{stages:[{id:'build-stage',name:'构建',tasks:[{id:'build',name:'镜像构建',type:'build',config}]}]})
  const h=await harness({get:r=>r.path==='/applications/app-project-a/workflows/flow-project-a'?{data:{workflow,issues:[]}}:undefined});t.after(h.stop)
  h.state.selectNode('build');h.state.updateSelectedNode({},{image_tag:'${EDO_GIT_TAG}'})
  await h.state.save(false)
  const saved=writes(h).find(r=>r.method==='put').body.stages[0].tasks[0].config
  assert.equal(saved.image_tag,'${EDO_GIT_TAG}')
  assert.equal(saved.image_name,'saved-image')
  assert.deepEqual(saved.variable_names,['KEEP_ME'])
  assert.equal(saved.variable_environment_id,'saved-group')
 }
})

const linkedBuildPlan={id:'shared-build',name:'通用镜像构建',kind:'dockerfile',dockerfile_path:'Dockerfile',context_path:'.',image_registry_id:'registry',is_active:true,timeout_seconds:1800}
async function linkedBuildHarness(options={}){
 const workflow=graph('project-a',{stages:[{id:'build-stage',name:'构建',tasks:[{id:'build',type:'build',name:'镜像构建',config:{build_plan_id:'shared-build',image_tag:'${EDO_GIT_TAG}',variable_names:['KEEP_ME']}}]}]})
 const h=await harness({...options,get:r=>options.get?.(r)||(r.path==='/build-plans'?{data:{build_plans:[linkedBuildPlan]}}:r.path==='/applications/app-project-a/workflows/flow-project-a'?{data:{workflow,issues:[]}}:undefined)})
 h.state.selectNode('build');await settle();return h
}
test('构建任务回显通用方案，修改跳到准确方案且更换需要显式解锁',async t=>{
 const h=await linkedBuildHarness({codes:[...permissions,'build_plan.update']});t.after(h.stop)
 assert.equal(h.state.buildSource(h.state.selectedNode.value),'plan')
 assert.equal(h.state.selectedLibraryBuildPlan.value.dockerfile_path,'Dockerfile')
 assert.equal(h.state.canModifyBuildPlan.value,true)
 h.state.modifyLinkedBuildPlan()
 assert.deepEqual(h.navigations[0].query,{plan:'shared-build',edit:'1',return_to:'/pipeline-plans/editor'})
 h.state.buildPlans.value.push({...linkedBuildPlan,id:'replacement'})
 h.state.changeLibraryBuildPlan('replacement');assert.equal(h.state.selectedNode.value.config.build_plan_id,'shared-build')
 h.state.unlockBuildPlanSelection();h.state.changeLibraryBuildPlan('replacement')
 assert.equal(h.state.selectedNode.value.config.build_plan_id,'replacement')
 assert.equal(h.state.selectedNode.value.config.inline_build,undefined)
})
test('构建来源二选一并保留自定义和通用草稿，保存不创建通用方案记录',async t=>{
 const h=await linkedBuildHarness();t.after(h.stop)
 h.state.changeBuildSource('inline')
 h.state.updateSelectedNode({},{inline_build:{...linkedBuildPlan,id:'',context_path:'worker'},cache:{key:'keep-cache',paths:['.cache'],lock_files:[]},build_outputs:undefined})
 h.state.changeBuildSource('plan')
 assert.equal(h.state.selectedNode.value.config.inline_build,undefined)
 assert.equal(h.state.selectedNode.value.config.build_plan_id,'shared-build')
 assert.equal(h.state.selectedNode.value.config.cache,undefined)
 h.state.changeBuildSource('inline')
 assert.equal(h.state.selectedNode.value.config.inline_build.context_path,'worker')
 assert.equal(h.state.selectedNode.value.config.cache.key,'keep-cache')
 assert.equal(h.state.selectedNode.value.config.build_plan_id,undefined)
 assert.equal(h.state.selectedNode.value.config.image_tag,'${EDO_GIT_TAG}')
 await h.state.save(false)
 const write=writes(h).find(r=>r.method==='put')
 assert.equal(write.body.stages[0].tasks[0].config.inline_build.context_path,'worker')
 assert.equal(write.body.stages[0].tasks[0].config.build_plan_id,undefined)
 assert.deepEqual(write.body.stages[0].tasks[0].config.variable_names,['KEEP_ME'])
 assert.equal(writes(h).some(r=>r.path==='/build-plans'),false)
})

test('脚本方案切回当前任务配置保留构建类型，往返与保存不创建库方案',async t=>{
 const scriptPlan={...linkedBuildPlan,kind:'script',script:'printf shared',artifact_path:'dist'}
 const h=await linkedBuildHarness({get:r=>r.path==='/build-plans'?{data:{build_plans:[scriptPlan]}}:undefined});t.after(h.stop)
 assert.equal(h.state.selectedBuildPlan.value.kind,'script')
 h.state.changeBuildSource('inline')
 assert.equal(h.state.selectedNode.value.config.inline_build.kind,'script')
 h.state.updateSelectedNode({},{inline_build:{...h.state.selectedNode.value.config.inline_build,script:'printf task-local'}})
 h.state.changeBuildSource('plan')
 assert.equal(h.state.selectedBuildPlan.value.kind,'script')
 assert.equal(h.state.selectedLibraryBuildPlan.value.script,'printf shared')
 h.state.changeBuildSource('inline')
 assert.equal(h.state.selectedNode.value.config.inline_build.kind,'script')
 assert.equal(h.state.selectedNode.value.config.inline_build.script,'printf task-local')
 await h.state.save(false)
 const write=writes(h).find(r=>r.method==='put')
 assert.equal(write.body.stages[0].tasks[0].config.inline_build.kind,'script')
 assert.equal(write.body.stages[0].tasks[0].config.inline_build.script,'printf task-local')
 assert.equal(writes(h).some(r=>r.path==='/build-plans'),false)
})
test('原构建方案停用或无权查看时保留关联，不自动改成自定义或其他方案',async t=>{
 for(const canRead of [true,false]){
  const h=await linkedBuildHarness({codes:permissions.filter(code=>canRead||code!=='build_plan.read')});t.after(h.stop)
  if(canRead)h.state.buildPlans.value=[{...linkedBuildPlan,is_active:false}]
  await settle()
  assert.equal(h.state.buildSource(h.state.selectedNode.value),'plan')
  assert.equal(h.state.selectedBuildPlanOptions.value[0].value,'shared-build')
  assert.equal(h.state.selectedBuildPlanOptions.value[0].disabled,true)
  assert.equal(h.state.selectedNode.value.config.build_plan_id,'shared-build')
  assert.equal(writes(h).length,0)
 }
})
