import assert from 'node:assert/strict'
import {test} from 'node:test'
import {readFileSync} from 'node:fs'
import * as vue from 'vue'
import {compileScript,compileTemplate,parse} from '@vue/compiler-sfc'
import {renderToString} from '@vue/server-renderer'
import ts from 'typescript'
import {loadFrontend,componentHarness} from './acceptanceHarness.mjs'
const utils=loadFrontend('../src/utils/kubernetesPlanOverride.ts')
const defaults=loadFrontend('../src/utils/kubernetesDeployment.ts').defaultKubernetesDeployment
const base=()=>({id:'shared',name:'通用 K8s',kind:'kubernetes',is_active:true,timeout_seconds:600,execution_config:{kubernetes:defaults()}})
const target={platform:'kubernetes',environment_id:'',runtime_id:'cluster',namespace:'order-api',workload_name:'api',container_name:'api'}
test('选择方案后固定初始配置，阶段独立修改，后续方案更新不改变未覆盖字段',()=>{
 const plan=base(),stage=utils.effectiveKubernetesPlan(plan)
 stage.deployment_target=target;stage.execution_config.kubernetes.cpu_request='400m';stage.execution_config.kubernetes.ports[0].enabled=true;stage.timeout_seconds=900;stage.execution_config.artifact_node_id='build-task'
 const override=utils.stageKubernetesOverride(plan,stage)
 assert.deepEqual(Object.keys(override.kubernetes).sort(),['cpu_request','ports'])
 assert.equal(override.timeout_seconds,900);assert.equal(override.artifact_node_id,'build-task');assert.deepEqual(override.deployment_target,target)
 assert.equal(plan.execution_config.kubernetes.cpu_request,'200m');assert.equal(plan.deployment_target,undefined)
 plan.execution_config.kubernetes.memory_request='350Mi';plan.execution_config.kubernetes.cpu_request='250m'
 const again=utils.effectiveKubernetesPlan(plan,override)
 assert.equal(again.execution_config.kubernetes.cpu_request,'400m');assert.equal(again.execution_config.kubernetes.memory_request,'300Mi')
 assert.equal(override.plan_snapshot.execution_config.kubernetes.memory_request,'300Mi')
})

