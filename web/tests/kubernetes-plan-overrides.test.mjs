import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {loadFrontend,componentHarness} from './acceptanceHarness.mjs'
const utils=loadFrontend('../src/utils/kubernetesPlanOverride.ts')
const defaults=loadFrontend('../src/utils/kubernetesDeployment.ts').defaultKubernetesDeployment
const base=()=>({id:'shared',name:'通用 K8s',kind:'kubernetes',is_active:true,timeout_seconds:600,execution_config:{kubernetes:defaults()}})
const target={platform:'kubernetes',environment_id:'',runtime_id:'cluster',namespace:'order-api',workload_name:'api',container_name:'api'}
test('通用方案只保存默认规则，阶段只存差异，后续方案更新仍继承未覆盖字段',()=>{
 const plan=base(),stage=utils.effectiveKubernetesPlan(plan)
 stage.deployment_target=target;stage.execution_config.kubernetes.cpu_request='400m';stage.execution_config.kubernetes.ports[0].enabled=true;stage.timeout_seconds=900;stage.execution_config.artifact_node_id='build-task'
 const override=utils.stageKubernetesOverride(plan,stage)
 assert.deepEqual(Object.keys(override.kubernetes).sort(),['cpu_request','ports'])
 assert.equal(override.timeout_seconds,900);assert.equal(override.artifact_node_id,'build-task');assert.deepEqual(override.deployment_target,target)
 assert.equal(plan.execution_config.kubernetes.cpu_request,'200m');assert.equal(plan.deployment_target,undefined)
 plan.execution_config.kubernetes.memory_request='350Mi';plan.execution_config.kubernetes.cpu_request='250m'
 const again=utils.effectiveKubernetesPlan(plan,override)
 assert.equal(again.execution_config.kubernetes.cpu_request,'400m');assert.equal(again.execution_config.kubernetes.memory_request,'350Mi')
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
 h.state.restore();assert.deepEqual(h.events.at(-1).args[0],{deployment_override:{deployment_target:target,artifact_node_id:'image'}})
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
test('高级配置关闭探针产生删除补丁，模板锁定及无权限事件不发出',t=>{
 const props=vue.reactive({modelValue:defaults(),disabled:false,lockTemplate:true})
 const h=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props,codes:['kubernetes.workload.update']});t.after(h.stop)
 h.state.selectTemplate('existing');assert.deepEqual(h.events,[])
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
