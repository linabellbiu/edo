import assert from 'node:assert/strict'
import test from 'node:test'
import * as vue from 'vue'
import {componentHarness} from './acceptanceHarness.mjs'
const hosts=[{id:'a',name:'主机 A'},{id:'b',name:'主机 B'}]

test('等主机组加载完成后默认全选，不把加载中的空列表保存为全不选',async t=>{
 const props=vue.reactive({hosts:[],ready:false,disabled:false})
 const h=componentHarness('../src/components/DeploymentHostSelection.vue',{props});t.after(h.stop)
 assert.equal(h.events.length,0)
 props.hosts=hosts;props.ready=true;await vue.nextTick()
 assert.deepEqual(h.events,[{name:'update:modelValue',args:[['a','b']]}])
})

test('保存的部分勾选、全不选和旧单主机都不会被重载或新增主机扩大',async t=>{
 for(const selection of [['a'],[]]){
  const props=vue.reactive({hosts:[...hosts],ready:true,modelValue:selection})
  const h=componentHarness('../src/components/DeploymentHostSelection.vue',{props});t.after(h.stop)
  props.hosts=[...hosts,{id:'c',name:'主机 C'}];await vue.nextTick()
  assert.deepEqual(h.state.selected.value,selection);assert.equal(h.events.length,0)
 }
 const old=componentHarness('../src/components/DeploymentHostSelection.vue',{props:{hosts,ready:true,legacyHostID:'a'}});t.after(old.stop)
 assert.deepEqual(old.state.selected.value,['a']);assert.equal(old.events.length,0)
})

test('撤权只读不会自动改写主机选择，不可用原主机保留为需处理项',t=>{
 const h=componentHarness('../src/components/DeploymentHostSelection.vue',{props:{hosts,ready:true,disabled:true,modelValue:['missing']}});t.after(h.stop)
 assert.deepEqual(h.state.missing.value,['missing']);assert.equal(h.events.length,0)
})