test('部署阶段打开后库方案刷新不改变初始参数，修改和恢复均以保存快照为准',async t=>{
 const plan=base()
 plan.execution_config.deployment_registry_id='initial-registry'
 const snapshot=utils.snapshotKubernetesPlan(plan)
 const props=vue.reactive({node:{id:'deploy',name:'部署',type:'deploy',config:{deployment_plan_id:'shared',deployment_override:{plan_snapshot:snapshot,deployment_target:target}}},plan,disabled:false,buildNodes:[]})
 const h=componentHarness('../src/components/KubernetesPlanStageFields.vue',{props,codes:['workflow.update','kubernetes.workload.update','kubernetes.network.update'],modules:{'@/utils/kubernetesPlanOverride':utils}});t.after(h.stop)
 await vue.nextTick()
 props.plan.name='更新后的库方案';props.plan.timeout_seconds=1800
 props.plan.execution_config.kubernetes.memory_request='600Mi'
 props.plan.execution_config.kubernetes.service_type='LoadBalancer'
 props.plan.execution_config.deployment_registry_id='new-registry'
 await vue.nextTick()
 assert.equal(h.state.effective.value.name,'通用 K8s')
 assert.equal(h.state.effective.value.execution_config.kubernetes.memory_request,'300Mi')
 assert.equal(h.state.effective.value.execution_config.kubernetes.service_type,'ClusterIP')
 assert.equal(h.state.effective.value.execution_config.deployment_registry_id,'initial-registry')
 const edited=JSON.parse(JSON.stringify(h.state.effective.value));edited.execution_config.kubernetes.replicas=3
 h.state.change({inline_deployment_plan:edited})
 props.node.config.deployment_override=h.events.at(-1).args[0].deployment_override
 await vue.nextTick()
 assert.equal(props.node.config.deployment_override.kubernetes.replicas,3)
 assert.equal(props.node.config.deployment_override.plan_snapshot.execution_config.kubernetes.memory_request,'300Mi')
 h.state.restore();props.node.config.deployment_override=h.events.at(-1).args[0].deployment_override
 await vue.nextTick()
 assert.equal(h.state.effective.value.execution_config.kubernetes.replicas,1)
 assert.equal(h.state.effective.value.execution_config.kubernetes.memory_request,'300Mi')
 assert.equal(h.state.effective.value.timeout_seconds,600)
 assert.equal(props.plan.execution_config.kubernetes.memory_request,'600Mi')
 assert.deepEqual(h.requests,[])
})
test('显式false、空数组与删除探针保持含义，端口数组整体替换',()=>{
 const plan=base();plan.execution_config.kubernetes.node_port=true;plan.execution_config.kubernetes.command=['/server'];plan.execution_config.kubernetes.readiness_probe={kind:'http',port:8080,path:'/ready'}
 const stage=utils.effectiveKubernetesPlan(plan,{kubernetes:{node_port:false,command:[],readiness_probe:null}})
 assert.equal(stage.execution_config.kubernetes.node_port,false);assert.deepEqual(stage.execution_config.kubernetes.command,[]);assert.equal(stage.execution_config.kubernetes.readiness_probe,undefined)
 const diff=utils.stageKubernetesOverride(plan,stage)
 assert.deepEqual(diff.kubernetes,{node_port:false,command:[],readiness_probe:null})
 assert.equal(plan.execution_config.kubernetes.readiness_probe.path,'/ready')
})
test('阶段恢复默认只清除参数覆盖，保留准确目标与上游绑定；禁用阶段不能修改',t=>{
 const props=vue.reactive({node:{id:'stage',name:'发布',type:'deploy',config:{deployment_plan_id:'shared',deployment_override:{deployment_target:target,artifact_node_id:'image',kubernetes:{replicas:2},timeout_seconds:900}}},plan:base(),disabled:false,buildNodes:[]})
 const h=componentHarness('../src/components/KubernetesPlanStageFields.vue',{props,codes:['workflow.update','kubernetes.workload.update','kubernetes.network.update'],modules:{'@/utils/kubernetesPlanOverride':utils}});t.after(h.stop)
 assert.equal(h.state.effective.value.execution_config.kubernetes.replicas,2)
 h.state.restore();assert.deepEqual(h.events.at(-1).args[0],{deployment_override:{deployment_target:target,artifact_node_id:'image',plan_snapshot:utils.snapshotKubernetesPlan(props.plan)}})
 props.disabled=true;h.state.restore();assert.equal(h.events.length,1)
})
test('固定方案目标覆盖仅提交可配置身份，不夹带数据库UUID和归属',()=>{
 const plan=base();plan.deployment_target={...target,id:'old-target',row_id:7,project_id:'project',is_active:true}
 const current=utils.effectiveKubernetesPlan(plan);current.deployment_target.runtime_id='another-cluster'
 const patch=utils.stageKubernetesOverride(plan,current)
 assert.deepEqual(patch.deployment_target,{...target,runtime_id:'another-cluster'})
})
test('清除上游任务后显式保留自动匹配，不恢复方案的原构建绑定',()=>{
 const plan=base();plan.execution_config.artifact_build_plan_id='plan-A'
 const current=utils.effectiveKubernetesPlan(plan,{artifact_node_id:'task-B'});current.execution_config.artifact_node_id=undefined
 const patch=utils.stageKubernetesOverride(plan,current)
 assert.equal(patch.artifact_node_id,'')
 const reloaded=utils.effectiveKubernetesPlan(plan,patch)
 assert.equal(reloaded.execution_config.artifact_build_plan_id,undefined);assert.equal(reloaded.execution_config.artifact_node_id,undefined)
})
test('恢复网络覆盖需要网络权限，保持工作负载配置可独立修改',t=>{
 const props=vue.reactive({node:{id:'stage',name:'发布',type:'deploy',config:{deployment_plan_id:'shared',deployment_override:{deployment_target:target,kubernetes:{node_port:true}}}},plan:base(),disabled:false,buildNodes:[]})
 const h=componentHarness('../src/components/KubernetesPlanStageFields.vue',{props,codes:['workflow.update','kubernetes.workload.update'],modules:{'@/utils/kubernetesPlanOverride':utils}});t.after(h.stop)
 assert.equal(h.state.canRestore.value,false);h.state.restore();assert.deepEqual(h.events,[])
})
test('高级配置关闭探针产生删除补丁，无权限事件不发出',t=>{
 const props=vue.reactive({modelValue:defaults(),disabled:false})
 const h=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props,codes:['kubernetes.workload.update']});t.after(h.stop)
 h.state.toggleProbe('readiness_probe',true);props.modelValue=h.events.at(-1).args[0]
 assert.equal(props.modelValue.readiness_probe.port,8080)
 const plan=base();plan.execution_config.kubernetes=JSON.parse(JSON.stringify(props.modelValue))
 h.state.toggleProbe('readiness_probe',false)
 const current={...plan,execution_config:{kubernetes:h.events.at(-1).args[0]}}
 assert.equal(utils.stageKubernetesOverride(plan,current).kubernetes.readiness_probe,null)
 const denied=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props:{modelValue:defaults(),disabled:false}});t.after(denied.stop)
 denied.state.toggleProbe('startup_probe',true);denied.state.update({command:['/server']});assert.deepEqual(denied.events,[])
})
test('高级配置在保存前拒绝非法工作目录、探针路径和不可完成的发布策略',()=>{
 const runtime=loadFrontend('../src/utils/kubernetesDeployment.ts').kubernetesDeploymentRuntimeError
 assert.match(runtime({...defaults(),working_dir:'/app/../private'}),/工作目录/)
 assert.match(runtime({...defaults(),strategy:'RollingUpdate',max_surge:'0',max_unavailable:'0'}),/同时为零/)
 assert.match(runtime({...defaults(),readiness_probe:{kind:'http',port:8080,path:'health'}}),/路径/)
 assert.equal(runtime({...defaults(),command:['/server'],args:['--port','8080'],strategy:'RollingUpdate',max_surge:'1',max_unavailable:'0',readiness_probe:{kind:'http',port:8080,path:'/ready'}}),'')
})

