import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {componentHarness,loadFrontend} from './acceptanceHarness.mjs'

test('资源数值与单位分离，原有精度不回写，清空数字后保留单位',async t=>{
 for(const [resource,value,number,unit] of [['cpu','200m','200','m'],['cpu','1','1',''],['memory','300Mi','300','Mi'],['memory','1Gi','1','Gi'],['memory','0.123456789012345678Gi','0.123456789012345678','Gi']]){
  const props=vue.reactive({resource,value,label:'资源',disabled:false})
  const h=componentHarness('../src/components/KubernetesResourceQuantityInput.vue',{props});t.after(h.stop)
  assert.equal(h.state.quantity.value.amount,number);assert.equal(h.state.selectedUnit.value,unit);assert.deepEqual(h.events,[])
  h.state.amount(null);props.value=h.events.at(-1).args[0];await vue.nextTick()
  h.state.amount('400');assert.equal(h.events.at(-1).args[0],'400'+unit)
  props.disabled=true;const count=h.events.length;h.state.amount('5');h.state.unit('Gi');assert.equal(h.events.length,count)
 }
})
test('资源单位可独立选择并序列化为 Kubernetes 数量，旧格式不被换算',async t=>{
 const props=vue.reactive({resource:'memory',value:'300Mi',label:'内存申请',disabled:false})
 const h=componentHarness('../src/components/KubernetesResourceQuantityInput.vue',{props});t.after(h.stop)
 h.state.unit('Gi');assert.equal(h.events.at(-1).args[0],'300Gi')
 props.value='512Ki';await vue.nextTick()
 assert.equal(h.state.selectedUnit.value,'Ki');assert.equal(h.state.quantity.value.amount,'512')
 assert.equal(h.state.units.value.find(v=>v.value==='Ki').label,'KiB')
 assert.equal(h.events.length,1)
})
test('HPA 默认关闭，可配置资源目标并保存阶段快照，清除 CPU 后不被补回',async t=>{
 const utils=loadFrontend('../src/utils/kubernetesDeployment.ts'),overrides=loadFrontend('../src/utils/kubernetesPlanOverride.ts')
 const base={name:'通用方案',kind:'kubernetes',timeout_seconds:600,execution_config:{kubernetes:utils.defaultKubernetesDeployment()}}
 const props=vue.reactive({modelValue:JSON.parse(JSON.stringify(base.execution_config.kubernetes)),disabled:false})
 const h=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props,codes:['kubernetes.workload.update']});t.after(h.stop)
 assert.equal(props.modelValue.hpa,undefined)
 h.state.hpa({enabled:true});props.modelValue=h.events.at(-1).args[0]
 assert.deepEqual(props.modelValue.hpa,{enabled:true,min_replicas:1,max_replicas:3,cpu_utilization:80})
 h.state.hpa({cpu_utilization:undefined,memory_utilization:70});props.modelValue=JSON.parse(JSON.stringify(h.events.at(-1).args[0]))
 h.state.hpa({max_replicas:6});props.modelValue=h.events.at(-1).args[0]
 assert.equal(props.modelValue.hpa.cpu_utilization,undefined)
 const patch=overrides.stageKubernetesOverride(base,{...base,execution_config:{kubernetes:props.modelValue}})
 const reopened=overrides.effectiveKubernetesPlan(base,JSON.parse(JSON.stringify(patch)))
 assert.equal(reopened.execution_config.kubernetes.hpa.memory_utilization,70);assert.equal(base.execution_config.kubernetes.hpa,undefined)
 props.disabled=true;const count=h.events.length;h.state.hpa({enabled:false});assert.equal(h.events.length,count)
 const denied=componentHarness('../src/components/KubernetesDeploymentTemplateFields.vue',{props:{modelValue:props.modelValue,disabled:false},codes:['workflow.update']});t.after(denied.stop)
 denied.state.hpa({enabled:false});assert.deepEqual(denied.events,[])
})
test('HPA 副本范围、至少一项指标及利用率上下限均校验',()=>{
 const utils=loadFrontend('../src/utils/kubernetesDeployment.ts'),config=utils.defaultKubernetesDeployment()
 for(const hpa of [{enabled:true,min_replicas:4,max_replicas:3,cpu_utilization:80},{enabled:true,min_replicas:1,max_replicas:3},{enabled:true,min_replicas:1,max_replicas:3,cpu_utilization:0},{enabled:true,min_replicas:1,max_replicas:3,memory_utilization:1001}])assert.match(utils.kubernetesDeploymentRuntimeError({...config,hpa}),/HPA/)
 assert.equal(utils.kubernetesDeploymentRuntimeError({...config,hpa:{enabled:true,min_replicas:2,max_replicas:8,memory_utilization:200}}),'')
})
