import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {componentHarness} from './acceptanceHarness.mjs'

for(const [label,method,code,item] of [
  ['角色','removeRole','role.delete',{id:'role-a',display_name:'测试角色',in_use:false}],
  ['部门','removeDepartment','department.delete',{id:'department-a',name:'测试部门',member_count:0}],
]) {
  test(`${label}删除失败必须拒绝确认Promise，保留原数据且不刷新或提示成功`,async t=>{
    const failure=new Error(`${label}仍有关联数据`)
    const h=componentHarness('../src/views/AccessView.vue',{codes:[code],api:{delete:async()=>{throw failure}}})
    t.after(h.stop)
    const records=method==='removeRole'?h.state.roles:h.state.departments
    records.value=[item]
    h.state[method](item)
    const confirmation=h.confirmations[0]
    assert.equal(h.requests.length,0)
    await assert.rejects(confirmation.onOk(),error=>error===failure)
    assert.equal(h.confirmations[0],confirmation)
    assert.deepEqual(records.value,[item])
    assert.equal(h.requests.length,1);assert.equal(h.requests[0].method,'delete')
    assert.deepEqual(h.notifications,[{kind:'error',value:failure.message}])
  })
}

function logs(t) {
  const pending=[],unmount=[]
  const h=componentHarness('../src/views/LogsView.vue',{
    modules:{vue:{...vue,onMounted(){},onBeforeUnmount:callback=>unmount.push(callback)}},
    api:{get:(_path,options)=>new Promise((resolve,reject)=>pending.push({options,resolve:data=>resolve({data}),reject}))},
  })
  t.after(h.stop)
  return {...h,pending,unmount}
}
const response=(id,hasMore=false)=>({items:[{id,message:`日志${id}`}],next_before_id:id,has_more:hasMore})

test('日志新筛选先返回后，旧成功响应不得覆盖列表和游标，并取消旧请求',async t=>{
  const h=logs(t)
  h.state.query.value='old';const old=h.state.load(false)
  h.state.query.value='new';const current=h.state.load(false)
  h.pending[1].resolve(response(20,true));await current
  h.pending[0].resolve(response(10,false));await old
  assert.equal(h.pending[0].options.signal?.aborted,true)
  assert.deepEqual(h.state.items.value.map(item=>item.id),[20])
  assert.equal(h.state.nextBeforeID.value,20);assert.equal(h.state.hasMore.value,true)
})

test('旧日志错误和finally不得提示过时错误或结束仍在进行的新查询loading',async t=>{
  const h=logs(t)
  h.state.query.value='old';const old=h.state.load(false)
  h.state.query.value='new';const current=h.state.load(false)
  h.pending[0].reject(new Error('过时查询失败'));await old
  const oldNotifications=[...h.notifications],newStillLoading=h.state.loading.value
  h.pending[1].resolve(response(20));await current
  assert.deepEqual(oldNotifications,[])
  assert.equal(newStillLoading,true)
  assert.equal(h.state.loading.value,false);assert.deepEqual(h.state.items.value.map(item=>item.id),[20])
})

test('新日志查询失败后，迟到旧错误不能替换新错误或清空原有结果',async t=>{
  const h=logs(t)
  h.state.items.value=response(5).items
  const old=h.state.load(false),current=h.state.load(false)
  h.pending[1].reject(new Error('新查询失败'));await current
  h.pending[0].reject(new Error('过时查询失败'));await old
  assert.deepEqual(h.notifications,[{kind:'error',value:'新查询失败'}])
  assert.deepEqual(h.state.items.value.map(item=>item.id),[5])
  assert.equal(h.state.loading.value,false)
})

test('追加旧日志时切换筛选，新请求接管loading且旧页不得混入新结果',async t=>{
  const h=logs(t)
  h.state.items.value=response(30,true).items;h.state.nextBeforeID.value=30;h.state.hasMore.value=true
  const older=h.state.load(true)
  assert.equal(h.pending[0].options.params.before_id,30)
  h.state.query.value='new';const current=h.state.load(false)
  const appendingAfterSwitch=h.state.loadingMore.value
  h.pending[1].resolve(response(40,true));await current
  h.pending[0].resolve(response(20,false));await older
  assert.equal(appendingAfterSwitch,false)
  assert.deepEqual(h.state.items.value.map(item=>item.id),[40])
  assert.equal(h.state.nextBeforeID.value,40);assert.equal(h.state.hasMore.value,true)
})

test('日志加载更早记录防止重复并发，首屏刷新期间不能追加旧游标',async t=>{
  const h=logs(t)
  h.state.nextBeforeID.value=30;h.state.hasMore.value=true
  const first=h.state.load(true),duplicate=h.state.load(true)
  const appendRequests=h.pending.length
  h.pending.forEach(item=>item.resolve(response(20,true)))
  await Promise.all([first,duplicate])
  assert.equal(appendRequests,1)
  const refresh=h.state.load(false),blockedAppend=h.state.load(true)
  const pendingAfterRefresh=h.pending.length
  h.pending.slice(1).forEach(item=>item.resolve(response(40)))
  await Promise.all([refresh,blockedAppend])
  assert.equal(pendingAfterRefresh,2)
  assert.deepEqual(h.state.items.value.map(item=>item.id),[40])
})

test('日志当前请求失败仍提示错误并保留已有结果，之后可以重试',async t=>{
  const h=logs(t)
  h.state.items.value=response(10).items
  const failed=h.state.load(false)
  h.pending[0].reject(new Error('当前日志暂不可用'));await failed
  assert.deepEqual(h.notifications,[{kind:'error',value:'当前日志暂不可用'}])
  assert.deepEqual(h.state.items.value.map(item=>item.id),[10]);assert.equal(h.state.loading.value,false)
  const retry=h.state.load(false)
  h.pending[1].resolve(response(20));await retry
  assert.deepEqual(h.state.items.value.map(item=>item.id),[20])
})

test('日志离开页面取消读取，迟到成功或失败均不改写页面或提示错误',async t=>{
  for(const reject of [false,true]) {
    const h=logs(t)
    const pending=h.state.load(false)
    h.unmount.forEach(callback=>callback())
    if(reject)h.pending[0].reject(new Error('离开后的旧错误'))
    else h.pending[0].resolve(response(10))
    await pending
    const requestsBeforeRetry=h.pending.length
    const retry=h.state.load(false)
    h.pending.slice(requestsBeforeRetry).forEach(item=>item.resolve(response(20)))
    await retry
    assert.equal(h.unmount.length,1)
    assert.equal(h.pending[0].options.signal?.aborted,true)
    assert.equal(h.pending.length,requestsBeforeRetry)
    assert.deepEqual(h.state.items.value,[]);assert.deepEqual(h.notifications,[])
  }
})
