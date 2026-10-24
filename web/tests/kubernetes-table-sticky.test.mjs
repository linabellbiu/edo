import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import postcss from 'postcss'

const css=postcss.parse(readFileSync(new URL('../src/styles/kubernetes.css',import.meta.url),'utf8'))
const table='.kube-resource-table table.resource-table'
function declarations(selector){
 const result={}
 css.walkRules(rule=>{if(rule.selectors.includes(selector))rule.walkDecls(decl=>{result[decl.prop]=decl.value})})
 return result
}

test('Kubernetes 固定操作列不使用合并边框，滚动时不透出其他列边线',()=>{
 const style=declarations(table)
 assert.equal(style['border-collapse'],'separate')
 assert.equal(style['border-spacing'],'0')
})

test('普通、隔行和悬停使用实色底板，选中状态保留主题颜色',()=>{
 assert.equal(declarations(`${table} tbody > tr > td`).background,'var(--edo-surface)')
 for(const state of [':nth-child(even)',':hover']){
  const value=declarations(`${table} tbody > tr${state} > td`).background
  assert.match(value,/var\(--edo-surface\)/)
  assert.doesNotMatch(value,/transparent|rgba?\(/)
 }
 assert.equal(declarations(`${table} tbody > tr.active > td`).background,'var(--edo-primary-soft)')
 const rules=[];css.walkRules(rule=>rules.push(rule.selector))
 assert.ok(rules.indexOf(`${table} tbody > tr.active > td`)>rules.indexOf(`${table} tbody > tr:hover > td`))
})

test('固定操作单元格和表头具有明确层级，规则不外溢其他业务表格',()=>{
 assert.equal(declarations(`${table} .table-actions`)['z-index'],'1')
 assert.equal(declarations(`${table} .table-action-heading`)['z-index'],'2')
 const global=declarations('.resource-table')
 assert.equal(global['border-collapse'],undefined)
 assert.equal(declarations('tbody > tr:hover > td').background,undefined)
})
