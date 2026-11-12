import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import * as yaml from 'yaml'
import {loadFrontend} from './acceptanceHarness.mjs'
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve))}
function harness(t,post){
 const events=[],requests=[],timers=new Map(),unmounts=[]
 let timerID=0
 const auth=vue.reactive({user:{id:'actor',project_id:'project',permissions:['workflow.update']}})
 const props=vue.reactive({node:{id:'deploy',config:{}},plan:{kind:'compose_stack',compose_yaml:'services:\n  api:\n    image: ${EDO_IMAGE_TAG}\n  web:\n    image: ${EDO_IMAGE_TAG}\n'},buildNodes:[{id:'api-build',name:'API 构建',config:{inline_build:{kind:'dockerfile'}}},{id:'web-build',name:'Web 构建',config:{build_plan_id:'web-plan'}},{id:'file-build',name:'文件构建',config:{inline_build:{kind:'script'}}}],buildPlans:[{id:'web-plan',kind:'dockerfile'}],disabled:false,applicationID:'app'})
 const component=loadFrontend('../src/components/PipelineImageBuildFields.vue',{
  vue:{...vue,onBeforeUnmount:fn=>unmounts.push(fn)},yaml,'@/stores/auth':{useAuthStore:()=>auth},'@/api/resources':{apiErrorMessage:()=> '读取失败'},'@/api/client':{__esModule:true,default:{post:async(path,body,options)=>{requests.push({path,body,options});return post?post():{data:{services:['api','web']}}}}}
 },{setTimeout:fn=>{const id=++timerID;timers.set(id,fn);return id},clearTimeout:id=>timers.delete(id)})
 const scope=vue.effectScope(),state=scope.run(()=>component.default.setup(props,{expose(){},emit:(name,patch)=>{events.push(patch);Object.assign(props.node.config,patch)}}))
 t.after(()=>{unmounts.forEach(fn=>fn());scope.stop()})
 return {state,props,auth,events,requests,async flush(){const pending=[...timers.values()];timers.clear();pending.forEach(fn=>fn());await settle()}}
}
test('多镜像部署显式选择来源，不自动取第一条；服务分别关联并保留已保存值',async t=>{
 const h=harness(t);await h.flush()
 assert.deepEqual(h.state.services.value,['api','web']);assert.deepEqual(h.state.options.value.map(v=>v.value),['api-build','web-build']);assert.equal(h.events.length,0)
 h.state.change({image_build_node_id:'api-build',compose_build_bindings:[{service:'api',build_node_id:'api-build'},{service:'web',build_node_id:'web-build'}]})
 h.props.node={id:'deploy',config:JSON.parse(JSON.stringify(h.props.node.config))};await settle()
 assert.equal(h.props.node.config.image_build_node_id,'api-build');assert.equal(h.state.bindings.value[1].build_node_id,'web-build')
 h.props.buildNodes=[];await settle();assert.match(h.state.options.value[0].label,/不可用/);assert.equal(h.props.node.config.image_build_node_id,'api-build')
 h.props.disabled=true;h.state.change({image_build_node_id:undefined});assert.equal(h.props.node.config.image_build_node_id,'api-build')
})
test('仓库与配置来源解析服务；切任务或项目时取消并丢弃迟到结果',async t=>{
 let resolve;const pending=new Promise(yes=>resolve=yes),h=harness(t,()=>pending)
 h.props.node.config.compose_source={kind:'repository',path:'compose.yaml'};await settle();await h.flush()
 assert.equal(h.requests[0].body.source.path,'compose.yaml')
 h.auth.user.project_id='another';await settle();resolve({data:{services:['stale']}});await settle()
 assert.equal(h.requests[0].options.signal.aborted,true);assert.deepEqual(h.state.services.value,[])
 h.props.node.config.compose_source={kind:'upstream',path:'compose.yaml'};await settle();await h.flush();assert.equal(h.requests.length,1)
 h.state.change({compose_build_bindings:[{service:'worker',build_node_id:'api-build'}]});assert.equal(h.state.bindings.value[0].service,'worker')
})
