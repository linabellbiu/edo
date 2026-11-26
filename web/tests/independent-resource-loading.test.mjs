import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const settle=()=>new Promise(resolve=>setImmediate(resolve))
const permissions=['application.read','repository.read','credential.read','build_plan.read','image_registry.read','pipeline_run.read','release_plan.read','deployment.read','deployment_plan.read','workflow.read']
function workspace(t,{section='applications',query={},get}={}){
 const h=componentHarness('../src/views/DevOpsView.vue',{props:vue.reactive({section}),query,codes:permissions,api:{get},modules:{'@/utils/deploymentInstances':loadFrontend('../src/utils/deploymentInstances.ts')},globals:{localStorage:{getItem:()=> 'project-a'},document:{hidden:true}}})
 t.after(()=>{h.state.cancelResourceReads();h.stop()})
 return h
}

test('应用主体先显示；慢候选项独立失败及重试，不丢失其他成功结果',async t=>{
 const slow=deferred();let fail=true
 const h=workspace(t,{get:path=>path==='/repositories'?(fail?slow.promise:Promise.resolve({data:{repositories:[{id:'repo-a'}]}})):Promise.resolve({data:{applications:[{id:'app-a',workflows:[]}],image_registries:[{id:'registry-a'}]}})})
 await h.state.refresh()
 assert.equal(h.state.loading.value,false)
 assert.equal(h.state.applications.value[0].id,'app-a')
 assert.equal(h.state.registries.value[0].id,'registry-a')
 assert.equal(h.state.resourcePending.value.repositories,true)
 slow.reject(new Error('仓库列表读取失败'));await settle()
 assert.equal(h.state.resourceErrors.value.repositories,'仓库列表读取失败')
 assert.equal(h.state.applications.value[0].id,'app-a')
 fail=false;await h.state.resourceReaders.repositories()
 assert.equal(h.state.repositories.value[0].id,'repo-a')
 assert.equal(h.state.resourceErrors.value.repositories,undefined)
})

test('发布记录立即读取，不等待应用列表或无关候选项',async t=>{
 const slow=deferred()
 const h=workspace(t,{section:'release-plans',query:{view:'runs'},get:path=>path==='/applications'?slow.promise:Promise.resolve({data:{pipeline_runs:[{id:'run-a',application_id:'app-a',status:'running',created_at:'2026-11-16T00:00:00Z'}],total:1}})})
 await h.state.refresh()
 assert.equal(h.state.loading.value,false)
 assert.equal(h.state.runListLoading.value,false)
 assert.equal(h.state.runs.value[0].id,'run-a')
 assert.equal(h.state.resourcePending.value.applications,true)
 assert.ok(h.requests.some(request=>request.path==='/pipeline-runs'))
 slow.resolve({data:{applications:[]}});await settle()
})

test('一个运行的部署明细未完成时，其他运行状态仍能再次刷新',async t=>{
 const slow=deferred();let reads=0
 const run={id:'run-a',application_id:'app-a',status:'running',stage:'build',created_at:'2026-11-16T00:00:00Z'}
 const h=workspace(t,{section:'release-plans',query:{view:'runs'},get:path=>path.startsWith('/deployments?pipeline_run_id=')?slow.promise:Promise.resolve({data:{pipeline_runs:[{...run,status:++reads>1?'succeeded':'running'}],total:1}})})
 h.state.lastRunQuery=h.state.runQueryKey.value;h.state.runs.value=[run];h.state.expandPipelineRun(true,run);await settle()
 assert.equal(h.state.runDeploymentLoading.value['run-a'],true)
 await h.state.refreshRunState()
 await h.state.refreshRunState()
 assert.equal(h.state.runs.value[0].status,'succeeded')
 assert.equal(h.requests.filter(request=>request.path==='/pipeline-runs').length,2)
 assert.equal(h.requests.filter(request=>request.path.startsWith('/deployments?pipeline_run_id=')).length,1,'仍在途的同一运行明细不反复取消重发')
 slow.resolve({data:{deployments:[]}});await settle()
})

