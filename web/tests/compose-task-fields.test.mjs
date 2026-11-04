import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import * as yaml from 'yaml'
import {loadFrontend} from './acceptanceHarness.mjs'
const yamlModule=loadFrontend('../src/utils/kubernetesYaml.ts',{yaml})
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve))}
function harness(get){
 const events=[],unmounts=[],requests=[]
 const auth=vue.reactive({user:{id:'operator',project_id:'one',permissions:['deployment_plan.read','workflow.update']},canAny(codes){return codes.some(code=>this.user.permissions.includes(code))}})
 const props=vue.reactive({node:{id:'deploy',type:'deploy',config:{deployment_plan_id:'plan',compose:{project_name:'team',tag_variable:'VERSION',use_sudo:false,adopt_existing:true}}},buildNodes:[{id:'build',name:'API'}],disabled:false})
 const component=loadFrontend('../src/components/ComposeTaskFields.vue',{
  vue:{...vue,onBeforeUnmount:fn=>unmounts.push(fn)},'@/stores/auth':{useAuthStore:()=>auth},'@/utils/kubernetesYaml':yamlModule,
  '@/api/resources':{apiErrorMessage:()=> '读取失败'},'@/api/client':{__esModule:true,default:{get:async(path,options)=>{requests.push({path,options});return get?get():{data:{deployment_plans:[{id:'plan',kind:'compose_stack',compose_yaml:'services:\n  api:\n    image: registry.example.com/api:${VERSION}\n  jobs:\n    image: registry.example.com/api:${VERSION}\n'}]}}}}}
 }).default
 const scope=vue.effectScope(),state=scope.run(()=>component.setup(props,{expose(){},emit:(name,patch)=>{events.push({name,patch});Object.assign(props.node.config,patch)}}))
 return {state,events,props,auth,requests,stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}
test('多镜像任务从 EDO 方案读取服务，共用上游构建且同步完整制品来源',async t=>{
 const h=harness();t.after(h.stop);await settle();assert.equal(h.state.kind.value,'compose_stack')
 h.state.readServices();h.state.updateBinding(0,{artifact_node_id:'build'});h.state.updateBinding(1,{artifact_node_id:'build'})
 assert.deepEqual(h.props.node.config.artifact_node_ids,['build']);assert.equal(h.props.node.config.artifact_node_id,'build')
 assert.equal(h.props.node.config.compose_images.length,2);assert.equal(h.props.node.config.compose.tag_variable,'VERSION')
 h.state.bindings([]);assert.deepEqual(h.props.node.config.artifact_node_ids,[]);assert.equal(h.props.node.config.artifact_node_id,'')
})
test('切换项目中止旧方案读取，禁用或撤权后不更新配置',async t=>{
 let resolve;const pending=new Promise(yes=>{resolve=yes});const h=harness(()=>pending);t.after(h.stop)
 h.auth.user.project_id='two';await settle();assert.equal(h.requests[0].options.signal.aborted,true)
 h.props.disabled=true;h.state.updateSettings({project_name:'forbidden'});assert.deepEqual(h.events,[])
 h.props.disabled=false;h.auth.user.permissions=[];h.state.updateSettings({project_name:'forbidden'});await settle()
 resolve({data:{deployment_plans:[{id:'plan',kind:'compose_stack'}]}});await settle();assert.equal(h.state.kind.value,'');assert.deepEqual(h.events,[])
})
