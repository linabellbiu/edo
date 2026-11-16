import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const settle=()=>new Promise(resolve=>setImmediate(resolve))
const plan={id:'plan-a',name:'部署方案',kind:'docker',timeout_seconds:600,is_active:true,deployment_target_id:'target-a',deployment_target:{id:'target-a',environment_id:'group-a',host_id:'host-a',runtime_id:'docker-a'}}
const permissions=['deployment_plan.read','deployment_plan.update','workflow.read','workflow.update','cluster.read','deployment.read']
function harness(t,{listHosts,get,props={}}={}){
 const storage={project:'project-a'},auth=vue.reactive({loaded:true,authorizationReady:true,user:{id:'user-a',project_id:'project-a',permissions},canAny:keys=>keys.some(key=>permissions.includes(key))})
 const h=componentHarness('../src/views/DeploymentPlanView.vue',{props:vue.reactive(props),codes:permissions,api:{get:get||((path)=>Promise.resolve({data:{deployment_plans:[plan],endpoints:[{id:'docker-a',is_active:true}],clusters:[]}}))},modules:{'@/stores/auth':{useAuthStore:()=>auth},'@/api/infrastructure':{listHosts:listHosts||(()=>Promise.resolve([])),listEnvironments:()=>Promise.resolve([{id:'group-a',is_active:true}]),environmentIDsOf:host=>host.environment_ids||[]},'@/utils/resourceList':loadFrontend('../src/utils/resourceList.ts',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})}})},globals:{localStorage:{getItem:()=>storage.project},window:{addEventListener(){},removeEventListener(){}}}})
 t.after(()=>{h.state.cancelResourceReads();h.stop()})
 return {...h,auth,storage}
}

test('部署方案列表不等待主机列表，编辑保留已有目标，失败可单独重试',async t=>{
 const hosts=deferred();let retry=false
 const h=harness(t,{listHosts:()=>retry?Promise.resolve([{id:'host-a',is_active:true,environment_ids:['group-a'],capabilities:[{kind:'docker',status:'ready',runtime_id:'docker-a'}]}]):hosts.promise})
 await h.state.refresh()
 assert.equal(h.state.loading.value,false)
 assert.equal(h.state.plans.value[0].id,'plan-a')
 assert.equal(h.state.resourcePending.value.hosts,true)
 h.state.edit(plan)
 assert.equal(h.state.form.host_id,'host-a');assert.equal(h.state.form.runtime_id,'docker-a')
 assert.equal(h.state.targetOptionsPending.value,true)
 hosts.reject(new Error('主机列表读取失败'));await settle()
 assert.equal(h.state.resourceErrors.value.hosts,'主机列表读取失败')
 assert.equal(h.state.form.runtime_id,'docker-a')
 assert.match(h.state.validate(),/执行目标读取失败/)
 retry=true;await h.state.readPlanResource('hosts')
 assert.equal(h.state.targetOptionsFailed.value,false)
 assert.equal(h.state.form.runtime_id,'docker-a')
 assert.equal(h.state.plans.value[0].id,'plan-a')
})

test('流水线内联部署编辑立即显示，辅助主机请求不阻塞表单且不清空保存的目标',async t=>{
 const hosts=deferred()
 const h=harness(t,{props:{inline:true,inlineOpen:true,inlineValue:{...plan,deployment_target:plan.deployment_target}},listHosts:()=>hosts.promise})
 await settle()
 assert.equal(h.state.formOpen.value,true)
 assert.equal(h.state.form.name,'部署方案')
 assert.equal(h.state.form.runtime_id,'docker-a')
 assert.equal(h.state.resourcePending.value.hosts,true)
 hosts.resolve([]);await settle()
 assert.equal(h.state.form.runtime_id,'docker-a','选项到达也不自动替换原关联')
})

test('切换项目中止旧候选请求，迟到成功与失败不会污染新项目',async t=>{
 const old=deferred(),fresh=deferred(),signals=[]
 const h=harness(t,{listHosts:signal=>{signals.push(signal);return signals.length===1?old.promise:fresh.promise}})
 await h.state.refresh()
 h.storage.project='project-b';h.state.syncProject();h.auth.user.project_id='project-b';await settle()
 assert.equal(signals[0].aborted,true)
 fresh.resolve([{id:'new-host',is_active:true,capabilities:[]}]);await settle()
 old.resolve([{id:'old-host',is_active:true,capabilities:[]}]);await settle()
 assert.deepEqual(h.state.hosts.value.map(host=>host.id),['new-host'])
 assert.deepEqual(h.state.resourceErrors.value,{})
})
