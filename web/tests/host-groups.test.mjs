import assert from 'node:assert/strict'
import test from 'node:test'
import {componentHarness, loadFrontend} from './acceptanceHarness.mjs'
import * as vue from 'vue'

const groups = loadFrontend('../src/utils/hostGroups.ts')
const rows = [{id:'a',name:'研发'}, {id:'b',name:'上海',parent_id:'a'}, {id:'c',name:'一组',parent_id:'b'}, {id:'d',name:'其他'}]

test('主机组树保留层级，搜索下级时保留祖先，父组候选排除自身及下级',()=>{
  assert.deepEqual(groups.hostGroupTree(rows).map(v=>[v.key,v.children.map(c=>c.key)]),[['a',['b']],['d',[]]])
  assert.equal(groups.hostGroupTree(rows,'一组')[0].children[0].children[0].key,'c')
  assert.deepEqual(groups.hostGroupOptions(rows,'b'),[{value:'a',label:'研发'},{value:'d',label:'其他'}])
  assert.equal(groups.hostGroupOptions(rows)[2].label,'研发 / 上海 / 一组')
  assert.deepEqual([...groups.hostGroupDescendants(rows,'a')],['a','b','c'])
})

test('无效旧层级不递归失控，仍可展示并修正',()=>{
  const invalid=[{id:'a',name:'A',parent_id:'b'},{id:'b',name:'B',parent_id:'a'},{id:'c',name:'C',parent_id:'unknown'}]
  assert.equal(groups.hostGroupTree(invalid).length,3)
  assert.equal(groups.hostGroupOptions(invalid).length,3)
})

test('从组节点创建下级正确带上父组，编辑不丢成员；主机列表汇总子组并去重',async t=>{
  const list=loadFrontend('../src/utils/resourceList.ts',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})}})
  const h=componentHarness('../src/views/HostView.vue',{codes:['deployment.read','deployment.create','deployment.update','cluster.read'],modules:{'@/utils/hostGroups':groups,'@/utils/resourceList':list,'@/api/infrastructure':{environmentIDsOf:host=>host.environment_ids||[]}}})
  t.after(h.stop)
  h.state.environments.value=rows.map(row=>({...row,is_active:true,hosts:row.id==='b'?[{id:'host'}]:[]}))
  h.state.hosts.value=[{id:'host',name:'Host',address:'',is_active:true,environment_ids:['a','b'],capabilities:[]},{id:'nested',name:'Nested',address:'',is_active:true,environment_ids:['c'],capabilities:[]}]
  h.state.createGroup('b')
  assert.equal(h.state.groupForm.parent_id,'b')
  h.state.editGroup(h.state.environments.value[1],'details')
  assert.equal(h.state.groupForm.parent_id,'a')
  assert.deepEqual(h.state.groupForm.host_ids,['host'])
  assert.ok(!h.state.parentGroupOptions.value.some(option=>['b','c'].includes(option.value)))
  h.state.selectGroup('a')
  assert.deepEqual(h.state.filteredHosts.value.map(host=>host.id),['host','nested'])
})
