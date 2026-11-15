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
 assert.equal(defaults.node_port,false);assert.deepEqual(defaults.ports.map(p=>[p.enabled,p.port,p.target_port]),[[false,8080,8080]])
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

test('端口可按用户配置增删，清空后重建，网络撤权时事件均被阻止',async t=>{
 const {loadFrontend}=await import('./acceptanceHarness.mjs')
 const props=vue.reactive({modelValue:loadFrontend('../src/utils/kubernetesDeployment.ts').defaultKubernetesDeployment(),disabled:false})
 const h=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props,codes:['kubernetes.workload.update','kubernetes.network.update']});t.after(h.stop)
 h.state.addPort();props.modelValue=h.events.at(-1).args[0]
 assert.equal(props.modelValue.ports.length,2)
 h.state.port(1,{name:'dns',protocol:'UDP',port:53,target_port:53});props.modelValue=h.events.at(-1).args[0]
 assert.equal(props.modelValue.ports[1].protocol,'UDP')
 h.state.removePort(0);props.modelValue=h.events.at(-1).args[0];assert.equal(props.modelValue.ports[0].name,'dns')
 h.state.removePort(0);props.modelValue=h.events.at(-1).args[0];assert.deepEqual(props.modelValue.ports,[])
 h.state.addPort();props.modelValue=h.events.at(-1).args[0];assert.equal(props.modelValue.ports.length,1)
 props.disabled=true;const count=h.events.length
 h.state.addPort();h.state.removePort(0);h.state.port(0,{port:443});assert.equal(h.events.length,count)
 const denied=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props:{modelValue:props.modelValue,disabled:false},codes:['kubernetes.workload.update']});t.after(denied.stop)
 denied.state.addPort();denied.state.removePort(0);denied.state.port(0,{port:443});assert.deepEqual(denied.events,[])
})
test('动态端口允许同号不同协议，拒绝重复名称、协议映射和节点端口',async()=>{
 const {loadFrontend}=await import('./acceptanceHarness.mjs'),utils=loadFrontend('../src/utils/kubernetesDeployment.ts')
 const cfg=utils.defaultKubernetesDeployment();cfg.ports=[{name:'dns',enabled:true,port:53,target_port:53,protocol:'UDP'},{name:'dns-tcp',enabled:true,port:53,target_port:53,protocol:'TCP'}]
 assert.equal(utils.kubernetesDeploymentPortError(cfg),'')
 cfg.ports[1].protocol='UDP';assert.match(utils.kubernetesDeploymentPortError(cfg),/协议重复/)
 cfg.ports[1].protocol='TCP';cfg.ports[1].name='dns';assert.match(utils.kubernetesDeploymentPortError(cfg),/名称重复/)
 cfg.ports[1].name='dns--tcp';assert.match(utils.kubernetesDeploymentPortError(cfg),/名称应为/)
 cfg.service_type='NodePort';cfg.ports[1].name='dns-tcp';cfg.ports[0].node_port=30053;cfg.ports[1].node_port=30053;assert.match(utils.kubernetesDeploymentPortError(cfg),/节点端口重复/)
 cfg.ports=[];assert.equal(utils.kubernetesDeploymentPortError(cfg),'')
})

test('服务类型显式值优先，旧NodePort布尔配置只解释不改写',async t=>{
 const {loadFrontend}=await import('./acceptanceHarness.mjs'),utils=loadFrontend('../src/utils/kubernetesDeployment.ts')
 assert.equal(utils.defaultKubernetesDeployment().service_type,'ClusterIP')
 const legacy={...utils.defaultKubernetesDeployment(),service_type:undefined,node_port:true}
 const original=JSON.stringify(legacy)
 assert.equal(utils.kubernetesServiceType(legacy),'NodePort')
 const props=vue.reactive({modelValue:legacy,disabled:false})
 const h=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props,codes:['kubernetes.workload.update','kubernetes.network.update']});t.after(h.stop)
 await settle();assert.deepEqual(h.events,[]);assert.equal(JSON.stringify(legacy),original)
 assert.equal(h.state.serviceType.value,'NodePort');assert.equal(h.state.usesNodePort.value,true)
 for(const type of ['ClusterIP','LoadBalancer','ExternalName','NodePort']){
  h.state.changeServiceType(type);props.modelValue=h.events.at(-1).args[0];await settle()
  assert.equal(props.modelValue.service_type,type)
  assert.equal(props.modelValue.node_port,true)
  assert.equal(h.state.usesNodePort.value,['NodePort','LoadBalancer'].includes(type))
 }
 const denied=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props:{modelValue:legacy,disabled:false},codes:['kubernetes.workload.update']});t.after(denied.stop)
 denied.state.changeServiceType('LoadBalancer');denied.state.update({service_type:'ExternalName',external_name:'api.example.com'});assert.deepEqual(denied.events,[])
})