test('资源刷新按类别去重，撤销权限和范围改变后不回填迟到数据',async t=>{
 const slow=deferred()
 const h=workspace(t,{get:()=>slow.promise})
 const first=h.state.resourceReaders.repositories(),second=h.state.resourceReaders.repositories()
 assert.equal(h.requests.filter(request=>request.path==='/repositories').length,1)
 h.auth.permissions.delete('repository.read')
 slow.resolve({data:{repositories:[{id:'stale'}]}});await Promise.all([first,second])
 assert.equal(h.state.repositories.value.length,0)
 // 取消旧范围请求不能继续保持局部忙碌，也不能把迟到错误显示到新范围。
 h.auth.permissions.add('repository.read')
 h.state.cancelResourceReads()
 assert.deepEqual(h.state.resourcePending.value,{})
})

test('失败状态在重试完成前保留，多项重试互不等待且不重复请求在途项',async t=>{
 const repository=deferred(),registry=deferred()
 const h=workspace(t,{get:path=>path==='/repositories'?repository.promise:registry.promise})
 h.state.resourceErrors.value={repositories:'仓库暂不可用','image-registries':'镜像目录暂不可用'}
 h.state.retryResourceReads(['repositories','image-registries'])
 assert.equal(h.requests.length,2)
 assert.equal(h.state.resourceErrors.value.repositories,'仓库暂不可用')
 assert.equal(h.state.resourceErrors.value['image-registries'],'镜像目录暂不可用')
 h.state.retryResourceReads(['repositories','image-registries']);assert.equal(h.requests.length,2)
 registry.resolve({data:{image_registries:[{id:'registry-a'}]}});await settle()
 assert.equal(h.state.resourceErrors.value['image-registries'],undefined)
 assert.equal(h.state.resourceErrors.value.repositories,'仓库暂不可用')
 assert.equal(h.state.registries.value[0].id,'registry-a')
 repository.reject(new Error('仓库仍不可用'));await settle()
 assert.equal(h.state.resourceErrors.value.repositories,'仓库仍不可用')
})

test('授权校准只等待当前身份并重新读取一次列表，成功目录和页面不被清空',async t=>{
 for(const code of ['EDO_AUTHORIZATION_PENDING','EDO_AUTHORIZATION_CHANGED']){
  const ready=deferred();let reads=0
  const h=workspace(t,{get:async()=>{if(++reads===1)throw Object.assign(new Error('授权校准'),{code});return {data:{repositories:[{id:'fresh'}]}}}})
  h.auth.waitForAuthorization=()=>ready.promise
  h.state.repositories.value=[{id:'existing'}]
  const request=h.state.resourceReaders.repositories();await settle()
  assert.equal(reads,1);assert.equal(h.state.repositories.value[0].id,'existing');assert.deepEqual(h.state.resourceErrors.value,{})
  ready.resolve(true);await request
  assert.equal(reads,2);assert.equal(h.state.repositories.value[0].id,'fresh');assert.deepEqual(h.state.resourceErrors.value,{})
  assert.ok(h.requests.every(item=>item.method==='get'))
 }
})

test('授权校准失败、取消、撤权及重复校准不循环重试或制造分项错误',async t=>{
 for(const condition of ['unavailable','cancelled','revoked','again']){
  const ready=deferred();let reads=0
  const h=workspace(t,{get:async()=>{reads++;throw Object.assign(new Error('授权校准'),{code:'EDO_AUTHORIZATION_CHANGED'})}})
  h.auth.waitForAuthorization=()=>ready.promise
  h.state.repositories.value=[{id:'existing'}]
  const request=h.state.resourceReaders.repositories();await settle()
  if(condition==='cancelled')h.state.cancelResourceReads()
  if(condition==='revoked')h.auth.permissions.delete('repository.read')
  ready.resolve(condition!=='unavailable');await request
  assert.equal(reads,condition==='again'?2:1)
  assert.equal(h.state.repositories.value[0].id,'existing')
  assert.deepEqual(h.state.resourceErrors.value,{})
 }
})

test('真实网络错误不被授权校准吞掉或自动重试',async t=>{
 const h=workspace(t,{get:async()=>{throw Object.assign(new Error('连接超时'),{code:'ECONNABORTED'})}})
 h.auth.waitForAuthorization=()=>{throw new Error('不应等待授权')}
 await h.state.resourceReaders.repositories()
 assert.equal(h.requests.length,1);assert.equal(h.state.resourceErrors.value.repositories,'连接超时')
})

