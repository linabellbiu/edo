import assert from 'node:assert/strict'
import test from 'node:test'
import * as vue from 'vue'
import {componentHarness,loadFrontend} from './acceptanceHarness.mjs'
const groupModule=loadFrontend('../src/utils/hostGroups.ts')
const component=(props)=>componentHarness('../src/components/DeploymentHostSelection.vue',{props,modules:{'@/utils/hostGroups':groupModule}})
const defaults={groupID:'group',groups:[{id:'group',name:'主机组'}]}
const hosts=[{id:'a',name:'主机 A'},{id:'b',name:'主机 B'}]

test('等主机组加载完成后默认全选，不把加载中的空列表保存为全不选',async t=>{
 const props=vue.reactive({...defaults,hosts:[],ready:false,disabled:false})
 const h=component(props);t.after(h.stop)
 assert.equal(h.events.length,0)
 props.hosts=hosts;props.ready=true;await vue.nextTick()
 assert.deepEqual(h.events,[{name:'update:modelValue',args:[['a','b']]}])
})

test('保存的部分勾选、全不选和旧单主机都不会被重载或新增主机扩大',async t=>{
 for(const selection of [['a'],[]]){
  const props=vue.reactive({...defaults,hosts:[...hosts],ready:true,modelValue:selection})
  const h=component(props);t.after(h.stop)
  props.hosts=[...hosts,{id:'c',name:'主机 C'}];await vue.nextTick()
  assert.deepEqual(h.state.selected.value,selection);assert.equal(h.events.length,0)
 }
 const old=component({...defaults,hosts,ready:true,legacyHostID:'a'});t.after(old.stop)
 assert.deepEqual(old.state.selected.value,['a']);assert.equal(old.events.length,0)
})

test('撤权只读不会自动改写主机选择，不可用原主机保留为需处理项',t=>{
 const h=component({...defaults,hosts,ready:true,disabled:true,modelValue:['missing']});t.after(h.stop)
 assert.deepEqual(h.state.missing.value,['missing']);assert.equal(h.events.length,0)
})


test('树形子组和主机分别定位，跨组勾选保留已选主机并按 UUID 去重',t=>{
 const groups=[{id:'root',name:'研发'},{id:'group',name:'生产',parent_id:'root'},{id:'test',name:'测试',parent_id:'root'}]
 const props=vue.reactive({...defaults,groups,hosts,hostOptions:{group:hosts,test:[{id:'c',name:'主机 C'},hosts[0]]},ready:true,modelValue:['a']})
 const h=component(props);t.after(h.stop)
 assert.equal(h.state.treeData.value[0].checkable,false,'纯组织节点不能作为空的主机执行范围')
 assert.equal(h.state.treeData.value[0].children[0].title,'生产')
 assert.equal(h.state.treeData.value[0].children[0].children[0].hostID,'a')
 assert.equal(h.state.treeValue.value[0].halfChecked,true)
 h.state.choose([{value:'group:test'}],[],{triggerValue:'group:test',checked:true})
 assert.deepEqual(h.events.at(-1),{name:'change',args:[{groupID:'group',hostIDs:['a','c']}]})
 h.state.choose([{value:'host:test:c'}],[],{triggerValue:'host:test:c',checked:true})
 assert.deepEqual(h.events.at(-1).args,[{groupID:'group',hostIDs:['a','c']}])
 assert.deepEqual(props.modelValue,['a'])
})

test('下拉逐台取消、组内全选和清除均保持精确 UUID，不自动扩展下级组',t=>{
 const h=component({...defaults,hosts,hostOptions:{group:hosts},ready:true,modelValue:['a','b']});t.after(h.stop)
 assert.equal(h.state.treeValue.value[0].halfChecked,false)
 h.state.choose([{value:'group:group'}],[],{triggerValue:'host:group:a',checked:false})
 assert.deepEqual(h.events.at(-1).args,[{groupID:'group',hostIDs:['b']}])
 h.state.choose([{value:'host:group:a'}],[],{triggerValue:'group:group',checked:false})
 assert.deepEqual(h.events.at(-1).args,[{groupID:'group',hostIDs:[]}])
 h.state.choose(undefined)
 assert.deepEqual(h.events.at(-1).args,[{groupID:'',hostIDs:[]}])
})

test('只读或未加载时拒绝树选择；Kubernetes 只选组不写主机',t=>{
 for(const patch of [{ready:false},{disabled:true}]){
  const h=component({...defaults,hosts,ready:true,modelValue:['a'],...patch});t.after(h.stop)
  h.state.choose([{value:'group:group'}],[],{triggerValue:'group:group',checked:true});assert.deepEqual(h.events,[])
 }
 const h=component({...defaults,hosts,ready:true,groupOnly:true});t.after(h.stop)
 assert.equal(h.state.treeValue.value,'group:group');assert.deepEqual(h.events,[])
 h.state.choose('group:group');assert.deepEqual(h.events.at(-1).args,[{groupID:'group',hostIDs:[]}])
 assert.equal(h.state.treeData.value[0].children,undefined)
})


test('未分组主机可选择，跨组移动或退出所有组后准确回显且不自动改写',async t=>{
 const a={id:'a',name:'主机 A'},b={id:'b',name:'未分组主机'}
 const props=vue.reactive({...defaults,hosts:[a],allHosts:[a,b],hostOptions:{group:[a]},ready:true,modelValue:['a']})
 const h=component(props);t.after(h.stop)
 const ungrouped=h.state.treeData.value.find(node=>node.title==='未分组')
 assert.equal(ungrouped.children[0].hostID,'b')
 h.state.choose([{value:ungrouped.children[0].key}],[],{triggerValue:ungrouped.children[0].key,checked:true})
 assert.deepEqual(h.events.at(-1).args,[{groupID:'group',hostIDs:['a','b']}])
 h.events.length=0
 props.hosts=[];props.hostOptions={group:[],other:[a]};props.groups=[...defaults.groups,{id:'other',name:'新组'}]
 await vue.nextTick()
 assert.deepEqual(h.state.missing.value,[])
 assert.ok(h.state.treeValue.value.some(node=>node.value==='host:other:a'))
 assert.deepEqual(h.events,[])
 props.hostOptions={group:[],other:[]};await vue.nextTick()
 assert.ok(h.state.treeValue.value.some(node=>node.value==='host::a'))
 assert.deepEqual(h.events,[])
})

test('只有未分组主机且没有组时仍可保存精确选择，不选其他主机',t=>{
 const h=component({groups:[],hosts:[],allHosts:hosts,ready:true,modelValue:['a']});t.after(h.stop)
 assert.equal(h.state.error.value,'')
 assert.ok(h.state.treeValue.value.some(node=>node.value==='host::a'))
 h.state.choose([{value:'host::b'}],[],{triggerValue:'host::b',checked:true})
 assert.deepEqual(h.events.at(-1).args,[{groupID:'',hostIDs:['a','b']}])
})