test('阶段独立覆盖仓库镜像和 Tag，重开和恢复资源默认保留准确镜像绑定',t=>{
 const plan=base();Object.assign(plan.execution_config,{deployment_registry_id:'shared-registry',deployment_input_version:1,deployment_inputs:[{kind:'registry',registry_id:'shared-registry',image:'api',tag:'v1'}]})
 const stage=utils.effectiveKubernetesPlan(plan);Object.assign(stage.execution_config,{deployment_registry_id:'stage-registry',deployment_input_version:1,deployment_inputs:[{kind:'registry',registry_id:'stage-registry',image:'stage-api',tag:'${EDO_IMAGE_TAG}'}]})
 const override=utils.stageKubernetesOverride(plan,stage)
 assert.equal(override.deployment_registry_id,'stage-registry')
 assert.equal(override.deployment_inputs[0].image,'stage-api')
 const reopened=utils.effectiveKubernetesPlan(plan,override)
 assert.equal(reopened.execution_config.deployment_registry_id,'stage-registry')
 assert.equal(plan.execution_config.deployment_registry_id,'shared-registry')
 const props=vue.reactive({node:{id:'stage',type:'deploy',name:'部署',config:{deployment_plan_id:'shared',deployment_override:{...override,kubernetes:{replicas:3}}}},plan,disabled:false,buildNodes:[]})
 const h=componentHarness('../src/components/KubernetesPlanStageFields.vue',{props,codes:['workflow.update','kubernetes.workload.update'],modules:{'@/utils/kubernetesPlanOverride':utils}});t.after(h.stop)
 h.state.restore();assert.deepEqual(h.events.at(-1).args[0].deployment_override,override)
})

