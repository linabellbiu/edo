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








