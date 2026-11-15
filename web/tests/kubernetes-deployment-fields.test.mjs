import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {componentHarness} from './acceptanceHarness.mjs'
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
function harness({codes=['workflow.read','workflow.update','cluster.read'],selected='',kind='kubernetes'}={}){
 const lookups=[]
 const props=vue.reactive({node:{id:'deploy',type:'deploy',name:'部署',config:{inline_deployment_plan:{name:'部署',kind,timeout_seconds:600,deployment_target:{platform:kind==='kubernetes'?'kubernetes':'ssh',environment_id:'',runtime_id:selected}}}},disabled:false,buildNodes:[],buildPlans:[],buildTaskLabels:{}})
 const h=componentHarness('../src/components/PipelineDeployFields.vue',{props,codes,api:{get:async path=>({data:{clusters:path==='/kubernetes/clusters'?[{id:'ready',name:'可用集群',is_active:true},{id:'inactive',name:'已停用集群',is_active:false}]:[]}})},modules:{'@/api/infrastructure':{listEnvironments:async()=>{lookups.push('groups');return []},listHosts:async()=>{lookups.push('hosts');return []},environmentIDsOf:host=>host.environment_ids||[]}},globals:{localStorage:{getItem:()=>null},setTimeout:()=>1,clearTimeout(){}}})
 return {...h,props,lookups}
}
test('Kubernetes直接读取集群，不查询主机组或主机，不生成虚构的执行主机',async t=>{
 const h=harness();t.after(h.stop);await settle()
 assert.deepEqual(h.lookups,[])
 assert.deepEqual(h.requests.map(row=>row.path),['/kubernetes/clusters'])
 assert.deepEqual(h.state.clusterOptions.value,[{value:'ready',label:'可用集群',disabled:false}])
 assert.deepEqual(h.events,[])
 h.state.target({runtime_id:'ready',environment_id:'',host_ids:undefined,host_id:undefined})
 const target=h.events.at(-1).args[0].inline_deployment_plan.deployment_target
 assert.equal(target.environment_id,'');assert.equal(target.runtime_id,'ready');assert.equal(target.host_id,undefined)
})
test('已停用或不可读的原集群保留禁用名称，无权限不查询，切换普通部署仍加载主机组',async t=>{
 const h=harness({selected:'inactive'});t.after(h.stop);await settle()
 assert.deepEqual(h.state.clusterOptions.value.find(row=>row.value==='inactive'),{value:'inactive',label:'已停用集群',disabled:true})
 assert.deepEqual(h.events,[])
 const denied=harness({codes:['workflow.read','workflow.update'],selected:'old'});t.after(denied.stop);await settle()
 assert.deepEqual(denied.requests,[]);assert.match(denied.state.error.value,/集群查看权限/)
 assert.deepEqual(denied.state.clusterOptions.value,[{value:'old',label:'原集群不可用，请重新选择',disabled:true}])
 h.props.node.config.inline_deployment_plan.kind='script';await settle()
 assert.deepEqual(h.lookups,['groups','hosts'])
})
test('显式仓库镜像决定容器补空值，不从前序构建推断部署仓库',async t=>{
 const h=harness();t.after(h.stop);await settle()
 h.props.applicationName='order-api'
 h.props.node.config.inline_deployment_plan.execution_config={deployment_registry_id:'selected',deployment_input_version:1,deployment_inputs:[{kind:'registry',registry_id:'selected',image:'services/payment-api',tag:'v2'}]}
 await settle()
 const plan=h.events.at(-1).args[0].inline_deployment_plan
 assert.equal(plan.deployment_target.container_name,'payment-api');assert.equal(plan.execution_config.deployment_registry_id,'selected')
 assert.equal(plan.execution_config.artifact_node_id,undefined)
})
test('命名空间与Deployment默认应用名，容器名来自选定镜像；不覆盖手动输入',async t=>{
 const h=harness();t.after(h.stop);await settle()
 h.props.applicationName='order_api'
 h.props.buildNodes=[{id:'image',name:'构建',type:'build',config:{image_name:'services/order_backend',inline_build:{kind:'dockerfile',image_registry_id:'registry'}}}]
 await settle()
 const target=h.events.at(-1).args[0].inline_deployment_plan.deployment_target
 assert.equal(target.namespace,'order-api');assert.equal(target.workload_name,'order-api');assert.equal(target.container_name,'order-backend')
 h.props.node.config.inline_deployment_plan.deployment_target={...target,namespace:'manual',container_name:'manual-api'}
 h.props.buildNodes[0].config.image_name='changed';await settle()
 assert.equal(h.props.node.config.inline_deployment_plan.deployment_target.namespace,'manual');assert.equal(h.props.node.config.inline_deployment_plan.deployment_target.container_name,'manual-api')
})
test('标准模板使用约定资源，默认端口全部关闭；无RBAC不能通过事件修改网络',async t=>{
 const {loadFrontend}=await import('./acceptanceHarness.mjs')
 const defaults=loadFrontend('../src/utils/kubernetesDeployment.ts').defaultKubernetesDeployment()
 assert.deepEqual([defaults.replicas,defaults.cpu_request,defaults.memory_request,defaults.cpu_limit,defaults.memory_limit],[1,'200m','300Mi','1','1Gi'])
 assert.equal(defaults.node_port,false);assert.deepEqual(defaults.ports.map(p=>[p.enabled,p.port,p.target_port]),[[false,8080,8080],[false,8888,8888]])
 const h=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props:{modelValue:defaults,disabled:false}});t.after(h.stop)
 assert.equal(h.state.resourceDisabled.value,true);assert.equal(h.state.networkDisabled.value,true)
 h.state.port(0,{enabled:true});assert.deepEqual(h.events,[])
})
test('显式镜像修改不覆盖用户手填的容器名称',async t=>{
 const h=harness();t.after(h.stop);await settle()
 h.props.applicationName='orders'
 h.props.node.config.inline_deployment_plan.deployment_target.container_name='manual-api'
 h.props.node.config.inline_deployment_plan.execution_config={deployment_registry_id:'selected',deployment_input_version:1,deployment_inputs:[{kind:'registry',registry_id:'selected',image:'api',tag:'v1'}]}
 await settle()
 assert.equal(h.props.node.config.inline_deployment_plan.deployment_target.container_name,'manual-api')
 assert.ok(h.events.every(event=>event.args[0].inline_deployment_plan.deployment_target.container_name==='manual-api'))
})
