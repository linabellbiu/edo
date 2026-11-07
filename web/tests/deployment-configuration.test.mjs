import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
async function harness(get){
 const events=[],requests=[],unmounts=[],storage={project:'project'},window=new EventTarget()
 const auth=vue.reactive({authorizationState:'ready',user:{id:'actor',project_id:'project',department_id:'department',permissions:['workflow.update','delivery_variable.read']},canAny(codes){return codes.some(code=>this.user.permissions.includes(code))}})
 const props=vue.reactive({config:{configuration_files:[]},applicationID:'app',environmentID:'prod',disabled:false})
 const row=(id,patch={})=>({id,name:`${id}.env`,kind:'config_env',version:1,is_active:true,project_id:'project',application_id:'app',environment_id:'prod',...patch})
 const client={get:async(path,options)=>{requests.push({path,options});return get?get():{data:{variables:[row('selected'),row('legacy',{kind:'env'}),row('other-project',{project_id:'another'}),row('other-app',{application_id:'another'}),row('other-env',{environment_id:'test'})]}}}}
 const component=loadFrontend('../src/components/DeploymentConfigurationFields.vue',{'vue':{...vue,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn)},'@/api/client':{__esModule:true,default:client},'@/api/resources':{apiErrorMessage:error=>error.message},'@/stores/auth':{useAuthStore:()=>auth}},{localStorage:{getItem:()=>storage.project},window}).default
 const scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(...event)=>events.push(event)})});await settle()
 return {state,props,auth,storage,events,requests,stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}
test('部署显式选择配置 UUID 与生成路径，配置选择不混入旧变量或其他范围',async t=>{
 const h=await harness();t.after(h.stop)
 assert.deepEqual(h.state.options(0).map(item=>item.value),['selected'])
 h.props.config.configuration_files=[{id:'',path:''}]
 h.state.choose(0,'selected')
 assert.deepEqual(h.events.at(-1),['change',{configuration_files:[{id:'selected',path:'selected.env'}]}])
 assert.equal(h.requests[0].options.headers['X-EDO-Project-ID'],'project')
 assert.ok(h.state.validPath('config/application.yaml'));assert.ok(!h.state.validPath('../escape'))
 assert.ok(!h.state.validPath('.edo/lock'));assert.ok(!h.state.validPath('/etc/config'))
})
test('切项目、撤权或加载失败后不能提交保留的配置草稿',async t=>{
 for(const mutation of ['project','permission','error']){
  const h=await harness(mutation==='error'?()=>Promise.reject(new Error('配置读取失败')):undefined);t.after(h.stop)
  if(mutation==='project')h.storage.project='another'
  if(mutation==='permission')h.auth.user.permissions=[]
  await settle();h.state.change({configuration_files:[{id:'selected',path:'.prod.env'}]})
  assert.equal(h.events.length,0,mutation)
 }
})
test('旧范围的迟到配置响应不回填新范围候选项',async t=>{
 const responses=[];const h=await harness(()=>new Promise(yes=>{responses.push(yes)}));t.after(h.stop)
 h.storage.project='another';h.auth.user.project_id='another';await settle()
 responses[0]({data:{variables:[{id:'old',name:'old.env',kind:'config_env',version:1,is_active:true,project_id:'project'}]}});await settle()
 assert.equal(h.state.configurations.value.length,0);h.state.change({configuration_files:[]});assert.equal(h.events.length,0)
})