test('首次列表因授权校准未能读取时不标记已加载，恢复权限后可自动补读',async t=>{
 let unavailable=true,reads=0
 const h=workspace(t,{get:async path=>{
  if(path!=='/applications')return {data:{}}
  reads++;if(unavailable)throw Object.assign(new Error('授权校准'),{code:'EDO_AUTHORIZATION_PENDING'})
  return {data:{applications:[{id:'fresh-app',workflows:[]}]}}
 }})
 h.auth.user.project_id='project-a';h.auth.authorizationReady=true;h.auth.waitForAuthorization=async()=>false
 await h.state.refresh()
 assert.equal(h.state.loading.value,false);assert.equal(h.state.resourceReady.value.applications,undefined)
 unavailable=false;h.state.refreshAuthorizedResourceScope();await settle()
 assert.equal(reads,2);assert.equal(h.state.applications.value[0].id,'fresh-app')
 h.state.refreshAuthorizedResourceScope();await settle();assert.equal(reads,2,'已有有效快照不反复重新读取')
})

test('运行分页授权校准后只重读一次，真实读取失败归入工具栏并保留已有运行',async t=>{
 let attempts=0,fail=false
 const h=workspace(t,{section:'release-plans',query:{view:'runs'},get:async()=>{
  attempts++;if(fail)throw new Error('运行列表暂不可用')
  if(attempts===1)throw Object.assign(new Error('授权校准'),{code:'EDO_AUTHORIZATION_CHANGED'})
  return {data:{pipeline_runs:[{id:'run-current',status:'running'}],total:1}}
 }})
 h.auth.waitForAuthorization=async()=>true
 await h.state.loadRunPage()
 assert.equal(attempts,2);assert.equal(h.state.runs.value[0].id,'run-current')
 fail=true;await h.state.loadRunPage()
 assert.equal(h.state.resourceStatusErrors.value['run-list'],'运行列表暂不可用')
 assert.equal(h.state.runs.value[0].id,'run-current')
 fail=false;h.state.retryResourceReads(['run-list']);await settle()
 assert.equal(h.state.resourceStatusErrors.value['run-list'],undefined)
})

function history(t){
 const pending=[],unmounts=[],props=vue.reactive({runId:'run-a',status:'running',updatedAt:'1'})
 const component=loadFrontend('../src/components/PipelineTaskHistory.vue',{vue:{...vue,onBeforeUnmount:fn=>unmounts.push(fn)},'@/api/client':{__esModule:true,default:{get:(path,options)=>new Promise((resolve,reject)=>pending.push({path,options,resolve,reject}))}},'@/api/resources':{apiErrorMessage:error=>error.message}}).default
 const scope=vue.effectScope(),state=scope.run(()=>component.setup(props,{expose(){}}))
 t.after(()=>{unmounts.forEach(fn=>fn());scope.stop()})
 return {state,props,pending}
}

test('任务状态不等待检查报告，回传失败只影响回传区且可以独立重试',async t=>{
 const h=history(t)
 h.pending.find(p=>p.path.endsWith('/tasks')).resolve({data:{tasks:[{id:'task-a',node_id:'node-a',status:'running'}]}})
 await settle()
 assert.equal(h.state.tasks.value[0].id,'task-a')
 assert.equal(h.state.loading.value.tasks,undefined)
 assert.equal(h.state.loading.value.reports,true)
 h.pending.find(p=>p.path.endsWith('/check-deliveries')).reject(new Error('回传读取失败'));await settle()
 assert.equal(h.state.errors.value.deliveries,'回传读取失败')
 assert.equal(h.state.tasks.value[0].id,'task-a')
 const count=h.pending.length,retry=h.state.loadSection('deliveries')
 assert.equal(h.pending.length,count+1)
 assert.ok(h.pending.at(-1).path.endsWith('/check-deliveries'))
 h.pending.at(-1).resolve({data:{deliveries:[]}});await retry
 assert.equal(h.state.errors.value.deliveries,undefined)
})

test('切换运行时中止旧任务与报告请求，迟到结果不能混入新运行',async t=>{
 const h=history(t),old=h.pending.slice()
 h.props.runId='run-b';await vue.nextTick()
 assert.ok(old.every(request=>request.options.signal.aborted))
 h.pending.find(p=>p.path==='/pipeline-runs/run-b/tasks').resolve({data:{tasks:[{id:'new-task'}]}});await settle()
 for(const request of old)request.resolve({data:{tasks:[{id:'old-task'}],reports:[{id:'old-report'}],deliveries:[{id:'old-delivery'}]}})
 await settle()
 assert.equal(h.state.tasks.value[0].id,'new-task')
 assert.deepEqual(h.state.reports.value,[])
 assert.deepEqual(h.state.deliveries.value,[])
})
