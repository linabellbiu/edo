import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'
import { baseParse } from '@vue/compiler-dom'

// ant-design-vue 的 AutoComplete 会把默认插槽的第一个子节点当成“自定义输入元素”
// （getInputElement -> slots.default.call(slots)，不传插槽 props）。
// 因此给 a-auto-complete 传 <template #default="{ value }"> 会解构 undefined 抛错，
// 该控件整块渲染失败，页面上只剩 label —— 2026-11-17 部署配置“镜像”字段就是这样变空的。
const root = new URL('../src/', import.meta.url)

function vueFiles(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) return vueFiles(path)
    return entry.name.endsWith('.vue') ? [path] : []
  })
}
function walk(node, visit) {
  visit(node)
  for (const child of node.children ?? []) walk(child, visit)
  for (const branch of node.branches ?? []) walk(branch, visit)
}
function defaultSlotChildren(node) {
  return (node.children ?? []).filter(child => {
    if (child.type === 2) return child.content.trim() !== ''
    if (child.type === 3) return false
    if (child.type === 1 && child.tag === 'template') return child.props?.some(prop => prop.type === 6 && prop.name === 'slot' ? prop.value?.content === 'default' : prop.name === 'v-slot' && !prop.arg)
    return true
  })
}

test('a-auto-complete 不使用默认插槽作为输入元素', () => {
  const violations = []
  for (const path of vueFiles(root.pathname)) {
    const source = readFileSync(path, 'utf8')
    if (!source.includes('<a-auto-complete')) continue
    const name = path.split('/').pop()
    const template = parse(source, { filename: name }).descriptor.template
    if (!template) continue
    const { children } = baseParse(template.content)
    for (const root of children) walk(root, node => {
      if (node.type !== 1 || node.tag !== 'a-auto-complete') return
      if (defaultSlotChildren(node).length) violations.push(`${name}:${node.loc.start.line} 给 a-auto-complete 传了默认插槽，会替换输入框`)
    })
  }
  assert.deepEqual(violations, [])
})

test('部署镜像字段保留上游构建产物选项且不包默认插槽', () => {
  const source = readFileSync(new URL('../src/components/DeploymentConfigurationFields.vue', import.meta.url), 'utf8')
  const field = source.match(/<a-auto-complete aria-label="部署镜像" [^>]*\/>/)
  assert.ok(field, '部署镜像必须是自闭合的 a-auto-complete')
  assert.match(field[0], /:options="kubeImageOptions"/)
  assert.match(field[0], /placeholder="选择上游构建产物"/)
  assert.doesNotMatch(source, /<a-auto-complete aria-label="部署镜像"[\s\S]{0,400}?<template #default/)
  assert.doesNotMatch(source, /<template #default="\{value\}">\{\{ value \}\}<\/template>/)
})
