import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import * as configText from '../src/utils/configText.ts'
import {loadFrontend} from './acceptanceHarness.mjs'

const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const revision=(version,patch={})=>({version,is_secret:false,created_by:'author',created_by_name:'配置编辑者',created_at:'2026-11-08T03:00:00Z',hidden:false,...patch})
async function harness({secret=false,reveal=false,api}={}){
 const requests=[],events=[],unmounts=[],storage={project:'project-a'}
 const props=vue.reactive({open:true,target:{id:'config-a',name:'orders_settings',kind:'config_yaml',is_secret:secret,version:3,project_id:'project-a'}})
 const auth=vue.reactive({user:{id:'user',project_id:'project-a',department_id:'department',permissions:['delivery_variable.read',...(reveal?['delivery_variable.reveal']:[])]},canAny(codes){return codes.some(code=>this.user.permissions.includes(code))}})
 const client=Object.fromEntries(['get','post'].map(method=>[method,async(path,bodyOrOptions,options)=>{
  const request={method,path,options:method==='get'?bodyOrOptions:options};requests.push(request)
  const response=await api?.(request);if(response)return response
  return {data:path.endsWith('/revisions')?{revisions:request.options.params.page===1?[revision(3,{is_secret:secret}),revision(2,{is_secret:secret})]:[revision(1,{is_secret:secret})],total:3,current_version:3}:{revision:revision(Number(path.split('/').filter(v=>v!=='reveal').at(-1)),{is_secret:secret,value:path.includes('/2')?'port: 8080\n':'port: 9090\n'})}}
 }]))
 const component=loadFrontend('../src/components/ConfigurationHistoryDrawer.vue',{
  vue:{...vue,onBeforeUnmount:fn=>unmounts.push(fn)},'@/utils/configText':configText,'@/stores/auth':{useAuthStore:()=>auth},'@/api/client':{__esModule:true,default:client},'@/api/resources':{apiErrorMessage:()=> '读取失败，请重试'},
 },{localStorage:{getItem:()=>storage.project}}).default
 const scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(...event)=>events.push(event)})});await settle()
 return {state,props,auth,storage,requests,events,stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}

test('历史先加载元数据，分页保留版本选择，对比请求固定项目和具体版本',async t=>{
 const h=await harness();t.after(h.stop)
 assert.equal(h.requests.length,1);assert.ok(h.requests[0].path.endsWith('/revisions'))
 assert.equal(h.state.beforeVersion.value,2);assert.equal(h.state.afterVersion.value,3)
 await h.state.compare()
 assert.equal(h.state.beforeContent.value,'port: 8080\n');assert.equal(h.state.afterContent.value,'port: 9090\n')
 assert.equal(h.requests.filter(v=>v.method==='post').length,0)
 assert.ok(h.requests.every(v=>v.options.headers['X-EDO-Project-ID']==='project-a'))
 await h.state.loadMore();assert.equal(h.state.revisions.value.length,3);assert.equal(h.state.beforeVersion.value,2)
 h.state.beforeVersion.value=1;assert.equal(h.state.beforeContent.value,undefined);assert.equal(h.state.afterContent.value,undefined)
})

test('密钥版本不自动读取，独立权限不足不能对比，明确读取使用审计接口',async t=>{
 const h=await harness({secret:true});t.after(h.stop)
 await h.state.compare();assert.equal(h.requests.length,1);assert.equal(h.state.canCompare.value,false)
 h.auth.user.permissions.push('delivery_variable.reveal');await settle();assert.equal(h.requests.length,2)
 await h.state.compare();assert.equal(h.requests.filter(v=>v.method==='post'&&v.path.endsWith('/reveal')).length,2)
 assert.equal(h.state.afterContent.value,'port: 9090\n')
 h.state.close();assert.equal(h.state.afterContent.value,undefined);assert.deepEqual(h.events.at(-1),['update:open',false])
})

test('切换项目或撤销正文权限立即清除内容，旧读取迟到不回填',async t=>{
 for(const change of [h=>{h.props.open=false},h=>{h.auth.user.permissions=['delivery_variable.read']}]){
  let resolve;const pending=new Promise(yes=>resolve=yes)
  const h=await harness({secret:true,reveal:true,api:req=>req.method==='post'?pending:undefined});t.after(h.stop)
  const reading=h.state.compare();const old=h.requests.filter(v=>v.method==='post');change(h);await settle()
  assert.ok(old.every(req=>req.options.signal.aborted));assert.equal(h.state.afterContent.value,undefined)
  resolve({data:{revision:revision(3,{value:'迟到的正文'})}});await reading;assert.equal(h.state.afterContent.value,undefined)
 }
 const h=await harness();t.after(h.stop);await h.state.compare();h.storage.project='project-b';h.auth.user.project_id='project-b';await settle()
 assert.equal(h.state.afterContent.value,undefined);assert.equal(h.state.revisions.value.length,0);await h.state.compare();assert.equal(h.requests.length,3)
})

test('隐藏正文或读取失败不能伪装成空文件，真实空文件可以查看',async t=>{
 for(const result of [{hidden:true},{value:'',hidden:false}]){
  const h=await harness({api:req=>req.path.endsWith('/revisions')?undefined:{data:{revision:revision(Number(req.path.split('/').at(-1)),result)}}});t.after(h.stop)
  await h.state.compare()
  if(result.hidden){assert.equal(h.state.afterContent.value,undefined);assert.match(h.state.error.value,/读取失败/)}else assert.equal(h.state.afterContent.value,'')
 }
})