test('ExternalName要求DNS，隐藏端口草稿不执行且切回保留原配置',async t=>{
 const {loadFrontend}=await import('./acceptanceHarness.mjs'),utils=loadFrontend('../src/utils/kubernetesDeployment.ts')
 const props=vue.reactive({modelValue:{...utils.defaultKubernetesDeployment(),service_type:'NodePort',ports:[{name:'web',enabled:true,port:80,target_port:8080,node_port:30080}]},disabled:false})
 const h=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props,codes:['kubernetes.workload.update','kubernetes.network.update']});t.after(h.stop)
 const ports=JSON.stringify(props.modelValue.ports)
 h.state.changeServiceType('ExternalName');props.modelValue=h.events.at(-1).args[0];await settle()
 assert.match(utils.kubernetesDeploymentServiceError(props.modelValue),/DNS/)
 h.state.update({external_name:'api.example.com.'});props.modelValue=h.events.at(-1).args[0];await settle()
 assert.equal(utils.kubernetesDeploymentRuntimeError(props.modelValue),'')
 const count=h.events.length;h.state.addPort();h.state.removePort(0);h.state.port(0,{port:443});assert.equal(h.events.length,count)
 h.state.changeServiceType('ClusterIP');props.modelValue=h.events.at(-1).args[0];await settle()
 assert.equal(props.modelValue.external_name,'api.example.com.');assert.equal(JSON.stringify(props.modelValue.ports),ports)
 const unfinished={...props.modelValue,service_type:'ExternalName',ports:[{name:'',enabled:true,port:0,target_port:0}]}
 assert.equal(utils.kubernetesDeploymentRuntimeError(unfinished),'')
 unfinished.service_type='ClusterIP';assert.match(utils.kubernetesDeploymentRuntimeError(unfinished),/端口/)
 for(const name of ['api.example.com','api.example.com.','database'])assert.equal(utils.kubernetesDeploymentServiceError({...props.modelValue,service_type:'ExternalName',external_name:name}),'')
 for(const name of ['', 'API.example.com','192.0.2.1','192.0.2.1.','::1','https://api.example.com','api.example.com:443','api.example.com/path','api.example.com..',' api.example.com'])assert.match(utils.kubernetesDeploymentServiceError({...props.modelValue,service_type:'ExternalName',external_name:name}),/DNS/)
 assert.match(utils.kubernetesDeploymentServiceError({...props.modelValue,service_type:'ExternalName',external_name:'a'.repeat(255)}),/254/)
 assert.match(utils.kubernetesDeploymentServiceError({...props.modelValue,service_type:'ExternalName',external_name:'a'.repeat(64)+'.example.com'}),/DNS/)
 assert.equal(utils.kubernetesDeploymentServiceError({...props.modelValue,service_type:'ClusterIP',external_name:'unfinished://draft'}),'')
 assert.match(utils.kubernetesDeploymentServiceError({...props.modelValue,service_type:'ClusterIP',external_name:'域'.repeat(100)}),/254/)
 assert.match(utils.kubernetesDeploymentServiceError({...props.modelValue,service_type:'invalid'}),/服务类型/)
})

test('ClusterIP暂存节点端口不生效，NodePort与LoadBalancer均校验节点端口',async()=>{
 const {loadFrontend}=await import('./acceptanceHarness.mjs'),utils=loadFrontend('../src/utils/kubernetesDeployment.ts')
 const cfg={...utils.defaultKubernetesDeployment(),ports:[{name:'http',enabled:true,port:80,target_port:8080,node_port:80}]}
 assert.equal(utils.kubernetesDeploymentPortError(cfg),'')
 for(const type of ['NodePort','LoadBalancer']){
  cfg.service_type=type;assert.match(utils.kubernetesDeploymentPortError(cfg),/节点端口/)
  cfg.ports[0].node_port=30080;assert.equal(utils.kubernetesDeploymentPortError(cfg),'');cfg.ports[0].node_port=80
 }
 cfg.ports=[{name:'web',enabled:false,port:80,target_port:8080,node_port:30080},{name:'metrics',enabled:false,port:9090,target_port:9090,node_port:30080}]
 assert.match(utils.kubernetesDeploymentPortError(cfg),/节点端口重复/)
})
