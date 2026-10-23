import assert from 'node:assert/strict'
import {test} from 'node:test'
import {runListHarness,runFilterModule as filters} from './pipelineRunTestHarness.mjs'

const records=Array.from({length:225},(_,i)=>({id:`run-${i+1}`,row_id:i+1,application_id:'app',status:i%2?'succeeded':'failed'}))
test('运行筛选默认按ID倒序，日期按上海时区包含结束日，URL保留上下文',()=>{
  assert.deepEqual(filters.parsePipelineRunFilters({}),{status:'',from:'',to:'',order:'desc',page:1,pageSize:10})
  const range=filters.pipelineRunDateBounds(['2026-10-23','2026-10-24'])
  assert.deepEqual(range,{from:'2026-10-22T16:00:00.000Z',to:'2026-10-24T16:00:00.000Z'})
  assert.deepEqual(filters.pipelineRunDateRange(range),['2026-10-23','2026-10-24'])
  const query=filters.pipelineRunRouteQuery({view:'runs',project:'p',run:'old'}, {...filters.parsePipelineRunFilters({}),status:'failed',order:'asc',page:2,...range})
  assert.equal(query.project,'p');assert.equal(query.run,undefined);assert.equal(query.page,'2');assert.equal(query.order,'asc')
  assert.equal(filters.parsePipelineRunFilters({page:'NaN',page_size:'200',order:'drop'}).pageSize,10)
  assert.deepEqual(filters.pipelineRunDateBounds(['2026-02-30','2026-03-01']),{from:'',to:''})
})
test('筛选和分页交给服务端，超过200条仍能准确翻页，刷新保留条件',async()=>{
  const {state,route,requests}=runListHarness({records,query:{status:'failed',order:'asc',page:'11'}})
  await state.loadRunPage()
  assert.equal(requests[0].params.status,'failed');assert.equal(requests[0].params.order,'asc')
  assert.equal(state.runTotal.value,113);assert.equal(state.runs.value[0].id,'run-201')
  assert.equal(state.runPagination.value.current,11)
  await state.refreshRunState();assert.equal(route.query.page,'11');assert.equal(state.runs.value[0].id,'run-201')
  state.changeRunFilters({status:'succeeded'});assert.equal(route.query.page,undefined);assert.equal(route.query.status,'succeeded')
})
test('准确打开筛选列表之外的历史运行，关闭后恢复原列表与分页',async()=>{
  const {state,route}=runListHarness({records,query:{status:'succeeded',page:'2',run:'run-225'}})
  await state.loadRunPage();await state.revealRequestedRun()
  assert.equal(state.detachedRun.value.id,'run-225');assert.equal(state.runDrawerEntry.value.id,'run-225')
  assert.equal(state.runs.value.some(row=>row.id==='run-225'),false)
  state.collapseAllRuns();assert.equal(route.query.run,undefined);assert.equal(route.query.status,'succeeded');assert.equal(route.query.page,'2')
})
test('快速切换筛选时忽略迟到响应，错误保留可重试提示',async()=>{
  const pending=[]
  const {state,route}=runListHarness({get:()=>new Promise((resolve,reject)=>pending.push({resolve,reject}))})
  const first=state.loadRunPage();route.query.status='failed';const second=state.loadRunPage()
  pending[1].resolve({data:{pipeline_runs:[records[0]],total:1}});await second
  pending[0].resolve({data:{pipeline_runs:[records[1]],total:100}});await first
  assert.equal(state.runs.value[0].status,'failed');assert.equal(state.runTotal.value,1)
  const third=state.loadRunPage();pending[2].reject(new Error('查询失败，请重试'));await third
  assert.equal(state.runListError.value,'查询失败，请重试');assert.equal(state.runListLoading.value,false)
})
test('同次发布跨页的运行由独立请求完整载入，筛选不截断详情',async()=>{
  const groupRuns=records.slice(0,12).map(row=>({...row,release_plan_id:'plan',release_plan_execution_id:'batch'}))
  const {state,requests}=runListHarness({get:async(_path,{params})=>({data:params.release_plan_execution_id?{pipeline_runs:groupRuns,total:12}:{pipeline_runs:groupRuns.slice(0,10),total:12}})})
  await state.loadRunPage();const group=state.runListEntries.value[0]
  state.runDrawerEntryID.value=group.id;await state.loadExecutionGroup(group)
  assert.equal(state.runDrawerEntry.value.runs.length,12)
  assert.equal(requests[1].params.release_plan_execution_id,'batch');assert.equal(requests[1].params.status,undefined)
})
