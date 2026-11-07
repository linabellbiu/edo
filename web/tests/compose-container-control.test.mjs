import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const source=readFileSync(new URL('../src/components/ComposeContainerControlModal.vue',import.meta.url),'utf8')
const {outputText}=ts.transpileModule(compileScript(parse(source).descriptor,{id:'container-control-modal'}).content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}})
function harness(action='recreate',permissions=['deployment.read',`deployment.container.${action}`]){
  const props=vue.reactive({open:true,deploymentID:'deployment',containerID:'b'.repeat(64),action})
  const auth=vue.reactive({authorizationState:'ready',user:{id:'actor',project_id:'project',permissions},canAny:codes=>codes.some(code=>auth.user.permissions.includes(code))})
  const requests=[],pending=[],submissions=[],posts=[],events=[]
  const client={get:(path,options)=>new Promise((resolve,reject)=>{requests.push({path,options});pending.push({resolve,reject})}),post:(path,body,options)=>new Promise((resolve,reject)=>{submissions.push({path,body,options});posts.push({resolve,reject})})}
  const modules={vue:{...vue,watch(){},onBeforeUnmount(){}},'@/stores/auth':{useAuthStore:()=>auth},'@/api/client':{__esModule:true,default:client},'@/api/resources':{apiErrorMessage:error=>error.message}}
  const module={exports:{}};new Function('require','module','exports',outputText)(name=>modules[name]||{},module,module.exports)
  const state=module.exports.default.setup(props,{expose(){},emit:(...value)=>events.push(value)})
  const preview={service:'api',container_id:props.containerID,action,digest:'f'.repeat(64),run_to_completion:false,containers:[{id:'a'.repeat(64),name:'api-1'},{id:'b'.repeat(64),name:'api-2'}]}
  return {state,props,auth,requests,pending,submissions,posts,events,preview}
}
test('重建先只读核对全部副本，用户确认后只提交一次绑定摘要的请求',async()=>{
  const h=harness(),opening=h.state.loadPreview()
  assert.equal(h.submissions.length,0)
  assert.equal(h.requests[0].options.params.action,'recreate')
  h.pending[0].resolve({data:{preview:h.preview}});await opening
  assert.equal(h.state.preview.value.containers.length,2)
  const execution=h.state.execute();await h.state.execute()
  assert.equal(h.submissions.length,1)
  assert.equal(h.submissions[0].path,`/deployments/deployment/runtime/containers/${h.props.containerID}/recreate`)
  assert.deepEqual(h.submissions[0].body,{digest:h.preview.digest})
  assert.equal(h.submissions[0].options.headers['X-EDO-Project-ID'],'project')
  const runtime={deployment_id:'deployment',kind:'compose',state:'running'}
  h.posts[0].resolve({data:{runtime}});await execution
  assert.deepEqual(h.events,[['completed',runtime],['update:open',false]])
})
test('确认失效或执行失败后必须重新预检，不重放旧确认，也不自动重试写入',async()=>{
  const h=harness('restart'),opening=h.state.loadPreview();h.pending[0].resolve({data:{preview:h.preview}});await opening
  const execution=h.state.execute();h.posts[0].reject(new Error('容器已变化，请重新确认'));await execution
  assert.equal(h.state.preview.value,undefined)
  assert.match(h.state.error.value,/重新确认/)
  await h.state.execute();assert.equal(h.submissions.length,1)
  const recheck=h.state.loadPreview();h.pending[1].resolve({data:{preview:{...h.preview,digest:'e'.repeat(64)}}});await recheck
  assert.equal(h.submissions.length,1,'再次读取不等于同意执行')
})
test('三个权限独立校验，撤权、切项目、关闭或切换容器后丢弃旧响应',async()=>{
  for(const action of ['stop','restart','recreate']){
    const denied=harness(action,['deployment.read','deployment.execute','cluster.execute']);await denied.state.loadPreview();await denied.state.execute();assert.equal(denied.requests.length,0);assert.equal(denied.submissions.length,0)
  }
  for(const change of ['permission','project','closed','container']){
    const h=harness(),opening=h.state.loadPreview()
    if(change==='permission')h.auth.user.permissions=[]
    if(change==='project')h.auth.user.project_id='other-project'
    if(change==='closed')h.state.cancel()
    if(change==='container')h.props.containerID='c'.repeat(64)
    h.pending[0].resolve({data:{preview:h.preview}});await opening;await h.state.execute()
    assert.equal(h.state.preview.value,undefined,change);assert.equal(h.submissions.length,0,change)
  }
})

test('配置变化的重启明确转为重建，并独立要求重建权限',async()=>{
 const h=harness('restart',['deployment.read','deployment.container.restart','deployment.container.recreate'])
 const opening=h.state.loadPreview();h.pending[0].resolve({data:{preview:{...h.preview,kind:'compose',effective_action:'recreate',configuration_changed:true}}});await opening
 assert.equal(h.state.effectiveAction.value,'recreate');assert.equal(h.state.label.value,'重建服务')
 const execution=h.state.execute();assert.equal(h.submissions.length,1);assert.ok(h.submissions[0].path.endsWith('/restart'))
 h.posts[0].resolve({data:{runtime:{deployment_id:'deployment',kind:'compose'}}});await execution
 const denied=harness('restart'),deniedOpening=denied.state.loadPreview();denied.pending[0].resolve({data:{preview:{...denied.preview,effective_action:'recreate',configuration_changed:true}}});await deniedOpening
 assert.equal(denied.state.allowed.value,false);await denied.state.execute();assert.equal(denied.submissions.length,0)
})
