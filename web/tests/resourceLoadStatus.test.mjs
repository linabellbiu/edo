import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import * as vue from 'vue'
import { parse } from '@vue/compiler-sfc'
import { loadFrontend } from './acceptanceHarness.mjs'

function status(t){
 const props=vue.reactive({errors:{},pending:{},labels:{repositories:'代码仓库',hosts:'主机名称',deployments:'部署记录'}}),events=[]
 const component=loadFrontend('../src/components/ResourceLoadStatus.vue',{
  vue:{...vue,useId:()=> 'resource-status-preview'},'vue-i18n':{useI18n:()=>({t:(key,values)=>values?`${key}:${values.count}`:key})},
 }).default
 const scope=vue.effectScope(),state=scope.run(()=>component.setup(props,{expose(){},emit:(name,keys)=>events.push({name,keys})}))
 t.after(()=>scope.stop());return {props,state,events}
}

test('多个相同错误只展示一份原因，但保留准确资源范围和真实失败数量',t=>{
 const h=status(t)
 h.props.errors={repositories:'暂时无法读取',hosts:'暂时无法读取',deployments:'没有读取权限'}
 assert.equal(h.state.label.value,'resourceLoadStatus.failed:3')
 assert.deepEqual(h.state.groups.value,[{message:'暂时无法读取',keys:['repositories','hosts']},{message:'没有读取权限',keys:['deployments']}])
 h.props.pending.hosts=true;h.state.retry(h.state.groups.value[0].keys)
 assert.deepEqual(h.events,[{name:'retry',keys:['repositories']}])
 h.props.pending.repositories=true;h.state.retry(h.state.groups.value[0].keys)
 assert.equal(h.events.length,1)
 delete h.props.errors.repositories;delete h.props.errors.hosts
 assert.deepEqual(h.state.groups.value,[{message:'没有读取权限',keys:['deployments']}])
})

test('无错误和后台加载共用同一固定状态入口，不把等待或重试显示成新增提示行',t=>{
 const h=status(t)
 assert.equal(h.state.label.value,'resourceLoadStatus.title')
 h.props.pending.repositories=true
 assert.equal(h.state.label.value,'resourceLoadStatus.title');assert.deepEqual(h.state.pendingKeys.value,['repositories'])
 h.state.retry(['repositories']);assert.deepEqual(h.events,[])
 const source=readFileSync(new URL('../src/components/ResourceLoadStatus.vue',import.meta.url),'utf8')
 assert.match(source,/\.resource-load-status\{width:132px;height:32px;flex:0 0 132px/)
 assert.match(source,/trigger="click"/);assert.match(source,/aria-haspopup="dialog"/)
 assert.match(source,/@keydown\.esc\.stop\.prevent="open = false"/)
 assert.doesNotMatch(source,/:loading=|is-busy/)
})

test('DevOps 各工作区的唯一状态入口放在现有工具栏，不再循环插入页头错误块',()=>{
 const source=readFileSync(new URL('../src/views/DevOpsView.vue',import.meta.url),'utf8')
 assert.doesNotMatch(source,/<a-alert[^>]*v-for="\(error,key\) in resourceErrors"/)
 const {descriptor}=parse(source),entries=[]
 function visit(node,parents=[]){if(node.type===1&&node.tag==='ResourceLoadStatus')entries.push({node,parents});for(const child of node.children||[])visit(child,[...parents,node])}
 visit(descriptor.template.ast)
 assert.equal(entries.length,5)
 for(const {node,parents} of entries){
  assert.ok(!node.props.some(prop=>prop.type===7&&['if','show','for'].includes(prop.name)))
  assert.ok(parents.some(parent=>parent.type===1&&(parent.tag==='PageToolbar'||parent.props.some(prop=>prop.type===6&&prop.name==='class'&&prop.value?.content==='apps-filter-actions')||parent.props.some(prop=>prop.type===7&&prop.name==='slot'&&prop.arg?.content==='toolbar-status'))))
 }
 for(const file of ['BuildPlanWorkspace','ReleasePlanWorkspace']){
  const workspace=readFileSync(new URL(`../src/components/${file}.vue`,import.meta.url),'utf8')
  assert.match(workspace,/<PageToolbar>\s*<slot name="toolbar-status"\s*\/>/)
 }
})
