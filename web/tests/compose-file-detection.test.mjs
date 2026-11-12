import assert from 'node:assert/strict'
import test from 'node:test'
import * as vue from 'vue'
import {componentHarness} from './acceptanceHarness.mjs'

const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
function harness({source={kind:'repository',path:''},disabled=false,codes=['workflow.read','workflow.update','repository.read'],post}={}){
 const props=vue.reactive({node:{id:'deploy',name:'Docker Compose',type:'deploy',config:{compose_source:source,inline_deployment_plan:{name:'Docker Compose',kind:'compose_stack',compose_yaml:'services:\n  api:\n    image: alpine\n'}}},applicationID:'app',branch:'main',disabled,buildNodes:[]})
 const auth=vue.reactive({user:{id:'operator',project_id:'one',permissions:[...codes]},permissions:new Set(codes),canAny(required){return required.some(code=>this.permissions.has(code))}})
 const closes=[]
 const h=componentHarness('../src/components/PipelineDeployFields.vue',{props,codes,api:{post:post||(()=>Promise.resolve({data:{path:'docker-compose.yaml',commit_sha:'a'.repeat(40)}}))},modules:{'@/stores/auth':{useAuthStore:()=>auth},vue:{...vue,onBeforeUnmount:callback=>closes.push(callback)},'@/api/infrastructure':{listEnvironments:async()=>[],listHosts:async()=>[],environmentIDsOf:host=>host.environment_ids||[]}},globals:{localStorage:{getItem:()=>null},setTimeout:()=>1,clearTimeout(){}}})
 return {...h,auth,props,close(){closes.forEach(callback=>callback());h.stop()},detections(){return h.requests.filter(request=>request.path.endsWith('/compose-file'))}}
}

test('空仓库路径自动填入检测到的真实文件名，只改任务草稿不自动保存或执行',async t=>{
 const h=harness();t.after(h.close);await settle()
 assert.equal(h.detections().length,1)
 assert.deepEqual(h.detections()[0].body,{ref:'main'})
 assert.deepEqual(h.events,[{name:'change',args:[{compose_source:{kind:'repository',path:'docker-compose.yaml'}}]}])
 assert.equal(h.requests.some(request=>/pipeline-runs|deployment-plans|\/workflows\//.test(request.path)),false)
})

test('已有路径保持原值；切换仓库来源先留空再检测，不猜测compose.yaml',async t=>{
 const existing=harness({source:{kind:'repository',path:'deploy/custom.yml'}});t.after(existing.close);await settle()
 assert.equal(existing.detections().length,0);assert.deepEqual(existing.events,[])
 const h=harness({source:undefined});t.after(h.close)
 h.props.node.config.compose_source=undefined;await settle()
 h.events.length=0
 h.state.source('repository')
 assert.deepEqual(h.events.at(-1).args,[{compose_source:{kind:'repository',path:''}}])
})

test('检测期间手动输入或清空立即取消，迟到结果不覆盖用户路径',async t=>{
 for(const value of ['deploy/custom.yaml','']){
  const pending=deferred(),h=harness({post:()=>pending.promise});t.after(h.close)
  h.state.repositoryPath(value)
  assert.equal(h.detections()[0].options.signal.aborted,true)
  pending.resolve({data:{path:'compose.yaml'}});await settle()
  assert.deepEqual(h.events,[{name:'change',args:[{compose_source:{kind:'repository',path:value}}]}])
  assert.equal(h.state.repositoryDetecting.value,false)
 }
})

test('切换项目、应用、任务、分支、来源及撤权后拒绝旧响应；关闭取消请求',async t=>{
 const changes=[
  h=>{h.auth.user.project_id='another-project'},
  h=>{h.props.applicationID='other-app'},
  h=>{h.props.node={...h.props.node,id:'another-task'}},
  h=>{h.props.branch='release'},
  h=>{h.props.node.config.compose_source={kind:'configuration',configuration_id:'config'}},
  h=>{h.props.disabled=true},
  h=>{h.auth.permissions.delete('workflow.update');h.auth.user.permissions=[]},
 ]
 for(const change of changes){
  const old=deferred(),fresh=deferred();let count=0
  const h=harness({post:()=>++count===1?old.promise:fresh.promise});t.after(h.close)
  const original=h.detections()[0]
  change(h);await settle()
  assert.equal(original.options.signal.aborted,true)
  old.resolve({data:{path:'compose.yaml'}});await settle()
  assert.deepEqual(h.events,[])
 }
 const pending=deferred(),closed=harness({post:()=>pending.promise})
 closed.close();assert.equal(closed.detections()[0].options.signal.aborted,true)
 pending.resolve({data:{path:'compose.yaml'}});await settle();assert.deepEqual(closed.events,[])
})

test('未找到和读取失败明确提示且允许手填；只读或无编辑权限不自动改写',async t=>{
 for(const [response,message] of [[()=>Promise.resolve({data:{path:''}}),'仓库根目录未找到 Compose 文件，请手动填写相对路径'],[()=>Promise.reject(new Error('代码仓库读取失败')),'代码仓库读取失败']]){
  const h=harness({post:response});t.after(h.close);await settle()
  assert.equal(h.state.repositoryDetectionMessage.value,message);assert.deepEqual(h.events,[])
  h.state.repositoryPath('deploy/compose.yml');assert.equal(h.state.repositoryDetectionMessage.value,'')
  assert.equal(h.events.at(-1).args[0].compose_source.path,'deploy/compose.yml')
 }
 for(const patch of [{disabled:true},{codes:['workflow.read','repository.read']}]){
  const h=harness(patch);t.after(h.close);await settle();assert.equal(h.detections().length,0);assert.deepEqual(h.events,[])
 }
})
