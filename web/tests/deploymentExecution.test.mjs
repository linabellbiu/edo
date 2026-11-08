import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {loadFrontend} from './acceptanceHarness.mjs'

const execution = loadFrontend('../src/utils/deploymentExecution.ts')
test('部署方案只接管执行参数，不带入通知、流水线或目标身份',()=>{
 const task={deployment_plan_id:'plan',deployment_source:'plan',notifications:[{id:'notification'}],variable_names:['SETTING'],deployment_script:'printf ok',deployment_inputs:[{kind:'upstream',path:'output.tar.gz',artifact_node_id:'old-node'}]}
 const value=execution.deploymentExecution(task,[{id:'old-node',config:{build_plan_id:'reusable-build'}}])
 assert.deepEqual(value,{variable_names:['SETTING'],deployment_script:'printf ok',deployment_inputs:[{kind:'upstream',path:'output.tar.gz',artifact_build_plan_id:'reusable-build'}]})
 value.variable_names.push('ANOTHER');assert.deepEqual(task.variable_names,['SETTING'])
 assert.equal(task.deployment_inputs[0].artifact_node_id,'old-node')
 const clear=execution.clearTaskDeploymentExecution()
 assert.ok(Object.hasOwn(clear,'deployment_script'));assert.equal(clear.deployment_script,undefined)
 assert.ok(!Object.hasOwn(clear,'notifications'));assert.ok(!Object.hasOwn(clear,'deployment_plan_id'))
})

test('部署方案字段使用方案权限，撤权或跨项目旧回调不能更新',async t=>{
 const auth=vue.reactive({user:{id:'user',project_id:'project',permissions:['deployment_plan.update']},canAny(codes){return codes.some(code=>this.user.permissions.includes(code))}})
 const storage={project:'project'},events=[],unmounts=[]
 const props=vue.reactive({modelValue:{variable_names:['ORIGINAL']},plan:{name:'方案',kind:'docker',timeout_seconds:600},disabled:false,editPermission:'deployment_plan.update'})
 const component=loadFrontend('../src/components/DeploymentExecutionFields.vue',{
  vue:{...vue,onBeforeUnmount:fn=>unmounts.push(fn)},'@/stores/auth':{useAuthStore:()=>auth},'@/api/client':{__esModule:true,default:{get(){assert.fail('没有资源读取权限时不应请求资源')}}},'@/api/resources':{apiErrorMessage:()=> '读取失败'},
 },{localStorage:{getItem:()=>storage.project}}).default
 const scope=vue.effectScope(),state=scope.run(()=>component.setup(props,{expose(){},emit:(name,value)=>events.push({name,value})}))
 t.after(()=>{unmounts.forEach(fn=>fn());scope.stop()})
 await vue.nextTick()
 assert.equal(state.canEdit.value,true)
 state.change({deployment_script:'printf plan'});assert.equal(events.length,1);assert.deepEqual(props.modelValue,{variable_names:['ORIGINAL']})
 props.disabled=true;state.change({deployment_script:'forbidden'});assert.equal(events.length,1)
 props.disabled=false;auth.user.permissions=[];state.change({deployment_script:'forbidden'});assert.equal(events.length,1)
 auth.user.permissions=['deployment_plan.update'];storage.project='another';state.change({deployment_script:'forbidden'});assert.equal(events.length,1)
 state.updateTimeout(900);state.updateHealthURL('https://forbidden.test');assert.equal(events.length,1)
 storage.project='project';auth.user={...auth.user};props.modelValue={variable_names:['ORIGINAL'],compose:{project_name:'saved-project',tag_variable:'${EDO_IMAGE_TAG}',services:['api'],use_sudo:false,adopt_existing:false}}
 state.updateHealthURL('https://example.test/ready')
 assert.deepEqual(events.at(-1).value,{variable_names:['ORIGINAL'],compose:{...props.modelValue.compose,health_url:'https://example.test/ready'}})
 assert.equal(props.modelValue.compose.health_url,undefined)
 state.updateTimeout(900);assert.deepEqual(events.at(-1),{name:'update:timeoutSeconds',value:900})
 props.disabled=true;const count=events.length
 state.updateTimeout(1200);state.updateHealthURL('https://forbidden.test');assert.equal(events.length,count)
})

test('旧文件明确绑定重复构建中的一个任务时保留原关联，不改成歧义方案来源',()=>{
 const value=execution.deploymentExecution({deployment_inputs:[{kind:'upstream',path:'output',artifact_node_id:'selected'}]},[{id:'selected',config:{build_plan_id:'shared'}},{id:'another',config:{build_plan_id:'shared'}}])
 assert.deepEqual(value.deployment_inputs,[{kind:'upstream',path:'output',artifact_node_id:'selected'}])
})