test('环境挂载与调度方案默认按阶段差异覆盖，清空数组和删除标签保留语义',()=>{
 const plan=base();plan.execution_config.kubernetes.options={labels:{team:'default'},environment:[{name:'APP_ENV',kind:'value',value:'base'}],volumes:[{name:'data',kind:'pvc',source_name:'app-data',mount_path:'/data'}],node_selector:{'kubernetes.io/os':'linux'}}
 const stage=utils.effectiveKubernetesPlan(plan,{kubernetes:{options:{labels:{team:null},environment:[],volumes:[],min_ready_seconds:0}}})
 assert.equal(stage.execution_config.kubernetes.options.labels.team,undefined)
 assert.deepEqual(stage.execution_config.kubernetes.options.environment,[])
 assert.deepEqual(stage.execution_config.kubernetes.options.volumes,[])
 assert.deepEqual(stage.execution_config.kubernetes.options.node_selector,{'kubernetes.io/os':'linux'})
 const diff=utils.stageKubernetesOverride(plan,stage)
 assert.equal(diff.kubernetes.options.labels.team,null)
 assert.equal(diff.kubernetes.options.min_ready_seconds,0)
 assert.equal(plan.execution_config.kubernetes.options.environment[0].value,'base')
})

test('Service类型和DNS按阶段差异保存，保留旧bool与端口草稿，不反写共享方案',()=>{
 const plan=base();delete plan.execution_config.kubernetes.service_type;plan.execution_config.kubernetes.node_port=true
 const original=JSON.stringify(plan)
 const legacyOverride=utils.effectiveKubernetesPlan(plan,{kubernetes:{node_port:false}})
 assert.equal(legacyOverride.execution_config.kubernetes.node_port,false)
 assert.equal(legacyOverride.execution_config.kubernetes.service_type,undefined)
 assert.deepEqual(utils.stageKubernetesOverride(plan,legacyOverride).kubernetes,{node_port:false})
 const current=utils.effectiveKubernetesPlan(plan,{kubernetes:{service_type:'ExternalName',external_name:'api.example.com'}})
 assert.equal(current.execution_config.kubernetes.service_type,'ExternalName')
 assert.equal(current.execution_config.kubernetes.node_port,true)
 assert.deepEqual(current.execution_config.kubernetes.ports,plan.execution_config.kubernetes.ports)
 const diff=utils.stageKubernetesOverride(plan,current)
 assert.deepEqual(diff.kubernetes,{service_type:'ExternalName',external_name:'api.example.com'})
 const reopened=utils.effectiveKubernetesPlan(plan,diff)
 assert.equal(reopened.execution_config.kubernetes.external_name,'api.example.com')
 assert.equal(JSON.stringify(plan),original)
 const clusterIP=utils.effectiveKubernetesPlan(plan,{kubernetes:{service_type:'ClusterIP',external_name:'saved.example.com'}})
 assert.equal(clusterIP.execution_config.kubernetes.node_port,true,'旧bool不应覆盖显式类型，也不应在打开时被重写')
 assert.equal(clusterIP.execution_config.kubernetes.service_type,'ClusterIP')
 const cleared=utils.effectiveKubernetesPlan(current,{kubernetes:{external_name:null,service_type:'LoadBalancer'}})
 const clearedDiff=utils.stageKubernetesOverride(current,cleared)
 assert.equal(clearedDiff.kubernetes.external_name,null)
 assert.equal(clearedDiff.kubernetes.service_type,'LoadBalancer')
})

