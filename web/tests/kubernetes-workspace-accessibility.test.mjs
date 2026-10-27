import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileTemplate, parse } from '@vue/compiler-sfc'

function elements(node, predicate) {
  const found = node.type === 1 && predicate(node) ? [node] : []
  return found.concat((node.children || []).flatMap(child => elements(child, predicate)))
}
const attribute = (node, name) => node.props.find(prop => prop.type === 6 && prop.name === name)?.value?.content
const binding = (node, name) => node.props.find(prop => prop.type === 7 && prop.name === 'bind' && prop.arg?.content === name)?.exp?.content
function template(path) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const compiled = compileTemplate({ source: descriptor.template.content, filename: path, id: 'workspace-accessibility' })
  assert.deepEqual(compiled.errors, [])
  return descriptor.template.ast
}

test('部分加载的范围说明可以聚焦和点击，不再只依赖悬停', () => {
  const tree = template('../src/views/KubernetesView.vue')
  const control = elements(tree, node => node.tag === 'button' && attribute(node, 'class') === 'partial-badge')
  assert.equal(control.length, 1)
  assert.equal(attribute(control[0], 'type'), 'button')
  assert.equal(binding(control[0], 'aria-label'), "t('k8s.searchLoaded')")
  const tooltip = elements(tree, node => node.tag === 'a-tooltip' && binding(node, 'title') === "t('k8s.searchLoaded')")[0]
  assert.equal(binding(tooltip, 'trigger'), "['hover','focus','click']")
})

test('工作负载健康原因提供焦点和触控入口，保留状态文字', () => {
  const tree = template('../src/components/KubeWorkloadSummary.vue')
  const tooltip = elements(tree, node => node.tag === 'a-tooltip' && binding(node, 'key') === 'condition.type')[0]
  assert.equal(binding(tooltip, 'trigger'), "['hover','focus','click']")
  assert.equal(elements(tooltip, node => attribute(node, 'tabindex') === '0').length, 1)
})

test('本地预览保留异常状态、真实关联和端口场景，不把预览当生产数据', async () => {
  const { createKubernetesPreviewData } = await import('./fixtures/kubernetes-preview-data.mjs')
  const data = createKubernetesPreviewData()
  assert.equal(data.overview('preview').counts.pods.count, 2)
  assert.equal(data.overview('staging').counts.pods.count, 0)
  assert.equal(data.overview('staging').counts.nodes.count, 2)
  assert.equal(data.resources.ingresses[0].spec.rules[0].http.paths[0].backend.service.port.number, 8080)
  assert.equal(data.pods[1].status.phase, 'Pending')
  assert.equal(data.eventsFor(data.pods[1])[0].type, 'Warning')
  assert.equal(data.pods[0].metadata.ownerReferences[0].uid, data.revisions[0].uid)
  const history = data.nodeHistory(data.nodes[0].metadata.name)
  assert.equal(history.uid, data.nodes[0].metadata.uid)
  assert.equal(history.points.length, 31)
  assert.ok(history.points.every(point => point.metrics_state === 'ok' && point.pods_state === 'ok'))
})
