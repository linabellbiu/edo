import assert from 'node:assert/strict'
import {test} from 'node:test'
import {runListHarness} from './pipelineRunTestHarness.mjs'

const pending={id:'run',application_id:'app',status:'awaiting_approval',current_node_id:'gate'}
const approval=changes=>({node_id:'gate',required_approvals:2,approved_count:0,approved_by_me:false,can_approve:true,reason:'等待审核',...changes})
function open(h){h.state.runs.value=[{...pending}];h.state.runDrawerEntryID.value='run';h.state.expandedRunIDs.value=['run'];return h.state.runs.value[0]}
function response(detail){return {data:{pipeline_run:detail}}}

test('审核状态取当前读取者投影：本人已投票显示1/2并禁用，其他审核人仍可审核',async()=>{
 const h=runListHarness({get:async()=>response({...pending,approval:approval({approved_count:1,approved_by_me:true,can_approve:false,reason:'已审核，等待其他审核人'})})})
 let run=open(h)
 await h.state.loadRunApproval(run.id)
 assert.equal(h.state.currentRunApproval(run).approved_count,1)
 assert.equal(h.state.currentRunApproval(run).approved_by_me,true)
 assert.equal(h.state.canApproveRun(run),false)
 await h.state.approveRun(run)
 assert.equal(h.posts.length,0)
 const other=runListHarness({get:async()=>response({...pending,approval:approval({approved_count:1})})})
 run=open(other);await other.state.loadRunApproval(run.id)
 assert.equal(other.state.canApproveRun(run),true)
})

test('缺失投影、节点不符、读取失败和无审核权限均禁止提交',async()=>{
 for(const detail of [{...pending},{...pending,approval:approval({node_id:'old-gate'})}]){
  const h=runListHarness({get:async()=>response(detail)}),run=open(h)
  await h.state.loadRunApproval(run.id);await h.state.approveRun(run)
  assert.equal(h.state.canApproveRun(run),false);assert.equal(h.posts.length,0)
  assert.match(h.state.runApprovalErrors.value.run,/无法读取最新审核状态/)
 }
 const failed=runListHarness({get:async()=>{throw new Error('读取审核状态失败')}}),run=open(failed)
 failed.state.runApprovals.value.run=approval()
 await failed.state.loadRunApproval(run.id);await failed.state.approveRun(run)
 assert.equal(failed.posts.length,0);assert.equal(failed.state.runApprovalErrors.value.run,'读取审核状态失败')
 const denied=runListHarness({get:async()=>response({...pending,approval:approval()}),canAny:codes=>!codes.includes('deployment.review')}),deniedRun=open(denied)
 await denied.state.loadRunApproval(deniedRun.id);await denied.state.approveRun(deniedRun)
 assert.equal(denied.posts.length,0)
})

test('提交前重新检查最新权限状态，不以旧按钮状态重复审核',async()=>{
 let checks=0
 const h=runListHarness({get:async()=>response({...pending,approval:approval(++checks>1?{can_approve:false,approved_by_me:true,approved_count:1}:{})})}),run=open(h)
 await h.state.loadRunApproval(run.id)
 assert.equal(h.state.canApproveRun(run),true)
 await h.state.approveRun(run)
 assert.equal(checks,2);assert.equal(h.posts.length,0)
 assert.equal(h.state.canApproveRun(run),false)
})

test('一次点击只提交一次，成功后重新读取本人票数，不把首票显示成整轮完成',async()=>{
 let voted=false,finish
 const h=runListHarness({records:[pending],get:async path=>path==='/pipeline-runs/run'?response({...pending,approval:approval(voted?{approved_count:1,approved_by_me:true,can_approve:false}:{} )}):{data:{pipeline_runs:[pending],total:1}},post:()=>new Promise(resolve=>{finish=()=>{voted=true;resolve({data:{pipeline_run:pending}})}})}),run=open(h)
 await h.state.loadRunApproval(run.id)
 const first=h.state.approveRun(run)
 await new Promise(resolve=>setImmediate(resolve))
 await h.state.approveRun(run)
 assert.equal(h.posts.length,1);assert.equal(h.state.approvingRunID.value,'run')
 finish();await first
 assert.equal(h.state.currentRunApproval(run).approved_count,1)
 assert.equal(h.state.currentRunApproval(run).approved_by_me,true)
 assert.equal(h.state.canApproveRun(run),false)
 assert.equal(h.state.runs.value[0].status,'awaiting_approval')
})

test('项目切换后丢弃迟到审核响应，不把其他项目状态带入当前按钮',async()=>{
 let finish
 const h=runListHarness({get:()=>new Promise(resolve=>{finish=resolve})}),run=open(h)
 const loading=h.state.loadRunApproval(run.id)
 h.auth.user.project_id='other-project'
 finish(response({...pending,approval:approval()}));await loading
 assert.equal(h.state.currentRunApproval(run),undefined)
 assert.equal(h.state.canApproveRun(run),false)
})
