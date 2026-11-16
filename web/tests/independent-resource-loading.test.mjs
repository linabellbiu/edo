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
