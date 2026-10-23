import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'

function harness(saved=[]){
  const values=new Map([['edo.navigation-history:default',JSON.stringify(saved)]])
  const storage={getItem:key=>values.get(key)??null,setItem:(key,value)=>values.set(key,value)}
  const browser={history:{state:{position:1}}}
  const source=readFileSync(new URL('../src/stores/tabs.ts',import.meta.url),'utf8')
  const code=ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
  const module={exports:{}}
  new Function('require','module','exports','window','sessionStorage','localStorage',code)(key=>key==='vue'?vue:{defineStore:(_id,factory)=>factory},module,module.exports,browser,storage,storage)
  const store=module.exports.useTabsStore()
  const visit=(path,position,back,forward=null)=>{browser.history.state={position,back,forward};store.visit({fullPath:path,meta:{title:path}})}
  return {store,visit,values,browser,reload:()=>harness(JSON.parse(values.get('edo.navigation-history:default')))}
}

test('自动补齐筛选参数替换当前历史，顶部返回直接回到总览',async()=>{
  const {store,visit}=harness(),calls=[]
  visit('/applications',1)
  visit('/kubernetes?view=overview',2,'/applications')
  const overview='/kubernetes?view=overview&cluster=test&namespace=prod'
  visit(overview,2,'/applications')
  const pods='/kubernetes?view=workloads&resource=pods&cluster=test&namespace=prod'
  visit(pods,3,overview)
  assert.deepEqual(store.history.value.map(item=>item.path),['/applications',overview,pods])
  await store.back({back:()=>calls.push('back'),push:path=>calls.push(path)},pods)
  assert.deepEqual(calls,['back'])
})

test('浏览器后退与前进不追加虚假访问，刷新后仍可连续返回',async()=>{
  const h=harness(),overview='/kubernetes?view=overview',pods='/kubernetes?view=workloads&resource=pods'
  h.visit('/applications',1)
  h.visit(overview,2,'/applications')
  h.visit(pods,3,overview)
  h.visit(overview,2,'/applications',pods)
  assert.equal(h.store.history.value.length,3)
  assert.deepEqual(h.store.recent(overview).map(item=>item.path),['/applications'])
  h.visit(pods,3,overview)
  assert.deepEqual(h.store.recent(pods).map(item=>item.path),[overview,'/applications'])
  const refreshed=h.reload(),calls=[]
  refreshed.visit(pods,3,overview)
  await refreshed.store.back({back:()=>calls.push('back'),push:path=>calls.push(path)},pods)
  refreshed.visit(overview,2,'/applications',pods)
  await refreshed.store.back({back:()=>calls.push('back'),push:path=>calls.push(path)},overview)
  assert.deepEqual(calls,['back','back'])
})

test('同一路径的多次访问按当前位置返回，后退后打开新资源丢弃原前进分支',()=>{
  const {store,visit}=harness()
  visit('/applications',1)
  visit('/kubernetes?view=overview',2,'/applications')
  visit('/applications',3,'/kubernetes?view=overview')
  visit('/kubernetes?view=workloads&resource=pods',4,'/applications')
  visit('/applications',1,undefined,'/kubernetes?view=overview')
  assert.equal(store.canGoBack('/applications'),false)
  visit('/kubernetes?view=overview',2,'/applications','/applications')
  visit('/kubernetes?view=network&resource=services',3,'/kubernetes?view=overview')
  assert.equal(store.history.value.length,3)
  assert.equal(store.history.value.at(-1).path,'/kubernetes?view=network&resource=services')
})

test('旧会话记录可读取，第一次导航清理无浏览器位置的旧记录',()=>{
  const {store,visit}=harness([{title:'nav.overview',path:'/'},{title:'k8s.overview',path:'/kubernetes?view=overview'}])
  visit('/kubernetes?view=workloads&resource=pods',4)
  assert.equal(store.history.value.length,1)
  assert.equal(store.canGoBack('/kubernetes?view=workloads&resource=pods'),false)
})
