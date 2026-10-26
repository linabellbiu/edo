import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse } from '@vue/compiler-sfc'
import { baseParse } from '@vue/compiler-dom'

const source = readFileSync(new URL('../src/views/KubernetesView.vue', import.meta.url), 'utf8')
const template = parse(source).descriptor.template.content
const entries = []
function inspect(node, parents = []) {
  if (node.type === 1 && node.tag === 'a-button' && node.props.some(prop => prop.type === 7 && prop.name === 'on' && prop.arg?.content === 'click' && prop.exp?.content === 'clusterDrawer=true')) entries.push({node, parents})
  for (const child of node.children ?? []) inspect(child, [...parents, node])
}
inspect(baseParse(template))

test('接入集群入口位于总览内容区，不在公共工具栏中', () => {
  const toolbar = template.match(/<PageToolbar\b[\s\S]*?<\/PageToolbar>/)?.[0]
  if(toolbar)assert.doesNotMatch(toolbar, /k8s\.addCluster|clusterDrawer/)
  const panel = template.match(/<section[^>]*class="resource-panel">[\s\S]*?<\/section>/)?.[0]
  assert.ok(panel)
  assert.match(panel, /@click="clusterDrawer=true"/)
  assert.equal(entries.length, 1)
})

test('只有总览且具备创建权限时才显示接入集群入口', () => {
  const entry = entries[0]
  assert.ok(entry)
  const condition = [...entry.parents, entry.node].flatMap(node => (node.props ?? []).filter(prop => prop.type === 7 && prop.name === 'if').map(prop => `(${prop.exp?.content})`)).join(' && ')
  assert.ok(condition)
  const visible = new Function('group', 'can', `return ${condition}`)
  for (const group of ['overview', 'nodes', 'workloads', 'config', 'storage', 'network']) {
    for (const allowed of [true, false]) {
      assert.equal(visible(group, permission => permission === 'cluster.create' && allowed), group === 'overview' && allowed)
    }
  }
})