test('恢复Service类型或DNS覆盖必须有网络权限，恢复后保留目标及镜像绑定',t=>{
 for(const network of [{service_type:'LoadBalancer'},{external_name:'changed.example.com'},{service_type:'ClusterIP',external_name:null}]){
  const props=vue.reactive({node:{id:'stage',type:'deploy',name:'部署',config:{deployment_plan_id:'shared',deployment_override:{deployment_target:target,deployment_registry_id:'registry',deployment_input_version:1,deployment_inputs:[{kind:'registry',registry_id:'registry',image:'api',tag:'v1'}],kubernetes:network,timeout_seconds:900}}},plan:base(),disabled:false,buildNodes:[]})
  const denied=componentHarness('../src/components/KubernetesPlanStageFields.vue',{props,codes:['workflow.update','kubernetes.workload.update'],modules:{'@/utils/kubernetesPlanOverride':utils}});t.after(denied.stop)
  assert.equal(denied.state.canRestore.value,false);denied.state.restore();assert.deepEqual(denied.events,[])
  const allowed=componentHarness('../src/components/KubernetesPlanStageFields.vue',{props,codes:['workflow.update','kubernetes.workload.update','kubernetes.network.update'],modules:{'@/utils/kubernetesPlanOverride':utils}});t.after(allowed.stop)
  allowed.state.restore()
  const restored=allowed.events.at(-1).args[0].deployment_override
  assert.equal(restored.kubernetes,undefined);assert.equal(restored.timeout_seconds,undefined)
  assert.deepEqual(restored.deployment_target,target)
  assert.deepEqual(restored.deployment_inputs,[{kind:'registry',registry_id:'registry',image:'api',tag:'v1'}])
  assert.equal(restored.deployment_registry_id,'registry')
 }
})

test('方案库详情真实模板以只读组件展示准确网络快照，旧bool配置不被补写',async()=>{
 const source=readFileSync(new URL('../src/views/DeploymentPlanView.vue',import.meta.url),'utf8')
 const start=source.indexOf('<ResourceDetailDrawer '),end=source.indexOf('</ResourceDetailDrawer>',start)
 assert.ok(start>0&&end>start)
 const template=source.slice(start,end+'</ResourceDetailDrawer>'.length)
 const bindings=compileScript(parse(source).descriptor,{id:'plan-network-detail'}).bindings
 const result=compileTemplate({source:template,filename:'DeploymentPlanView.vue',id:'plan-network-detail',compilerOptions:{bindingMetadata:bindings}})
 assert.deepEqual(result.errors,[])
 const code=ts.transpileModule(result.code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const module={exports:{}};new Function('require','module','exports',code)(()=>vue,module,module.exports)
 const slot=vue.defineComponent({inheritAttrs:false,setup:(_,context)=>()=>vue.h('section',context.slots.default?.())})
 for(const network of [{service_type:'ExternalName',external_name:'api.example.com',node_port:false},{service_type:'LoadBalancer',node_port:false},{node_port:true}]){
  const plan=base();Object.assign(plan.execution_config.kubernetes,network)
  if(!Object.hasOwn(network,'service_type'))delete plan.execution_config.kubernetes.service_type
  const original=JSON.stringify(plan),shown=[]
  const readOnly=vue.defineComponent({props:{modelValue:Object,disabled:Boolean},setup:props=>{shown.push({model:props.modelValue,disabled:props.disabled});return()=>vue.h('div',{'data-network-preview':props.modelValue.service_type||'legacy'})}})
  const state=vue.proxyRefs({detailPlan:vue.ref(plan),detailOpen:vue.ref(true),canUpdate:vue.ref(false),t:key=>key,kindName:()=> 'Kubernetes',environmentName:()=> '',destinationName:()=> '',updatedTime:()=> '',deploymentConfiguration:()=>({summary:'通用部署方案'}),ResourceDetailDrawer:slot,KubernetesDeploymentTemplateFields:readOnly,DeploymentExecutionFields:slot,YamlCodeEditor:slot})
  const app=vue.createSSRApp({render:()=>module.exports.render({},[],{},state,{}, {})})
  for(const name of ['a-form','a-tag','a-button'])app.component(name,slot)
  const html=await renderToString(app)
  assert.equal(shown.length,1)
  assert.equal(shown[0].disabled,true)
  assert.equal(shown[0].model.external_name,network.external_name)
  assert.equal(shown[0].model.node_port,network.node_port)
  assert.ok(html.includes('data-network-preview'))
  assert.equal(JSON.stringify(plan),original)
 }
})
